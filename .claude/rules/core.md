# Core coding conventions

These govern all Go code in this repo. Review your changes this turn against
them before finishing.

## Comments

Do not add comments to code unless explicitly told to do so.

## Deep modules

We are fond of deep modules: a small, simple interface in front of
substantial functionality. The interface is the cost — every caller and
every reader pays it — while the implementation behind it is paid for once.
Judge a module by that ratio. A wide surface over a thin implementation is
a shallow module; so is an interface that makes callers ferry state or
repeat setup the module should own.

Symptoms to hunt:

- Pass-through parameters: state threaded through layers that only hand it
  on. Bake it in at construction so intermediate signatures stay small.
- Per-call setup of things with construction lifetimes (clients, buffers,
  derived config) — the call site is doing the module's job.
- The same few lines repeated at every call site: the interface is too
  shallow there; push the pattern down.

Depth is not indirection: a wrapper that merely renames an existing call
adds surface without hiding anything. Deleting it is the deep-module move.

## Boundaries

Prefer compiler-enforced seams over convention. When code needs a boundary
(test infra vs tests, subsystem vs callers), put it in its own package and
let the export list be the contract — don't rely on naming or discipline to
keep callers out of internals. Keep export lists minimal; unexported is the
default.

## API design

When requirements widen, stop and rework the existing API surface to cover
the new case — do not spawn a new entry point shaped around the exact case
at hand. Case-shaped additions compound: divergent functions that almost do
the same thing, ever harder to reason about or keep clean. Two entry points
for one operation is the smell to hunt.

Design for the callers that exist — plain arguments over option machinery,
explicit call sites over baked-in defaults — but design the surface as a
whole, not per-request. Under-abstraction is a failure mode too: without a
cohesive structure the system accumulates duplication and latent risk until
it falls in on itself.

## Verbosity

Line count is surface area for defects, not safety. Extra scaffolding,
wrappers, and repeated plumbing make code harder for a human to verify and
invite the same thing being duplicated again and again. Prefer the smallest
implementation that states its intent plainly; verbosity that doesn't add a
check or a guarantee is a cost.

## Dependencies

Don't add a dependency for something we can write or already own in a few
dozen lines. Before reaching for a library, check whether an existing
primitive in this codebase can be evolved to fit. Owning a small, specific
mechanism beats configuring a general one.

## Errors

Discriminate errors by matching, never by string. To pull a typed error out
of a return, use the generic `errors.AsType[*T](err)` — it returns
`(*T, bool)`, needs no pointer-to-a-nil dance, and unwraps the chain. Reach
for it over `errors.As` (older, reflective) and over a bare `err.(*T)` type
assertion (skips unwrapping); errorlint enforces this. Use `errors.Is` for
sentinel values.

Pick the carrier by what the caller needs. When the caller acts on data the
failure carries, define a typed error that holds it (as `CorrelatableError`
holds the MSH the ACK path echoes). When the caller only classifies, a
sentinel `var Err… = errors.New(…)` is enough. Design the return as a total
set: every error a function yields should be one of its declared kinds, so
callers can branch exhaustively without a dead "unexpected" arm.

A module speaks one dialect for its discriminable outcomes across its whole
exported surface — an outcome enum, sentinels, or typed errors — chosen by
what callers do with the results. Outcomes from one method and sentinels
from a sibling force callers to switch discrimination styles mid-flow and
split the total set across two vocabularies; when a new entry point wants a
different dialect, that is the cue to rework the surface, not to mix.

## Pure helpers, caller-owned policy

A resolve/lookup/parse helper returns data plus a discriminable outcome
(sentinels or typed errors forming a total set) — it does not act on the
caller's behalf. Writing an HTTP response, logging-and-continuing, or
otherwise deciding the reaction inside the helper hides control flow at
the call site and locks every caller into one policy.

Don't over-correct: a module whose job *is* the effect (a writer, a
recorder) should own it fully, and when several callers map the same
outcomes to identical reactions, that shared mapping can become its own
named function — caller-owned policy means callers choose, not that they
re-type the same choice.
