# Mirth Connect (NextGen Connect) HL7v2 test fixtures

- Source: https://github.com/nextgenhealthcare/connect
- Tag: `4.5.2` (commit 1835dca44426ef99b3ce65f860580c52c51034a2), the last
  release published under the Mozilla Public License 2.0
- License: MPL-2.0 (see `LICENSE` in this directory)

Every file under `messages/` is a byte-for-byte copy of the upstream file at
the same repository-relative path. Line endings (CRLF or LF, as upstream has
them) are kept; the corpus test normalizes them when it frames each message
for MLLP, as any sender would.

Included:

- `donkey/src/test/java/com/mirth/connect/donkey/test/hl7/{small,large}.hl7`
- `server/tests/filereader/**/*.hl7` (the batch files hold several messages;
  the test splits them at each `MSH` line, as Mirth's batch reader does)
- `server/tests/test-hl7-*-input.txt`, including `test-hl7-batch-input.txt`,
  whose `FHS`/`BHS`/`BTS`/`FTS` envelope segments the test drops
- `server/tests/test-hl7-1636-output.txt` and the three
  `test-hl7-missing-*-output.txt` files: although named as outputs, each is a
  complete HL7 message
- `simplesender/samples/TestADT.txt` (124 ADT messages in one file)

Excluded, and why:

- `server/tests/test-hl7-single-field-output.txt` and
  `server/tests/test-hl7-single-segment-output.txt`: not messages (a lone
  field value and a lone PID segment)
- `simplesender/samples/hl7.txt`: SimpleSender's own `[SOM]`-delimited format,
  not raw HL7
- `server/tests/filereader/containsempty/!notempty/01.hl7`: identical to
  `server/tests/filereader/nonbatch/01.hl7`
