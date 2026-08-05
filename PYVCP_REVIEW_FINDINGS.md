# pyvcp widget-centric migration — Review Findings (Tier 2)

**Modules:** `internal/pyvcpmodule` (1585 non-test / 555 test LOC after migration), the
Python/Tkinter client (`lib/python/pyvcp_client.py`, `pyvcp_widgets.py`, `vcpparse.py`),
and the protocol (`src/gmi/idl/pyvcp.gmi` v2 + `src/gmi/VCP_MIGRATION.md`). Phase 6
(UI-adjacent) per `PRODUCTION_READINESS.md`. Companion codegen fix in
`internal/gmicompile/cgen` (see V-9).

**Context — the fix-and-migrate ruling (2026-07-22).** The original production-readiness
plan called for reviewing the *pin-centric* pyvcp port. In parallel, a widget-centric
rewrite had been implemented on branch `migrate-vcp`. Ruling: reviewing code slated for
deletion is negative-value work — instead, **review-and-fix the rewrite and adopt it**.
Branch `pyvcp-restruct` (off `production-readyness`) cherry-picked the 14-commit
`migrate-vcp` range (`4e7ffd122a^..56ea3f28e2`); both conflicts were benign (the newer
base had already improved those areas: `pathres.Resolve` in `module.go`, a lint-fix to
code the rewrite deletes, and the codegen auto-merge combined both complementary halves
of the nullable-scalar fix). History then squashed to two clean commits — `0ce9fa4583`
(gmicompile nullable codegen) + `f5f112f6ba` (the migration) — verified byte-identical
to the pre-squash tip.

**Why the design is sound (and is now the template for further UI-framework ports):**

- **Server-authoritative.** The server owns HAL pins, min/max constraints, quantization,
  derived pins, and timer accrual. Clamping and pin logic live in one trusted place —
  which matters because `@rest_export true` makes every client an untrusted wire peer.
- **Event-in / delta-state-out.** Clients send gestures (press/release/toggle/select/
  set/increment); the server pushes per-widget state over a delta-encoded WS watch
  (first message full snapshot, then changed widgets only). Multi-client sync falls out
  for free; a pin-centric protocol structurally cannot provide it without every client
  re-implementing widget semantics.
- **Matches the rest of stratuMAK** — GMI REST + WS watch with `Delta:true`, the same pattern
  halscope and stat already use; `config=` goes through `pathres.Resolve` like every
  other module (pyvcpmodule is one of the nine call sites the 2026-07-22 containment
  audit verified clean).

**Method + caveat.** The review ran *inside* the fix-and-migrate session, not as a
standalone adversarial pass — recorded here so the doc does not imply otherwise. Depth
was Tier-2-equivalent in practice: primary read-through of server, client and protocol
against the original Python pyvcp as parity oracle, under the standing transferable risk
classes (① goroutine ownership — where the CRITICAL was found; ④ fixed-but-untested —
every fix below carries a test; ⑤ untrusted-wire — event handler bounds/robustness) plus
a client-side pass. Two scary pre-adoption findings **evaporated on the new base** and
are not listed below: a CRITICAL use-after-free variant (the base had since grown
`WatchRegistry.UnregisterByInstance` and `unload.go` already called it — though the
residual race was real, see V-1) and the codegen nullable-f64 break (the base had
reworked the emitter; the merge combined both halves, residual gaps in V-9).

All findings are **fixed on `pyvcp-restruct`** unless marked *deferred*.

---

## CRITICAL

### V-1 — use-after-free on unload: an open watch pushLoop outlives the panel's HAL pins
`internal/pyvcpmodule/panel.go`, `module.go`. `[FIXED]`

`UnregisterByInstance` alone does **not** stop an already-open watch pushLoop — the loop
holds the captured callback closure and keeps calling into the panel after `Destroy()`
has run `comp.Exit()`, i.e. reads freed HAL pin storage from a live goroutine. Same
class as launcher/halscope lifecycle findings: goroutine ownership, the risk class 2.9
parity checks cannot catch.

Fixed by making the panel own its liveness: `Destroy()` sets `panel.closed` under `mu`
**before** `comp.Exit()`; the watch callback and the event handler bail when closed; and
every pin accessor is nil-safe as a second layer. Covered by
`module_hal_test.go` `TestDestroyClosesUseAfterFree` against a real in-process HAL.

## HIGH

### V-2 — server: all HAL-input processing ran only while a UI client was watching
`internal/pyvcpmodule/module.go`, `panel.go`. `[FIXED]`

`scan()` — checkbutton `changepin` edges, jogwheel reset, `param_pin` override, timer
run/reset — executed inside the watch callback, so it ran **only while a browser was
connected**, and its rate was coupled to the number of watch clients. A panel's HAL
inputs were dead without a UI attached, which inverts the server-authoritative design.
Fixed: `scan()` runs on a module-owned 100 ms ticker (`Start`/`Stop`/`run`),
independent of any client; the watch callback only snapshots state.

