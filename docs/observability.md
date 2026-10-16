# Observability

hl7gw writes structured logs to stdout, serves Prometheus metrics, and
answers liveness and readiness probes, so it fits whatever monitoring stack
you already run. Message bodies, transformed XML and ACK payloads are never
logged. They're kept in the journal and can be read through the admin API.

## Logs

Logs go to **stdout** as one JSON object per line, written with Go's
`log/slog`. The only thing written to stderr is the fatal `error: …` line
printed when the gateway refuses to start. The optional `log` config block
controls the output:

```json
"log": {"level": "info", "format": "json"}
```

`level` is one of `debug`, `info`, `warn` or `error` (default `info`).
`format` is `json` or `text` (default `json`); `text` is slog's `key=value`
form, which is easier to read locally.

Every record carries `time`, `level` and `msg`. Records about a channel also
carry `channel`, and records about a message carry `message_id` (the journal
row id). Error text is in `err`, and elapsed times are in `duration_ms`.

The `msg` values below are a stable contract:

| level | msg | attrs |
|---|---|---|
| info | `starting` | version, commit |
| info | `listening` | channel, addr, tls, serial and expires (mTLS only) |
| info | `admin api listening` | addr |
| info | `stopped accepting connections` | channel |
| warn | `shutdown deadline reached, abandoned in-flight work` | |
| info | `shutdown complete` | |
| info | `server cert reloaded` | channel, serial, expires |
| info | `client ca pool reloaded` | channel, certs |
| warn | `server cert reload failed, still serving previous cert` | channel, err |
| warn | `client ca reload failed, still verifying against previous pool` | channel, err |
| debug | `connection opened` | channel, remote |
| info | `connection closed` | channel, remote, reason, messages, duration_ms, err (when the reason carries one) |
| info | `tls client cert accepted` | channel, remote, cn, issuer, serial, expires |
| warn | `tls handshake failed` | channel, remote, err |
| warn | `at tcp conn capacity, pausing accepts` | channel, max |
| info | `below tcp conn capacity, resuming accepts` | channel |
| warn | `accept failed, retrying` | channel, backoff, err |
| debug | `message received` | channel, message_id |
| info | `message processed` | channel, message_id, control_id, message_type, outcome, attempts, status_code (when the downstream answered), duration_ms |
| warn | `dropping uncorrelatable message` | channel, message_id |
| warn | `accepted message with missing required msh fields` | channel, message_id, control_id, missing |
| warn | `delivery attempt failed` | channel, message_id, origin, attempt, status_code (when the downstream answered), err |
| error | `message persistence failed` | channel, message_id, operation, err |
| warn | `ack write failed` | channel, message_id, err |
| debug | `ack sent` | channel, message_id, code |
| error | `recording ack outcome failed` | channel, message_id, err |
| info | `message dry run` | channel, outcome, control_id, message_type |
| info | `replay finished` | channel, message_id, outcome |
| warn | `readiness check failed` | channel, err |
| info | `admin api request` | method, path, status, duration_ms (authenticated routes only) |
| warn | `admin api unauthorized` | method, path, remote |

`connection closed` reasons:

- `peer_closed`: the sender closed the connection.
- `drain`: shutdown closed it.
- `timeout`: the idle or frame-read deadline passed.
- `protocol_error`: a bad MLLP trailer.
- `write_error`: the ACK couldn't be written.
- `error`: anything else.

Persistence `operation` values: `create_message`, `record_transform`,
`record_delivery_attempt`, `record_ack`, `get_message`.

## Metrics

`GET /metrics` on the admin port serves the Prometheus text exposition
format without authentication. Any Prometheus-compatible scraper can target
it directly, such as Prometheus, the OpenTelemetry Collector, Grafana Agent
or the Datadog agent. Label values are lowercase.

| metric | type | labels |
|---|---|---|
| `hl7gw_messages_total` | counter | channel, outcome (`success`, `parse_failed`, `delivery_failed`, `persistence_failed`, `uncorrelatable`) |
| `hl7gw_message_duration_seconds` | histogram | channel. Measured from frame read to ACK written. |
| `hl7gw_delivery_attempts_total` | counter | channel, origin (`inbound`, `replay`), result (`success`, `failed`), status_code (empty when there was no response) |
| `hl7gw_delivery_duration_seconds` | histogram | channel. One observation per HTTP attempt. |
| `hl7gw_acks_total` | counter | channel, code (`aa`, `ae`, `ar`) |
| `hl7gw_messages_in_flight` | gauge | channel |
| `hl7gw_connections_open` | gauge | channel |
| `hl7gw_connections_total` | counter | channel, close_reason (the `connection closed` reasons, plus `tls_handshake_failed`) |
| `hl7gw_accept_paused` | gauge | channel. Reads 1 while the channel is at its connection limit. |
| `hl7gw_tls_handshake_failures_total` | counter | channel |
| `hl7gw_tls_cert_expiry_timestamp_seconds` | gauge | channel. The NotAfter time of the served server certificate. |
| `hl7gw_persistence_errors_total` | counter | channel, operation |
| `hl7gw_build_info` | gauge (always 1) | version, commit, go_version |

The standard Go runtime (`go_*`) and process (`process_*`) collectors are
also registered.

A stalled downstream shows up as a rising `hl7gw_messages_in_flight` and a
climbing `hl7gw_connections_total{close_reason="timeout"}`. That's because
backpressure holds the sender instead of returning an error.

## Health

- `GET /healthz` answers `200 ok` whenever the process is serving HTTP. It
  checks nothing else, so an orchestrator only restarts the gateway when it's
  wedged.
- `GET /readyz` answers `200 ok` when every channel's journal database
  answers a query. If one doesn't, it answers `503` with problem details
  naming the channel. The downstream API is deliberately not probed: the
  gateway's job is to accept and journal messages whether or not the
  downstream is reachable, and downstream health is visible in
  `hl7gw_delivery_attempts_total`.

On SIGTERM, the admin listener closes within milliseconds while in-flight
MLLP work drains. Probes therefore see a refused connection rather than a
`503` during the drain. Orchestrators treat both as not ready.
