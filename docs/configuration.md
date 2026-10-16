# Configuration

hl7gw reads a single JSON file given by `-config`:

```sh
hl7gw -config /etc/hl7gw/hl7gw.json
```

Unknown keys are rejected. The gateway refuses to start, with an `error: …`
line on stderr, if the file is invalid, if a named environment variable is
unset or empty, or if TLS material can't be loaded.

## Top level

| Key | Required | Default | Meaning |
|---|---|---|---|
| `admin_api.port` | yes | | Port for the admin API, metrics and probes. Must not clash with a channel port. |
| `admin_api.token_env` | yes | | Name of the environment variable holding the admin API bearer token. |
| `channels` | | `[]` | The channels, described below. |
| `data_dir` | | `data` | Directory for the journals. Each channel writes `<data_dir>/<channel>.db`. |
| `tls_reload_interval_seconds` | | `120` | How often mTLS certificates and client CA files are re-read from disk. |
| `log.level` | | `info` | `debug`, `info`, `warn` or `error`. |
| `log.format` | | `json` | `json`, or `text` for slog's `key=value` form. |

## Channels

| Key | Required | Default | Meaning |
|---|---|---|---|
| `name` | yes | | Letters, digits, `_` and `-`. Unique. Used in the database file name, metrics labels and admin API calls. |
| `tcp_listener.port` | yes | | The MLLP port. Unique across channels. |
| `tcp_listener.idle_timeout_seconds` | yes | | Close a connection that hasn't started a new message within this time. |
| `tcp_listener.max_connections` | | `64` | Concurrent connections. At the limit, the listener pauses accepting new ones. |
| `tcp_listener.tls.mode` | yes | | `off` or `mtls`. |
| `destination.url` | yes | | Absolute `http` or `https` URL that messages are POSTed to. |
| `destination.auth.type` | yes | | Must be `bearer`. |
| `destination.auth.token_env` | yes | | Name of the environment variable holding the bearer token sent to the destination. |

### Mutual TLS

With `tls.mode` set to `mtls`, all of these are required:

| Key | Meaning |
|---|---|
| `server_cert_path` | PEM certificate (chain) the listener serves. |
| `server_pk_path` | PEM private key for that certificate. |
| `client_ca_path` | PEM bundle of CAs that client certificates must chain to. |
| `min_version` | `1.2` or `1.3`, as a JSON number. |

Clients must present a certificate that verifies against `client_ca_path`.
This is checked during the handshake, before any MLLP bytes are read. Every
`tls_reload_interval_seconds`, the gateway re-reads all three files:

- A changed certificate and key pair is served to new handshakes.
- A changed CA bundle is used to verify new clients.
- A file that is missing, unparsable or mismatched is logged and ignored, and
  the previously loaded material stays in use.

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
  "admin_api": { "port": 8081, "token_env": "HL7GW_ADMIN_TOKEN" },
  "channels": [
    {
      "name": "adt",
      "tcp_listener": {
        "port": 2575,
        "idle_timeout_seconds": 300,
        "max_connections": 10,
        "tls": {
          "mode": "mtls",
          "server_cert_path": "/etc/hl7gw/server-cert.pem",
          "server_pk_path": "/etc/hl7gw/server-key.pem",
          "client_ca_path": "/etc/hl7gw/client-ca.pem",
          "min_version": 1.2
        }
      },
      "destination": {
        "url": "https://api.internal/hl7/adt",
        "auth": { "type": "bearer", "token_env": "ADT_API_TOKEN" }
      }
    }
  ]
}
```
