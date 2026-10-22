# Testing conventions

Applies when a `*_test.go` file changed this turn.

E2E tests exercise the binary's external contract and must stay an
independent oracle: they hand-roll their inputs (config files, wire frames,
expected ACKs) rather than importing production code to generate them. If a
contract change breaks an e2e test, that's the test doing its job — update
it deliberately, never by re-deriving expectations from the code under test.

Complete HL7 payloads and their expected outputs (messages, ACKs,
transformed XML) live as e2e/testdata files embedded into the tests, not as
string literals in test code. Inline literals are for the cases a file
cannot serve: payloads built around runtime values (a rotating control ID,
a generated giant field), and intentionally broken fragments where the
exact bytes are the case's subject and belong beside the case name.

Coverage belongs at the e2e level. When a behaviour genuinely cannot be
reached by an e2e test (no pty for the binary, an injected I/O fault, a race
only an in-process harness can force), keep the unit test and name that
behaviour in the PR description with the reason e2e could not cover it.