### V-3 — client: auto-name counter desync between Python client and server
`lib/python/pyvcp_widgets.py`. `[FIXED]`

The client incremented its per-type counter (`pyvcp_*.n += 1`) on a different condition
than the server's `autoName`, so explicit-`halpin` panels desynced the generated IDs and
every later auto-named widget bound to the wrong server widget. The fix is **subtle**
because the server itself is not uniform: it increments unconditionally for
scale/spinbox/dial/jogwheel (their autoBase is always computed) but only-when-empty for
the rest — so the client fix was checkbutton → inside the `if halpin is None:` branch,
jogwheel → outside, scale/spinbox/dial → unchanged. Pinned on the server side by
`panel_test.go` `TestAutoNameCountersMatchClient`.

### V-4 — client: one raising widget callback killed the Tk poll loop for good
`lib/python/pyvcp_client.py`. `[FIXED]`

`_poll_tk_queue` only rescheduled itself after all callbacks returned; any exception
skipped the `after()` re-arm and **froze the entire panel permanently**. Not
theoretical: a `value: null` state for a u32/s32 widget raised `int(None)` in
`_on_state`. Fixed: the reschedule is exception-safe and the null case is handled.

## MEDIUM

### V-5 — protocol: min/max used 0 as a "no limit" sentinel
`src/gmi/idl/pyvcp.gmi`, server + client. `[FIXED]`

The server used NaN internally for "no limit" and destroyed the distinction on the wire
via `nanToZero` — making a real limit of 0 (a 0..100 scale, a −100..0 bar)
indistinguishable from "unbounded". `WidgetDef.min`/`max` are now `f64?` (null = no
limit); this is what surfaced the codegen gaps in V-9. The related IDL doc bug — 
`WidgetEvent.value` documented "NaN = not used" although JSON cannot carry NaN — fixed
to the value=0 convention the code implements.

### V-6 — protocol: TIMER was client-computed, violating server authority
Server + client. `[FIXED]`

The timer widget accrued elapsed time in the client, so two clients showed two different
timers and a reconnect reset the display. Elapsed is now accrued server-side in `scan()`
and pushed in `WidgetState.value`; `state`/`reset` carry run/reset. Covered by
`TestScanTimerServerAuthoritative`.

### V-7 — client: silent widget regressions vs the original pyvcp
`lib/python/pyvcp_widgets.py`. `[FIXED]`

The rewrite downgraded `meter` to a text stub and dropped the `title`, `option`,
`image` and `axisoptions` widgets entirely — existing panel XMLs would render wrong or
fail. Meter gauge restored; dropped widgets restored.

## LOW

### V-8 — codegen: nullable-scalar emission incomplete (first `f64?` consumer)
`internal/gmicompile/cgen/dispatch_c.go`. `[FIXED — commit 0ce9fa4583]`

pyvcp's IDL is the first to send nullable floats through the C dispatch generator, which
exposed missing branches in the nullable-scalar pointer idiom: i8/u8 in
`emitFieldCToGo`/`emitFieldGoToC`, and i8/u8/f32/f64 in `emitParamGoToC`. Completed
consistently across all three emission paths, purely additive (non-nullable output
byte-unchanged for every existing IDL), with a round-trip test covering nullable
f64/f32/i8/u8 as both struct fields and parameters. Slots under the already-closed
gmicompile Tier-1 review (`GMICOMPILE_REVIEW_FINDINGS.md`) rather than reopening it.

### V-9 — assorted robustness/hygiene
Server. `[FIXED]`

The derived integer pin (`-i` from `-f`) saturates via `toS32` instead of wrapping
(`TestToS32Saturates`); pin refs are nil-safe throughout; stale `PyvcpWatchCallbacks`
comments removed (the type did not exist at review time — the watch was hand-registered;
since D-2 closed, `PyvcpWatchCallbacks` exists again as *generated* code and the module
implements it);
out-of-range `SELECT` index and unknown-widget events are rejected without panic
(`TestHandleEventRadioSelectBounds`, `TestHandleEventUnknownWidgetNoPanic`).

---

## Found by runtests AFTER the review — the reason the parity-test rule exists

Both are pin-inventory regressions the code review missed and `tests/pyvcp` caught,
because only the runtests comparison checks the created pin set against the original
Python pyvcp's:

### V-10 — dial: `param_pin` gated behind `param_pin=1`, but Python pyvcp always creates it
`internal/pyvcpmodule/panel.go`. `[FIXED]` — the dial's param pin is now always created
(the old server even carried the comment "always created, like Python pyvcp").

### V-11 — `halparam="name"` silently dropped for scale/spinbox/dial
`internal/pyvcpmodule/panel.go`. `[FIXED]` — scale discarded it, spinbox/dial never read
it; now carried via `widgetDef.paramName` (`TestParamPinHalparamName`).

