# Phase 5 review findings — services & auxiliaries (second half)

Tier-2 adversarial review, 2026-07-22, of the Phase-5 modules that the
2026-07-21 network-modules pass did **not** cover:

| Module | LOC | Coverage before → after |
|---|---|---|
| `internal/persist_sqlite` | 334 | 10.3 % → 86.0 % |
| `internal/tooltable` | 354 | 2.1 % → 89.0 % |
| `internal/emccalib` + `internal/calibreg` | 330+46 | 9.1 % / 100 % → 43.2 % / 100 % |
| `internal/halstream` | 94 | 100 % → 100 % (was already covered; the new surface is too) |
| `cmd/halsampler`, `cmd/halstreamer` | 146+142 | 0 % (end-to-end via `tests/ws-stream`) |

The network half (`apiserver`/`halrest`/`inirest`/`mqttbridge`/`halscope`) is in
`NETWORK_MODULES_REVIEW_FINDINGS.md`; its open tail is N7 and N9.

**Lenses applied.** The same untrusted-wire lens as the network pass — all four
service IDLs (`persist.gmi`, `tooltable.gmi`, `emccalib.gmi`, `halscope.gmi`) are
`@rest_export true`, so their arguments arrive from the unauthenticated loopback
surface — plus the standing transferable risk classes (goroutine ownership, 2.9
edge parity, fixed-but-untested) and, for the two file-format parsers, a
line-by-line diff against the C they replace.

All findings below are **fixed** unless marked otherwise. Each fix carries a
regression test; the ones marked *mutation-verified* were confirmed by reverting
the fix and watching the new test fail.

---

## HIGH

### E-1 — emccalib: the tunable index aliased an orphaned array
`internal/emccalib/module.go` (was line 88). **CONFIRMED.** `[FIXED]`

The index was built as `e.index[key] = &e.tunables[len(e.tunables)-1]` *inside*
the append loop. Every pointer captured before a reallocation aliases the old
backing array, so the lookup path and the iteration path silently diverge for
all but the last few entries — reproduced directly: with four tunables, writing
`99` through the slice leaves the first two index entries reading `0` and `1`.

The visible failure is **Revert**. `SaveIni` writes the value it just persisted
into `e.tunables[i].iniValue`, but `Revert` read `iniValue` through the index —
so "revert" kept restoring the value from process start no matter how many times
the operator had saved. On a machine being tuned live, revert is the escape
hatch, and it was quietly pointing at stale data.

Fixed by storing an `int` position and reading through a `lookup()` helper that
documents the locking contract. The class is now unrepresentable: there is no
pointer to go stale.

### T-1 — tooltable: a malformed `.tbl` field became a zero offset
`internal/tooltable/import_tbl.go` `parseTblLine`. **CONFIRMED.** `[FIXED]`
*mutation-verified*

Only the `T` and `P` conversions were checked. Every offset — `X Y Z A B C U V
W D I J` — used `v, _ := strconv.ParseFloat(...)`, so an unparsable field became
**0.0** and the line was still imported as a valid tool.

