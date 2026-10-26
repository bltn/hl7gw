# Configuration

hl7gw reads a single JSON file: `hl7gw.json` in the current directory, or the
path given to `run -config`:

```sh
hl7gw run
hl7gw run -config /etc/hl7gw/hl7gw.json
```

`hl7gw configure` writes one interactively. It asks for each channel (the
mutual TLS material only when `source.tls.mode` is `mtls`), the admin port and
`data_dir`, rejecting invalid answers on the spot. `log.level`, `log.format`,
`cert_reload_interval` and `retention` are not asked; they are written with
the defaults below so they can be edited in the file. The admin API token is generated into
`admin.token` beside the config file.

Unknown keys are rejected. The gateway refuses to start, with an `error: …`
line on stderr, if the file is invalid, if a token or password file is missing
or empty, or if TLS material can't be loaded.

Durations are Go duration strings: `30s`, `1m`, `2h`, `1h30m`. Paths are
resolved against the working directory the gateway runs from.

## Top level

| Key | Required | Default | Meaning |
|---|---|---|---|
| `admin.port` | yes | | Port (1–65535) for the admin API, metrics and probes. Must not clash with a channel port. |
| `admin.token_file` | yes | | File holding the admin API bearer token. Surrounding whitespace is trimmed. |
| `channels` | | `[]` | The channels, described below. |
| `data_dir` | | `data` | Directory for the channel databases. Each channel writes `<data_dir>/<channel>.db`. |
| `cert_reload_interval` | | `2m` | How often mTLS certificates and client CA files are re-read from disk. |
| `retention` | | `30d` | How long persisted messages are kept: a whole number of days such as `30d`, or `off` to keep everything. See [Retention](#retention). |
| `log.level` | | `info` | `debug`, `info`, `warn` or `error`. |
| `log.format` | | `auto` | `json`, `text` for slog's `key=value` form, or `auto` for `text` on a terminal and `json` otherwise. |

## Channels

| Key | Required | Default | Meaning |
|---|---|---|---|
| `name` | yes | | Letters, digits, `_` and `-`. Unique. Used in the database file name, metrics labels and admin API calls. |
| `source.port` | yes | | The MLLP port (1–65535). Unique across channels. |
| `source.idle_timeout` | yes | | Close a connection that hasn't started a new message within this time. |
| `source.max_connections` | | `64` | Concurrent connections. At the limit, the listener pauses accepting new ones. |
| `source.tls.mode` | yes | | `off` or `mtls`. |
| `destination.url` | yes | | Absolute `http` or `https` URL that messages are POSTed to. |
| `destination.auth.type` | yes | | `none`, `bearer` or `basic`. |
| `destination.auth.token_file` | bearer only | | File holding the bearer token sent to the destination. Surrounding whitespace is trimmed. |
| `destination.auth.username` | basic only | | Username for HTTP Basic auth. Must not contain `:`. |
| `destination.auth.password_file` | basic only | | File holding the Basic auth password. Surrounding whitespace is trimmed. |

### Destination auth

`destination.auth` is always required, and `type` says how each message is
authenticated to the destination:

- `bearer` sends `Authorization: Bearer <token>`, with the token read from
  `token_file`.
- `basic` sends `Authorization: Basic <base64(username:password)>`, with the
  password read from `password_file`.
- `none` sends no `Authorization` header. It has to be written down rather
  than being the default so that a channel never goes out unauthenticated
  because a key was forgotten.

A key that belongs to another type (`token_file` under `basic`, `username`
under `bearer`, anything but `type` under `none`) is rejected at startup, so a
stale key can't be silently ignored.

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

Token and password files are read once at startup; changing one needs a restart.

### Retention

Every message is persisted, so each channel's database grows with traffic.
`retention` bounds it by age: a message received more than that many days
ago is deleted, together with its delivery attempts, whatever its ACK or
delivery outcome was. The database is operational evidence, not the system of
record; 30 days covers replay and debugging, and a shorter value keeps less
PHI on disk.

The sweep runs once at startup, after the channel databases are opened and
before any MLLP listener or the admin API binds, so a backlog is cleared
before new traffic arrives and `/readyz` is not answered until it finishes. It then runs
every hour. Each sweep logs `retention sweep` per channel and counts the rows
it removed in `hl7gw_messages_pruned_total`. A sweep that fails (a locked or
damaged database) is logged as `retention sweep failed` and the gateway
carries on; the next hourly sweep retries.

Deleting rows does not shrink the database file: SQLite reuses the freed
pages for new messages. Run `VACUUM` against the file offline if you need the
disk space back. Replaying a pruned message through the admin API answers
404.

`off` keeps every message forever. The gateway logs `retention off` at
startup, and disk usage is yours to manage.

## Fixed limits

These aren't configurable:

| Limit | Value |
|---|---|
| Time to finish reading a frame once it has started | 30 s |
| Time to write an ACK | 15 s |
| TLS handshake | 10 s |
| Each HTTP delivery attempt | 10 s |
| Delivery attempts per message | 2, with 500 ms between them |
| Graceful shutdown deadline | 20 s |
| Retention sweep interval | 1 h |
| Messages deleted per retention transaction | 1000 |

## Example

```json
{
  "data_dir": "/var/lib/hl7gw",
  "retention": "30d",
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
