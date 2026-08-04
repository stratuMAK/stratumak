# gmicompile (cgen emission logic) — Review Findings (Tier 1)

**Module:** `src/stmak/internal/gmicompile/cgen` — the code generator that emits Go↔C bridges,
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
  members → `STMAK_API_NONBLOCKING`). (Cleared.)

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

**STATUS (2026-07-20): G-M2 + G-M3 + G-L3 + G-L2 FIXED** (commits `6d08f75307`, `9f1ace9fa5`) —
type mappers unified/deduplicated (py/ts drift closed), dead `client_go_internal.go` and dead
`--server-ws` mode removed. Remaining are **deferred as documented-known** (see fix ordering §4):
G-M4, G-L1, G-L4, G-L5, G-L7 — all opt-in-client DX or latent-with-no-trigger, plus G-L6's
`[N]string` half.

**STATUS (2026-07-20, matrix reconciliation session): G-L4 + G-L6 (residual) FIXED via fail-fast
guards** — the two silent-wrong emitter fallbacks now `panic` with a shape-naming message instead
of emitting broken/mismapped cgo. `cTypeForAPICgo` no longer falls through to `C.int` for an
unsupported shape (`dispatch_c.go`), and `emitFieldGoToC` rejects fixed-array-of-string rather
than running it through the scalar path (non-compiling C, no `CString`/free). Both were
**latent-no-trigger** (grep over all 33 generated packages: no fixed-array-of-string, no `ptr`
struct field), so this is pure fail-fast hardening — a full `make` regen of every package is
**byte-identical** (guards never fire) and the cgen suite is green with two new guard tests
(`TestCTypeForAPICgoRejectsUnsupportedShape`, `TestEmitFieldGoToCRejectsFixedArrayOfString`).
Reconciliation confirmed all prior fix commits landed and both dead files (`server_ws.go`,
`client_go_internal.go`) are gone.

