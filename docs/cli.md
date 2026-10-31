# Operator subcommands

The subcommands talk to the running gateway over its unix socket (`socket` in
the config, `<data_dir>/hl7gw.sock` by default). They read the config file to
find it: `hl7gw.json` in the current directory, or `-config <path>`. A relative
socket path is resolved against the config file's directory, so with `-config`
the subcommands work from any directory.

```sh
hl7gw messages [-config <path>] [-pretty] [-limit <n>] [-before <id>] <channel> [<id>]
hl7gw replay   [-config <path>] [-pretty] <channel> <id>
hl7gw process  [-config <path>] [-pretty] [-dry-run] <channel> [<file>]
```

Results are JSON on stdout. A failure is an `error: …` line on stderr. The
exit code is 0 on success, 1 when the command fails, 2 for a usage error.

`-pretty` prints a readable view instead of JSON: a table for the list, and
for a record, sections for the raw message (one segment per line), the
transformed XML, the ACK, and each delivery attempt. An attempt whose payload
is the record's transformed XML says `same as transformed` rather than
repeating it. In the raw message and ACK, control characters other than the
CR segment terminator are shown escaped (`\n`, `\t`, `\x0b`), so a sender's
wrong line endings stay visible.

## messages

`messages <channel>` lists persisted messages, newest first, one compact JSON
object per line. `-limit` caps the count (default 50, at most 500);
`-before <id>` lists only messages with a lower id.

| field | |
|---|---|
| `id` | Row id, used by `replay` and `messages <channel> <id>`. |
| `received_at` | When the frame was read. |
| `control_id`, `message_type` | MSH-10 and MSH-9; empty when the header could not be read. |
| `transform_error` | `COULD_NOT_PARSE_HL7`, or `null`. |
| `attempts` | Delivery attempts recorded, inbound and replays. |
| `delivery` | Outcome of the last attempt, `SUCCESS` or `FAILED`; `null` when none. |
| `status_code` | HTTP status of the last attempt; `null` when the downstream did not answer. |

`messages <channel> <id>` prints the full record: raw message, transformed
XML, the ACK sent, and every delivery attempt with its payload and response.

## replay

POSTs the message's stored XML to the channel's destination again, records
the attempt with `origin: REPLAY`, and prints it. A message with no delivery
attempts cannot be replayed.

## process

Runs a raw HL7 message (from `<file>`, or stdin) through the channel's
pipeline: persisted, transformed, delivered. Prints the resulting record. No
ACK is recorded. Messages up to 16 MiB are accepted.

`-dry-run` parses and transforms only, and prints the result; nothing is
persisted or delivered.