**Transferable rule (goes into the template):** every migrated UI framework needs a
runtests-level **pin/behavior parity test against the original framework** — pin-mapping
regressions do not show up in unit tests written against the new design's own contract.

---

## Deferred — with rationale, not silently dropped

### D-1 — no snapshot/sequence marker on watch frames
A client cannot *detect* desync; it can only rely on ordering. Deferred because the fix
is a **shared-infrastructure wire change for every watch consumer** (halscope, stat, …),
not a pyvcp-local one; practical exposure is ~nil on an ordered TCP WebSocket, and a
reconnect always yields a fresh full snapshot. Revisit if a watch transport without
total ordering ever appears.

### D-2 — `watch_state` is hand-registered, not generated — **CLOSED 2026-07-22**
The IDL could not express `map<widget_id, WidgetState>`, so the watch was registered
manually, undercutting "GMI as single source of truth" for exactly this one surface —
and, templated, it would have become one hand-written watch per migrated framework.

Closed by implementing `map[string]T` in gmicompile, deliberately **narrow**: the
original "large codegen feature" costing dated from when REST went through the C
dispatch — after the Go-native handler switch, the C ABI is the only place a map has
no shape, and a watch never crosses it. So: string keys only (a JSON object key IS a
string), allowed **only** as the full return type of a watch-only func, no nesting, no
nullable values; every other placement fails the checker. The C-provider surface
(header vtable, call wrapper, dispatch, FuncMeta) skips such a func with an emitted
comment — a map watch is servable by Go providers only. New `@watch_delta true`
annotation emits the `Delta: true` per-key delta registration pyvcp had set by hand;
rejected on non-watch funcs and binary watches. Type mappings: Go `map[string]T`, TS
`Record<string, T>`, Python `dict[str, T]` (the generated py-WS client deserializes
map values via `from_dict`; the TS bigint reviver walks map values). `pyvcp.gmi` now
declares `watch_state() -> map[string]WidgetState` with `@watch_delta true`;
`pyvcpmodule` registers via the generated `RegisterPyvcpWatch` and implements the
typed `WatchState() (map[string]pyvcp.WidgetState, error)` — the hand registration is
gone. Documented in `src/gmi/idl/README.md` (Type System + Maps + `@watch_delta`).

Every existing IDL's output is byte-identical by construction (the new emission
triggers only on `TypeMap`/`WatchDelta`, which no other IDL uses — verified by grep)
and `TestMapWatchDeltaOmittedWhenUnset` pins the no-`@watch_delta` case. Verified:
parser/checker/emitter tests (map syntax, non-string-key rejection, 7 checker
confinement cases, Go/C/TS/Py surface assertions in `cgen/map_watch_test.go`),
pyvcpmodule 19 tests `-race` clean, `stmak-lint-full` 0, **`tests/pyvcp` runtests
green** — the wire contract is unchanged, proven by the untouched hand-written Python
client passing against the generated registration.

---

## Verification

- `go test ./internal/pyvcpmodule/...` — 19 tests (unit + HAL-backed against a real
  in-process HAL via keep-alive `TestMain` + `hallibtest` link shim), **pass, `-race`
  clean**. Fault paths covered: unload/UAF teardown, unknown-widget event, out-of-bounds
  SELECT, clamp/quantize/saturation edges, param-pin override.
- `stmak-lint` 0 issues; gofmt clean; `stmakd` builds.
- **`tests/pyvcp` runtests passes** — the pin-inventory parity check that caught
  V-10/V-11.
- Build/porting gotchas recorded for the next module: IDL `string?` → Go `*string`
  (client needs an empty→null adapter for `format`/`text`); the HAL-backed test binary
  needs `link_test.go` blank-importing `internal/hallib/hallibtest` and a bare
  `hal.NewComponent` keep-alive `TestMain` (no `internal/halcmd` import).

## Template status

Widget-centric / server-authoritative is the **ruled template** for the remaining UI
framework ports (`src/gmi/VCP_MIGRATION.md` is the how-to). Three conditions attach,
tracked as a cross-cutting item in `PRODUCTION_READINESS.md`:

1. **D-2 before the second consumer** — DONE 2026-07-22: `map[string]T` +
   `@watch_delta` are IDL features (see D-2 above); a migrated framework's watch is
   fully generated, nothing left to hand-register.
2. **User-code ruling before gladevcp/qtvcp** — pyvcp is the easy case (purely
   declarative XML). GladeVCP/QtVCP embed arbitrary user Python with direct HAL access;
   whether user extension code runs server-side (plugin) or client-side (against GMI
   only) is a design decision to take *before* those ports start, not during.
3. **Pin/behavior parity test is mandatory** per migrated framework (see V-10/V-11).