The C parser this replaces (`src/emc/sai/sai_tooltable.cc` `parse_tool_line`,
derived from 2.9's `tooldata.cc`) does `if (!valid) return -1` after every
`sscanf`: one malformed field rejects the whole line. That difference matters
because of what the zero means — a zeroed **tool-length offset** is a tool
driven into the work. A corrupt or hand-edited tool table should lose the tool,
not silently relocate it to Z0.

Now every conversion is checked and a bad field rejects the line, matching the
C. A rejected line is logged (path, line number, text, reason) rather than
dropped in silence: this is a one-shot migration of the operator's tool data,
and a tool that vanishes without a word is only discovered when it is called up.

### T-4 — tooltable: every lookup of an unstored tool failed
`internal/tooltable/module.go` `GetTool`. **CONFIRMED.** `[FIXED]`

`GetTool` of a tool not in the table returned `unexpected end of JSON input`
instead of the documented zero entry. Found by writing the first end-to-end test
of the module.

The not-found branch keyed off the error text (`strings.Contains(err.Error(),
"not found:")`), matching persist's message. That branch was **unreachable**: the
in-process GMI client cannot report an error at all for a func returning a struct
(see G-1 below), so `err` is always nil and the code fell through to
`json.Unmarshal("")`.

Detection now keys off the empty value, which is unambiguous here because
tooltable only ever stores JSON. The general problem is G-1.

---

## MEDIUM

### P-1 — persist_sqlite: unbounded namespace growth from the REST surface
`internal/persist_sqlite/module.go` `Open`. **CONFIRMED.** `[FIXED]`
*mutation-verified*

`persist.gmi` gives `open` `POST /{namespace}`, so the namespace argument comes
off the wire. Every distinct name created a `<ns>.db` plus its `-wal`/`-shm`
sidecars, an `*sql.DB` with its own connection pool, and a **permanent** slot in
`m.handles` — with `Close` a no-op and nothing bounding the count. A caller
walking the name space exhausts file descriptors and disk.

Added `maxNamespaces` (256 — real configs use a handful, one per consumer per
instance, see `configs/sim/axis/multiinst`) and `maxNameLen` (64, so an over-long
name is an honest rejection at the door instead of an `ENAMETOOLONG` surfacing
from inside sqlite). An already-open namespace still resolves at the limit: the
cap bounds growth, it does not lock out existing consumers.

### P-2 — persist_sqlite: `delete_all`/`open` cycling grew the handle slice
`internal/persist_sqlite/module.go`. **CONFIRMED.** `[FIXED]` *mutation-verified*

`DeleteAll` nils the db but left the slot behind, and `Open` only ever appended.
Both verbs are REST-reachable, so cycling the pair grew `m.handles` without bound
while the number of live namespaces stayed at one. `Open` now reuses a vacated
slot before growing.

### T-3 — tooltable: a transient read error replayed the legacy `.tbl`
`internal/tooltable/module.go` `Start`. **CONFIRMED.** `[FIXED]`

`entries, _ := m.db.GetEntries(...)` discarded the error, so "could not read the
namespace" was indistinguishable from "the namespace is empty" — and empty
triggers the one-shot legacy import, which upserts the shipped `.tbl` over the
live table. A transient failure at startup therefore silently reverted every
tool offset edited since the migration. A namespace we cannot read is not a
namespace we may overwrite; the error now fails `Start`.

### T-5 — tooltable: `Start` published the persist client unsynchronised
`internal/tooltable/module.go`. **CONFIRMED.** `[FIXED]`

`m.db` and `m.dbHandle` were written without the lock the four API methods read
them through. On the runtime REST load path the API server is already serving
while `Start` runs (launcher `loadModuleNamed`: construct → register → Start),
so this races every handler. Both fields are now published together under
`m.mu`, and only once the namespace is open.

### T-6 — tooltable: a request between construction and `Start` hit a nil client
`internal/tooltable/module.go`. **CONFIRMED.** `[FIXED]`

Same window as T-5, different failure: the API is registered in the constructor
but the client is bound in `Start`, so a request landing in between dereferenced
nil. `net/http` recovers per-request so the process survived with a 500, but an
honest error beats an unwound goroutine. All four API methods now go through
`ready()`.

### E-3 — emccalib: a stale line number overwrote an unrelated INI key
`internal/emccalib/module.go` `updateINIFile`. **CONFIRMED.** `[FIXED]`
*mutation-verified*

The rewrite located each value purely by its recorded line number. Source lines
are captured when the INI is parsed at startup and a save can come much later,
so an INI edited on disk in the meantime had a *different* key silently
overwritten with a tuning value. The rewrite now confirms the line still holds
the key it recorded, and warns instead of writing when it does not.

### E-4 — emccalib: saving destroyed inline comments
`internal/emccalib/module.go` `replaceINIValue`. **CONFIRMED.** `[FIXED]`
*mutation-verified*

`P = 100 # tuned 2024-03, do not raise` became `P = 150.5`. The operator's own
annotations are frequently the only record of *why* a value is what it is.

The comment is now re-attached, split at exactly the boundary the INI parser
reads at. That rule is subtle — `#` counts only when preceded by whitespace (so
the hex colour `#ff0000` survives) and `;` is data, never a comment (so a
`;`-chained `MDI_COMMAND` survives) — and it was already written down once, in
`pkg/inifile`. Rather than copy it, `stripInlineComment` was refactored into an
exported `SplitInlineComment` that both the reader and the writer use, so the two
cannot drift.

### S-1 — halsampler: a zero-pin config made it spin forever
`internal/halstream/stream.go` `ParseHeader`. **CONFIRMED.** `[FIXED]`

A `cfg:` header declaring no pins makes every sample zero bytes wide, and
halsampler's frame walk is `for off+sampleSize <= len(data); off += sampleSize`
— with `sampleSize == 0` the condition always holds and the offset never
advances, so it printed blank lines forever. The header is the only place that
can tell, so `ParseHeader` now rejects an empty type list and both clients exit
with `unexpected header`.

---

## LOW

### T-2 — tooltable: a lowercase `.tbl` imported as an empty table
`internal/tooltable/import_tbl.go`. **CONFIRMED.** `[FIXED]` *mutation-verified*

The C matches keys through `toupper(token[0])`, so a lowercase tool table loads.
Matching only uppercase dropped every field and then rejected the line for having
no tool number.

### T-7 — tooltable: an unparsable entry vanished from `ListTools` in silence
`[FIXED]` — now logged. A corrupt record must not take the whole listing down,
but a tool that is simply gone from every UI should not be a silent event.

### E-2 — emccalib: `SetPin`/`Revert` used the tunable after unlocking
**CONFIRMED.** `[FIXED]` Both unlocked `e.mu` and *then* read fields through the
`*tunable`, concurrently with `SaveIni` writing `iniValue` through the same
elements. The needed values are now copied out under the lock.

### E-5 — emccalib: registered under a hard-coded instance name
**CONFIRMED.** `[FIXED]` `RegisterEmccalibAPI(reg, "emccalib", e)` ignored the
instance name, so `load emccalib <mycalib>` published its API at the wrong
address and a second instance collided with the first instead of getting its own.
Multi-instance gomods are a live pattern here (`configs/sim/axis/multiinst` loads
two `persist_sqlite` and two `tooltable` instances). Now uses `name`, like every
other gomod. The shipped webapp client defaults to the instance name `emccalib`,
which is what `load emccalib` still produces.

### S-2 — halstream: `ReadRaw` had no bounds check
**CONFIRMED.** `[FIXED]` `frame[i*8:]` panicked on a truncated frame. halsampler
happens to guard its own walk, but a shared wire-decoding helper must not depend
on every caller getting that right. Out-of-range reads now return 0.

### S-3 — halstreamer: `#` comment lines were not skipped
**CONFIRMED.** `[FIXED]` The classic halstreamer and the filestream cmod both
skip them, so replaying a capture file with a header comment aborted with
`expected N values, got 1`.

### S-4 — `httpToWS` duplicated in both CLIs
**CONFIRMED.** `[FIXED]` Byte-identical in halsampler and halstreamer. Moved to
`internal/halstream` — the package that exists precisely to stop those two
drifting apart.

### P-3 — persist_sqlite: `Close` is a no-op, and the IDL says otherwise
**CONFIRMED — documentation.** `[PARTLY FIXED]`

The no-op is **correct** and now says why: handles are *shared* (`Open` hands
the same handle to every caller of a namespace, so honouring one consumer's
`Close` would pull the database out from under the others), and `close` has no
`@method`/`@path` in `persist.gmi`, so it is not REST-reachable and no in-process
consumer calls it. A real close would need refcounting, which buys nothing while
the module's lifetime is the process's.

**Still open:** `persist.gmi`'s own comment on `close` promises "releases
resources", which is now the only place stating something untrue. Not changed
here because editing the IDL regenerates code across packages, which does not
belong in the same commit as a behaviour review. One-line follow-up.

---

## Open / referred

### G-1 — a GMI data-returning call cannot report failure
`internal/gmicompile/cgen` (`--client-go`), surfaced via T-4. **CONFIRMED.**
`[OPEN — architecture, needs a ruling]`

For a func returning a struct, the generated in-process client ends with a
literal `return result, nil`:

```go
func (cl *PersistClient) GetEntry(handle int32, key string) (Entry, error) {
        out := C.call_persist_get_entry(cl.cb, cHandle, cKey)
        ...
        return result, nil          // <- the error is structurally unreachable
}
```

The C callback returns the struct **by value** with no `rc` out-param, so there
is nowhere for a failure to travel. Void-returning funcs are fine — they get an
`rc` and do report errors (`emcio`: `return fmt.Errorf("io_abort: rc=%d", rc)`).
The split across the tree: **23** struct-returning client methods hard-code
`nil` (persist 8, motstat 10, tooltable 4, emcio 1); `motctl` has none.

Consequences beyond T-4: every in-process consumer of `persist` (tooltable,
milltask, ngcpreview) is blind to storage failures. A sqlite error and a missing
row are the same event to the caller — precisely the confusion the comment at
`GetTool` was written to prevent, describing an intent the boundary cannot
deliver.

This is the same class as the already-tracked "Surface RCS command errors to
clients" cross-cutting item: an error contract that has to be decided once and
applied at the generator, not worked around per module. tooltable is now correct
for *its* data shape (empty value ⇒ absent); that reasoning does not generalise
to a type whose zero value is legitimate. Options are an `rc` out-param on
struct-returning callbacks, or an explicit result-wrapper type in the IDL —
generator work either way, and it touches the C ABI, so it needs a ruling before
anyone starts.

Worth noting against the record: `PRODUCTION_READINESS.md` currently states "No
open gmicompile findings remain". This is a new one, found by testing a
*consumer* rather than the generator — which is roughly the argument for having
done the consumer pass.

### Deliberately not changed

- **`persist_sqlite` serialises all DB work under one `RWMutex`** even though
  `*sql.DB` is a safe concurrent pool, so a slow query blocks every other
  namespace on that instance. It is also what makes the handle-slice access safe.
  Correct as written and well within budget for a config store; noted so the next
  reader does not mistake it for an oversight.
- **`emccalib.updateINIFile` is not atomic** (`os.Create` truncates, then
  writes). A crash mid-write leaves a truncated INI — but a `.bak` with the
  pre-save contents is written first, and that is now pinned by a test. A
  write-temp-then-rename would be strictly better; recorded, not done, because it
  changes the backup story the operator may already rely on.
- **`GetTunables` holds `e.mu` across one `halcmd.GetP` cgo call per tunable.**
  Bounded by the tunable count and only reachable from a REST handler.

---

## Verification

Per-module fast gate, on every commit: `go build ./...`, `go vet`, `gofmt -l`,
`go test -race`. All clean.

Coverage: `persist_sqlite` 10.3 → 86.0 %, `tooltable` 2.1 → 89.0 %, `emccalib`
9.1 → 43.2 %, `halstream` 100 % before and after. `emccalib` stays lowest because
`GetTunables`/`SaveIni` read live HAL pins through `halcmd.GetP`; the pure logic
they wrap — the index, the INI rewrite, the comment split — is covered.

Mutation-verified (fix reverted, new test observed to fail): T-1, T-2, P-1, P-2,
E-3, E-4.

**Owed at the phase checkpoint:** a full runtests round. Three tests exercise
this code end-to-end and none of them are unit tests — `tests/ws-stream`
(halsampler ↔ halstreamer loopback, which S-3 and S-4 touch), the
`configs/sim/axis/multiinst` instances (two `persist_sqlite`, two `tooltable`),
and any config with a legacy `.tbl` to migrate (T-1/T-2/T-3).
