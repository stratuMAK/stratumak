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
`NETWORK_MODULES_REVIEW_FINDINGS.md`; it is **closed** (N7, N9 and the U/FP
coverage gap all landed 2026-07-22).

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

**CLOSED 2026-07-22.** `persist.gmi`'s own comment on `close` promised "releases
resources", which was the last place stating something untrue. It now says what
the call is — a caller releasing its interest — and why the sqlite provider makes
it a no-op (shared handles), while keeping it in the API for a provider that
hands out per-caller handles. The edit is comment-only: regenerating from the
old and new IDL produces byte-identical output, because a `#` comment is not
`@doc` and reaches no emitter.

---

## Open / referred

### G-1 — a GMI data-returning call cannot report failure
`internal/gmicompile/cgen` (`--client-go`), surfaced via T-4. **CONFIRMED.**
`[FIXED 2026-07-22 — @rc_error; see "Resolution" at the end of this section]`

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

**Scope (corrected 2026-07-22 after tracing the emitter).** **13** client methods
are structurally unable to report a failure: `persist` 8 (`Open`,
`GetNamespaces`, `GetEntries`, `GetEntry`, `SetEntry`, `DeleteEntry`,
`SetEntries`, `DeleteAll`), `tooltable` 4 (`ListTools`, `GetTool`, `PutTool`,
`DeleteTool`), and `emcio.GetStatus`. A further 6 `motstat` methods and
`motstat.GetAnalogInput` also end in `nil`, but those are `@returns_value` — the
`rc` *is* the answer — which is a deliberate contract, not this defect.

**The REST surface is NOT affected.** A generated REST `CommandMeta` handler
calls the Go *provider* directly (`result, err := impl.GetTool(...)`; `if err !=
nil { return nil, err }`) and never crosses the C ABI. Only **in-process GMI
consumers** are blind — and that includes the C ones.

Consequences beyond T-4 — the live in-process consumers, all blind:
`internal/tooltable` and `internal/halscope` → `persist`; `internal/task`
(milltask) and `internal/ngcpreview` → `tooltable`; and on the C side
`emc/iotask/ioControl_v2.c` → `tooltable` (5 `tt->get_tool(...)` call sites, on
the **tool-change path**) and `internal/task/interp_param_io_persist.c` →
`persist`. A sqlite error and a missing row are the same event to every one of
them — precisely the confusion the comment at `GetTool` was written to prevent,
describing an intent the boundary cannot deliver.

**G-2 (found while scoping G-1): the dispatch emitter treats an `out` param as
an input.** `cgen/dispatch_c.go` unmarshals the out param *from the request*,
passes it in, and marshals only the `rc` as the result — the value the provider
filled in is discarded (`motstatDispatchGetStatus` in
`generated/gmi/motstat/motstat_cgo.go` is the live example). Latent today because
the only IDLs using `out` are `@rest_export false`, but it is the direct blocker
for the recommended fix below.

#### RULING 2026-07-22 (user)

**Do G-2 plus the storage APIs, and do it after Phase 5 closes.**

- **In scope:** fix the dispatch emitter (G-2), convert `persist.gmi` and
  `tooltable.gmi` to the `out`-param + `i32` form, update the two C consumers
  (`emc/iotask/ioControl_v2.c`, `internal/task/interp_param_io_persist.c`),
  regenerate and rebuild both sides together.
- **Out of scope:** `emcio.GetStatus` (its consumer situation is untraced; it can
  be argued on its own merits later) and the `@returns_value` contracts, which
  stay as they are.
- **Sequencing:** *after* the Phase-5 remainder. The network half's `U`/`FP`
  tail and N7 **closed 2026-07-22** (halrest 0 → 87.1 %, mqttbridge 0 → 86.8 %,
  halscope 4.1 → 91.3 %, apiserver 45.6 → 96.2 %; two new bugs N10/N11 found and
  fixed). N9 followed the same day with the user ruling: both an overall and a
  WebSocket connection cap, INI-configurable. See
  `NETWORK_MODULES_REVIEW_FINDINGS.md`. G-1/G-2 then gets its own session.
- Because the conversion touches `ioControl_v2.c`, which is on the tool-change
  path, it owes a **full runtests round**, not the per-module fast gate.

#### RESOLUTION 2026-07-22 — landed as `@rc_error`

Both findings are fixed; the detail is in the `PRODUCTION_READINESS.md` status
log. What is worth carrying forward from doing it:

- **The mechanism is an opt-in annotation, not an inference.** `@rc_error` says
  "the i32 return is the status channel, the out param is the payload". It has to
  be declared: a plain `i32` next to an `out` param is a *value* the provider
  supplies itself, which is exactly what canon's `get_tool_by_number` does with
  its `-1` for "not found". Inferring the contract would have silently rewritten
  that API's meaning.
- **Both Go signatures survive the conversion unchanged** — consumer *and*
  provider stay `GetEntry(h, k) (Entry, error)`, because the trampoline maps the
  provider's `error` onto the rc and the client maps it back. That is what kept
  the change tractable: no Go call site moved.
- **`[]T out` was a new generator capability**, not just a re-plumbing. A
  callee-allocated slice cannot be a caller-provided buffer, so it travels in an
  owning `<api>_<elem>_slice_t {data, len}`. Without it the three
  slice-returning methods — the ones both C consumers actually use — would have
  stayed blind.
