# gmicompile (cgen emission logic) — Review Findings (Tier 1)

**Module:** `src/gomc/internal/gmicompile/cgen` — the code generator that emits Go↔C bridges,
cgo clients, REST/WS dispatch, publish/stream code, and type validation for the **~39 GMI
packages**. **Tier 1** per `PRODUCTION_READINESS.md`: the *emission logic* is the highest
multiplier in the tree — one wrong emission pattern replicates into every generated package
(risk class 3). The parser/AST/check side is Tier 2 and out of scope here.

**Method:** four *independent* AI review passes, each on a bounded file-cluster + lens —
(A) cgo handle/pointer transit, (B) memory-ownership emission, (C) dispatch/watch/publish +
RT-safety annotation, (D) type-mapping/constraint/duplication. Each ground-truthed the
generator against **actual generated output** in `generated/gmi/*` (all 33 committed packages
swept) and, where needed, freshly generated output from a locally-built `modcompile`. The
synthesizer independently read the `bridge_go.go` handle-transit emission. Date: 2026-07-19.

**Verdict tags:** `CONFIRMED` = verified in emitted output, survives refutation ·
`PLAUSIBLE` = real but dormant/latent or dependent on a design decision. No code was changed.

---

## Headline

**The two scariest, previously-fixed classes are verified closed generator-wide:**
- **cgo handle transit** (the persist-`cgo.Handle` production crash): correctly and
  *universally* emitted across all 33 packages, both directions — `ctx` is `C.uintptr_t`
  everywhere, `call_*` wrappers deref fn+ctx inside C, `Free*` reads ctx via a C accessor, and
  **zero** handles park in a GC-scanned Go pointer slot. (Cleared — see below.)
- **returned-data ownership** (the returned-string leak): correctly and *completely* emitted
  for every returning shape that actually occurs (string, `[]string`, struct-with-strings,
  `[]struct`, nested) — no confirmed leak/double-free/UAF; alloc/free symmetric; ownership doc
  consistent; the one C provider (ethercat) complies. (Cleared.)
- **RT-safety annotation** the hardening work depends on: **complete** (58/58 `@rt_safe` `mot`
  members → `GOMC_API_NONBLOCKING`). (Cleared.)

So the generator is in good shape on the catastrophic classes. The review surfaced **one
production-relevant live defect** — and it *root-causes a known open bug* to this module.

**STATUS (2026-07-19): G-H1 + G-M1 FIXED** (commit `57c162d2ca`) — the publish drain now emits a
retained, sequence-numbered, bounded buffer + a per-connection `WatchFactory` (drain hook emits
`Factory:`), with a runtime multi-subscriber regression test in `internal/publishtest`. The
`PRODUCTION_READINESS` "Operator messages lost" entry is re-pointed here.

**STATUS (2026-07-20): G-H2 + G-L6 (PrimPtr half) FIXED** (commit `04b1d14df9`) — `--server-go`
now emits callback/`ptr` params as `unsafe.Pointer`/`void*`/`uint64` (via `isOpaquePtrParam`),
`cTypeForAPICgo` maps `PrimPtr`→`unsafe.Pointer`, and `emitCommands` skips opaque-ptr functions.
`mcode_handler` was migrated to the generated `--server-go` bridge and its hand-written provider
retired (only the milltask-specific invocation kept); `tests/mcode-handler` passes end-to-end.