**STATUS (2026-07-21): G-M4 + G-L5 FIXED** (commits `d7d3e7fe7f`, `7d8d51408f`). **G-M4** — 64-bit
ints now cross the wire as JSON strings (protobuf3 convention), consistent across Go/Python/TS
clients: Go uses native `json:",string"` on scalar i64/u64 (response fields + POST/PUT/PATCH body
params; works through pointers/nil); Python converts at the from_dict/to_dict/body seam; TS types
them `bigint` with recursive per-type revivers (BigInt() over nested structs+slices) wired into
REST returns, WS subscribe callbacks, and WS command results. Two **fail-loud guards** added (no
current IDL trips them): the check layer rejects a 64-bit REST **path/query** param (encodeParams
coerces to a bare number → JS truncation), and `--client-python` rejects an API whose 64-bit field
is reachable only through a **nested** named type (Python from_dict doesn't recurse). `newthread`'s
`period_ns` is now bigint; webapp consumers convert bigint→number at the display boundary. Full
gmicompile suite + stmak build + halshow/latency webapps green; all 6 webapps `vue-tsc --force`
clean (a separate commit `1926c82ca8` fixed pre-existing halscope errors the regen surfaced).

**G-M4 regression + root-cause fix (commit `69c6bea407`):** the `,string` change broke
`tests/ethercat/sim-cli` — `cmd/ethercat` carried a **hand-written duplicate** of every ethercat
wire type (no `,string`), so `unmarshal DeviceStats.tx_count` failed against the now-string server.
The "no external client" note above missed in-repo **hand-written Go consumers**. Fixed the class:
added an ethercat `--client-go` target (→ `generated/gmi/ethercatclient`) and refactored
`cmd/ethercat` onto the generated client with qualified names, **deleting the hand-written client**
— generated types are the single source of truth and can't drift. halcmd never broke (it already
consumes its generated client — the model). Two `--client-go` generator fixes this required: a
numeric REST path param was passed bare to `url.PathEscape` (wants a string) → now
`fmt.Sprintf("%v", …)` first (halcmd's path params are strings, so it never hit it); and an additive
`New<X>ClientInstance(baseURL, instance)` constructor (default delegates) so the CLI keeps a
configurable instance (`EC_INST`). Lesson: any wire-format change breaks hand-written consumers —
sweep `grep 'uint64\|int64.*json:"' cmd/ internal/ | grep -v ,string` (ethercat was the only
wire-facing one). 2 generator tests; all generated clients + `cmd/*` rebuild clean.
**G-L5** — all C array bounds now route through one `#define`-aware helper (`cArraySizeStr`;
`serverGen.arraySizeStr` delegates), so header/cgo-bridge/dispatch/external-client agree (e.g. kins
bridge is now `joints[KINS_MAX_JOINTS]` not `[16]`) and an unresolved `ArrayLenName` can never emit
`[0]`; Go array bounds stay numeric. All generated cgo recompiles clean; 2 tests.

**Remaining: NONE — both former deferrals CLOSED (2026-07-21, `505e87d19f`).** G-L1 landed as an
additive capability (not an RT-session deferral): the investigation confirmed there is no RT-invoked
`@callback` today and the four existing ones are task/worker-level (must stay blocking), so `@rt_safe`
on a `@callback` now stamps the `_cb` typedef `STMAK_API_NONBLOCKING` — default-false, byte-identical
for existing callbacks — ready for the first RT consumer without needing the clang worktree now. G-L7
landed as fail-loud (Option B): every silent-drop site in `--client-c` now errors at generate time; the
sweep revealed the generator faithfully supports only 5 of 16 `@rest_export` IDLs, so the full recursive
rewrite (G-L7/A) stays deferred-until-consumer. See the updated §G-L1/§G-L7 entries.

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

### G-M4 — TS clients collapse `i64`/`u64` → JS `number`, truncating above 2⁵³ — **FIXED (2026-07-21, `d7d3e7fe7f`)**
Resolved wire-format-wide (not TS-only): 64-bit ints serialize as JSON **strings** (protobuf3
convention) across Go (`json:",string"`), Python (int↔str at the seam), and TS (`bigint` +
recursive revivers). Body 64-bit params supported symmetrically; 64-bit REST path/query params and
Python nested-64-bit fields now **fail loud** at gmicompile. See the STATUS block at the top.
`client_ts.go:342-343` (inherited by `client_ts_ws.go`)
**CONFIRMED · MED/LOW (TS clients only, opt-in)** — real large-valued u64 exist:
`canon.update_tag(tag_ptr: u64)` (a raw pointer!), ethercat byte/packet counters, `kins` bitmasks.
A generated TS client truncates these; for `tag_ptr` that corrupts a pointer sent to the server.
Python maps `i64/u64 → int` (correct); the Go/C server sides are correct. Bites only a user who
generates a TS client for a large-u64 API. **Fix:** emit `string`+bigint for 64-bit ints in TS,
or at minimum a generator warning.

---

## LOW / latent

- **G-L1 — `@callback` (`_cb`) typedefs are not `STMAK_API_NONBLOCKING`-annotated. DONE (capability
  added, 2026-07-21, `505e87d19f`).** Investigation corrected the framing: there is **no RT-invoked
  `@callback` today** — the four real ones (`interp_ext` oword/remap ×3, `mcode_handler` handler) are
  all task/worker-level and *must* stay blocking-capable (`mcode_handler.handler` blocks on `abort_fd`),
  and everything actually RT-invoked (mot/tp/hm2_serial `@rt_safe`) rides on `func`→`_fn` typedefs that
  were already annotated. So nothing was mis-typed. But since stmak is a general framework and an RT
  callback is a legitimate future need, the capability was wired symmetric to the `_fn` precedent:
  `ast.Callback.RTSafe`; `parseCallback` applies `@rt_safe` (other annotations before a callback still
  error); `emitCallbackDecls` stamps `STMAK_API_NONBLOCKING` iff RTSafe. Additive/non-breaking (default
  false → existing callbacks byte-identical). The clang `-Wfunction-effects` check only bites when a
  real RT `@callback` appears — same as `_fn` — so this is **out of the RT-hardening bucket**. Tests:
  parser (RTSafe set + default-false + guard) + cgen (`callback_rtsafe_test.go`).
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
- **G-L4 — `[N]string` / array-of-slice elements are neither allocated nor freed. FIXED
  (fail-fast).** `dispatch_c.go` `emitFreeCAllocs` TypeArray (L396-406) handles only named-struct
  elements; the alloc side (`emitFieldGoToC` TypeArray→primitive) ran `[N]string` through the
  scalar path, emitting non-compiling C with no `CString`/freeList. No current IDL has `[N]string`
  (all fixed arrays are primitive or string-less structs). Rather than implement the (unused)
  alloc/free, `emitFieldGoToC` now **panics with a shape-naming message** on fixed-array-of-string
  — a build-time generator failing loud beats silently-broken cgo. Regen byte-identical; guard
  test added. Revisit to *implement* only if an IDL introduces the shape.
- **G-L5 — array-size symbol drift. FIXED (2026-07-21, `7d8d51408f`).** The header mapper emitted
  the `#define` name (`MOTSTAT_MAX_JOINTS`) while the cgo-bridge/dispatch/external-client copies
  emitted the raw number. Now all C array bounds route through one helper (`cArraySizeStr`;
  `serverGen.arraySizeStr` delegates), so every C copy agrees and an unresolved `ArrayLenName` can
  never emit `[0]`; Go bounds stay numeric (C `#define` invisible to Go). E.g. kins bridge is now
  `joints[KINS_MAX_JOINTS]`. All generated cgo recompiles clean; 2 tests.
- **G-L6 — cgo `ptr`/array → `C.int` silent fallback. FIXED (fail-fast).** (struct-field angle of
  G-H2; `dispatch_c.go` `cTypeForAPICgo`.) The PrimPtr half was fixed in `04b1d14df9`; the
  residual `C.int` default for any other unsupported shape (a fixed array reaching the mapper, a
  future unmatched primitive) now **panics** naming the shape+api instead of silently truncating a
  pointer/array at the FFI boundary. Unreached today (dead `client_go_internal.go:517` copy already
  deleted in G-L3). Regen byte-identical; guard test added.
- **G-L7 — external C REST client silently drops fields it can't emit. DONE via fail-loud (Option B,
  2026-07-21, `505e87d19f`); full recursion deferred-until-consumer.** `--client-c` is a *published*
  modcompile feature with **zero in-tree consumers and no test**; it silently dropped fields in BOTH
  directions (receive inlined one level of primitive-scalar nesting; send serialized primitives only,
  with a literal `// would go here` TODO stub emitting empty arrays for slice-of-struct). Rather than a
  speculative recursive rewrite for a feature nobody builds, added `failf` (sets `g.err` → build fails)
  + `default:` guards at every silent-drop site across all 5 emitters, and upgraded 2 "type not found"
  warnings to hard errors. **The fail-loud sweep is the finding:** of 16 `@rest_export` IDLs only **5
  generate cleanly, 11 fail loud** — the generator was producing broken clients for ~69% of the real
  REST surface, and the gap is broader than "nested struct": narrow scalars (u8/i16/f32), enum-typed
  fields (resolve to `TypeNamed` but lookup skips `api.Enums`), non-string slices, depth-≥2, and
  slice-of-struct. `--help` now documents the supported subset. G-L7/A (recursive parity with the
  Go/Py/TS clients + a compile-and-run C consumer test) remains deferred until a real C consumer needs
  it. Tests: `client_failloud_test.go` (synthetic fail-loud + supported-shapes-succeed + real-IDL
  characterization pinning "unsupported ⇒ `--client-c:`-attributed error, never silent").

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
- **RT-safety annotation:** `@rt_safe` `_fn` typedefs emit `STMAK_API_NONBLOCKING` complete and
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
3. **G-M2 + G-M3 + G-L3 — DONE** (commit `6d08f75307`). Deleting dead `client_go_internal.go`
   removed the duplicate `cgoType` (cgo mapping is now solely `cTypeForAPICgo`); the py/ts
   mappers were completed (`i16`/`u16`) and deduplicated (`py_ws`→`primitiveToPyType`, as
   `ts_ws`→`primitiveToTSType` already did) so they can't drift. The complete, non-drifting
   server mappers (`primitiveToCType`/`primitiveToGoType`/`cTypeForAPICgo`) were intentionally
   left as-is — a full shared-table rewrite risks the server build across ~39 packages for
   near-zero gain. **G-L2 — DONE** (commit `9f1ace9fa5`): dead `--server-ws` mode deleted.
