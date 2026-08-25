# ST frontend test corpus

Fixtures for the `.st` frontend described in
`docs/dev/MODCOMPILE_ST_FRONTEND.md`. Four tiers, because a fixture is only
useful if it is clear what "passing" means for it.

| Tier | Directory | Contract |
| --- | --- | --- |
| Parse | `parse/` | Parses without a syntax error. Says nothing about whether it is a valid program. |
| Accept | `accept/` | Parses, passes sema, and lowers to the shared IR. |
| Reject | `reject/` | Produces a specific diagnostic, asserted by message and position — not merely "fails". |
| Golden | `golden/` | Compiles to a `.so`, loads into `stmakd`, and behaves as the accompanying HAL test asserts. |

## The accept tier is specification by example

`accept/` is first-party and doubles as the readable definition of the dialect:
what a stratuMAK ST component looks like, written out rather than described.
Each file is a component someone might plausibly write, not a syntax exercise,
and each is checked to declare a PROGRAM, carry a summary for docgen, and
expose some HAL surface -- a fixture that stops being a component has drifted
from its purpose, and a parse-only test would not notice.

They earn their keep three times: today they must parse, from phase 2 they must
pass sema, and from phase 3 they compile, load and run as golden fixtures.

`thirdparty/` holds imported corpora, each in its own directory with the
upstream licence and a README recording provenance and what was excluded. Those
files are test input only — never compiled into, linked with, or shipped in any
binary — and each import's licence must also appear in `debian/copyright`,
which covers the source package even though tests reach no customer.

## Writing rejection fixtures

A `reject/` case asserts the message, not just the failure. A fixture that
passes because the compiler crashed, or because an unrelated earlier error
fired, is worse than no fixture: it goes green while the diagnostic it was
written for silently stops working. Assert the text and the position.

## On borrowing from other compilers

Test *ideas* carry no licence; test *files* do. MatIEC (GPL-3), RuSTy and
iec-checker (LGPL-3) are all worth reading for coverage — MatIEC's
`tests/syntax/identifier` matrix, which probes the same identifier used as a
datatype, function, function block, label, program, enumeration value and
variable, is a good model for the case-insensitive scoping rules in
`MODCOMPILE_ST_FRONTEND.md` §5.2 — but reimplement the idea here rather than
copying the files. A reimplementation also gets to use our own attribute syntax
and diagnostics, which the originals cannot.
