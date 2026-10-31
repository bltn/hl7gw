# Architecture

This document covers how a message moves through hl7gw, why it's shaped the
way it is, and where things live in the code.

## Message lifecycle

Each connection handles one message at a time:

1. **Read** one MLLP frame.
2. **Persist** the raw message to the channel's SQLite database. This happens
   before parsing, so even a malformed message is kept.
3. **Parse** the message and convert it to XML. If the header is readable
   but the rest isn't, the sender gets `AE` and nothing goes to your API. If
   the header itself can't be read, there's no control ID to reply to, so
   no ACK is sent.
4. **Deliver** the XML to your API, retrying once.
5. **Choose the ACK.** `AA` means it was delivered. `AE` means it couldn't be
   parsed or delivered. `AR` means persisting it failed: the message
   isn't safely stored, so the sender should resend it.
6. **Write the ACK**, persist it alongside the message, and go back to step 1.

Connections and channels run in parallel. Within a connection, messages are
handled in the order they arrive.

### What happens when…

- **Your API is slow.** The sender waits for its ACK. There's no internal
  queue to fill: the MLLP connection is the backpressure.
  `hl7gw_messages_in_flight` rises while this is happening.
- **Your API is down.** Each message is persisted, tried twice and answered
  with `AE`. hl7gw doesn't redeliver it on its own. Either the sender
  resends it or you replay it with `hl7gw replay`.
- **Too many senders connect.** Each channel accepts up to `max_connections`
  (64 by default). At the limit it stops accepting until a connection closes,
  and `hl7gw_accept_paused` reads 1.
- **A sender goes quiet.** An idle connection is closed after
  `idle_timeout`. A frame that starts but doesn't finish within 30 s
  also closes the connection.
- **The process crashes or loses power.** Every message is committed to disk
  (SQLite WAL, `synchronous=FULL`) before it's parsed, so nothing a sender
  got an ACK for is lost. A message caught mid-flight gets no ACK, so the
  sender resends it, and it may then be persisted twice.
- **You deploy (SIGTERM or SIGINT).** Listeners stop accepting, idle
  connections close, and the socket and metrics port close. Messages already in progress finish delivery and get
  their ACK, but a failed delivery isn't retried. A 20-second hard limit
  applies, after which the remaining work is abandoned and the process exits.
- **The database grows.** Rows older than `retention` (30 days by default)
  are deleted at startup, before the listeners open, and then hourly,
  delivery attempts included. The file doesn't shrink on its own; SQLite
  reuses the space.

## Design decisions

### Acknowledge after delivery, not on receipt

The ACK is written only once your API has answered. That gives `AA` one
simple meaning: your API has the message. Recovering from a downstream outage
then relies on the retry logic every MLLP sender already has, so there's no
second queue for the operator to run and monitor.

**Cost:** your API's latency becomes the sender's latency, and an outage turns
into a stream of `AE`s that senders must resend.

### No internal queue: backpressure comes from the protocol

Each connection reads a frame, processes it to completion, writes the ACK,
and only then reads the next frame. A slow downstream therefore slows every
sender, and memory doesn't grow while it does. Per-connection ordering comes
for free. The only capacity limit is `max_connections`, which pauses
accepting new connections at the limit instead of refusing them.

**Cost:** throughput per connection is bounded by your API's latency.
Throughput scales with the number of sender connections, not with buffering.

### Persist first

The raw message is committed before it's parsed, so malformed messages are
still kept for inspection and replay. If that write fails, the sender gets
`AR`, so an `AA` is never sent for a message that isn't on disk. SQLite runs
in WAL mode with `synchronous=FULL`, so a committed row survives a crash or a
power loss.

**Cost:** there are several fsyncs per message (the message, its transform,
each delivery attempt, and the ACK).

### One SQLite database per channel

There's no database server to install, and channels are isolated from each
other. Readiness is reported per channel (`/readyz` names the failing one),
and a channel's database can be moved or archived on its own.

**Cost:** hl7gw runs on a single node. Running several instances means
running several independent sets of databases.

### No message semantics

hl7gw moves messages; it doesn't interpret them. MSH-10 is treated as the
sender's correlation handle: it's carried through to your API and echoed in
MSA-2, but never used for deduplication, ordering or routing. Idempotency
belongs in your API, which owns the processing.

### One generic XML shape

Every message becomes `<HL7Message>` with numbered elements for each
segment, field, component and subcomponent. There are no per-version or
per-message-type schemas to maintain, and your API picks out the fields it
needs.

### Secrets live in files

The config file names token and password files and never holds secrets, so it
can be committed and reviewed like any other file. A missing or empty secret
file stops startup, so a misconfiguration can't fail later at runtime.

### Operate through the gateway, from the shell

The gateway is the only process that opens the channel databases. The
operator subcommands (`messages`, `replay`, `process`) are clients of it over
a unix socket in `data_dir`, so access is the file permissions on that
directory, and a replay or a test message runs through the same pipeline,
metrics and log stream as live traffic. The socket is also the gateway's
lock on the data directory: a second instance pointed at it refuses to start.

**Cost:** the subcommands need the gateway running.

## Code map

```
main.go                 subcommand dispatch (run, configure, messages, replay, process),
                        flags and signal handling
internal/gateway        composition root: wires config, stores, listeners and the metrics port;
                        maps a processing outcome to an ACK code
internal/ops            the operator subcommands: the unix-socket server inside the gateway
                        and the CLI client that calls it
internal/httpserve      runs an http.Server under the errgroup with ctx-driven shutdown
internal/configure      the interactive wizard behind `hl7gw configure`; writes a config
                        that config.Load accepts
internal/config         loads and validates the JSON config, reads secret files, loads TLS
                        material and hot-reloads it
internal/console        terminal detection, colour and the text log handler
internal/source         MLLP listeners: accept loop, connection cap, TLS handshake,
                        the per-connection read → handle → ACK loop, shutdown drain
internal/mllp           MLLP framing
internal/hl7            parsing, MSH access, XML encoding, ACK construction
internal/processor      the pipeline: persist → parse/transform → deliver; also replay and dry run
internal/delivery       the HTTP client (one retry) and recording of delivery attempts
internal/store          per-channel SQLite persistence
internal/retention      the retention sweep: once at startup, then hourly
internal/record         what's persisted about a message and its delivery attempts
internal/metrics        Prometheus collectors; serves /metrics and the health probes
internal/shutdown       the drain deadline
internal/tls            certificate and CA pool loading
e2e/                    black-box tests that drive the built binary
```

The pipeline runs in `processor.ParseTransformDeliver`. MLLP connections and
`hl7gw process` both call it, so the two entry points can't drift apart.
