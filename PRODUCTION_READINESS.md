# GOMC Production Readiness

Goal: prototype machines can be delivered to customers. Assumptions:

- Short access paths exist to debug/fix issues in the field (observability + deployment story required).
- All hard functional safety (protection of human life/health) is handled by **external certified hardware**.
  The software must not be silently load-bearing for any safety function — see [Safety boundary](#safety-boundary).

This document tracks the per-submodule verification pipeline. Pattern proven with milltask:
AI review vs. LinuxCNC 2.9 → findings doc → fix PRs → tests → sign-off
(see `MILLTASK_REVIEW_FINDINGS.md`).

**Companion doc — hard-RT correctness:** functional/parity review lives here; the real-time
guarantee (Go-scheduler isolation, forbidden-call enforcement in the RT path, memory
locking/prefault, deadline-miss → E-stop, the jitter-histogram soak) is tracked separately in
**`RT_HARDENING_CHECKLIST.md`**. That doc owns the RT-correctness of the inherited `cmod/*` C
(motion/tp/homing/lcec/hostmot2) that this matrix defers, and the "RT/latency validation"
cross-cutting item below points at its §3.

---

## Immediate next steps

1. **Runtests against gomc** — DONE, full migration complete (2026-07-13 first green; sweeps
   2026-07-15 lifecycle / sync-I/O / G64-blending; **2026-07-19 the whole suite is now
   un-xfailed and every category — incl. the Category D full-instance tests — is ported**):
   **232 run / 232 successful / 0 failed / 0 xfail / 0 skipped** (`runtests.log`).
   (10 obsolete module-loading over-limit xfails were removed, user ruling — see the ledger §3b.)
   `tests/DISPOSITION.md` is the authoritative ledger.
2. **CI gates** — DONE (2026-07-13; runtests dedup 2026-07-15): `ci.yml` `gomc` job = build +
   C-warning gate (owned paths, `scripts/check-gomc-cwarnings`) + `make gomc-check` (vet, tests,
   pinned golangci-lint v2.12.2 with a no-NEW-findings merge-base gate, fmt). The full runtests
   suite runs once per PR in `rip-and-test` (as the name says), which now uploads the
   failure-log artifacts; both jobs are intended required checks.
   `nightly-gomc.yml` = `gomc-test-race` + runtests against a race-built gomc-server.
   First `-race` sweep over the full module found+fixed a data race (ads notification test mock).
   **Branch protection on `gomc` — DONE** (required checks configured on GitHub: gomc + rip-and-test).
   **Lint burn-down (legacy baseline, `make gomc-lint-full`).** ⚠ **Baseline was wrong (69):**
   golangci-lint's default `max-issues-per-linter=50` capped the display, so errcheck was
   under-reported as 50. True totals (caps off): **errcheck 428, unused 19** (the 428 after a
   policy change below; ~288 with the old excludes).
   - **errcheck — DONE (2026-07-19, 0 module-wide).** **Policy decision (user): removed the
     `exclude-functions` allowlist from `.golangci.yml` entirely** — no global suppression of a
     method-set; every ignored error is now explicit at the call site (`_ =`) or a real check.
     The correctness reason: a blanket `(*os.File).Close` exclude silently hides a failed flush
     on a WRITE path (lost data, no error). Removing it took the count 288→428 and surfaced
     exactly those. Fixed all 428 across ~55 files (5 parallel sub-agents + classicladder) with
     a per-site policy: write-path Close/flush, DB ops, config/limit setters, real
     Marshal/Unmarshal → checked/returned/logged; genuinely unactionable cleanup/output → `_ =`;
     tests fail on setup/decode via `t.Fatalf`. Behavioral finds it exposed: **`task/inihal.go`
     now surfaces silently-dropped `mc.Set*` motion-limit/velocity/homing setter failures**
     (errors.Join → logged; the machine could have run with limits disagreeing with the HAL
     pins); all GMI-generator/`copyFile`/`persist_sqlite`/`emccalib`/`halcmd`/`inifile`
     write-file closes now checked; a latent double-close fixed in `gmiGenerateServerMeta`.
     Verified: errcheck 0, `go build`/`go vet` clean, `make -C src gomc-test` green (28 pkg ok).
     Commits: tranche 1 (apiserver/ads/ethercat/daemon/inifile/hal-cli), tranche 2 (excludes
     removed + fan-out), classicladder.
   - **unused — DONE (2026-07-19, 0).** All 19 removed. The two that "looked like guards that
     should be called" (`task/guards.go` requireState/requireMode) were **investigated against
     the 2.9 source before deleting** (per the reject→auto-switch model change): they mirror
     2.9's reject-if-wrong-mode check (emctaskmain.cc:2213), which gomc deliberately replaced
     with server-side `ensureMode()` auto-switch — never-called scaffold, not a missing guard.
     The other 17 (test scaffolding, superseded cgen helpers) were each triaged as genuinely
     orphaned and deleted. **`make gomc-lint-full` is now 0 findings across all linters.**
   **Websocket migration — DONE (2026-07-19):** `nhooyr.io/websocket` →
   `github.com/coder/websocket` v1.8.15 (drop-in re-home) across all 5 import sites + the
   `go.mod.in` template; the SA1019 staticcheck exclusion is removed. Verified: build/vet/lint 0,
   `make -C src gomc-test` green (apiserver ws tests exercise it live).
   (Branch protection on `gomc` is now configured — done.)
   Original plan for reference:
   - `go build ./...` + `go test -race ./...` in `src/gomc`
   - `go vet` + `golangci-lint` (incl. `staticcheck`, `unused`) — baseline first, then ratchet
   - gomc runtests subset from step 1
3. **Parity check for failing/fault paths** — DONE (fault-path-parity PR #259 + certification
   #256/#260). The capture corpus only yielded 3 trustworthy oracles (lines, arcs, spindle —
   `tests/milltask-parity/`), so the fault paths were done as **written-spec tests verified
   against the 2.9 C source** (`~/source/linuxcnc-2.9`), not capture conversion:
   - **Aborts / estop / interp-error / sequencer-hard-fault** — `on_abort` with the real reason
     enum, `seqFaultExit` hardware-stop + motion comm watchdog, ESTOP_RESET running 2.9's abort
     sequence; 10 review-round findings fixed. Coverage: `fault_path_test.go` (8 mutation-verified
     unit tests) + `tests/abort/{seq-fault-recovery,estop-while-running,on_abort_command-crazy-move}`.
   - **feed-per-rev (G95)** — 2.9 velocity-mode spindle sync ported, `GET_EXTERNAL_FEED_RATE`
     made feed-mode aware; `tests/abort/feed-rate` green.
   - **tool-change** — covered by the lifecycle sweep; **sync/m66/dio** — by the sync-I/O cluster
     (`single-step`, `remap/remap-io`).
   - Only **dwell-drain** has no dedicated fault-path test (lowest-risk of the set) — optional follow-up.

**Bug FIXED this pass (production-relevant): shutdown deadlock with ≥2 HAL threads.** Any config with `BASE_PERIOD>0` (base + servo thread — most stepper configs) hung forever on shutdown: `task_wait()` re-acquired `thread_lock` on the cooperative-exit path, so the first HAL task to be deleted exited holding it and the next task's `pthread_join` blocked. Fixed in `src/gomc/internal/hallib/uspace_rtapi_lib.c` (leave `thread_lock` released on cooperative exit). This was also the root cause of the runtests full-instance flakiness (hung shutdown → leaked gomc-server → shared-REST-port collision → stalled suite). Verified: lathe/abort-g64 now shut down ~0s; 0 leaked servers.

**Runtests migration — COMPLETE (2026-07-19).** All categories are re-enabled and green,
with nothing left xfailed or skipped: Category C (standalone interp), the HAL `test.hal`
bucket, the `halrun`-in-`test.sh` and halcompile/build tests, and the full-instance
(Category D) tests via the Python NML→`src/gmi/python` REST port. Infra added over the
effort: `gomc-server -f` one-shot + `-f --serve` resident HAL modes, `scripts/halrun`
shim, `tests/hal-stream-driver.sh`, `lib/python/gomc_test.py`. Final: 232/232 successful,
0 xfail, 0 skipped (`runtests.log`).

### Component gaps surfaced by runtests re-enablement

- **FIXED (lifecycle sweep, 2026-07-15): G43 Hn tool-length offset.** Was three stacked bugs: the ×25.4 offset value was the units bug (fixed by mm-everywhere); the startup failure was `pkgTTClient` published after `RS274NGC_STARTUP_CODE` ran; and `GetToolByNumber` reported missing tools as found-with-zero-offset (classic errors). See `MILLTASK_LIFECYCLE_SWEEP.md`. (tests/rs274ngc-startup, tests/tlo — un-xfailed)
- **FIXED (lifecycle sweep): RANDOM_TOOLCHANGER startup tool detection.** `iocontrol_start` now restores `toolInSpindle` from the pocket-0 table entry (-1 when none), per 2.9. Also fixed en route: the random `load_tool` swap moved the wrong entry (keyed `get_tool(0)` instead of the toolInSpindle entry), the .tbl importer rejected `T0 Pn` marker lines, and `find_tool_index(0)` baked non-random semantics into random G43 H0/T0. (tests/io-startup/random/*, tests/t0/random-*, tests/tool-info/random-* — un-xfailed)
- **FIXED (2026-07-16): jog/teleop + joint-mode + limit status.** Was three stacked bugs (tests/hard-limits, tests/halui/jogging un-xfailed; regression-checked against 30 homing/jog/teleop tests incl. jogwheel-axis):
  1. **Homed machine trapped in TELEOP** (`src/emc/motion/control.c` `do_homing_sequence`). The refactor that moved the homing FSM from homing.c into motmod changed the return contract from **edge**- to **level**-triggered: it returned `(all_homed && !homing_active)` — true on *every* servo cycle while homed — so the caller (control.c auto-teleop switch, gated `motion_state==FREE`) re-ran `switch_to_teleop_mode()` every cycle, instantly overriding an operator `EMCMOT_FREE` (`teleop_enable(0)` / `halui.mode.joint 1`) and making joint jogging impossible. Restored the original `base_do_homing()` contract: return 1 only on the all-homed rising edge, **and clear `homing_active` on that edge** (a joint can report homed and still-active on the same cycle; once in teleop this function is short-circuited and a stale `homing_active` would freeze `axis_handle_jogwheels()` — this was the jogwheel-axis regression during the fix).
  2. **`status.limit[]` was a direction, not a bitmask** (`internal/task/stat.go`). It set `+1`/`-1` for OnPos/OnNeg; classic `linuxcnc.stat().limit[j]` is a bitmask (minHardLimit=1, maxHardLimit=2, minSoftLimit=4, maxSoftLimit=8). Now `OnNegLimit→1`, `OnPosLimit→2` (soft-limit bits stay clear, matching the per-joint soft-limit flags gomc motion doesn't expose).
  3. **halui "jog selected" didn't re-target on a selection change** (`internal/task/halui.go` `checkJointSelection`). The held `halui.joint.selected.minus/plus` pin produces no edge in `checkJointJog`, so changing the selected joint left the old joint running and never started the new one. Ported the classic halui.cc `jselect_changed` block: stop the deselected joint (unless independently jogging via its own pins) and start the newly-selected one.
  The two ported tests also dropped their NML `echo_serial_number` / `command.serial` diagnostic prints (no gomc equivalent — synchronous `wait_complete`), matching the tests/startup-state precedent.
- **FIXED (lifecycle sweep): tool-tracking cluster.** (a) `#5400`/`#<_current_tool>` stale after M6: the tooltable's key-0 spindle snapshot loses its toolno (`PutTool` clobbers it) — the spindle canon getter now resolves via `toolInSpindle` + the live entry. (b) `M61 Q<n>`: the interp passed a pocket index where gomc io expects a tool number. Also: `GET_EXTERNAL_(SELECTED_)TOOL_SLOT` now return classic pocket semantics, T0-prepare runs the classic HAL handshake, and `[EMCIO]TOOL_CHANGE_POSITION` is implemented (was missing entirely). See `MILLTASK_LIFECYCLE_SWEEP.md`. (tests/tool-info/*, tests/toolchanger/*, tests/t0/* — un-xfailed)
- **RESOLVED — user M-codes (M1xx / `[RS274NGC]USER_M_PATH`): intentional removal, will not come back (user ruling 2026-07-15).** Compiled **cmod/gomod** handlers registered via the `mcode_handler` GMI are the intended replacement (see `MISSING_FEATURES.md` "Intentional divergences"; covered by tests/mcode-handler). Migrating a classic config means porting its M1xx scripts to a cmod/gomod. `USER_M_PATH` is read by nothing (interp and task both); leftover `USER_M_PATH=` lines in re-baselined test INIs are dead keys.
- **FIXED (lifecycle sweep): abort does not restore interp modal state + g5x desync.** gomc now implements 2.9's `emcTaskStateRestore` → `Interp::restore_from_tag`: the canon captures the interp's packed `state_tag_t` per executed block (`UPDATE_TAG`), motion segments carry it, and `abortLocked` (AUTO) restores the executing segment's modal state — which also reconciles the canon g5x shadow and term-cond via the restore's own canon emissions. (tests/statbuffer-g5x-abort un-xfailed; tests/abort/g64's modal checks pass — it stays xfail on the separate G64-blending gap below)
- **FIXED (2026-07-15): G64 blending parity — naive-CAM port + two production-relevant TP-config bugs.** tests/abort/g64 now passes ALL checks with extents matching 2.9 exactly (G61 exact-stop 5.000, G64 P0.5 → 4.500, plain G64 → 3.725, G64 Q6 → 0.000). Three stacked causes:
  1. **Naive-CAM detector ported** (`internal/task/canon_naivecam.go`): 2.9's `chained_points`/`see_segment`/`flush_segments` (emccanon.cc:875-1030) incl. the ARC_FEED chord-deviation arc flattening, with the full flush-site sweep over every canon entry point (sync-I/O canons flush FIRST — the M62-M68 ordering contract). Merged segments carry the LAST chained point's line number and state tag, and pin the tag + `Interp::active_modes`-decoded status codes on their motionMap entry (`pinMotionState`, new shim `interp_active_modes_from_tag`) so a merged move flushed during a LATER line's execute doesn't report/restore readahead modal state (the g64 test's never-executed `G64 P1 Q2` guard). 9 dedicated unit tests.
  2. **Arc blending was silently OFF on every gomc machine.** Nothing ever sent `EMCMOT_SETUP_ARC_BLENDS` (the motctl IDL had no function; the C handler existed unreachable; the config stayed calloc-zeroed) while 2.9 defaults `[TRAJ]ARC_BLEND_ENABLE=1` — so the TP ran parabolic-fallback-only corner blending everywhere: degraded corner speed and near-exact-stop G64 paths. Fixed end-to-end: IDL `setup_arc_blends` + `h_setup_arc_blends` (motctl_handlers.c) + `SetupArcBlends` pushed from `loadTraj` with 2.9's keys and defaults (1, 0, 50, 4, 100.0, 0.1).
  3. **Every teardown wiped the operator's modal G64 P from the TP.** `pushDefaultTermCond` (sequencer restarts: aborts, mode switches) unconditionally pushed the G64/0.0254mm default — so the mode switch before EVERY AUTO run reset the TP's tolerance and an MDI `G64 P<tol>` never reached the program's moves (2.9 preserves TP term cond across aborts/mode switches; tpClear keeps it). It now re-asserts the canon's CURRENT modal term cond (boot state == the default, so startup behavior is unchanged).

  **Ruled intended 2.9 parity (2026-07-15, verified against the 2.9 tree): a `G64 P<tol>` persists across programs.** Program A's tolerance stays in effect for program B when B issues no G64 of its own — this is exactly what real 2.9 does, not a gomc leak. 2.9's M2/M30 deliberately excludes motion control mode from its reset list (`interp_convert.cc:4533` — "set at machine start-up but not automatically reset by any of the stopping codes"); the only reset is a full interp re-init (`emcTaskPlanInit` → `Interp::init` → `INIT_CANON`), which 2.9 issues solely at task startup or an explicit `EMC_TASK_PLAN_INIT` — never on program open/run, and not on abort (abort's `Synch()` copies the TP's current — leftover — tolerance back INTO the interp). Do not "fix" this by re-defaulting the term cond between programs.
  Also fixed en route: a latent mechanical-port bug in `src/emc/tp/tp.c` (the `emcmotConfig`→getter conversion had corrupted a `tp_debug_print` argument list — compiled out normally, broke `-DTP_DEBUG` builds).
- **FIXED (2026-07-19, `57c162d2ca`): Operator messages lost — PRODUCTION-RELEVANT. ROOT-CAUSED to gmicompile, not the apiserver (`GMICOMPILE_REVIEW_FINDINGS.md` G-H1 + G-M1).** The generated publish drain (`internal/gmicompile/cgen/publish_go.go`) exposed a **single shared** `Watch` closure whose read was a **destructive flush** (`events = nil`), and `publish_drain_hook.go` registered it as `Watch:` (shared across connections) instead of `Factory:` (per-connection). So with N WS subscribers each operator message reached exactly one of them; single-subscriber loss then compounded via `pushLoop`'s byte-identical dedup (apiserver/ws_handler.go). Twin defect **G-M1**: `d.events` grew unbounded when no subscriber was attached. **Fixed (no apiserver change — the `WatchFuncMeta.Factory` seam was already wired at ws_handler.go:381):** the drain now emits a retained, sequence-numbered, bounded buffer + a `WatchFactory()` giving each connection its own cursor, and the drain hook emits `Factory:`. Multi-subscriber regression test added in `internal/publishtest`. (Earlier suspected as the cause of interp/oword-mdi-sub-update's xfail; disproven 2026-07-19 — that sub's `(print,…)` goes to interp stdout, not the error channel.)
- **`motion-logger` interceptor — DONE (cmod built + 2 tests green).** Implemented as an **interceptor/proxy** cmod (`src/emc/motion-logger/motion_logger_cmod.c` → `cmod/motion-logger.so`): registers `motctl`/`motstat` under its own instance name (milltask's `[EMCMOT]EMCMOT=motion-logger`), looks up the real motmod by `mot_instance=`, logs + forwards every call (real motmod = real motion + real status; no faking). milltask picks it up via a new `[EMCMOT]MOTION_INSTANCE` INI fallback (module.go). Converted + passing via runtests: `tests/motion-logger/{basic,mountaindew}`. Remaining: `tests/interp/m98m99/12`, `tests/abort/*crazy-move` (timing-dependent), and `tests/motion-logger/startup-gcode-abort` (blocked on the STARTUP_CODE gap below). Still TODO (under human review, `GOMC_PORT_SPEC.md` steps 2-3): rewire `tests/milltask-parity` to the interceptor, then **delete the `#ifdef MILLTASK_PARITY_TRACE` `motcmd_trace()` hook from `src/emc/motion/command.c`** so production RT carries no test instrumentation. Known gomc-vs-classic stream diffs (for the parity review): gomc omits `JOG_ABORT` for non-existent joints and the trailing `SET_SPINDLESYNC`; decoded-motctl format differs from the classic raw dump.
- **FIXED: main-program `M99` now loops in task.** gomc had the `interp_set_loop_on_main_m99` binding (`interp.go:203`) but milltask never called it, so `M99` at main level ended the program instead of looping (classic sets it in `emctask.cc:461`). Added `interp.SetLoopOnMainM99(true)` to milltask `initInterpreter`. (unblocks tests/interp/m98m99/12)
- **Minor: gomc `rs274` standalone emits extra `ON_RESET()` canon calls** vs the classic dump (one after `SET_FEED_REFERENCE`, two at `PROGRAM_END`). Benign-looking (interp reset lifecycle) but breaks byte-exact `expected` comparison; re-baselined where it appears. Worth a look if canon-call parity matters. (tests/interp/m98m99/12)
- **FIXED (fault-path parity, PR #259): `ON_ABORT_COMMAND` now wired.** `interp on_abort` runs on every abort path (including `recoverSeqFault` for producer-less sequencer faults) with the classic reason enum as the numeric argument to a configured `[RS274NGC]ON_ABORT_COMMAND` (`internal/task/commands.go`). (tests/abort/on_abort_command-crazy-move remains gated on the `gmi.Stat` queue-depth gap below)
- **`rtapi_shmem_delete` not exported to cmods.** A cmod calling `rtapi_shmem_delete` fails to dlopen ("undefined symbol: rtapi_shmem_delete"), though `rtapi_shmem_new`/`rtapi_shmem_getptr` ARE exported (shmem allocates) and delete is used internally in hal_lib.c. Add it to the cmod symbol exports. (tests/rtapi-shmem — after the .comp was fixed to proper multi-instance)
- **FIXED (2026-07-20, `GMICOMPILE_REVIEW_FINDINGS.md` G-H2, commit `04b1d14df9`): gmicompile `--server-go` mis-typed callback/ptr params.** The generated `_bridge.go` //export trampoline used to type `callback`/`ptr` params as `C.int`, truncating 64-bit pointers. Now `isOpaquePtrParam` routes them as `unsafe.Pointer`/`void*`/`uint64` (mirroring the correct `IsPtr` handling), `cTypeForAPICgo` maps `PrimPtr`→`unsafe.Pointer`, and `emitCommands` skips opaque-ptr functions (unmarshalable over REST). `mcode_handler` was migrated to `--server-go` and its hand-written provider (`internal/task/mcode_provider.go`) reduced to the milltask-specific handler invocation only; `tests/mcode-handler` passes end-to-end.
- **Streaming timing: live streamer/sampler doesn't preserve one-line-per-thread-cycle multiplicity.** Converted sampler/streamer HAL tests where the *temporal pattern* matters (a value held across N consecutive input lines should yield N sample rows) don't reproduce classic timing: gomc yields a different number of repeated rows (values are correct, row multiplicity differs). Stateless single-shot streams (abs.0: 7 distinct values → 7 samples) convert cleanly; held-value / debounced / stateful ones don't. Suspect the streamer advance vs sampler capture is not locked one-per-thread-cycle under the live WS feed. Blocks tests/mux, tests/multiclick (both now xfail on this, multi-instance rework done); worth checking whether "passing" streaming tests only pass because their expected was regenerated to gomc timing.
- **`gmi.Stat` missing motion queue depth.** No `queue` / `active_queue` / `queue_full` fields, though the controller has it (`motstat get_queue_depth`). Drivers that gate on readahead fill (`while s.queue > 1000`) can't. (tests/abort/{on_abort_command,stop-button}-crazy-move)
- **FIXED: `RS274NGC_STARTUP_CODE` now executed.** `runStartupCode` (`internal/task/commands.go`) runs `[RS274NGC]RS274NGC_STARTUP_CODE` through the interpreter once at task startup, after the tool-table provider is live, matching 2.9. Remaining known divergence: startup code containing *motion* faults exec_state at estop (2.9 parks the move in the interp_list) — tracked as the "Startup-code motion at estop" cross-cutting item below.
- **`gmi.Stat` field gaps** (client, not controller): missing `cycle_time`, `max_acceleration`, `max_velocity`, `program_units`, `queued_mdi_commands`, `tool_from_pocket`; joint position is `joint_actual_position` (not `joint_position`). Some full-instance drivers simplified their status-waits around these. (tests/startup-state, tests/mdi-queue-length)
- **CORRECTION (was wrongly reported):** `(DEBUG,msg)` / OPERATOR_DISPLAY messages DO reach the `gmi` ErrorChannel as `(13, msg)` — they work with a poll-loop + settle. (tests/interp/oword-mdi-sub-update xfails for other reasons.)
- **FIXED (2026-07-15): milltask synchronized-I/O (M67/M62 + blended motion).** The actual sync-I/O loss was the motctl single-slot send race, fixed 2026-07-14 in `104f633164` ("motctl: serialize command send/ack — concurrent senders lost commands"): a concurrent sender could overwrite the shared command slot before the RT side (one command per servo cycle) consumed it, so the sequencer's `SET_AOUT` was silently dropped during full-speed AUTO read-ahead. Re-verified with position-correlated pin sampling: outputs now apply at exactly the right segment boundaries in plain AUTO **and** single-step mode (aout toggles as each iteration's first non-zero-length move activates, matching 2.9's `tpToggleDIOs` semantics). What still failed afterwards was the *test driver contract*, two client-side bugs fixed 2026-07-15: (1) `gmi.Stat.poll()` was a no-op over a 50 ms WS push cache — a driver polling right after a command could see a pre-command snapshot (tests/single-step saw `interp_state==IDLE` right after its first STEP commands and declared the program finished in 50 ms); poll() now does a synchronous fresh REST `GET /stat`, restoring classic `linuxcnc.stat.poll()` semantics for every ported driver. (2) tests/single-step's driver compared gomc-mm positions against inch goals. tests/single-step and tests/remap/remap-io un-xfailed; remap-io passed 5/5 consecutive runs (was intermittent). The same sweep un-xfailed **tests/lathe**: its "jog overshoot from WS-lagged gmi.Stat" diagnosis was doubly stale — after mm-everywhere it failed deterministically on mm-vs-inch positions, and its continuous jog was then killed mid-travel by the jog dead-man watchdog (below); fixed in the driver + `linuxcnc_util.jog_axis` (jog refresh in the wait loop), 19/20 green. **The residual 1/20 is closed (2026-07-17):** `jog_axis` asserted the non-jogged axes had not moved with float `==` on a live status feed (and gomc scales mm -> machine units on the way out, so a 1-ULP wobble yields a different float). It fired on axes that had not moved — the failure printed "axis z moved from 0.000 to 0.000". Now compares against `IDLE_AXIS_EPSILON`; 6/6 green.
- **Client API contract: continuous jogs are dead-man'd (INTENDED model change — document for every GUI port).** gomc's task kills a REST/GMI continuous jog not refreshed within 2 s (`internal/task/task.go` `jogTimeout`, monitor `checkJogWatchdog`) as runaway protection for disconnected clients; HAL-pin-driven jogs (`JogFromHAL`) are exempt. Classic NML jogs ran until JOG_STOP with no such contract, so any ported client that starts a continuous jog and waits (GUIs, halui-like drivers, linuxcnc_util) MUST re-issue the jog within the interval. Also remember the client boundary is mm: jog velocity is mm/s (a classic inch-config driver passing machine-units/s jogs 25.4× too slow).
- **FIXED: `haljson` nil-INI segfault under `gomc-server -f`.** `newHaljsonModule` dereferenced `ini.SourceFile()` to resolve a relative `config=` path even when loaded without an INI (one-shot/resident `-f` HAL file), panicking with a nil-pointer SIGSEGV — the same class of bug as the earlier pyvcp nil-INI fix. Now resolves relative paths against the cwd when `ini == nil` (`internal/haljson/module.go`). Surfaced by porting tests/halmodule.0. (Worth a sweep: any module resolving `config=` via `ini.SourceFile()` without a nil-guard has this bug.)
- **FIXED: public-header hygiene (2 installed headers referenced uninstalled headers).** `include/axis.h` pulled in `gomc_hal.h`/`gomc_log.h` (cmodule ABI, internal) and `include/inifile.h` pulled in `<iniparse.h>` (not installed) — neither compiled standalone. Root causes: `emc/motion/axis.h` was wrongly listed in the public `SRCHEADERS` (it is an internal motion-module header, consumed only by in-tree `src/emc/motion/*.c` / `tp.c`), and `iniparse.h` (a real public dep of the widely-used `inifile.h`) was missing from `SRCHEADERS`. Removed `axis.h` from the public set and added `iniparse.h` (`src/Makefile`). tests/build/header-sanity now passes; all 61 installed headers compile standalone.
- **FIXED: the `interp_ext` GMI provider was never registered — C remaps / O-words were non-functional.** `interp_ext` (`register_oword` / `register_remap_prolog` / `_epilog` — the C replacement for Python O-word subs and `py=` remap prolog/epilog) was declared "Provider: milltask.so", but nothing ever registered it. So `stdglue.c` (the C port of `stdglue.py`) and any custom handler got `interp_ext API not found` at `Start()` and failed — meaning **C-cmod remaps (T/M6/M61/S/F via stdglue) and C O-word subs did not work at all**. Fixed by registering the provider under the milltask instance in `milltask.Start()` after `initInterpreter()`, with `ctx` = the interp's `Interp*` and the callback fields pointing at librs274's `interp_ext_register_*` routing functions (hand-written `internal/task/interp_ext_provider.go`, mirroring the mcode_handler provider). Verified: the `configs/sim/axis/remap/stdglue-cmod` demo now starts (it also needed `LIB:linuxcnc.hal` added), and `tests/interp-ext` registers + dispatches a C O-word.
- **FIXED: C O-word positional args were not passed to the handler.** A C-registered O-word (`interp_ext register_oword`) called as `o<name> call [a] [b]` received `n_args=0`. Two causes: the `CT_PYTHON_OWORD_SUB` dispatch path (`interp_o_word.cc`) skipped the arg-copy the NGC-sub path does (it never populated `#1..#30`/`n_args`), and `interp_python.cc` gathered args via `find_named_param("1"..)` — but positional params are *numbered*, not named. Fixed: the O-word dispatch now copies `eblock->params` into `#1..#30` and sets `n_args` (save/restore around the synchronous call), and the handler-gather reads the numbered subroutine params. Verified: tests/interp-ext passes `[10] [20]` → the handler sees `n_args=2 arg[0]=10 arg[1]=20`.
- **modcompile: generated `New()` flattens init errors to `-1`.** The generated component constructor has a single `err:` label that always `return -1` (`internal/modcompile/cgen/cgen.go:914`), so a specific error code from `EXTRA_SETUP` (e.g. `-ERANGE`) — or any pin/param/init failure — is lost; the launcher reports `factory returned error code -1` regardless. The load correctly *fails* (behavior is right), but the diagnostic errno is generic. Classic halcompile propagated the actual code. Minor; fix by returning the real error code on the `EXTRA_SETUP` path. (Surfaced porting tests/module-loading/rtapi-app-main-fails.)
- **`libgmi.so` does not declare its `libcurl`/`libcjson` dependencies.** `objdump -p lib/libgmi.so` lists only `libc` as `NEEDED`, yet it uses curl and cJSON symbols — so external consumers must add `-lcurl -lcjson` themselves (a properly-packaged shared lib should link its own deps). Minor; tests/build/ui links them explicitly and passes. (Surfaced re-expressing the classic NML client build test against libgmi.)
- **FIXED (mqtt-bridge, surfaced porting tests/mqtt): nil-INI segfault + double-prefixed pin names + no offline test path.** Three issues in `internal/mqttbridge`, all fixed while porting the classic `mqtt` test: (1) `newMQTTBridge` dereferenced `ini.SourceFile()` to resolve a relative `config=` path, segfaulting under `gomc-server -f` (no INI) — same nil-INI class as the haljson/pyvcp fixes; now falls back to cwd when `ini == nil` (`module.go`). (2) Every HAL pin was double-prefixed (`mqtt-bridge.mqtt-bridge.cnc-position.X`) because the bridge manually prepended the component name that `hal.NewPin` already adds; now single-prefixed (`bridge.go`). (3) The bridge could only dial a real broker (paho `SetConnectRetry(true)` → `start()` blocks forever offline) and exposed no publish liveness — so it was untestable without a broker. Added a `dryrun` load arg (`load mqtt-bridge config=… dryrun`) that skips the broker but still runs the publish loops, plus a `<name>.publish-count` liveness pin that advances per publish tick (mirrors the classic `mqtt-publisher --dryrun` + `lastpublish`). tests/mqtt drives motion and asserts publish-count advances.
- **`HAL_PORT` type not exposed via `haljson`/REST — DEFERRED (documented).** `HAL_PORT` is fully implemented in `hal_lib` (the C core), but the client boundary can't reach it: `haljson.parseHalType` only knows bit/float/s32/u32, and there is no REST read/write for port buffers. So a client (e.g. Python) cannot create or drive a PORT pin. Implementing PORT over REST is nontrivial (buffer/stream semantics) and deferred. Consequence: tests/pyhal ports its scalar (s32/u32/float/bit) coverage but omits the PORT read/write/peek portion, which is documented here rather than tested.


Real gomc behavior gaps found while converting HAL tests (tests skipped with
reasons; these are component bugs, not test problems):

- **`conv_float_u32` missing** — comp absent entirely (no cmod, not in registry). (limit3/constraints)
- **`logic` ignores `personality=`** — only the `.time` pin is created, not the configured and/or/in-NN pins. (loadrt.1)
- **INTENDED gomc model change (not a gap): `stepgen` module-param instance count.** `load stepgen <stepgen.0> step_type="2,2,2"` creates 1 instance, not 3 — gomc has no array module params and derives instance count **solely from the explicit name list**, by design (the same call already used for `num_chan=`/`count=`, which were converted to name lists). A scalar module param is applied identically to every named instance. modparam.0 was rewritten to the gomc idiom (asserts scalar-param *application* via the `.phase-A` pin, `51abb8369b`); it never existed on 2.9. (modparam.0)
- **`mux_generic` single-instance only** — rejects the classic multi-instance comma config (`mux-gen.NN`); errors `invalid character ',' in config string`. (mux, multiclick)
- **RESOLVED (not a gap): mb2hal debug output routing.** The INI-DEBUG dump was thought unreachable, but the DBG calls DO fire — they route through `gomc_log_debugf` at slog *debug* level, filtered out at the default INFO level. Running the server at `-d 0` surfaces the full dump; the test normalizes the slog wrapper back to the classic `mb2hal <fnct>` form and passes (`c0b7fc6853`). No component change needed. (mb2hal.1a/2a)
- **one-shot `list`/`show` render nothing to stdout** — the `-f` executor's halparse path doesn't emit list/show output (worked around via resident server + `halcmd`).
- **INTENDED gomc model change (not a gap):** there is no `singleton` concept and no rt/userspace separation — a single cmod can provide both realtime and userspace behavior. So `option singleton`, `option rtapi_app no` (+ custom `rtapi_app_main`), and userspace `--install` (.c→`bin/`) have no direct modcompile equivalent BY DESIGN. Tests built on those concepts (rtapi-shmem, module-loading/rtapi-app-main-fails, halcompile/userspace-count-names) must be re-evaluated against the single-cmod model, not treated as blocked.
- **`conv_*` comp family** — FIXED: was unbuilt (generator existed but `CMOD_COMPS` wildcard missed the ungenerated files); wired into the build, all 11 now in `cmod/`. Enables `limit3/constraints`.
- **`modcompile` gaps vs `halcompile`**:
  1. **relative include path** — FIXED: modcompile now adds the source file's own directory to `-I`. (halcompile/relative-header enabled)
  2. **name-match enforcement** — FIXED: modcompile now rejects `component <name>;` != filename (normalizing `-`→`_`). (halcompile/names enabled)
  3. **personalities non-functional** — no `--personalities` flag, AND comps ignore `personality=` at load (only `.time` pin created). `modcompile --personalities=2` exits 0 (silently ignores unknown flag — modcompile likely should reject unknown flags). (halcompile/personalities_mod; ties to the `logic` personality gap above)
- **gomc HAL lock model differs** — `all|tune|none`, not the classic 4-level `LOAD/CONFIG/PARAMS/RUN`; `status`/lock rendering absent. (halrun-lock unfixable as-is)
- **No two-pass HAL loading (TWOPASS)** in gomc. (twopass, twopass-personality)
- **`halcmd getp` prints a verbose line** (`s32 OUT name = val`), not a bare value — output-parsing tests must `awk '{print $NF}'`.
- **`halcmd getp` does not resolve RW params** — `getp and2.0.tmax` prints an empty value even though `show param` lists `and2.0.tmax` with its real value, and `getp` on a pin (`and2.0.time`) works. Function-timing params (`.tmax`) must be read via `show param` (tests/threads.1 reads tmax this way). Minor; worth aligning getp with the param table.
- **hostmot2 sim / hm2 test comp** path not validated on gomc. (hm2-idrom)

---

## Review tiers

Manual review of everything is not realistic (~60k LOC of Go alone). Risk-based split:

- **Tier 1 — human review mandatory.** State machines, abort/error paths, concurrency
  ownership, and everything on the [hotspot list](#tier-1-hotspots). ~8k LOC total.
- **Tier 2 — AI review with adversarial verification.** Independent AI passes attempt to
  refute each finding; a human only adjudicates findings that survive (CONFIRMED).
  One findings doc per submodule (`<MODULE>_REVIEW_FINDINGS.md`).
- **Tier 3 — mechanical checks only.** Lint, `-race`, deadcode, and spot checks.
  Applies to generated code (`generated/gmi/*`), thin CLI wrappers, test scaffolding.

Review checklist applied in Tiers 1+2 (from the project kickoff list):

- quick fix taken instead of clean implementation
- unused code (largely automated via `staticcheck`/`unused`)
- redundant code that should be refactored
- magic numbers where enum/const should be used — is generated code (gmi) available for it?
- hand-written code where generated (gmi) code should be used
- compatibility macros or shims — none allowed
- thread-local porting hacks (no real thread/multi-instance safety)
- mix of concerns
- workarounds for missing gmi/architecture features (file as gmi feature requests instead)
- functional differences (regressions) vs. LinuxCNC 2.9
- TODO/FIXME/HACK markers (currently 18 in non-generated Go code)
- polling that should be event-driven
- artificial timeout handling
- logic and error handling validation

Known transferable risk classes from the milltask review — check explicitly in every module:

1. **Goroutine ownership** — who starts it, who stops it, shutdown ordering. 2.9 had no
   goroutines, so parity checks cannot catch this. Requires `-race` + an ownership writeup.
2. **2.9 edge parity** — error/abort paths diverge more easily than happy paths.
3. **Codegen duplication** — a bug in `gmicompile` replicates into all 39 generated packages;
   review the generator once, thoroughly, instead of its output 39 times.
4. **Fixed-but-untested** — every review fix needs a test that would have caught it.

---

## Submodule matrix

Stages: **L**int clean · **R**eview done (tier per row) · **F**indings fixed ·
**U**nit tests adequate · **RC** race clean · **FP** fault paths tested · **S**ign-off

LOC = non-test Go lines / test lines (2026-07-11 snapshot).

### Phase 0 — done

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/task (milltask) | 12445/4839 | 1 | ☐ | ✅ | ✅ | ✅ | ✅ | ✅ | ◐ |

Milltask review closed and merged; **fault-path parity done** (PR #259 + certification
#256/#260 — written-spec tests vs the 2.9 source; see [Immediate next steps](#immediate-next-steps)
§3). Remaining before full sign-off (`S`): lint-clean (`L`, part of the CI lint burn-down) and
the final human sign — the functional/parity work itself is complete.

### Phase 1 — foundation (bugs here multiply into everything else)

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| pkg/hal | 1088/191 | 1 | ✅ | ✅ | ✅ | ◐ | ✅ | ◐ | ◐ |
| internal/gmicompile | 10755/2141 | 1 (emission logic) / 2 (rest) | ✅ | ◐ | ◐ | ◐ | ✅ | — | ◐ |
| generated/gmi/* boundary | n/a | 3 (spot-check vs IDL) | ☐ | ☐ | ☐ | — | ☐ | — | ☐ |
| internal/realtime | 47/28 | 1 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| internal/gmi | 376/262 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| pkg/gomc, pkg/cmodule | 94/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |

**`pkg/hal` — reviewed 2026-07-20 (Tier 1 hotspot #1; the binding layer every RT interaction
crosses).** Architecture verified: two module kinds cross this boundary differently — **cmods**
(C plugins) are pure C (RT funcs + non-RT **pthreads**, lifecycle in their own C `Stop`) and
**never touch `pkg/hal`**; **gomods** (Go, `gomc.Module`) are the only users of `hal.Component`,
and stop their goroutines via `gomc.Module.Stop()` + the module's own stop channel (e.g.
`bridge.stopCh`), driven by the launcher's `stopGoModules`/`destroyGoModules`. Findings:
- **H-1 (correctness, FIXED):** `Pin.String()` took `RLock` then called `Get()` (which
  `RLock`s again) — a recursive read-lock that Go's `RWMutex` forbids and that deadlocks if a
  `Set()` writer contends between the two. `String()` no longer locks (name/direction immutable,
  `Type()`/`Get()` self-lock). Regression test added (`pin_test.go`).
- **H-2 (dead code + false doc, FIXED):** `Component.Running()`/`Stop()`/`done`/`running` had
  **zero** callers — leftovers from a standalone `for comp.Running()` userspace-component model
  that `gomc.Module` replaced — and `doc.go` claimed automatic SIGTERM/SIGINT handling that
  never existed (no `signal.Notify`, no goroutine; the launcher owns shutdown). Removed the
  scaffolding; `doc.go` rewritten to the real `gomc.Module` lifecycle. (User-ruled removal.)
- **H-3 (silent no-op, SURFACED + documented):** an unlinked `HAL_PORT` string pin has no
  backing buffer, so `Pin[string].Set()` silently dropped the write (`halPortWrite`'s `false`
  return was discarded) and `Get()` returned `""`. Added `Pin[T].TrySet() error` (scalars always
  nil; string path returns `ErrPortWriteFailed` on a dropped write); `Set()` now delegates to it
  fire-and-forget. `adsbridge`'s string `writeFn` now returns `TrySet` so an undeliverable ADS
  string write surfaces as an ADS `ErrInternal` instead of a false ACK. Contract documented at
  `NewPin`/`Get`/`Set`/`TrySet`.
- **H-4 (documented, in `hal_lib.c` not pkg/hal):** the in-process HAL data segment is torn down
  when the last component exits; a subsequent `hal_init` returns EINVAL (HAL not cleanly
  re-initializable in one process). Production-safe (components created once, never cycled); the
  test binary works around it with a keep-alive `TestMain`.
- **H-5 (design note):** `Pin`'s `RWMutex` serializes only Go-side callers; the RT C thread
  writes the same shared-memory cell bypassing it. Genuinely needed for the multi-step PORT
  framing; for scalars it's Go-side-only serialization over an inherently lock-free HAL cell —
  documented, not "fixed."
Coverage raised from 54→191 test lines (round-trip for all scalar types, the `String()`-vs-`Set()`
concurrency regression, `TrySet` failure). `U`/`FP` left ◐: `Component` lifecycle (Ready/Exit),
`LookupValue`, and the linked-port round-trip still want tests. Verified: build ./... green, vet
clean, `go test`/`-race` green, gofmt clean. Awaiting final human sign (`S`).

**`internal/gmicompile` (cgen emission logic) — Tier-1 review DONE 2026-07-19/20 (hotspot #2;
the risk-class-3 multiplier — one wrong emission replicates into ~39 generated packages).**
Full findings + verdicts in `GMICOMPILE_REVIEW_FINDINGS.md`. Method: four independent AI passes,
each ground-truthed against the actual generated output in `generated/gmi/*` (all 33 committed
packages swept). The two catastrophic classes are verified **closed generator-wide**: cgo handle
transit (the persist-`cgo.Handle` production crash — `ctx` is `C.uintptr_t` everywhere, no handle
parks in a GC-scanned Go pointer slot, both directions) and returned-data ownership (no leak /
double-free / UAF for any returning shape that occurs). Live/production findings all FIXED:
- **G-H1 + G-M1** (`57c162d2ca`): operator-message loss root-caused *here*, not the apiserver —
  the publish drain emitted a single shared destructive-flush `Watch` (N subscribers → each message
  reaches exactly one) plus an unbounded accumulator with no subscriber. Now a retained,
  sequence-numbered, bounded ring + per-connection `WatchFactory`, with a multi-subscriber
  regression test in `internal/publishtest`.
- **G-H2 + G-L6/PrimPtr** (`04b1d14df9`): `--server-go` truncated callback/`ptr` params to `C.int`
  (64-bit pointer truncation); now routed as `unsafe.Pointer`/`void*`/`uint64`. `mcode_handler`
  migrated to the generated bridge; hand-written provider retired.
- **G-M2/M3/L2/L3** (`6d08f75307`, `9f1ace9fa5`): type mappers unified (py/ts drift closed), dead
  `client_go_internal.go` and dead `--server-ws` mode removed.
- **G-L4 + G-L6/residual** (`bb14a10d1e`): the two silent-wrong emitter fallbacks now **panic**
  with a shape-naming message instead of emitting broken/mismapped cgo (`cTypeForAPICgo` no longer
  defaults to `C.int`; `emitFieldGoToC` rejects `[N]string`). Both latent-no-trigger — a full `make`
  regen of every package is byte-identical (guards never fire); two guard tests added.
`R`/`F`/`U`/`S` left ◐ because (a) only the **Tier-1 emission logic** was in scope — the parser/AST
side is Tier 2, not yet reviewed; and (b) the remaining findings are **deferred as NEEDING MANUAL
REVIEW / a design decision, not auto-fixable**: G-M4 (TS 64-bit `number`→`string`+bigint = client
API-surface call), G-L1 (`_cb` RT annotation — cross-cutting, belongs to `RT_HARDENING_CHECKLIST.md`
item 1b), G-L5 (array-size symbol drift — latent, fix touches emission across ~39 packages), G-L7
(external `*_client.c` nested-struct parser — feature work needing a consumer test). Each still
needs its own test (risk class 4). `RC` ✅ (module-wide `-race` sweep green). `FP` — (a code
generator, not a runtime fault-path module). Verified across the fix commits: full `make` regen
git-clean, cgen suite green, build/vet green.

**`internal/realtime` — reviewed 2026-07-20 (Tier 1; functional review done, awaiting final
human sign `S`).** Architecturally reduced to a startup stub: `New()`/`Start()` are called
exactly once (`launcher.go:230` full `Run()`; `halrun.go:85` subset), no goroutines, no
shared-memory lifecycle, **no cyclic path** — so Tier-1 hotspot #6 ("no GC-managed allocation
in cyclic paths") does not apply here; the cyclic RT paths live in `cmod/*` /
`RT_HARDENING_CHECKLIST.md`. RT modules load in-process via `dlopen` (halcmd shims); HAL/RTAPI
shm is in-process heap, so the 2.9 `realtime.in` SysV-shm `ipcrm` cleanup is obsolete
(`ipc_cleanup.go` correctly empty). **Two cleanups applied (user ruling — remove vestigial
checks):** (1) dropped the `/dev/zero` "sanity check" — the in-process heap rtapi never
touches `/dev/zero` (confirmed: it appears nowhere else in gomc), so the check validated a
resource the runtime doesn't use and gave false RT-readiness confidence; (2) removed the dead
`RTAPI_DEBUG` branch — it only logged and propagated nowhere (no `getenv` consumer in the
uspace rtapi; msg level is the server `-d` flag). `Start()` is now an honest minimal validator
that keeps its `error` return as the launcher's startup contract seam. Verified: vet + `go test`
green, launcher builds, gofmt clean.

**`internal/launcher` + `internal/daemon` — reviewed 2026-07-20 (Tier 1 hotspot #4; process
supervision, startup/shutdown ordering, restart-after-crash, goroutine ownership).** The linear
startup (`Run`) and ordered shutdown (`doCleanup`, idempotent via `cleanupOnce`) are careful and
correct: RT barrier (`StopThreads`) before any resource free, retain stopped before the barrier,
log ring torn down last with the handler cleared first, every cleanup step gated on `halComp != nil`
and log-not-return so all steps run. `daemon.go` clean (one minor: parent+child both write the
pidfile, unlocked — same PID, low severity). The concurrency exposure is concentrated in the
**runtime REST load/unload surface**, not the single-threaded startup/shutdown. Findings:
- **L-1 (concurrency, FIXED):** `shutdown()` closed `shutdownCh` via a non-atomic
  `select/default` check-then-close. Three independent goroutines call it (SIGINT/SIGTERM handler
  `launcher.go:161`, halrun handler `halrun.go:81`, REST-server death watcher via `fail()`
  `rest_server.go:113`); two firing in the same instant → `panic: close of closed channel`,
  crashing the process instead of running the ordered shutdown (leaving HAL/RT + shm loaded — the
  exact failure the design avoids elsewhere). Replaced with `shutdownOnce sync.Once`, mirroring
  `cleanupOnce`. Regression test `TestShutdown_ConcurrentSafe` (64-goroutine stress, green under
  `-race`).
- **L-2 (partial-startup crash, FIXED, C side):** `stopCModules` called `cmod_call_stop` on
  **every** module unconditionally, while `startCModules` and `unload.go:97` both guard on
  `cm.started`. After a partial-startup failure (`startCModules` returns mid-loop), later modules
  are loaded-but-not-started; bulk-stop then violates their start-before-stop contract and can
  crash. Guarded `stopCModules` with `cm.started`. (Not unit-tested: `cmod_call_stop` is a cgo
  call on a real module pointer — mirrors the already-established `unload.go` contract.)
- **L-3 (data race, FIXED 2026-07-21):** `l.cModules`, `l.goModules`,
  and `l.cModArena` were mutated with **no lock** from HTTP-handler goroutines
  (`runtimeLoadModule`/`UnloadModule`, wired via `halrest.SetLoad/UnloadModuleFunc`) while the
  shutdown goroutine iterates/frees them (`stopCModules`/`destroyCModules`, cmodules.go:566 nils
  the arena). The only mutexes in the package are `subsMu` and `fatalMu`. Two concurrent REST
  load/unloads, or a runtime unload racing shutdown, is a genuine data race (slice realloc during
  iteration; double-free of arena C strings). **Not a blind-mutex fix:** the `gomc_ini_get*`
  `//export` callbacks (gomc_env.go:265/273/291/295) append to `cModArena` and are invoked *during*
  cmod load/init from C-plugin threads — a single mutex over both the load path and the arena would
  self-deadlock (non-reentrant). Recommended design: a dedicated `arenaMu` held only around the
  append/free (never across a C call) + a `modMu` serializing the REST load/unload handlers with
  snapshot-under-lock in the shutdown iterators. Mitigant narrowing the load-vs-shutdown window:
  `doCleanup` stops the REST server first (`stopAPIServer`, 2s `http.Server.Shutdown` drains
  in-flight handlers) before iterating modules — but concurrent REST-to-REST load/unload still
  races. Decide whether runtime REST load/unload is a supported production path before sizing.
  **RESOLVED (2026-07-21): runtime REST load/unload IS supported (user ruling) → full fix landed.**
  Two locks per the recommended design: **`arenaMu`** guards `cModArena`, held only around each
  append (via `arenaAppend`) and the free-and-nil loop, **never** across a cgo call — so the
  re-entrant `gomc_ini_get*` `//export` appends on the loading goroutine can't self-deadlock;
  **`modMu`** serializes `loadModuleNamed`/`UnloadModule` end-to-end (held across the cgo
  `cmod_call_*` calls — safe because no `//export` callback takes `modMu`) and guards the
  `cModules`/`goModules` slices, with snapshot-under-lock in the shutdown iterators (destroy
  variants nil the live slice under the lock). A **`shuttingDown` gate** (set under `modMu` in
  `doCleanup` right after `stopAPIServer`) makes any straggler load/unload fail fast with
  `ESHUTDOWN`, so the iterators run with no concurrent mutator. Lock nesting only `modMu ⊃ arenaMu`.
  Mutation-verified `-race` regression `TestLoadRace` + gate test `TestShutdownGate`
  (`loadunload_race_test.go`) — HAL-free (Go-module path); the real cmod `gomc_ini_get` re-entrancy
  is exercised by the nightly `-race` runtests load/unload cycle.
- **L-4 (LOW, documented):** `stopGoModules`/`unloadGoModule` call `Module.Stop()` without a
  started-guard (goModule has no `started` field). Internally consistent (both paths do it), so
  only a bug if `gomc.Module.Stop()` is unsafe without a prior `Start()`; verify the interface
  contract, then either document it or add symmetric `started` tracking.
- **L-5 / L-6 (LOW):** `l.apiServer` field write/read is unsynchronised (safe only under the
  current start-before-stop ordering; would race if runtime restart is added); the signal-handler
  goroutine + `signal.Notify` are never `Stop()`d (orphaned goroutine + leaked registration —
  harmless for a process that then exits, matters only if a `Launcher` is constructed twice in one
  process, e.g. tests). `retainSync`'s 1s busy-wait can delay shutdown by up to 1s if the servo
  thread stalls.
Verified: vet clean, build ./launcher + ./daemon green, `go test -race ./launcher/...` green.
Row → L R F ✅ (L-3 fixed 2026-07-21); `U`/`FP`/`S` pending (L-4 contract check + L-5/L-6 + human sign).

### Phase 2 — field I/O (drives real iron; highest risk per untested line)

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| cmd/ethercat | 3867/90 | 1 | ✅ | ✅ | ✅ | ◐ | ✅ | ◐ | ◐ |
| internal/ads | 1763/1700 | 2 | ✅ | ✅ | ◐ | ◐ | ✅ | ◐ | ☐ |
| internal/adsbridge | 498/47 | 2 | ✅ | ✅ | ◐ | ☐ | ✅ | — | ☐ |
| internal/adsconfig | 1473/2988 | 2 | ✅ | ✅ | ◐ | ◐ | ✅ | — | ☐ |
| internal/adsmodule | 163/0 | 2 | ✅ | ✅ | ✅ | ☐ | ✅ | — | ☐ |

**ADS cluster — reviewed 2026-07-21 (Tier 2, adversarial). Full findings +
verdicts in `ADS_REVIEW_FINDINGS.md`.** Net-new gomc code (no 2.9 oracle); the ADS/AMS server
binds `0.0.0.0:48898` with **no protocol authentication**, so every command handler is reachable
by any host that can route to the controller. Method: primary read-through + two independent
refutation passes (remote-DoS lens, concurrency/lifecycle lens). **Headline: a remote,
unauthenticated client could crash or OOM the motion controller with a single ~28-byte packet**
— all fixed this pass:
- **A1** SumWrite `uint32` overflow → slice panic (deterministic crash); **A2** unbounded
  `make` from the client-controlled sub-request count (≈137 GB alloc → OOM); **A3** unbounded
  process-image read `length` (≈4 GB, incl. a notification `sendLoop` that re-OOMs every 10 ms);
  **A4** no `recover()` in any goroutine (a wire-path panic killed the process). Bounds +
  `recover()` added; regression tests in `internal/ads/dos_test.go`.
- Robustness fixes: **A6** write deadlines, **A10** accept-loop backoff, **A11** idempotent
  `Stop()`, **A12** construction-error HAL-component leak. **A5 (partial):** closed the
  accept/register race + made the stage-2 read honor `quit`, narrowing the known **ADS2**
  shutdown-UAF window (the full free-barrier contract stays open, to decide with pkg/hal H1).
- **Cleared by refutation:** the notifyManager races and the SymbolTable lock model (incl. the
  suspected re-entrant-RLock deadlock) — locking there is correct; ADS1 already fixed.
- **Also fixed:** A8 (`[0..N]` array lower-bound silently mis-laid-out — added `Node.IsArray`,
  replacing the broken `ArrayStart>0` guards; regression test).
- **Open (adjudication / follow-up):** A5 contract, A7 (connection/subscription caps — needs the
  expected-HMI-count decision), A9 (0.0.0.0/no-auth → Safety-boundary doc), A13/A14 (low).
Verified: build/vet/gofmt clean, lint 0, `go test -race` green. `F` left ◐ (A5/A7/A8 open),
`U` ◐ (crash-path regression tests added; happy-path coverage already strong), `S` pending.

**EtherCAT sim-transport integration harness — M1+M2 done (2026-07-21).** The whole `cmd/ethercat`
+ lcec driver stack (Phase 2's highest-risk, untested-per-line area) is now testable **without
hardware**. The EtherCAT master's in-process, datagram-level slave emulator (`transport_sim`, an
`ec_transport_ops_t` impl that answers scan/AL-state/SII/CoE at the wire level) was promoted from
the submodule's test-only `tests/` into a **first-class, config-selectable transport**
(`EC_TRANSPORT_SIM`; `transportType="sim"`, `interface=<bus-description-file>`) — shipped, not
flag-gated (a user can dry-run a config). It stays RT-honest: the mutex-holding cyclic
`send`/`receive` are `ECRT_RT_TRUSTED`-annotated and `transport_sim.c` is in the master's
`rt-effects-check` (green). Branch: submodule `transport-sim` off `production-readyness`.
**Six ethercat runtests now** (all pass `scripts/runtests`, stable), exercising the driver
end-to-end on emulated slaves — **M2 complete**:
- `sim-basic` — driver reaches OP, PDOs surface as HAL pins.
- `sim-pdo-loopback` — PDO value round-trip both directions (a `loopback` sim slave echoes
  output process data SM2→SM3; `dout=N → din=N`).
- `sim-sdo-config` — startup SDO init-commands (`<sdoConfig>` written via CoE at bring-up, read
  back through the `ethercat` REST CLI). Also proved the CLI+REST path works resident.
- `sim-link-loss` — cable-pull scenario: a `<interface>.link` control file drops the link, the
  master reports the slave lost (pins fall), and rescans back to OP on recovery.
- `sim-multi-slave` — three CoE slaves (output-only / input-only / bidirectional) at positions
  0/1/2 all reach OP; round-trip on slave 2 verifies its shared-domain offset.
- (submodule) `test_sim_transport_file` — the registry/file-parse path.
The sim slaves use a CoE mailbox so the master configures PDOs the real way (no "does not support
CoE" fallback; clean log). DC (distributed clocks) deliberately skipped as niche. A real
`cmd/ethercat` parity bug was found + fixed en route (attached option form `-p0`, commit
`d7da8ef2bd`). Details + the M3 plan in the auto-memory `ethercat-sim-transport`. This makes the
Phase-2 EtherCAT rows (`cmd/ethercat` + the driver cmods) reviewable/testable now, and feeds the
cross-cutting "Simulation configs" / "Real-machine test plan" items below. **M3 done** — the
`cmd/ethercat` CLI was read-reviewed against the IgH source and its output-assertion test added
(`tests/ethercat/sim-cli`), fixing four parity bugs (`-p0`, clustered `-fq`, and the SM-direction
bug in `pdos`/`cstruct`/`xml`); Tier-1 hotspot #3 is substantially closed (see its entry).

### Phase 3 — supervision & startup (first thing a field tech touches)

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/launcher | 2599/237 | 1 | ✅ | ✅ | ◐ | ◐ | ✅ | ◐ | ☐ |
| internal/daemon | 157/0 | 1 | ✅ | ✅ | ✅ | ☐ | ✅ | — | ☐ |
| cmd/gomc-server | 266/0 | 2 | ✅ | ✅ | ✅ | ◐ | ✅ | — | ◐ |
| internal/config | 86/37 | 2 | ✅ | ✅ | ✅ | ◐ | ✅ | — | ◐ |
| internal/pkgreg | 353/0 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| pkg/inifile | 606/966 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |

**Phase 3 tail reviewed 2026-07-21 (Tier 2 + inifile vs the 2.9 C oracle). Full findings in
`PHASE3_REVIEW_FINDINGS.md`.** `pkg/inifile` is the runtime-critical one (every customer INI
must load identically). Two CONFIRMED parity divergences vs `libnml/inifile` fixed: **I-1**
backslash line-continuation was not implemented (158 shipped lines use it — e.g.
`[DISPLAY]APP = sim_pin \` lost every argument); **I-2** inline `;` was stripped as a
comment, silently truncating `MDI_COMMAND = G0 Z25;X0 Y0;Z0` to `G0 Z25` (36 shipped
occurrences; the C parser never strips `;`, and 0 configs use it as a comment). The narrow
whitespace-`#` strip is kept (reproduces 2.9's `strtod` numeric tolerance with least risk).
I-2 reverses a behavior a test encoded as intended — taken as a bug fix under "load
identically", **ruling to confirm** (see the findings doc). `internal/pkgreg`: **F1** a
typo'd `packages.conf` TYPE silently dropped a module (green build, module gone at runtime)
→ now a loud `file:line` build error; **F2** `_test.go`-only dirs no longer mis-discovered.
`cmd/gomc-server`/`internal/config` clean (2 LOW notes, no code change). Regression tests
added for every fix; `-race`/vet/gofmt/lint(0) green.

### Phase 4 — HAL tooling

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/halcmd + cmd/halcmd | 3540+1932/364 | 2 | ✅ | ✅ | ✅ | ◐ | ✅ | — | ◐ |
| internal/halparse | 1769/2330 | 2 | ✅ | ✅ | ◐ | ◐ | ✅ | — | ☐ |
| internal/halfile | 343/400 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| internal/haljson | 876/151 | 2 | ✅ | ✅ | ✅ | ◐ | ✅ | — | ◐ |
| internal/modcompile + cmd | 2909+1636/393 | 2 | ✅ | ✅ | ✅ | ◐ | ✅ | — | ◐ |
| internal/hallib | 30/0 | 3 | ✅ | ✅ | — | — | ✅ | — | ◐ |

**Phase 4 reviewed 2026-07-21 (Tier 2 adversarial + 2.9 oracles). Full findings
in `PHASE4_REVIEW_FINDINGS.md`.** No HIGH wire-reachable crash in the
REST-reachable command path — halcmd is defensively written (watch C-returns free
correctly, `net` caps pins at 64, show/save capped, `Save`-to-file not
REST-reachable). Fixes landed (each with regression tests; build cgo+nocgo, `-race`,
vet, gofmt, lint(0) green):
- **HJ-1 (cross-cutting lifecycle):** module unload never removed the apiserver
  **watch** registration (only the REST one) → after `Destroy` freed a module's
  pins, a later WS subscribe served stale/recycled HAL memory + the entry leaked.
  Added `WatchRegistry.UnregisterByInstance` + a shared `unregisterModuleAPIs()`
  called before `Destroy` in both unload paths (covers haljson **and** mqttbridge).
- **modcompile codegen (risk-class-3 multiplier):** MC-1 array-param defaults
  dropped; MC-2 `option data` leak on the New() err path; MC-3 string-modparam
  default not C-escaped; MC-5 function names not hyphenated (broke the shipped
  moveoff `addf mv.read-inputs`) — verified end-to-end regenerating comps; MC-7
  unknown-flag handling.
- **halparse HP-5** (template `seq/seq1/count` OOM clamp), **halcmd HC-1**
  (completion mid-line-TAB panic) **+ HC-3** (`list comp` fnmatch parity),
  **halfile HF-2/HF-5** (directory rejection + tilde expansion; nil-INI deref
  verified ABSENT), **haljson HJ-3/HJ-4** (array-size cap + rate clamp).
- **Tier-3 `internal/hallib`:** Go surface is a 12-line cgo link shim + test-only
  wrappers; the inherited 2.9 C core (`hal_lib.c`, `uspace_rtapi_lib.c`) is owned
  by `RT_HARDENING_CHECKLIST.md`. Cleared by inspection.

`F` left ◐ on **halparse** (HP-1/HP-2/HP-3/HP-4 open): CONFIRMED 2.9-tokenizer
parity divergences (substitution-before-comment/quote lexing → silent config
truncation; missing-INI-var silently ignored vs 2.9 fail-loud; extra backslash
escapes; continuation join-with-space) **deferred for a ruling** — they change
parse semantics across the shipped-config corpus (same character as inifile I-2)
and need full runtests + a keep/fix decision. Other flagged (not auto-fixable):
HC-2 (ambiguous arg-path heuristic), HJ-2 (drain contract — ties to ADS A5 /
pkg-hal H1), HF-1 (`LIB:` scope). `U` ◐ where happy-path coverage still wanted.

### Phase 5 — services & auxiliaries

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/apiserver | 2174/2446 | 2 | ✅ | ✅ | ◐ | ◐ | ✅ | ◐ | ☐ |
| internal/halrest | 659/0 | 2 | ✅ | ✅ | — | ☐ | ✅ | — | ☐ |
| internal/inirest | 87/171 | 2 | ✅ | ✅ | ✅ | ◐ | ✅ | — | ☐ |
| internal/mqttbridge | 791/0 | 2 | ✅ | ✅ | ✅ | ☐ | ✅ | — | ☐ |
| internal/halscope | 939/0 | 2 | ✅ | ✅ | ✅ | ◐ | ✅ | — | ☐ |
| cmd/halsampler, cmd/halstreamer | 146+174/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |

**Network modules (apiserver/halrest/inirest/mqttbridge/halscope) — reviewed 2026-07-21
(Tier 2, adversarial). Full findings in `NETWORK_MODULES_REVIEW_FINDINGS.md`.** Shared lens:
untrusted-wire alloc/panic → controller death + goroutine/lifecycle. Default REST/WS bind is
loopback (`127.0.0.1:5080`) but exposable via `GMC_REST_ADDR`. **Headline (N1): cross-site
WebSocket hijacking** — both upgraders skipped the Origin check (`InsecureSkipVerify:true`), and a
WS `call` dispatches real controller commands (jog/MDI/state), so a **browser tab on a malicious
page could drive the machine even on loopback**. Fixed: same-origin secure default via
`OriginPatterns`, opt-in `GMC_REST_ORIGINS` allow-list; regression test. Also fixed: **N2**
`recover()` in the spawned `pushLoop`/`pushLoopBinary` (a watch-fn cgo panic killed the process —
net/http recover does not cover spawned goroutines); **N3** `MaxBytesReader` (8 MiB) on the REST
body (OOM); **N4** `ReadHeaderTimeout`+`IdleTimeout` (Slowloris; not Read/Write which would kill
WS); **N5** pprof gated behind `GMC_REST_PPROF=1`; **N8** `recover()` in mqtt publish/message
goroutines; a stream `streamWg.Add` shutdown-race narrowed. **Cleared:** inirest `make` (bounded by
decoded array), halscope lifecycle (HS1 properly fixed), mqtt MQ1 (present), registry races +
webapp traversal. **Open:** **N6 = launcher L-3** (halrest load/unload confirms the unlocked
`cModules`/`goModules` race is *remotely reachable* — fix belongs to L-3); N7 (mqtt publish-count
semantics); N9 (connection cap — policy); and a **safety-boundary-doc** item: the REST/WS surface
has **no authentication** (model is "trusted local origin"; non-loopback needs a proxy/auth).
build/vet/gofmt clean, lint 0, `-race` green. halrest `F` marked — (no code change; its one
finding is L-3, owned by launcher).
| internal/persist_sqlite | 323/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/tooltable | 338/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/emccalib, internal/calibreg | 313+46/53 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |

### Phase 6 — UI-adjacent

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/ngcpreview | 1302/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/pyvcpmodule | 749/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |

### Deferred / frozen

| Module | Reason |
|---|---|
| internal/classicladder | **Mid-migration/reimplementation** — review after it settles; only lint + `-race` in CI until then |
| internal/tasktest | Test scaffolding (Tier 3) |
| cmod/* (motion, tp, homing, components) | Inherited 2.9 C code — algorithm risk low; the **binding boundary** is covered in Phase 1; **RT-correctness** (effects-check, non-blocking) tracked in `RT_HARDENING_CHECKLIST.md` (motmod/tpmod/homemod already verified clean) |
| panelui, tracking-test, linuxcnclcd, motion logger cmod host, classicladder UI, all UIs except axis, qtvcp/gladevcp | Not (fully) ported — tracked in `MISSING_FEATURES.md` |

---

## Tier 1 hotspots

Human review mandatory, in this order:

1. **pkg/hal** — the binding layer every realtime interaction crosses; 54 test lines.
   Focus: pin/signal lifecycle, type conversions, thread interaction, error propagation.
2. **gmicompile emission logic** (`internal/gmicompile/cgen`) — one wrong emission pattern
   replicates into 39 generated packages. Review generator + diff a sample of generated
   output against the IDL by hand. The parser/AST side is Tier 2.
   **DONE (2026-07-19/20) — see `GMICOMPILE_REVIEW_FINDINGS.md` + the Phase-1 note above.** Both
   catastrophic classes (cgo handle transit, returned-data ownership) verified closed
   generator-wide across all 33 packages; all live/production findings fixed (operator-message
   loss root-caused here; `--server-go` ptr truncation; mapper drift; fail-fast guards). Deferred
   as manual/design-decision (not auto-fixable): G-M4, G-L1, G-L5, G-L7. Tier-2 parser/AST still
   to review.
3. **cmd/ethercat** — **DONE (M1+M2+M3, 2026-07-21); hotspot substantially closed.** This is a
   *diagnostic CLI* (drop-in for the IgH `ethercat` tool, talks REST/GMI to the master at
   `GMC_REST_URL`) — it holds **no** state machine, watchdog, or slave-loss logic; every
   PREOP/SAFEOP/OP/watchdog reference is formatting of state read back from the master. Rather than
   only read-review the CLI, the sim transport was promoted from the master's test-only
   `transport_sim` into a **first-class, config-selectable transport** (`transportType=sim`,
   `interface=<bus-description-file>`) so the *whole lcec driver* is integration-tested with no
   hardware — see the Phase-2 harness note above. **M1+M2:** six ethercat runtests (bring-up, PDO
   round-trip, startup SDO, link-loss/rescan, multi-slave) exercise the driver end-to-end on
   emulated slaves. **M3 (CLI review, done):** the hand-written command formatting/parsing was
   read-reviewed against the authoritative IgH source (`master/tool/Command*.cpp`) and **four real
   bugs fixed** — option parsing `-p0` + clustered `-fq` (with `parseArgs()` extracted and the
   first `cmd/ethercat` unit test, `main_test.go`), and the SM-direction bug replicated across
   `pdos`/`cstruct`/`xml` (output SMs mislabelled). `master`/`slaves`/`sdos`/`config`/`domains` are
   parity-confirmed; `tests/ethercat/sim-cli` asserts the output format. **Deferred (lower
   priority):** deep-review of the rarely-used `reg/sii/foe/soe` read-write commands. **Two
   master-side follow-ups (not CLI bugs):** `version` shows the ioctl magic (0.0.37) because the
   master tool API (`ec_tool_module_t`) exposes no version string — needs the master to surface
   `PACKAGE_VERSION`; and the master reports `Phase: Idle` while active + doesn't fetch the SDO
   dictionary (both master/GMI-side, surfaced by the harness).
4. **internal/launcher + internal/daemon** — process supervision, startup/shutdown ordering,
   restart-after-crash. Focus: goroutine ownership, orphan handling, partial-startup failure.
5. **State machines & abort paths across modules** — wherever a Tier 2 AI review flags a
   state machine or abort/estop path, that section gets human eyes regardless of module tier.
   **Reviewed 2026-07-20 — see `STATE_MACHINE_REVIEW_FINDINGS.md`.** Covered the abort/estop
   surface not already done under milltask/#1/#4/#6: the C RT motion controller + CiA402
   homing (`src/emc/motion`), the C iocontrol rewrite (`src/emc/iotask`), and the Go-side
   connection/lifecycle machines (classicladder, halscope, ADS, mqtt, apiserver, ngcpreview).
   All CONFIRMED findings fixed (headline: a CRITICAL unrecoverable abort wedge in the v2
   iocontrol port, and a CiA402 estop hazard). PLAUSIBLE items left for adjudication.
6. **internal/realtime** — small, but sits on the RT boundary; verify no GC-managed
   allocation in cyclic paths.

---

## Cross-cutting work items

Not per-module; each needs an owner and a done-definition.

- [ ] **Safety boundary document** — list exactly which functions the external certified
  hardware covers (estop chain, limits, spindle stop, interlocks) and assert per module that
  the software is not load-bearing for any of them. <a name="safety-boundary"></a>
- [ ] **Security model / API authentication** (ruling 2026-07-21, see
  `NETWORK_MODULES_REVIEW_FINDINGS.md` + `gomc-rest-auth-and-loadunload-rulings` memory). The
  REST/WS control surface has **no authentication** today; this is a **deferred-but-required**
  deliverable, not "won't fix". Decisions taken: (a) **robustness is intrinsic** — harden the
  crash/DoS surface regardless of binding (endpoints will be exposed eventually); (b) bind
  **loopback only** until auth lands; ADS is machine-internal for now; (c) the auth *mechanism*
  (authN/TLS/coarse allow-deny) is an **external** reverse-proxy / API-gateway — **not** built
  into gomc; (d) auth needs **fine-grained permission control**, and per-command **authZ** needs a
  thin app-side seam in gomc at `handleAPIRequest`/`handleCall` (a gateway can't do it blind to
  command semantics); (e) the same-origin WS fix (N1) is complementary and stays regardless.
  Runtime REST module load/unload **is** a supported production path → **launcher L-3 gets the
  full locking fix** (now the highest-priority open Tier-1/L item).
- [ ] **Concurrency policy** — per-module goroutine ownership writeup; `-race` gate in CI.
- [ ] **Panic/robustness policy** — recover-and-log vs. die-and-restart, decided once,
  applied everywhere; watchdog/supervision behavior documented.
- [ ] **Observability** ("short access path"): consistent structured logging levels across
  modules; crash reports persisted on machine; one-command diagnostic bundle
  (logs + halscope capture + config + version).
- [ ] **Config compatibility corpus** — parse every shipped config in `configs/` through the
  new stack as a test; existing customer 2.9 INI/HAL files must load identically.
- [ ] **Deployment/rollback** — version stamp visible in UI/logs, defined update procedure,
  rollback path for field prototypes.
- [ ] **RT/latency validation** — latency-histogram soak test on target hardware; verify Go GC
  cannot stall any cyclic path. **Instrument ported** (2026-07-19: gomc-native latency-test on
  branch `latency-test` — C cmod + pthread drainer + `latency.gmi` + Vue/uPlot app; OK on first
  run). Remaining is the measurement itself: the **24–72 h soak under realistic + adversarial
  load on target hardware** with a published histogram — see `RT_HARDENING_CHECKLIST.md` §3.
  The deeper RT-correctness gaps that soak would exercise (deadline-miss → E-stop, effects-check
  scope, priority headroom) are tracked in `RT_HARDENING_CHECKLIST.md` §0 "Biggest open gaps",
  not here.
- [ ] **Fuzzing** — `go fuzz` targets for halparse, inifile, gmicompile parser.
- [ ] **Surface RCS command errors to clients** — command endpoints return the RCS code
  in-body with HTTP 200 (the cgo bridge flattens the Go error to `-1`), and the gmi python
  `Command` methods discard even that: a rejected MDI/state command is invisible to the
  caller. This matches classic `linuxcnc.command()` semantics (errors via the error channel
  + `wait_complete()==RCS_ERROR`), which is why it wasn't changed during the flaky-test fix
  (2026-07-14) — but for gomc-native clients/GUIs an explicit rc return (or opt-in raise)
  would remove a whole class of silently-doing-nothing bugs. Decide the API contract once,
  apply to gmi python + any future client bindings.
  **Partly settled (test-sync pass, 2026-07-17):** the *test* half is decided —
  `lib/python/gomc_test.py` provides a `Command` whose `wait_complete()` raises on the -1
  rather than returning it, and the suite constructs through it. `gmi.Command` itself was
  left drop-in-compatible on purpose: `bin/axis`, `linuxcnctop` and `manualtoolchange_ui`
  import gmi directly, so changing the contract underneath them is a product decision, not a
  test fix. What remains open is exactly that: whether gomc-native clients should get a
  raising/rc-returning variant. Also fixed in that pass: `_post` hard-coded a 10s socket
  timeout while `/wait-complete` blocks server-side for its full `timeout`, so any
  `wait_complete(t>10)` raised a socket error instead of ever returning -1 — the -1 contract
  was unreachable for long waits.
- **FIXED (2026-07-17): M-code completions were credited to the wrong job — the
  `Submit`/`CheckDone` handshake had no job identity.** The suspect recorded here on
  2026-07-16 was right, and the interleaving is now proven by a unit test rather than
  inferred. `mcodeHandler` signalled completion through a single bare `done bool`: it
  recorded that *a* job had finished, never *which* one. `McodeCmd.Execute` has one exit
  that does not consume `done` — `pollUntil` returning on abort while the handler is still
  running. From then on the worker's stale `done=true` is collected by the *next* M-code's
  waiter, and the sequencer runs permanently one job ahead of the worker: it reports
  M-codes complete that never ran, and `Submit` starts rejecting jobs outright with
  "worker busy" (`jobCh` is buffered 1) once the skew makes it collide with the
  still-queued predecessor. The "exactly one caller of each" argument in the old note is
  what made this look impossible — it is true, and irrelevant: the two callers that
  cross-talk are *successive* calls by the same caller, separated by an abort.
  **The race detector cannot see this** — every access to `done` correctly takes
  `resultMu`. It is a lost/misattributed update, not a data race, so the 2026-07-16 lead
  ("run the queue-buster under `-race`; a write-write on `done` would surface directly")
  was a dead end.
  Fixed by giving each submission its own buffered `resultCh` (`mcodeSub`): a completion
  can only ever be delivered to the job that produced it, and an abandoned job's result
  is garbage collected instead of being handed to the next caller. `Submit` now blocks
  until the worker accepts (unblocking on `seqAbort`) rather than failing a job whose only
  crime is that a previously abandoned handler is still draining. `done`/`result`/
  `resultMu`/`CheckDone` are gone — the class of bug is removed, not patched.
  Evidence, `TestMcodeHandlerNoResultCrosstalk` (2000 M-codes, aborts racing completion):
  before **1088/2000 jobs rejected "worker busy"** and only 910 handlers ran for 709
  reported successes; after **0 rejected, 1999 ran**, every success backed by a real
  invocation. Live: `tests/mdi-queue/simple-queue-buster` 45 runs (the same loop that
  caught the wedge on run 10 on 2026-07-16) — no wedge.
  Also fixed in the same seam: **`Abort()` could panic the process** ("close of closed
  channel"). Its check-and-close ran *outside* `h.mu`, so two of the four abort paths
  (three `mcodeAbort` call sites + `Stop`) racing would both observe an open channel and
  both close it; the same unlocked read could also close a channel `Submit` had already
  swapped out, aborting nothing. Now checked and closed under `h.mu`.
  `MILLTASK_GOROUTINE_PROBLEM.md` §5 identifies user M-codes as the ONE job that
  genuinely must block, so this handshake is the load-bearing seam of the current
  pipeline design — worth re-reading before changing it again.
  **The queue-buster's remaining flake is a DIFFERENT bug — see the persist GC crash
  below.** It is not the wedge: the `interp_state != 1 && queued_mdi_commands == 0`
  detector stays silent through it, because the sequencer is not stalled — the server is
  dead.
- [x] **gomc-server dies with a Go runtime GC fault in the generated `persist` GMI client —
  PRODUCTION-RELEVANT (uncontrolled controller death), found 2026-07-17, ROOT-CAUSED AND
  FIXED 2026-07-17 (`gmi: never let a cgo.Handle transit a Go pointer-typed slot`).**
  Not a hang and not an M-code bug: the process is *killed by the Go runtime*, after which
  every client poll gets `Connection refused` and the test driver blames its own 30s drain
  deadline — which is what made this look like the M-code wedge. Verbatim:

      runtime: bad pointer in frame
        github.com/sittner/linuxcnc/src/gomc/generated/gmi/persist.(*PersistClient).GetEntry
        at 0xc0004e10a0: 0x8
      fatal error: invalid pointer found on stack
      runtime.adjustpointers -> adjustframe -> copystack -> shrinkstack -> scanstack

  **Root cause (proven by the full traceback, preserved in the 2026-07-15 capture):** the
  crashing goroutine's innermost frame was `runtime.cgoCheckPointer({0xcead60, 0x8}, ...)`
  called from `GetEntry`'s cgo argument-check closure — the "bad pointer" `0x8` is the
  **cgo.Handle of the persist provider**, not corruption. The generated provider bridges
  store `cgo.NewHandle(impl)` — a small integer — in the C callbacks struct's `void *ctx`,
  and the generated client then passed `cl.cb.ctx` from Go as an `unsafe.Pointer` argument,
  putting a non-address value in a GC-scanned pointer-typed stack slot. Any stack move
  (`morestack → shrinkstack → copystack`) that scans such a slot while live trips the
  runtime's invalid-pointer check (nonzero value < `minLegalPointer`) and aborts the
  process. Intermittency (~1 in 25 runs of `tests/mdi-queue/simple-queue-buster`, 2 in
  ~47): the slot must be live at the exact moment a GC stack move scans it. Tool-change
  bias: that path is the deepest nested Go→C→Go stack (`interp_synch → canon_bridge_
  get_external_tool_table → tooltable_bridge_get_tool → persist GetEntry`), maximizing
  morestack probability with several handle-bearing frames live — the same traceback shows
  `canon_bridge_get_external_tool_table(0x8, ...)` receiving a handle as its
  `ctx unsafe.Pointer` trampoline parameter, the same bug in the receive direction.

  **The earlier sret hypothesis is REFUTED** — verified against the actual cgo-generated
  code before acting: cgo's shims are already stack-move-safe. The by-value struct return
  lands in `_cgo_r`, a local on the g0 (system) stack, the write-back pointer is adjusted
  by the `_cgo_topofstack()` delta after the call, and the wrapper zeroes the result slot
  before `runtime.cgocall` (verified in the disassembly). Return-struct size is irrelevant.

  **Fix (generator-level, all GMI packages):** `call_*` client wrappers now take the
  callbacks-struct pointer and dereference the function pointer and ctx inside C (Go never
  touches ctx); `//export` trampolines take ctx as `C.uintptr_t` (cgo.Handle's intended
  transit type per its docs) with matching `uintptr_t` extern decls; `Free*Callbacks`
  reads ctx back via a C accessor. Hand-written bridges with the same receive-direction
  bug fixed identically: `internal/task/ini_accessor.go`, `internal/task/mcode_provider.go`,
  launcher log/ini env callbacks (`gomc_env.go`/`cmodules.go`). The launcher api and
  kinstest callbacks keep `void *ctx` — theirs is NULL, which is a legal pointer value.
  Verified: 50 consecutive runs of `simple-queue-buster` under `GOGC=10` (≈10× the GC
  cycles, so far more shrink-scan opportunities than the stock 1-in-25 detection rate) —
  0 failures.

  Also fixed alongside (2026-07-17): the returned-string leak. Providers hand out data
  under "caller owns returned data" (the Go bridges `C.CString`/calloc it), but the
  generated clients freed only returned slice *arrays*, never strings — every
  `GetEntry`/`GetTool` leaked its strings. The generated clients now free all C
  allocations (strings, slice buffers, nested) after converting to Go, and the generated
  headers document the ownership rule (returned string/slice data must be malloc'd, never
  static). Safe by audit: the only string-bearing client returns are persist and
  tooltable, both Go-provided. The REST dispatch path is closed too (2026-07-17,
  second commit): dispatch funcs now free returned strings and slice-element
  allocations — this covered the polled watch functions (halcmd `watch_items`,
  halscope `watch_state`, pyvcp `watch_pins`), so that leak was hot, not admin-only.
  The provider audit (gmicompile-parser sweep over every IDL) found exactly one C
  provider with string-bearing returns: the ethercat cmod, which deliberately
  returned borrowed pointers into thread-local buffers under a
  "dispatch-copies-before-next-call" contract. That contract is gone; it now
  strdups all returned strings (`ret_str`) and `format_mac` mallocs. Verified live:
  repeated REST hits on halcmd /pins (739 pins × 7 strings), /signals, /threads,
  tooltable list/get, persist namespaces — all healthy.
  Repro/instrumentation notes kept for posterity: the evidence lives in the test's
  `stderr` file, NOT the runtests output, and a later green run overwrites it — copy it
  out on first capture. `grep -cE '^\*\*\* '` is NOT a failure count (it matches XFAIL
  prefixes); read the `N failed + M expected` line.
- [ ] **Startup-code motion at estop faults exec_state** — `RS274NGC_STARTUP_CODE` executes
  at task init exactly like 2.9, but gomc's canon dispatches straight to motion, so a startup
  file containing motion (e.g. `tests/motion-logger/startup-gcode-abort`'s `o<init> call`)
  gets its move rejected at estop and the machine boots showing EXEC_ERROR, where 2.9 parks
  the move in the interp_list. Documented + enshrined in that test's gold and
  `tests/motion-logger/parity-vs-2.9/PARITY_FINDINGS.md` ("Not cleanly certifiable").
  Follow-up options: defer startup canon output until the machine is on, or clear the exec
  error after startup-code processing. Only affects configs whose startup code moves — modal
  startup codes (G20/G64/...) are unaffected.

## Test environment

- **Simulation configs** — gomc sim config set sufficient to run the runtests subset and
  fault-injection tests (abort mid-motion, estop during tool change, component crash/restart,
  GMI peer loss).
- **Automated integration tests** — runtests in CI (see Immediate next steps) + gomc-specific
  integration tests for what runtests cannot express (restart, supervision, GMI-level behavior).
- **Real-machine test plan** — only what simulation cannot cover: latency/jitter on target
  hardware, EtherCAT with real slaves, homing on physical switches, spindle/VFD behavior,
  diagnostic-bundle pull. Written checklist, executed per prototype before delivery.

## Status log

| Date | Event |
|---|---|
| 2026-07-09 | milltask review closed, merged (PR #248) |
| 2026-07-11 | This document created |
| 2026-07-15 | Tool-change/lifecycle porting sweep complete (`MILLTASK_LIFECYCLE_SWEEP.md`): 13 gaps fixed across milltask/canon/interp/iocontrol/tooltable, 17 tests un-xfailed (G43 Hn, tool tracking, M61, RANDOM_TOOLCHANGER, TOOL_CHANGE_POSITION, abort modal-state restore via restore_from_tag, g5x desync, tool_from_pocket in stat) |
| 2026-07-19 | Runtests migration complete — 232/232 successful, 0 xfail, 0 skipped (all categories incl. Category D full-instance ported; `runtests.log`). gomc-native latency-test ported (branch `latency-test`), OK on first run — RT/latency soak instrument now in hand (`RT_HARDENING_CHECKLIST.md` §3) |
| 2026-07-20 | `internal/realtime` reviewed (Phase 1, Tier 1). Confirmed off the cyclic RT path (startup-only stub, no goroutines/shm). Removed two vestigial checks (`/dev/zero` sanity, dead `RTAPI_DEBUG` branch); `Start()` now an honest minimal validator. vet/test/build green. Row → L R F U RC ✅, FP —, S ◐ (awaiting final human sign) |
| 2026-07-20 | `pkg/hal` reviewed (Phase 1, Tier 1 hotspot #1). Fixed `Pin.String()` recursive-RLock deadlock (H-1); removed dead `Running()`/`Stop()`/`done`/`running` scaffolding + rewrote false signal-handling doc (H-2, user-ruled); surfaced silent HAL_PORT string-write drops via new `Pin.TrySet()`, wired `adsbridge` to it, documented the sized-port contract (H-3); documented HAL re-init-after-teardown limit (H-4) + the Pin-mutex-vs-RT-writer design note (H-5). Coverage 54→191 test lines. build/vet/test/-race green. Row → L R F RC ✅, U FP ◐, S ◐ |
| 2026-07-20 | **Tier 1 hotspot #5 — state machines & abort/estop paths reviewed** (`STATE_MACHINE_REVIEW_FINDINGS.md`). Root-caused + fixed a **CRITICAL** unrecoverable abort wedge in the v2 iocontrol cmod port: the 2.9 free-running iocontrol loop (tool-change/abort-ack/fault serviced as non-blocking per-cycle state) had been ported into a blocking cgo busy-wait on the sequencer goroutine, so an abort/estop during a hung M6 froze the sequencer in cgo → `restartSequencer`/estop-reset joined `<-seqDone` forever (process kill to recover). Faithful port restores 2.9 semantics on **both** `ioControl.c`/`ioControl_v2.c`: `gmi_get_status`→`poll_inputs()` reaps the emc-abort/ack + fault latch on the monitor poll, `gmi_io_abort` non-blocking, tool loops bail on cleared request line, `pthread_mutex io_mtx` serialises shared state (released across usleep; estop read lock-free). Also fixed: **C1** CiA402 estop-homing hazard (drive left commanded in HOMING opmode while disabled — `do_cancel` now forces opmode=CSP synchronously, matching classic homing's free_tp kill); **CL1** classicladder free-without-join UAF (WaitGroup-join master+slave before `rt_free`, close conns to unblock parked reads); **HS1** halscope detached-saver storm/UAF (one coalescing saver joined before `halscope_free`); **CL3** atomic `rt.state`; **MQ1** mqtt subscribe-error + racy `pubCount`; **ADS1** notify client-addr race; **API1** watch-push error logging; **DOC1** stale iotask headers. cmods + `go build ./...` + vet + gofmt green. OPEN (PLAUSIBLE, adjudication): CL2 (C refresh publication protocol), NGC1, ADS2/ADS3. Test debt: abort-racing-hung-toolchange regression (runtests-level). |
| 2026-07-20 | **Tier 1 hotspot #2 — `internal/gmicompile` (cgen emission logic) review reconciled + matrix updated** (`GMICOMPILE_REVIEW_FINDINGS.md`). Four independent AI passes ground-truthed against all 33 generated packages; both catastrophic classes (cgo handle transit, returned-data ownership) verified closed generator-wide. Live findings fixed across `57c162d2ca` (G-H1/M1 operator-message loss root-caused here → retained bounded ring + per-connection `WatchFactory`), `04b1d14df9` (G-H2 `--server-go` ptr truncation), `6d08f75307`/`9f1ace9fa5` (G-M2/M3/L2/L3 mapper unify + dead-file removal), `bb14a10d1e` (G-L4/L6 fail-fast guards). Row → L RC ✅; R/F/U/S ◐ (Tier-2 parser/AST still unreviewed; G-M4/L1/L5/L7 deferred as manual/design, not auto-fixable); FP —. |
| 2026-07-20 | `internal/launcher` + `internal/daemon` reviewed (Phase 1, Tier 1 hotspot #4). Fixed `shutdown()` double-close panic race — 3 goroutines, non-atomic check-then-close → `close of closed channel` crashing ordered shutdown; now `shutdownOnce sync.Once` + 64-goroutine `-race` regression test (L-1). Fixed `stopCModules` calling stop on never-started modules after partial-startup failure (guarded on `cm.started`, matching unload.go/startCModules) (L-2). OPEN for manual review: L-3 module-state data race on the runtime REST load/unload surface (`cModules`/`goModules`/`cModArena` unlocked vs shutdown iteration; needs a locking design that avoids the `gomc_ini_get` `//export` re-entrancy deadlock). LOW documented: L-4 goModule Stop-without-started, L-5/L-6 apiServer field + orphan signal goroutine + retainSync 1s wait. vet/build/`-race` green. Row → L R F ◐, U FP S pending |
| 2026-07-20 | **Tier 1 hotspot #3 — cmd/ethercat unblocked; EtherCAT sim-transport integration harness M1 done.** Promoted the master's test-only `transport_sim` slave emulator to a first-class, config-selectable transport (`EC_TRANSPORT_SIM`; `transportType=sim`, `interface=<bus-description-file>`; RT-`TRUSTED` cyclic ops, in `rt-effects-check`). Submodule `670737d4` (branch `transport-sim`): moved `transport_sim.{c,h}` to `transport/`, file-driven `sim_open` parser, registry entry, `interface[16]→[64]`, new `test_sim_transport_file` (14 PASS/2 SKIP/0 FAIL). Superproject `b6c9c9a6a1` (conf.c `transportType=sim` + submodule bump) and `bb1ac3a0be` (`tests/ethercat/sim-basic` — first gomc EtherCAT runtests case: driver on an emulated slave reaches OP, PDOs map to HAL pins; passes `runtests`). Rebuilt `libethercat`+`cmod/ethercat.so`. Next: M2 (PDO round-trip/SDO/DC/link-loss), M3 (`bin/ethercat` REST-CLI assertions → closes the CLI review). |
| 2026-07-21 | **EtherCAT harness M2 complete — driver integration-tested hardware-free.** Five more increments across six runtests: `sim-pdo-loopback` (PDO value round-trip both ways via a `loopback` sim slave, submodule `6a7591e1`), `sim-sdo-config` (startup `<sdoConfig>` init-commands written via CoE, read back through the `ethercat` REST CLI — proving the CLI+REST path works resident), `sim-link-loss` (cable-pull: a `<interface>.link` control file drops the link → slave-lost pins → rescan back to OP, submodule `ecde9e8b`), `sim-multi-slave` (three CoE slaves output-only/input-only/bidirectional all reach OP; slave-2 round-trip verifies its domain offset), and a CoE-mailbox upgrade to all sim slaves (clean PDO config, 0 log errors). DC skipped (niche). **cmd/ethercat parity bug found + fixed** by the harness: the hand-rolled option parser rejected the attached getopt form `-p0` (IgH tool accepts it) — fixed `d7da8ef2bd`, guarded by the SDO test. Commits: submodule `6a7591e1`/`ecde9e8b`; superproject `2028c099ff`/`520abc1090`/`682e384ca7`/`d7da8ef2bd`/`90380552ee`/`2f2dd5d941`. Next: M3 — cmd/ethercat CLI read-review + output-assertion tests (closes Tier-1 hotspot #3). |
| 2026-07-21 | **EtherCAT M3 done — cmd/ethercat CLI reviewed; Tier-1 hotspot #3 substantially closed.** Read-reviewed the hand-written command formatting/parsing against the authoritative IgH source (`master/tool/Command*.cpp`); **four real parity bugs found + fixed:** option parser rejected the attached `-p0` (`d7da8ef2bd`) and clustered `-fq` (`bd40e617b9`, which also extracted `parseArgs()` + added `main_test.go`, the first unit test for the package); and the SM-direction bug — output sync managers mislabelled — replicated across `pdos` (`237a156b3b`), `cstruct` and `xml` (`4592b682e8`; real rule is control bit 0x04 = output). `master`/`slaves`/`sdos`/`config`/`domains` parity-confirmed (e.g. the `slaves` `0x<vid>:0x<pid>` fallback and `sdos` cached-dictionary read both match IgH). Test: `tests/ethercat/sim-cli` (`9c7d4e11de`) asserts CLI output format. Deferred: deep-review of rarely-used `reg/sii/foe/soe` commands. Master-side follow-ups (not CLI bugs): `version` shows ioctl magic (master tool API exposes no version string), `Phase: Idle` while active, no SDO-dictionary fetch. cmd/ethercat matrix row → L R F RC ✅, U FP S ◐. |
| 2026-07-21 | **Launcher L-3 FIXED (Tier-1 hotspot #4 follow-up).** Runtime REST module load/unload is a supported production path (user ruling) and halrest confirmed the unlocked `cModules`/`goModules`/`cModArena` race is remotely reachable (N6). Full locking landed: `arenaMu` guards `cModArena` (held only around `arenaAppend`/free, never across a cgo call — so the re-entrant `gomc_ini_get*` `//export` appends can't self-deadlock); `modMu` serializes `loadModuleNamed`/`UnloadModule` end-to-end (held across the cgo `cmod_call_*` — safe, no `//export` takes it) and guards the slices with snapshot-under-lock in the shutdown iterators (destroy nils under the lock); a `shuttingDown` gate (set under `modMu` in `doCleanup` after `stopAPIServer`) fails straggler load/unload fast with `ESHUTDOWN`. Lock nesting only `modMu ⊃ arenaMu`. Mutation-verified `-race` test `TestLoadRace` + `TestShutdownGate` (HAL-free Go-module path; real cmod re-entrancy covered by nightly `-race` runtests). build/vet/gofmt clean, lint 0, `-race` green. Launcher row → L R RC ✅, F ◐ (L-4/L-5/L-6 low open). |
| 2026-07-21 | **Network modules reviewed (Phases 4–6, Tier 2 adversarial)** — `NETWORK_MODULES_REVIEW_FINDINGS.md`. apiserver/halrest/inirest/mqttbridge/halscope, same untrusted-wire lens as ADS. **N1 (HIGH): cross-site WebSocket hijacking** — both WS upgraders set `InsecureSkipVerify:true`, a `call` action dispatches real controller commands, so a browser tab on a malicious page could drive the machine **even on the loopback default**. Fixed: same-origin secure default (`OriginPatterns`), opt-in `GMC_REST_ORIGINS`/`[GMC]REST_ORIGINS` allow-list, `TestWatchOriginCheck`. Also fixed: **N2** `recover()` in spawned `pushLoop`/`pushLoopBinary` (watch-fn cgo panic killed the process; net/http recover doesn't cover spawned goroutines); **N3** `MaxBytesReader` 8 MiB on REST body (OOM); **N4** `ReadHeaderTimeout`+`IdleTimeout` (Slowloris; not Read/Write — would kill WS); **N5** pprof gated behind `GMC_REST_PPROF=1`; **N8** `recover()` in mqtt publish/message goroutines; streamWg `Add` moved inside the lock (shutdown-vs-new-stream cgo-in-flight window). Cleared: inirest `make` (bounded), halscope HS1 (properly fixed), mqtt MQ1 (present), registry/webapp. Open: **N6 = launcher L-3** (halrest load/unload proves the unlocked module-map race is remotely reachable), N7 (mqtt publish-count), N9 (conn cap), + safety-boundary-doc: REST/WS has no auth (trusted-local-origin model). build/vet/gofmt clean, lint 0, `-race` green. Rows → apiserver/halrest/inirest/mqttbridge/halscope L R RC ✅. |
| 2026-07-21 | **Phase 3 tail reviewed — COMPLETE** (`PHASE3_REVIEW_FINDINGS.md`). `pkg/inifile` reviewed against the 2.9 C oracle (`libnml/inifile`); two CONFIRMED parity divergences fixed: **I-1** backslash line-continuation not implemented (158 shipped lines, incl. `[DISPLAY]APP = sim_pin \` losing all args) — now joins up to 20 lines like the C parser; **I-2** inline `;` stripped as a comment truncated `MDI_COMMAND = G0 Z25;X0 Y0;Z0` → `G0 Z25` (36 shipped uses, 0 configs use `;` as a comment, C parser never strips it) — `;` is now data; narrow whitespace-`#` strip kept for `strtod`-style numeric tolerance. I-2 reverses an intended-in-test behavior → **ruling to confirm** (bug fix, zero shipped regression). `internal/pkgreg`: **F1** typo'd TYPE silently dropped a module (green build, gone at runtime) → loud `file:line` error; **F2** `_test.go`-only dir mis-discovery fixed. `cmd/gomc-server`/`internal/config` clean (LOW notes only). Regression tests added for each fix; `go test`/`-race`/vet/gofmt/lint(0) green; inifile consumers (task, haljson, halfile, inirest) green. Rows → inifile/pkgreg L R F U RC ✅; gomc-server/config L R F RC ✅. **All Phase-3 modules now reviewed** (launcher/daemon under hotspot #4). |
| 2026-07-21 | **Phase 4 (HAL tooling) reviewed — Tier 2 adversarial + 2.9 oracles** (`PHASE4_REVIEW_FINDINGS.md`). halcmd+cmd/halcmd, halparse, halfile, haljson, modcompile+cmd, hallib. **No HIGH wire-reachable crash** in the REST-reachable command path (halcmd is defensively written). Fixes landed: **HJ-1 (cross-cutting)** — module unload never removed the apiserver *watch* registration (only the REST one), so `Destroy` freed a module's pins while the WatchAPI stayed live → a later WS subscribe served stale/recycled HAL memory + leaked the entry; added `WatchRegistry.UnregisterByInstance` + shared `unregisterModuleAPIs()` before `Destroy` in both unload paths (covers haljson **and** mqttbridge). **modcompile codegen (risk-class-3):** MC-1 array-param defaults dropped, MC-2 `option data` err-path leak, MC-3 string-modparam not C-escaped, MC-5 function names not hyphenated (broke shipped `moveoff addf mv.read-inputs` — verified end-to-end regenerating comps), MC-7 unknown-flag handling. **halparse HP-5** (template seq/count OOM clamp), **halcmd HC-1** (completion mid-line-TAB panic) + **HC-3** (`list comp` fnmatch parity), **halfile HF-2/HF-5** (dir rejection + tilde; nil-INI deref ABSENT), **haljson HJ-3/HJ-4** (array cap + rate clamp). hallib cleared (12-line cgo shim; C core owned by RT_HARDENING). **Deferred for a ruling:** halparse **HP-1/HP-2/HP-3/HP-4** — CONFIRMED 2.9-tokenizer parity divergences (substitution-before-comment/quote lexing → silent config truncation; missing-INI-var silently ignored vs 2.9 fail-loud; extra backslash escapes; continuation join-with-space) that change parse semantics across the shipped-config corpus (same character as inifile I-2) → need runtests + keep/fix decision; also HC-2 (arg-path heuristic), HJ-2 (drain contract → ADS A5/pkg-hal H1), HF-1 (`LIB:` scope). Regression tests per fix; build cgo+nocgo, `-race`, vet, gofmt, lint(0) green. Rows → halcmd/halfile/haljson/modcompile L R F RC ✅; halparse L R RC ✅ F ◐. |
| 2026-07-21 | **ADS cluster reviewed (Phase 2, Tier 2 adversarial)** — `ADS_REVIEW_FINDINGS.md`. Net-new code, no 2.9 oracle; server binds `0.0.0.0:48898` with no protocol auth. Two independent refutation passes (remote-DoS, concurrency/lifecycle). **Headline: a remote unauthenticated client could crash/OOM the motion controller with a single ~28-byte packet** — all fixed: A1 SumWrite `uint32` overflow → slice panic; A2 unbounded `make` from client sub-request count (≈137 GB → OOM); A3 unbounded process-image read `length` (≈4 GB, incl. notification `sendLoop` re-OOM every 10 ms); A4 no `recover()` in any goroutine. Bounds + `recover()` added; regression tests `internal/ads/dos_test.go`. Robustness: A6 write deadlines, A10 accept backoff, A11 idempotent `Stop()`, A12 construction-error HAL-component leak. A5 (partial): closed accept/register race + stage-2 read honors `quit`, narrowing the known ADS2 shutdown-UAF (full free-barrier contract still open, decide with pkg/hal H1). Refuted (locking correct): notifyManager races, SymbolTable lock model incl. suspected re-entrant-RLock deadlock. Open: A5 contract, A7 (conn/sub caps — HMI-count decision), A8 (`[0..N]` array silently mis-laid-out — fix proposed, separate commit), A9 (0.0.0.0/no-auth → safety-boundary doc), A13/A14 (low). build/vet/gofmt clean, lint 0, `-race` green. Rows → ads/adsbridge/adsconfig/adsmodule L R RC ✅, F ◐. |
