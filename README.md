# hl7gw

A lightweight, self-hosted HL7v2 gateway that's delightfully simple to run and operate.

- **One self-contained binary.** No dependencies to wrangle, no database servers, nada.
- **One simple config file.** JSON, with secrets read from files.
- **Observable by default.** Structured logs, Prometheus metrics and health probes with no setup.

## Getting started

**1. Download the binary**

```sh
curl -fsSL "https://github.com/bltn/hl7gw/releases/latest/download/hl7gw_$(uname -s)_$(uname -m).tar.gz" | tar -xz
```

**2. Set up your config file**

hl7gw runs against a single JSON file that defines the admin API and one or more channels. The quickest way to get one is the interactive wizard, which asks a few questions, writes `hl7gw.json` and generates an admin API token into `admin.token`:

```sh
./hl7gw configure
```

Or write it yourself. Example below:

```json
{
  "admin": { "port": 8081, "token_file": "admin.token" },
  "channels": [
    {
      "name": "adt",
      "source": { "port": 2575, "idle_timeout": "1m", "tls": { "mode": "off" } },
      "destination": {
        "url": "https://your-api.example.com/hl7",
        "auth": { "type": "bearer", "token_file": "adt.token" }
      }
    }
  ]
}
```

See [docs/configuration.md](docs/configuration.md) for every option.

**3. Run it**

Put each destination's bearer token in its file, then start the gateway:

```sh
echo your-api-token > adt.token
./hl7gw run -config hl7gw.json
```

**4. Explore the admin API**

For now, at least, the admin API (gated by the token in `admin.token_file`) is the best way to operate a running instance.

Run the binary and then open [http://localhost:8081/docs](http://localhost:8081/docs) for the full API reference, with a description of every endpoint.

## Message flow

```mermaid
sequenceDiagram
    participant S as Sender
    participant G as hl7gw
    participant J as Journal (SQLite)
    participant A as Your API
    S->>G: HL7v2 via MLLP over TCP
    G->>J: write raw message
    G->>A: POST XML
    A-->>G: 200
    G-->>S: ACK (AA)
    Note over S,G: the next message is read only after the ACK
```

The main thing to be aware of is that, for now, hl7gw only has one ACK model: on downstream delivery (or failure to do so). There's no ACK'ing as soon as the message arrives/has been persisted. This is deliberately conservative. What you lose in maximum throughput, you gain in simplicity and reliability:

- **No queues.** Nothing to run, size or drain.
- **An ACK is a real ACK.** `AA` means delivered, not "accepted, will forward
  later".
- **Built-in backpressure.** Senders slow down to your API's pace on their
  own.

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md): the full message lifecycle, failure behaviour, design trade-offs and a map of the code.
- [docs/configuration.md](docs/configuration.md): every config dial, plus hard-coded limits.
- [docs/observability.md](docs/observability.md): structured logs, Prometheus metrics and health probes.
- The admin API reference is served by a running instance at
  [http://localhost:8081/docs](http://localhost:8081/docs).

## Development

```sh
make build        # bin/hl7gw
make configure    # go run . configure -output hl7gw.json
make run          # go run . run -config hl7gw.json
make check        # lint, unit + e2e tests with a 100% coverage gate & short fuzzing run
make e2e_tests    # black-box tests against the built binary
make fuzz FUZZTIME=30s
make dist         # release archives + checksums into dist/
```

### Releasing

Bump the tag in `.release-version` (e.g. `v0.1.0`) and merge to `main`. Once
`CI` passes on that push, the `Release` workflow tags the commit and publishes
the archives; if that version is already released it does nothing. The workflow
can also be run by hand from the Actions tab against `main`.
