# CLAUDE.md

## What this is

See [README.md](README.md) for what hl7gw is, what it does today, and where
it's headed.

## Coding conventions

The code-authoring conventions for this repo (deep modules, boundaries, API
design, verbosity, dependencies, errors, comments, and the testing/e2e rules)
live in `.claude/rules/` — `core.md`, `tests.md`, `e2e.md`. They are enforced
by a `Stop` review gate (`.claude/hooks/review-gate.sh`, wired in
`.claude/settings.json`): when a response touches Go code, the gate injects the
relevant rules and forces one review pass against them before finishing. Read
those files before writing code here.

## Pull requests

When a ticket is speculative or carries an open question — "is X right?",
"should we do Y?", "check whether Z" — the PR description must state the take
you ran with: the answer (yes/no/the decision), and the reasoning that got
you there. Don't leave the question hanging for the reviewer to re-derive;
land on a position and show your work.

Coverage lives in e2e tests. If anything in the PR is covered only by a unit
test, the description names it and says why e2e couldn't reach it (see
`.claude/rules/tests.md`).