4. **Fail-fast guards for the latent silent-wrong shapes — DONE** (matrix-reconciliation session):
   G-L4 (`[N]string` alloc) and G-L6 (cgo `C.int` default) now panic with a shape-naming message
   rather than emit broken/mismapped cgo. Zero-risk (regen byte-identical over all 33 packages);
   two guard tests added. This is the automatable subset — converting silent-latent-corruption into
   a loud build error, exactly the doc's "make the default a generator error" recommendation.
5. **G-M4 + G-L5 — DONE** (2026-07-21, commits `d7d3e7fe7f`, `7d8d51408f`). G-M4: 64-bit ints as
   JSON strings across all clients (protobuf3 convention) + two fail-loud guards (64-bit REST
   path/query param; Python nested-64-bit) + body-param support; tests per target. G-L5: all C
   array bounds through one `#define`-aware helper (`cArraySizeStr`); regenerated cgo recompiles
   clean; 2 tests. See the STATUS blocks and the per-finding entries above.
6. **Former deferrals — BOTH DONE** (2026-07-21, `505e87d19f`). G-L1: `@rt_safe` on a `@callback`
   now annotates the `_cb` typedef `STMAK_API_NONBLOCKING` (additive capability, out of the
   RT-hardening bucket — no RT `@callback` exists today; existing callbacks are correctly blocking).
   G-L7: `--client-c` fail-loud — silent field-drops are now generate-time build errors; the sweep
   found the generator faithfully supports only 5/16 `@rest_export` IDLs, so full recursive parity
   (G-L7/A) stays deferred-until-a-real-C-consumer. Both have tests. **No open findings remain.**

**Every fix needs a test that would have caught it** (risk class 4) — for G-H1 a multi-subscriber
publish test; for G-H2 a generate+build check of a callback/ptr API under `--server-go`; for the
type-mapping fixes a golden-output test per target.
