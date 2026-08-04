# ClassicLadder Review Findings

Pre-merge review of the completed classicladder migration (branch
`migrate-classicladder`, 31 commits, ~15k lines: Go gomod + C RT engine +
GMI IDL + Vue/TS webapp + AXIS/launcher integration + docs + runtests),
run 2026-07-31 with five parallel adversarial reviewers — RT C engine, Go
write surface, IDL/codegen/launcher plumbing, webapp, tests/configs/docs.
User ruling: **fix all findings.** Everything below is FIXED on the branch
(commits `d586de4b66`..`1c4586aeac`) unless marked otherwise; every fix
carries a test that would have caught it (the milltask "fixed-but-untested"
rule).

The engine itself was already differentially verified against a headless
build of the 2.9 sources (`src/stmak/internal/classicladder/testdata/oracle`,
`oracle_test.go`); this review covered everything around and above it.

## Critical

| # | Finding | Resolution |
|---|---|---|
| C-1 | Size-override load args (`numRungs=`, `numBits=`, …) were never clamped to the fixed `CL_MAX_*` arrays: values legal under 2.9 (which sized shared memory dynamically) panicked pin creation (server death) or let `set_variable` write past the allocation; negatives corrupted backwards | **Root-caused away** (ruling: dynamic allocation, no config page): `classicladder_rt_alloc` lays every size-configurable array out in one block sized exactly from the args, 2.9-style; Go views them via `unsafe.Slice` with real bounds; parser refuses garbage/negatives/`CL_SIZE_LIMIT`-overs/unknown keys by name; region packing proven at odd pairwise-distinct sizes (`sizes_test.go`); `hide_gui` (write-only stub, un-keepable 2.9 promise) dropped |
| C-2 | Runtime-unload use-after-free: `Stop()` freed the C instance, but the launcher unloads Stop → unregister APIs → Destroy, so a connected editor's 100 ms `watch_rung_states` loop dereferenced freed memory | Free moved to `Destroy()` (halscope split); plus C-3 |
| C-3 | (generic, first exercised here) `WatchRegistry.UnregisterByInstance` deleted registry entries but left running push loops calling module callbacks through and after Destroy | Registry tracks live subscriptions per instance under the API-table lock (a subscribe that resolved before the sweep is refused, not raced in), cancels on unregister, and **drains** — bounded wait for loop exit — before returning (`ws_handler.go`, mutation-style tests) |
| C-4 | `load_project` rewrote the program in place under the running scan: no stop bracket, parsers published `used=1` before content, no `cl_prepare_all_datas_before_run` (load-while-RUN inherited the old program's timer/edge state), and a short `#NAME` line panicked *after* the wipe — emptied controller, modbus never restarted | 2.9's `StopRunIfRunning`/`RunBackIfStopped` done for real: new `rt->scanning` flag raised before the RT function reads `state` (Dekker pairing, seq_cst) — 2.9's `UnderCalculationPleaseWait` was never actually set by its HAL module. `loadCLPFile` states the errors-before-wipe invariant its callers rely on; `#NAME` parses leniently; prepare runs; `projectFile` updates under the lock (`loadproject_test.go`) |

## Major

| # | Finding | Resolution |
|---|---|---|
| M-1 | `set_rung` accepted wire `prevRung`/`nextRung`/`used` — one PUT could splice the scan into a freed slot's chain (the exposure `validateSectionChain` closed for `set_section`) | `set_rung` **ignores** the chain fields (server owns the chain; rung must exist); contract written into the IDL |
| M-2 | `set_program`: no chain validation at all, and shorter uploads left the old program's tail chained in (frankenprogram + leaked slots surfacing as spurious ENOSPC); stale compiled bytecode survived behind cleared expression strings | Whole-program twin of the chain validator walked against the uploaded rungs; slots past the upload cleared; `installExprCode` invalidates the tail; `set_program` brackets like the load it is and prepares dynamic state |
| M-3 | Runaway guard didn't compose: a tripped (C)alled sub-section stopped the PLC but not the in-flight scan — 99999^depth rung evaluations, hours of a wedged HAL thread | One iteration budget shared down the whole call tree of a top-level section (hang-not-fail regression test) |
| M-4 | `clearRung`'s self-link could trap an in-flight scan that read the old chain just before an unlink — one enormous scan overrun, silent PLC stop | Freed rungs keep their old links (a stale walk exits the section); delete paths additionally outwait the scan in flight (`waitScanSettled`) before wiping |
| M-5 | `^` silently changed meaning: 2.9's power operator consumed every caret (and its `pow_int` computed a^(2^b), not a^b — it also shadowed 2.9's own unreachable XOR level); here `^` is XOR, `**` is power — old projects change value with no warning | Kept by ruling (2.9's behavior was double-broken); divergence commented in the grammar; `loadCLPFile` warns per expression containing `^` |
| M-6 | Strings the line format cannot carry were accepted: a label/section-name/symbol/SFC-comment holding `\n` reloads as a *different program* (an SFC comment can become a step); commas shift symbol columns. Unreachable under 2.9's single-line GTK entries, reachable over REST | Refused at every write path (`validateText`); load stays lenient by design; refuse-not-escape ruling (escaping breaks 2.9 `.clp` interop) |
| M-7 | Refusals surfaced as HTTP 500 against the fault-status contract: out-of-range indexes, `ENOSPC` capacity, pathres containment/not-found on load/save | `errInvalidIndex` wraps ENOENT (404); ENOSPC joined the apiserver errno map (503, the FaultCapacity twin); pathres refusals wrap `FaultNotFound` (inirest precedent); errno classes asserted in tests |
| M-8 | Webapp Apply wrote staged expressions *before* the rung; a refusal stranded them committed while Cancel claimed to undo them | `set_rung` made **transactional** (IDL: carries `ExprSlot[]`; everything parses/compiles before anything lands) — also releases deleted elements' slots in the same write, closing the expression-slot leak; webapp sends one call |
| M-9 | VarSpy write drafts keyed by row index survived row removal — a value typed for %W0 aimed at whatever slid into row 0 (wrong-writes class) | Drafts keyed by variable identity; removal drops the draft |
| M-10 | A stale SFC draft survived `loadProject`/section ops (only the rung editor got the cross-cancel) and could overwrite the freshly loaded chart on Apply | SFC store registers a cancel hook; all structural/load paths cancel every editor |
| M-11 | Reconnect never refetched the program and nothing signalled that another client or a load rewrote it — fresh rung states painted onto a stale ladder indefinitely | `Status.generation` (bumped on every mutation) is the wire signal; the app refetches on generation movement and on reconnect; open drafts (copies by design) survive |
| M-12 | `gmi.registry`'s "never raises" contract missed `http.client.HTTPException` — a port-5080 squatter's `BadStatusLine` crashed AXIS at startup through the unguarded `has_api` probe | Exception joined the tuple (verified against a live non-HTTP responder); Ladder Editor menu entry re-probes uncached on menu post (runtime-load ruling); `exec` catch-guarded |
| M-13 | Docs actively harmful: `--modserver` (parsed as a positional project path → aborts the load) and StepConf endorsement (generates the removed 2.9 modules) | Both passages rewritten to what exists; StepConf *generator* fix deferred to the UI-migration effort |
| M-14 | `oracle_test.go` skipped when the 2.9 reference failed to *build* — the differential suite would go green exactly when its subject went missing | Build failure now FAILS (cgo already guarantees a toolchain wherever the package compiles) |

## Minor (all fixed)

`SetState(RUN)` TOCTOU (now decided under the lock, scan outwaited before the
reset) · over-long uploads refused with the sizing knob named instead of
silent truncation · `Status.projectFile` staleness + torn-string read ·
`set_section` lifecycle locked (un-use/re-language orphaned rungs
unreclaimably) · `applySequential`/`applySection` publish the used flag last ·
lenient-load SFC out-of-range refs compacted away + engine never fires a
transition with no valid upstream step (the fires-forever/settle-loop-burn
class) · div/0 reverted to 2.9 (result = dividend; INT32_MIN/−1 wraps instead
of SIGFPE) · `%T.V`/`%M.V` writes kept (UI forcing) with divergence comment ·
`packVar` refuses references that would alias under 16-bit masking ·
LOAD_VAR_IDX index-add overflow guarded · file presets saturate at int32 ·
`Program` arrays carry `@maxlen` (the size cap) · stale ownership comments in
`classicladder_rt.h` rewritten · live.ts stop/start double-connect ·
block-number clamp to configured counts · SymbolTable dirty-edit protection ·
SFC bottom-row step-and-transition checks room before placing · `emitModbusIOConf`
duplicate branches merged · doc default-size table / port-9502 / `hide_gui`
man entry / CI comment / motenc dead example / configure.ac GTK2 wording ·
`classicladder.0` runtests extended to drive the lube inputs (expected
regenerated from 2.9; cross-wired pin plumbing now diverges from the
reference).

## Checked and clean (for the record)

Scan path allocates nothing, takes no locks, does no I/O; every
file/API-supplied index reaching the engine is bounds-checked; watch plumbing
correct without `@watch_factory` (all three watches non-parameterised;
`@watch_delta` verified end-to-end); map-return-only-on-watch rule held; IDL ↔
handler parity 1:1; pin naming single-home (legacy `classicladder.0.*`);
all three path entry points contained via `internal/pathres`; the
`STMAK_SRC_BASE` cgo-relink fix holds; no client-side prefix tables or
expression parsers reintroduced; the sfc.test.ts ↔ `branched_sfc.clp` ↔
oracle fixture coupling is genuinely shared; fresh-checkout simulation
passed (every fixture tracked); `src/po` removal was exactly the dead GTK
block (the oracle's sources are untouched).

## Open / deferred

- Human sign-off (`S`).
- Modbus **frame logging**: the doc's debugging walkthrough presumes it;
  the Go master logs errors/echoes only. Needs a ruling: add frame logging
  or rewrite the section (transcripts are labelled as 2.9's meanwhile).
- StepConf still generates `loadrt classicladder_rt`/`loadusr classicladder`
  — belongs to the UI-migration effort, docs no longer endorse it.
- configure.ac's GTK2 check is vestigial (nothing uses it) — removal is
  functional, left for a ruling.
- `set_program`'s transient decode allocation is bounded by `@maxlen` = the
  size cap, not eliminated — acceptable under the loopback-only posture,
  revisit with the auth story.

## Verification at close (2026-07-31)

All 52 stmak packages green; classicladder package also `-race` clean; webapp
gates green (vue-tsc, eslint, 79 vitest); both classicladder runtests pass
against freshly regenerated 2.9 expectations (through real HAL, with size
overrides exercising the new allocator); **full runtests: 253/253**; branch
merges clean into `stmak`.