- **The finding under-counted the C consumers: five, not two.**
  `emc/iotask/ioControl.c` (v1 iocontrol), `internal/ngcpreview/module.go` and
  `internal/launcher/retain.go` were found by the compiler after the ABI change,
  not by the review that scoped the work. A grep for the API *header* would have
  found them; a grep for the two named files did not.
- **A generated remote client nearly broke silently.** `tooltable` does emit a
  TypeScript client (`webapp/tooledit`), and the first conversion turned
  `listTools(): Promise<ToolEntry[]>` into
  `listTools(tools: ToolEntry[]): Promise<number>` — the status as the result,
  the payload as a query parameter. The fix is `restView(fn)`, applied by every
  marshaling emitter; the regenerated client is now byte-identical to the
  pre-conversion one, which is the check that proves the wire format held.
- **A missing row is no longer an error.** persist's `GetEntry` returns the zero
  entry with a nil error; the status channel is reserved for storage actually
  failing. "Absent" is still distinguishable from "present and empty" — a stored
  row echoes its `Key` back.

(Superseded, kept for the record: until it landed, tooltable's empty-value convention (T-4) is the local mitigation
and the other consumers stay blind — that is a known, accepted gap, not an
oversight.)

#### How to fix it

The mechanism already exists and is in production: an `out` parameter plus an
`i32` return. `motstat.gmi` line 209 declares

```
func get_status(status: MotionStatus out) -> i32
```

and the generated cgo client is exactly what is wanted — and, critically, its Go
signature is *identical* to the struct-returning form:

```go
func (cl *MotstatClient) GetStatus() (MotionStatus, error) {
        var cStatus C.motstat_motion_status_t
        rc := int32(C.call_motstat_get_status(cl.cb, &cStatus))
        statusOut := motionStatusCToGo(&cStatus)
        if rc != 0 {
                return MotionStatus{}, fmt.Errorf("get_status: rc=%d", rc)
        }
        return statusOut, nil
}
```

So `func get_entry(handle: i32, key: string) -> Entry` becomes
`func get_entry(handle: i32, key: string, entry: Entry out) -> i32`, and every
**Go** consumer compiles unchanged — `GetEntry(h, k) (Entry, error)` before and
after. That is what makes this tractable: the churn is in the IDLs, the
generator, and the C consumers, not in the Go call sites.

Three things have to happen first, in this order:

1. **Fix G-2** (the dispatch emitter). Until an `out` param round-trips through
   dispatch instead of being read from the request and dropped, converting a
   `@rest_export true` API would break its C-provider dispatch path. This is the
   real prerequisite and it is generator-only work.
2. **Convert the IDLs.** Scope it to the *storage* APIs — `persist.gmi` and
   `tooltable.gmi` — not the whole tree. That is where a swallowed error is a
   silent data fault; for an RT-adjacent status API, a caller usually has
   nothing useful to do with an error anyway, and `@returns_value` is the honest
   contract there. `emcio.GetStatus` can follow or stay, on its own merits.
3. **Update the C consumers**, which is the only place the ABI break is felt:
   `emc/iotask/ioControl_v2.c` (5 `tt->get_tool` sites plus `put_tool` and
   `list_tools`) and `internal/task/interp_param_io_persist.c`. Provider and
   consumer share a header, so this is a single atomic change — there is no
   partial-rollout path, and it must land with a rebuild of both.

Rejected alternatives, for the record:

- **A trailing `int32_t *rc` alongside the struct return.** Smaller conceptual
  change, but it invents a second error convention next to the `out`+`rc` one
  that already exists and is proven. Two conventions is worse than one.
- **A blanket error channel on every callback** (an `rc` out-param everywhere,
  or last-error state on the `ctx`). Touches all 34 APIs and the RT-facing ones
  for no benefit; `ctx` state is also not safe for the RT callers.
- **Per-API sentinel conventions**, which is what tooltable does today (empty
  value ⇒ absent). Correct there because tooltable only ever stores JSON, so the
  zero value cannot occur legitimately — but it does not generalise, and leaving
  it as the general answer means every future consumer re-derives it, or
  doesn't.

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
**Run 2026-07-22 with the `@rc_error` conversion in: 241/241, 0 failed.**
`tooltable` coverage is 91.3 % after the conversion's end-to-end storage-failure
test.

**What is left in Phase 5** (2026-07-22, after the network half and G-1/G-2):

- `internal/inirest` `U` ◐ (57.5 %) — the one module the network coverage pass
  did not reach.
- ~~`internal/emccalib` `U` ◐ (43.2 %)~~ — **CLOSED 2026-07-22, 94.4 %.** It had
  been accepted because `GetTunables`/`SaveIni` read live HAL pins; the network
  pass's in-process HAL pattern (keep-alive `TestMain`; `RtapiInitializeApp` is
  enough here, no RT component involved) removed the excuse, and all four REST
  methods — previously at **0 %** — now run against real pins and a real INI file
  on disk. Includes the E-1 regression at the API level (tune → save → nudge →
  revert), the `pathres` containment check on the write path, and the soft-fail /
  hard-fail split described in the status log. Six mutations, all caught. Fixed in
  passing: the test helpers registered the API under `t.Name()`, which is
  process-global and never unregistered, so the package could not run under
  `-count>1`; now green under `-race -count=3`.
- `S` (human sign-off) on all ten rows.

Not Phase 5, tracked as cross-cutting: REST/WS authentication (ruled
deferred-but-required, loopback-only until settled) and "surface RCS command
errors to clients" — the same class as G-1 on a different surface.