Everything else below remains open for adjudication (type-mapping/duplication G-M2/M3/M4 +
LOW/latent items; G-L6's `[N]string`/array-field half is still latent).

---

## HIGH

### G-H1 — Operator-message loss is a GENERATOR defect: publish drain emits a shared, destructive-flush `Watch` instead of a per-connection `Factory`
`publish_go.go:236-243` (`Flush`: `events = nil`) · `publish_go.go:246-260` (`emitWatchFunc`) ·
`publish_drain_hook.go:141-152` (emits `Watch: drain.WatchFunc()`)
**CONFIRMED · HIGH · production-relevant**

The generated publish drain keeps one drain instance per API instance and exposes it through a
**single shared** `Watch` closure whose read is a **destructive flush** (`events := d.events;
d.events = nil`). Ground truth `generated/gmi/emcerror/emcerror_drain_hook.go`:
`Watches: [{Name:"get_errors", Watch: drain.WatchFunc()}]`.

*Failure scenario:* every WS connection subscribed to `emcerror/get_errors` shares that one
closure; each connection's `pushLoop` calls it and flushes the buffer — so **with N subscribers
each operator message reaches exactly one of them**. Single-subscriber loss compounds via the
consumer's `bytes.Equal(data, prevData)` dedup dropping byte-identical repeats. This is exactly
the `PRODUCTION_READINESS.md` "Operator messages lost — PRODUCTION-RELEVANT" item — **now
root-caused to the generator, not the apiserver.** Replicates to every `@publish` API;
`emcerror` (the operator error channel) is the load-bearing case.

*Fix (no apiserver change needed):* emit a retained, sequence-numbered, bounded buffer
(drop-oldest at cap) and a `WatchFactory()` returning a per-connection `WatchFunc` with its own
cursor; in `publish_drain_hook.go` emit `Factory: drain.WatchFactory()` instead of `Watch:`.
The consumer already invokes `watchMeta.Factory(sub.Args)` once per connection — the seam the
`PRODUCTION_READINESS` note calls out (`WatchFuncMeta.Factory`) is already wired.
**Well-scoped; needs a multi-subscriber regression test.** Also update the `PRODUCTION_READINESS`
"Operator messages lost" entry to point here.

### G-H2 — `--server-go` truncates `callback`- and bare-`ptr`-typed params to `C.int` (still-open; blocks retiring `mcode_provider.go`)
`dispatch_c.go:1205` (`cTypeForAPICgo` default → `C.int`, no `PrimPtr`/`TypeCallback` case) ·
`bridge_go.go:529` (`trampolineParam` default → `C.int`) · `bridge_go.go:868` (extern
`cParamDecl` default → `int`)
**CONFIRMED (fresh generation) · HIGH severity / currently DORMANT**

Freshly generating `--server-go` for `mcode_handler.gmi` produces a `//export` trampoline with
`fn C.int, user_data C.int` (32-bit) while the extern declares `void *user_data` — so on 64-bit
a real pointer/function-pointer is truncated to 32 bits, *and* the conflicting extern-vs-`//export`
prototypes likely fail to compile for any bare-`ptr` API. This is why `mcode_handler` uses the
hand-written `internal/task/mcode_provider.go` and its rule stays on `--server-c`. **Dormant:**
a full sweep found no committed generated package with a `C.int`-typed param (only
`mcode_handler` would hit it, and it routes around). The `--server-c` path is width-safe
(`void *`). Same root as **G-L6** (the struct-field angle).

*Fix:* add `case ast.PrimPtr`/`TypeCallback → "unsafe.Pointer"` to `cTypeForAPICgo` and
`trampolineParam`; emit `void *` in the extern `cParamDecl`; route the CToGo/dispatch types for
callback/ptr through the existing `IsPtr` `uintptr` treatment. Prerequisite for switching
`mcode_handler` to a generated bridge and deleting `mcode_provider.go`. **Validate by generating
+ building `mcode_handler` with `--server-go`.**

---

## MED

### G-M1 — Publish accumulator is unbounded when unconsumed (`d.events` grows forever with no subscriber)
`publish_go.go:194-214` (`drainAll` appends every ring slot) · only `Flush` shrinks it
**CONFIRMED · MED** — the drain goroutine runs from `Start()` regardless of subscribers, and
`Flush` is reached only by a WS subscriber. With no operator UI attached, every published error
accumulates on the Go heap indefinitely. Slow for `emcerror`; material for a higher-rate publish
API. **Fixed by the same retained-bounded-ring redesign as G-H1** (a fixed cap inherently bounds
memory).

### G-M2 — cgo type mapping is duplicated (`cTypeForAPICgo` vs `cgoType`) + 4 language copies — the structural cause of the drift below
`dispatch_c.go:1171` (`cTypeForAPICgo`) vs `client_go_internal.go:517` (`cgoType`); py/ts each
re-list the primitive set (`client_py.go:386`, `client_ts.go:336`, `client_py_ws.go:429`,
`client_ts_ws.go:380`)
**CONFIRMED (structural, risk class 3) · MED** — the same logical type mapping is re-implemented
in ≥6 places; a type addition or unsigned-width fix must touch all of them, and a miss silently
mismaps. This already produced G-M3. **Fix:** one shared primitive descriptor table all language
mappers consult; collapse the cgo pair into one `cgoType`. Folding in G-L5/G-L6.

### G-M3 — `i16`/`u16` fall through to `Any`/`unknown` in the python & TypeScript mappers
`client_py.go:386`, `client_ts.go:336`, `client_py_ws.go:429`
**CONFIRMED · MED (DX, not corruption)** — those mappers omit `i16`/`u16` (present in the C/Go
mappers), so an `i16`/`u16` field is typed `Any` (py) / `unknown` (ts). Only `ethercat.gmi` uses
them (u16 ×80), and the py/ts clients are opt-in CLI outputs not in the server build — so the
value still serializes as a JSON int; the impact is degraded type-safety and a live example of
the G-M2 drift. **Fix:** add the cases (or the shared table from G-M2).

### G-M4 — TS clients collapse `i64`/`u64` → JS `number`, truncating above 2⁵³
`client_ts.go:342-343` (inherited by `client_ts_ws.go`)
**CONFIRMED · MED/LOW (TS clients only, opt-in)** — real large-valued u64 exist:
`canon.update_tag(tag_ptr: u64)` (a raw pointer!), ethercat byte/packet counters, `kins` bitmasks.
A generated TS client truncates these; for `tag_ptr` that corrupts a pointer sent to the server.
Python maps `i64/u64 → int` (correct); the Go/C server sides are correct. Bites only a user who
generates a TS client for a large-u64 API. **Fix:** emit `string`+bigint for 64-bit ints in TS,
or at minimum a generator warning.

---

## LOW / latent

- **G-L1 — `@callback` (`_cb`) typedefs are not `GOMC_API_NONBLOCKING`-annotated; the publish
  inline producer has no independent enforcement seam.** `server.go:238-263`, `publish_c.go:133-173`.
  A cycle-invoked callback param loses its nonblocking type at the seam; matches the already-tracked
  **RT_HARDENING item 1b**. PLAUSIBLE/known; the publish inline body is in fact nonblocking-safe.
  Fix: optional `@rt_safe` on `@callback` types → annotate the `_cb` typedef.
- **G-L2 — `server_ws.go` dead-mode binary watch hardcodes the generation counter to `0`.**
  `server_ws.go:122-130`. The consumer dedup `gen > 0 && gen == sentGen` never fires → re-sends
  unchanged binary frames. **But `--server-ws` is not wired** (`cmd/modcompile/main.go` uses
  `--server-go`); no generated file uses it. CONFIRMED-dead. Fix: delete or reconcile with the
  live `server_go.go` watch path.
- **G-L3 — `client_go_internal.go` is dead code that frees nothing and mis-emits string returns.**
  `client_go_internal.go:478-498`: a `string` return emits `return string(result), nil` where
  `result` is `*C.char` (won't compile); struct-with-string leaks. **`GenerateClientGoInternal`
  is never invoked by the driver.** A loaded gun. Fix: delete (verify no test/other consumer
  first — `client_go_test.go` exists), or port the free logic in.
- **G-L4 — `[N]string` / array-of-slice elements are neither allocated nor freed.**
  `dispatch_c.go` `emitFreeCAllocs` TypeArray (L396-406) handles only named-struct elements; the
  alloc side for `[N]string` wouldn't compile. No current IDL has `[N]string` (all fixed arrays
  are primitive or string-less structs). CONFIRMED-latent. Fix: implement, or make the parser
  reject the shape with a clear error.
- **G-L5 — array-size symbol drift:** the header mapper emits the `#define` name
  (`MOTSTAT_MAX_JOINTS`) while the dispatch/client copies emit the raw number. Harmless today
  (parser resolves lengths), latent `[0]` if an `ArrayLenName` ever stays unresolved. Route all
  through the `#define`-aware helper. `server.go:156` vs `dispatch_c.go:1163`/`client.go`.
- **G-L6 — cgo `ptr`/array → `C.int` silent fallback** (struct-field angle of G-H2;
  `dispatch_c.go:1171`, `client_go_internal.go:517`). Unreached today (no `ptr` struct fields).
  Make the default a generator error, not `C.int`.
- **G-L7 — external C REST client parses only one level of struct nesting.**
  `client.go:582-620`. Deeper-nested / slice-of-struct-containing-slice fields are left zeroed in
  the `*_client.c` consumer path. Data-completeness gap (no leak/UAF in our code — external caller
  owns the `strdup`s). Fix: recurse the parser (already structurally ready).

## INFO / by-design (not bugs)
- Publish ring-full returns `-1` and the RT producer's caller may ignore it — a third burst
  message-loss vector, inherent to a lock-free ring (RT cannot block); acceptable, worth an
  emission comment. Dead `read_pos` ring field. `publish_c.go:116-156`.
- C-provider dispatch has no error channel by design (return marshaled, `nil` error unless JSON
  unmarshal fails); the Go-provider path propagates errors faithfully. **No error-drop bug.**

---

## Cleared (verified correct — recorded so the coverage is auditable)

- **Handle transit (production crash class):** every `//export` trampoline takes `ctx
  C.uintptr_t` (swept all 33 packages, 0 offenders); `call_*` wrappers deref fn+ctx inside C;
  `Free*Callbacks` reads ctx via a C accessor; `BuildCallbacks` passes `C.uintptr_t(h)`. No
  handle/uintptr parked in any GC-scanned Go pointer slot, both directions. **Closed
  generator-wide.**
- **Returned-data ownership (leak class):** free-side covers string, `[]string`,
  struct-with-strings, `[]struct`, nested (recursive, correct order/scoping); alloc/free
  symmetric; ownership doc in all 33 `*_api.h` matches the free-side assumption; ethercat C
  provider `strdup`/`malloc`/`calloc`s and sets the `_len` the free loop iterates. Double-free,
  UAF, empty-buffer-leak, static-string-freed — all **refuted**.
- **RT-safety annotation:** `@rt_safe` `_fn` typedefs emit `GOMC_API_NONBLOCKING` complete and
  correct (58/58 `mot`), gated to clang ≥ 20 via `__has_attribute`.
- **Constraint/validation:** `spindle_num @min(-1)` broadcast sentinel emits correctly (signed
  `i32`, inclusive bounds, named max resolved to literal); validation emitted **once** per func
  and shared by **both** REST dispatch and WS (no transport gap); `@regex` server-only by design;
  unsigned bounds safe (no `unsigned < 0`); enum width consistent (Go `int32` ↔ C enum).

---

## Suggested adjudication / fix ordering

1. **G-H1 + G-M1 — operator-message loss + unbounded accumulator.** The one production-relevant
   live defect; one redesign fixes both (retained seq'd bounded buffer + `WatchFactory`, emit
   `Factory:`). No apiserver change. **Highest priority; needs a multi-subscriber test.** Closes
   and re-classifies the `PRODUCTION_READINESS` "Operator messages lost" item.
2. **G-H2 + G-L6 (PrimPtr half) — `--server-go` callback/ptr → `C.int`. DONE** (commit
   `04b1d14df9`): `isOpaquePtrParam` at the four bridge sites, `PrimPtr`→`unsafe.Pointer` in
   `cTypeForAPICgo`, `emitCommands` skips opaque-ptr funcs; `mcode_handler` migrated to
   `--server-go`, hand-written provider retired, `tests/mcode-handler` green.
3. **G-M2 — unify type mapping** into one shared descriptor table; closes the G-M3 root and folds
   in G-L5/G-L6; de-risks all future drift. Then G-M3/G-M4 (py/ts widths) fall out.
4. **Clean deletions/guards (low-risk once confirmed unused):** G-L3 (delete dead
   `client_go_internal.go`), G-L2 (delete dead `--server-ws` mode), G-L4 (parser guard for
   `[N]string`).
5. **DX/robustness:** G-M4 TS 64-bit, G-L7 nesting depth, G-L1 `_cb` annotation.

**Every fix needs a test that would have caught it** (risk class 4) — for G-H1 a multi-subscriber
publish test; for G-H2 a generate+build check of a callback/ptr API under `--server-go`; for the
type-mapping fixes a golden-output test per target.
