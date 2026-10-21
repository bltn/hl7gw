# Configuration

hl7gw reads a single JSON file given to `run -config`:

```sh
hl7gw run -config /etc/hl7gw/hl7gw.json
```

`hl7gw configure` writes one interactively. It asks for every option below
(the mutual TLS material only when a channel requires client certificates),
rejects invalid answers on the spot, always sets `destination.auth.type` to
`bearer`, and generates the admin API token into `admin.token` beside the
config file.

Unknown keys are rejected. The gateway refuses to start, with an `error: …`
line on stderr, if the file is invalid, if a token file is missing or empty,
or if TLS material can't be loaded.

Durations are Go duration strings: `30s`, `1m`, `2h`, `1h30m`. Paths are
resolved against the working directory the gateway runs from.

## Top level

| Key | Required | Default | Meaning |
|---|---|---|---|
| `admin.port` | yes | | Port for the admin API, metrics and probes. Must not clash with a channel port. |
| `admin.token_file` | yes | | File holding the admin API bearer token. Surrounding whitespace is trimmed. |
| `channels` | | `[]` | The channels, described below. |
| `data_dir` | | `data` | Directory for the journals. Each channel writes `<data_dir>/<channel>.db`. |
| `cert_reload_interval` | | `2m` | How often mTLS certificates and client CA files are re-read from disk. |
| `log.level` | | `info` | `debug`, `info`, `warn` or `error`. |
| `log.format` | | `auto` | `json`, `text` for slog's `key=value` form, or `auto` for `text` on a terminal and `json` otherwise. |

## Channels

| Key | Required | Default | Meaning |
|---|---|---|---|
| `name` | yes | | Letters, digits, `_` and `-`. Unique. Used in the database file name, metrics labels and admin API calls. |
| `source.port` | yes | | The MLLP port. Unique across channels. |
| `source.idle_timeout` | yes | | Close a connection that hasn't started a new message within this time. |
| `source.max_connections` | | `64` | Concurrent connections. At the limit, the listener pauses accepting new ones. |
| `source.tls.mode` | yes | | `off` or `mtls`. |
| `destination.url` | yes | | Absolute `http` or `https` URL that messages are POSTed to. |
| `destination.auth.type` | yes | | Must be `bearer`. |
| `destination.auth.token_file` | yes | | File holding the bearer token sent to the destination. Surrounding whitespace is trimmed. |

### Mutual TLS

With `tls.mode` set to `mtls`, all of these are required:

| Key | Meaning |
|---|---|
| `cert_file` | PEM certificate (chain) the listener serves. |
| `key_file` | PEM private key for that certificate. |
| `client_ca_file` | PEM bundle of CAs that client certificates must chain to. |
| `min_version` | `"1.2"` or `"1.3"`. |

Clients must present a certificate that verifies against `client_ca_file`.
This is checked during the handshake, before any MLLP bytes are read. Every
`cert_reload_interval`, the gateway re-reads all three files:

- A changed certificate and key pair is served to new handshakes.
- A changed CA bundle is used to verify new clients.
- A file that is missing, unparsable or mismatched is logged and ignored, and
  the previously loaded material stays in use.

Token files are read once at startup; changing one needs a restart.

## Fixed limits

These aren't configurable yet:

| Limit | Value |
|---|---|
| Time to finish reading a frame once it has started | 30 s |
| Time to write an ACK | 15 s |
| TLS handshake | 10 s |
| Each HTTP delivery attempt | 10 s |
| Delivery attempts per message | 2, with 500 ms between them |
| Graceful shutdown deadline | 20 s |

## Example

```json
{
  "data_dir": "/var/lib/hl7gw",
  "log": { "level": "info", "format": "json" },
  "admin": { "port": 8081, "token_file": "/etc/hl7gw/admin.token" },
  "channels": [
    {
      "name": "adt",
      "source": {
        "port": 2575,
        "idle_timeout": "5m",
        "max_connections": 10,
        "tls": {
          "mode": "mtls",
          "cert_file": "/etc/hl7gw/server-cert.pem",
          "key_file": "/etc/hl7gw/server-key.pem",
          "client_ca_file": "/etc/hl7gw/client-ca.pem",
          "min_version": "1.2"
        }
      },
      "destination": {
        "url": "https://api.internal/hl7/adt",
        "auth": { "type": "bearer", "token_file": "/etc/hl7gw/adt.token" }
      }
    }
  ]
}
```
