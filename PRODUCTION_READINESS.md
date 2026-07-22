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
| internal/task (milltask) | 12445/4839 | 1 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ◐ |

Milltask review closed and merged; **fault-path parity done** (PR #259 + certification
#256/#260 — written-spec tests vs the 2.9 source; see [Immediate next steps](#immediate-next-steps)
§3). **Lint-clean confirmed 2026-07-21** (`L` ✅): `golangci-lint v2.12.2 run ./internal/task/...`
under the full `gomc-lint-full` linter set = 0 issues. Remaining before full sign-off (`S`): the
final human sign — the functional/parity work itself is complete.

### Phase 1 — foundation (bugs here multiply into everything else)

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| pkg/hal | 1088/444 | 1 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ◐ |
| internal/gmicompile | 10755/2141 | 1 (emission logic) / 2 (rest) | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| generated/gmi/* boundary | n/a | 3 (spot-check vs IDL) | — | ✅ | — | — | ✅ | — | ◐ |
| internal/realtime | 47/28 | 1 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| internal/gmi (kinstest) | 376/262 | 2 | ✅ | ✅ | — | ✅ | ✅ | — | ◐ |
| pkg/gomc, pkg/cmodule | 94/150 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |

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
Coverage raised from 54→444 test lines. First round (191): round-trip for all scalar types, the
`String()`-vs-`Set()` concurrency regression, `TrySet` failure. **`U`/`FP` closed 2026-07-21 (→ ✅):**
added `Component` lifecycle (Ready/Exit — including the non-idempotent second-`Ready` error and the
exit-then-reuse-name path), `LookupValue` across the pin/signal/not-found arms, and the **linked**
round-trip for both scalars and ports — netting an out-pin + in-pin to a shared signal so the
value written on the writer is read on the reader. That exercises the reason `Get`/`Set` dereference
the HAL pointer at access time (`hal_link` repoints the pin's data slot at the signal cell), and the
full framed port write/peek over a real backing buffer (vs the unlinked drop path). Netting needs
`hal_signal_new`/`hal_link`/`hal_port_alloc`, which pkg/hal intentionally does not expose and which
cgo forbids in `_test.go`; the primitives live in a new test-support package
`internal/hallib/halnettest` (mirrors the `hallibtest` convention). Verified: build ./... green, vet
clean, `go test`/`-race` green, gofmt + lint(0) clean. Awaiting final human sign (`S`).

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
**Parser/AST side — reviewed 2026-07-21 (Tier 2; the front-end `parser/scanner.go`+`parser.go`,
`ast/ast.go`, `check/check.go`, ~1383 lines).** Method: two independent AI reads (lexer+parser;
ast+check), each finding adjudicated against the source. The unifying theme: this is **build-time
tooling on trusted in-repo IDL**, so "produces uncompilable generated code" fails loud at `cc`
(annoying, not dangerous) — the real hazard is **silent-wrong that still compiles**. `check.go`
validates *only* inline `@constraints` (its constraint logic is correct — range-fit, `minlen≤arraylen`
boundary, `min>max` ordering, regex-compile, type-applicability all check out); it is not a structural
validator, so type-graph/name-uniqueness checks live in the parser or nowhere. **Four fail-loud fixes
landed** (`internal/gmicompile/parser/parser.go`), each closing a discarded-error / missing-default
site that silently built a wrong AST, and each matching the parser's own `parseConst`/
`parseConstraints` precedent: (1) enum value `strconv.Atoi` error was discarded → a non-integer member
silently became `0`; (2) fixed-array integer size discarded its parse error and had no range check →
`[0]`/`[-1]`/overflow silently became length 0; (3) the func-level annotation `switch` had **no
`default`** → a typo'd `@methdo`/`@rt_saef` silently dropped the HTTP method / RT flag; (4) duplicate
`const` silently overwrote the resolution map → array sizes / `@min`/`@max` resolved to whichever came
last. Four regression tests added; **all 33 shipped IDLs still parse clean and the full `make` GMI
regen is byte-identical** (the fixes only add error paths for malformed input).

**Deferred parser/AST items — worked through with the user 2026-07-21 and CLOSED** (each fix
gated the same way: 33 IDLs still clean + regen byte-identical + tests):
- **F1 (unknown named-type reference)** — `check.go` now has a structural type-existence pass:
  every `TypeNamed` in a field/param/return (recursing into array/slice elements) must resolve to
  a declared type, enum, callback, or import, else a `file:line` "unknown type" error (also catches
  a misspelled primitive like `i32x`). Was: dangling ref → uncompilable generated code (loud only
  at `cc`). 6 tests. (`5dc76a891d`)
- **F2/F3 (duplicate names)** — strict shared type namespace (type/enum/callback/import mutually
  unique) + per-scope uniqueness for funcs, stream servers, struct fields, callable params, and enum
  member names; duplicate enum *values* stay legal (aliases). 8 tests. (`78d1e05676`)
- **H4 (forward-referenced callback/import)** — a post-parse `reclassifyForwardRefs` pass re-resolves
  any `TypeNamed` naming a callback/import declared later in the file to the correct `TypeKind`, so
  the emitter (which switches on kind: callback→fn-ptr, named→struct) is order-independent. Was
  silently-wrong + order-dependent (and F1 masked the error path). 1 test. (`b49038cf23`)
- **Unterminated string literal** — `scanString` no longer swallows to EOF silently; the scanner
  records a lexical error that `Parse` surfaces as `file:line: unterminated string literal`. 1 test.
  (`fc9158fd9b`)
- **Consciously declined (user ruling):** hex/non-decimal literals — the IDL is decimal-by-design and
  `0x1F` already errors (not silent), so no change; and the two doubled diagnostics (F6 `@min/@max`
  on enum, F7 negative-on-unsigned) — current behavior judged correct/acceptable.

**Tier-1 emission-logic deferrals — 2 of 4 now DONE (2026-07-21).** **G-M4 FIXED** (`d7d3e7fe7f`):
64-bit ints cross the wire as JSON strings (protobuf3 convention) across Go/Python/TS clients —
Go native `json:",string"`, Python int↔str at the seam, TS `bigint` + recursive revivers; body
64-bit params supported; two **fail-loud** gmicompile guards (64-bit REST path/query param; Python
nested-64-bit field). `newthread(period_ns)` now bigint; webapp consumers convert at display. All 6
webapps `vue-tsc --force` clean (pre-existing halscope errors fixed, `1926c82ca8`). **G-L5 FIXED**
(`7d8d51408f`): all C array bounds route through one `#define`-aware helper (`cArraySizeStr`) so
header/bridge/dispatch agree and no `[0]` can leak; regenerated cgo recompiles clean.
**Final 2 deferrals — BOTH DONE (2026-07-21, `505e87d19f`; docs `a6b598240c`).** **G-L1 FIXED** as an
additive capability, *not* an RT-session deferral: the investigation confirmed there is **no RT-invoked
`@callback`** today (the four real ones — `interp_ext` oword/remap ×3, `mcode_handler` handler — are all
task/worker-level and must stay blocking; everything RT-invoked rides on already-annotated `_fn`
typedefs), so nothing was mis-typed. But since gomc is a general framework, `@rt_safe` on a `@callback`
now stamps the `_cb` typedef `GOMC_API_NONBLOCKING` (mirrors `_fn`; default-false → existing callbacks
byte-identical) — ready for the first RT consumer without needing the clang worktree now; **out of the
RT-hardening bucket.** **G-L7 FIXED** as fail-loud (Option B): every silent-drop site in `--client-c`
now errors at generate time. The sweep is the finding — of 16 `@rest_export` IDLs only **5 generate
cleanly, 11 fail loud** (narrow scalars, enum fields, non-string slices, depth-≥2, slice-of-struct), so
the generator was producing broken clients for ~69% of the real REST surface; `--help`/README now
document the supported subset. Full recursive parity (**G-L7/A**) stays deferred-until-a-real-C-consumer.
`F`/`U` now ✅. `RC` ✅ (module-wide `-race` sweep green). `FP` — (a code generator, not a runtime
fault-path module). **Only `S` (final human sign-off) remains open for this module.** Verified: full
`make` regen git-clean, gmicompile suite green (incl. G-L1/G-L7 tests), all generated cgo recompiles,
build/vet/gofmt/lint(0) green.

**`internal/realtime` — reviewed 2026-07-20 (Tier 1; functional review done, awaiting final
human sign `S`).** Architecturally reduced to a startup stub: `New()`/`Start()` are called
exactly once (`launcher.go:230` full `Run()`; `halrun.go:85` subset), no goroutines, no
shared-memory lifecycle, **no cyclic path** — so Tier-1 hotspot #6 ("no GC-managed allocation
in cyclic paths") does not apply here; the cyclic RT paths live in `cmod/*` /
`RT_HARDENING_CHECKLIST.md`. RT modules load in-process via `dlopen` (halcmd shims); HAL/RTAPI
shm is in-process heap, so the 2.9 `realtime.in` SysV-shm `ipcrm` cleanup is obsolete.
(This review originally kept `ipc_cleanup.go` — a file whose entire content was a comment
saying it is intentionally empty — as a marker for that. **Deleted 2026-07-22 on the same
"remove vestigial" ruling:** its rationale was already stated in full in the package doc of
`realtime.go`, which additionally pointed readers at the empty file; a file justified only by
its own existence is not a marker, it is noise. It was the only effectively-empty Go file in
the tree — the other candidates all load-bear: `hallib/cgo.go` carries the `#cgo` directives,
`hallibtest/doc.go` *is* the blank import, the per-package `link_test.go` shims are required
one-per-test-binary by Go, and `pkg/hal/doc.go` is real package documentation.)
**Two cleanups applied (user ruling — remove vestigial checks):** (1) dropped the `/dev/zero` "sanity check" — the in-process heap rtapi never
touches `/dev/zero` (confirmed: it appears nowhere else in gomc), so the check validated a
resource the runtime doesn't use and gave false RT-readiness confidence; (2) removed the dead
`RTAPI_DEBUG` branch — it only logged and propagated nowhere (no `getenv` consumer in the
uspace rtapi; msg level is the server `-d` flag). `Start()` is now an honest minimal validator
that keeps its `error` return as the launcher's startup contract seam. Verified: vet + `go test`
green, launcher builds, gofmt clean.

**`pkg/gomc` + `pkg/cmodule` — reviewed 2026-07-21 (Tier 2; the module-registration surface + the
cmod C-ABI headers).** `pkg/gomc` is two process-global registries: `RegisterModule`/`GetFactory`/
`HasModule` (module factories, `RWMutex`-guarded, panics on duplicate `init()` registration — a
fail-fast programmer error) and the ERROR-log hook fan-out. **One CONFIRMED finding, FIXED (GO-1,
HJ-1 + N2 class):** `OnLogError` had no unregister path, and `NotifyLogError` ran hooks synchronously
on the launcher's log-drain goroutine, which has **no `recover()`**. In `doCleanup`,
`destroyGoModules()` runs *before* the log ring's final flush (`stopDrain`→`drainAll`) and the drain
goroutine is still live during module destroy, so milltask's registered hook (which routes into
`Task.operatorError`) kept firing **after** its task was torn down — a stale hook calling into a
freed module during **normal shutdown** (not just REST unload). And any hook panic would have killed
the drain goroutine → whole-process death. Fix: `OnLogError` now returns an idempotent unregister
`func()` (slice-with-id removal); milltask chains it into `apiCleanup` so `Destroy` removes the hook;
`NotifyLogError` isolates each hook behind `recover()` + `slog.Error` so one bad hook can't take down
the drain. Tests: unregister-stops-delivery, unregister-one-of-many-preserves-order,
panic-isolation. `pkg/cmodule` is headers-only (the cmod C ABI: api/env/hal/ini/log/rtapi/user +
`rt_check`); spot-checked clean — no TODO/FIXME/HACK, bounded `vsnprintf` name construction,
name-length constants documented as mirroring `HAL_NAME_LEN`/`RTAPI_NAME_LEN`, and the only
duplication (the `nonblocking` attribute macro, triplicated across `rt_check.h`/`rtapi.h`/gmicompile
emission) is deliberate for the self-contained external-header set and cross-referenced in-place.
`FP` — (registries + ABI headers, not a runtime fault-path module). Verified: build ./... green, vet
clean, `go test`/`-race` green, gofmt + lint(0) clean. Awaiting final human sign (`S`).

**`internal/gmi` (= `internal/gmi/kinstest`) — reviewed 2026-07-21 (Tier 2; the kinematics GMI/API
boundary test harness).** `helpers.go` (the 376 non-test lines) is a **cgo test harness**, not
production: it `dlopen`s real kins cmod `.so`s, builds a stub `cmod_env` (log/ini/hal/api), and
drives `forward`/`inverse`/`type`/`switchable` through the C function pointers. It is not imported by
any production package (grep-confirmed); it lives in a normal `.go` file only because cgo + `//export`
+ `dlopen` cannot go in `_test.go` (same reason as the new `halnettest`). **No findings** (`F` —):
the harness faithfully mirrors the production path — the `//export`ed `register_api`/`get_api` stubs
route through the real `apiserver.DefaultRegistry`, and each test installs a fresh registry
(`NewRegistry`+`SetDefaultRegistry`) so there is no cross-test contamination. The only smells are
run-to-exit harness leaks (`stub_hal_malloc`'s `calloc`, the `dlopen` handle are never freed) — benign
for a test binary, not worth a fix. Tests exercise trivkins (load/register/forward/inverse/type/
switchable) and pumakins (7-case fwd↔inv round-trip + RPY convention). Verified: `go test`/`-race`
green, vet clean, lint(0). Awaiting final human sign (`S`).

**`generated/gmi/*` boundary — spot-checked 2026-07-21 (Tier 3; IDL→generated fidelity).** Per
risk-class 3 the generator (`internal/gmicompile`) was reviewed once and deeply (hotspot #2), so this
row is a sampled fidelity check of its output against `src/gmi/idl/*.gmi` (33 IDLs → generated
packages), not a re-audit. Three differently-shaped packages verified faithful: **kins** — `const
MAX_JOINTS=16`→`KINS_MAX_JOINTS`, enum values prefixed + preserved (`KINS_IDENTITY=1..BOTH=4`), the
9-field `Pose` struct, `[MAX_JOINTS]f64` arrays, `byref`→pointer, and the two Go/C reserved-word func
names correctly escaped (`switch`→`switch_` in C, `kinsDispatch*` in Go) while the wire/registry names
stay `"switch"`/`"type"`; **ini** — `namespace: string?` carried as `const char *` (NULL = absent, not
double-pointered to `char **`); **halcmd** — `newthread(fp: bool?, cpu_id: i32?)`, the issue-#265
target, carried as `const bool *fp`/`const int32_t *cpu_id` (NULL = absent) with the non-nullable
`period_ns` still by-value — i.e. the recent nullable-scalar-pointer ABI fix is correctly reflected in
the **committed** generated tree. Combined with the generator's byte-identical-regen guards and full
runtests green after the corpus-wide regeneration, the boundary is sound. `L`/`U`/`F`/`FP` — (generated
code: not linted, exercised via its consumers, no hand-findings); `RC` ✅ carried from the generator
review. Awaiting final human sign (`S`).

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
- **L-4 (contract, RESOLVED 2026-07-22 — documented, no code change):** `stopGoModules`/
  `unloadGoModule` call `Module.Stop()` without a started-guard (goModule has no `started` field,
  unlike cModule). The guard is *not* the fix: when `startGoModules` fails mid-loop the remaining
  modules are loaded-but-not-started and still must be stopped, so Stop-without-Start is the
  intended contract. All 17 in-tree implementations were audited against it — 7 are `Stop() {}`
  no-ops and every live one already guards its teardown (`halscope.saverStarted`,
  `stress_gc.startedOK` — whose comment states the contract verbatim —, classicladder's modbus
  `running` flags, milltask's `mon.stopCh`-nil / `seqAbort`-nil / `poslog.running` guards +
  a constructor-started mcode worker, ADS's nil-listener + `stopOnce`, mqtt's constructor-made
  `stopCh`). Written down where implementers will read it: the lifecycle contract now lives on
  `gomc.Module` (factory completeness, Stop-without-Start, at-most-once Stop, Stop-before-Destroy)
  with the launcher's no-guard rationale on `stopGoModules`. Test: `TestStopGoModules_WithoutStart`.
- **L-7 (latent double-Stop, FIXED 2026-07-22 — found while closing L-4):** `doCleanup`'s
  `halComp == nil` branch (startup failed before HAL init) re-ran `stopCModules`/`stopGoModules`,
  which steps 2/2b had already run unconditionally — a **second `Stop()` on every loaded module**,
  which the contract above forbids and which panics `close of closed channel` for any module whose
  Stop closes its own channel (mqttbridge, milltask's mcode worker). Unreachable today (module
  loading happens after `hal.NewComponent`), but it is the trap the contract exists to prevent.
  The branch now only owes the destroy half. Mutation-verified test
  `TestDoCleanup_HALUninitialized_StopsModulesOnce` (restoring the duplicate call panics it).
- **L-5 (FIXED 2026-07-22):** `l.apiServer` is now guarded by a dedicated `apiMu` with an
  `apiServerRef()` accessor; `stopAPIServer` takes the server *out* of the field under the lock and
  runs the 2 s `Shutdown` outside it, so a second or concurrent caller cannot shut the same
  instance down twice, and `startAPIServer` configures/serves one captured local instead of
  re-reading the field. The old code was safe only by call ordering — precisely what a future
  runtime-restart path would break. `-race` test `TestStopAPIServer_IdempotentAndConcurrent`
  (mutation-verified: reverting the accessor reports a DATA RACE).
- **L-6 (FIXED 2026-07-22):** the signal watcher (duplicated in `Run` and `RunHalFile`) blocked on
  a bare `<-sigCh` forever, so it outlived the shutdown it watched for and left `signal.Notify`
  delivering into a channel nobody reads — a leaked goroutine + runtime signal registration per
  `Launcher`. Both copies now call one `watchSignals()` helper that also selects on `shutdownCh`
  and `signal.Stop`s on the way out, and `doCleanup` closes `shutdownCh` (idempotent) so a startup
  error return or a one-shot halrun releases the watcher too. Tests
  `TestWatchSignals_ExitsOnShutdown` + `TestDoCleanup_ReleasesSignalWatcher` (both mutation-verified).
  `retainSync`'s busy-wait now spins for 5 ms and then backs off to 500 µs sleeps: the RT function
  clears the flag within one servo cycle, so the healthy path is unchanged, but a stalled RT no
  longer pins a CPU at 100 % for the full 1 s timeout (the shutdown-path final sync included).
Verified: build ./... green, vet clean, `go test -race ./launcher/... ./pkg/gomc/...` green,
`make gomc-lint-full` 0 issues, `make gomc-fmt-check` clean.
- **L-8 (nil-HAL SIGSEGV, FIXED 2026-07-22 — found by writing the unload tests):** both unload
  paths called `halcmd.FindCompID(name)` unconditionally, which goes straight into
  `halpr_find_comp_by_name` and **dereferences `hal_data`** — NULL until the first `hal_init`.
  So a runtime unload before HAL is up is a segfault, not an error return. The hooks are wired
  into halrest at the very top of `Run()` (before HAL init), which is what makes it reachable at
  all; the REST server starting later is what has kept it latent. Both paths now go through
  `halCompID()`, which returns 0 without HAL — and no HAL also means no RT functions to remove.
Row → **L R F U RC FP ✅** (all L-1…L-8 closed); only `S` (human sign-off) remains.
`U`/`FP` closed 2026-07-22: 237 → 898 test lines. **Unload/lifecycle:** the full runtime-unload
path for a Go module (stop → unregister REST *and* watch APIs → destroy → remove; bystander
untouched; a second unload is `ENOENT`, not a double Stop), the `EBUSY` dependency guard and
both records that must *not* block it (self-reference, consumer no longer loaded), and
`ENOENT` for an unknown name. **Fault paths:** a mid-loop `startGoModules` failure — the
scenario the Stop-without-Start contract exists for — leaves later modules never started, and
`doCleanup` must still stop and destroy every one of them exactly once; and `fail()` (the
REST-server-death path) keeps the FIRST error for `Run`'s return and triggers the ordered
shutdown once under concurrent callers. **Config/CLI surface:** REST address precedence
(env > INI > loopback default), the `REST_ORIGINS` allow-list parse including the empty →
same-origin-only secure default (N1), `initHalibPath` ordering, `setConfigEnv`, the halrun
tokenizer (quotes, comments, `#` inside quotes) and its `loadusr`/`waitusr` rejection.
Still untested by design: `cmodules.go`/`retain.go` need a real cmod `.so` and a running RT
thread — they are covered by the runtests suite, not unit tests.

### Phase 2 — field I/O (drives real iron; highest risk per untested line)

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| cmd/ethercat | 3867/410 | 1 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ◐ |
| internal/ads | 1763/1700 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ☐ |
| internal/adsbridge | 498/278 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ☐ |
| internal/adsconfig | 1473/2988 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ☐ |
| internal/adsmodule | 163/83 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ☐ |

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

**ADS cluster — remaining findings all resolved (2026-07-22, user rulings).** Closes the ADS
Phase-2 bucket except final human sign-off:
- **A5 (shutdown free-barrier)** — now that **pkg/hal H1** is fixed (component-liveness barrier makes
  a straggler pin access after `Exit()` return zero/`ErrComponentExited`, not corrupt freed memory),
  ADS `Stop()` was made a **true `wg.Wait()` join with no silent cap**. Sound because the wait is
  bounded (listener + all conns closed first; read/write deadlines everywhere), so once `Stop()`
  returns no goroutine can touch a pin before `Destroy()` frees them. `shutdownTimeout` removed.
- **A7 (resource caps)** — default **8 connections / 256 subscriptions per connection**, overridable
  per instance via `$max-connections` / `$max-subscriptions` in the `.conf`. Over-cap connections are
  closed; over-cap subscribes return `ErrDeviceNoMemory`.
- **A9 (network exposure)** — default `$bind` changed to **`127.0.0.1`**; remote exposure is now
  opt-in (`$bind 0.0.0.0`). The "no protocol auth on an exposed port" statement remains a line item
  for the cross-cutting Safety boundary document.
- **A13 / A14** — process-image write RMW now serialized under a full write lock; name-handle map
  capped at 65536 (`ErrDeviceNoMemory` over the cap).
Regression tests per fix (`internal/ads/hardening_test.go`, `internal/adsconfig/serverconf_test.go`);
build/vet/gofmt clean, pinned lint **0**, `go test -race` green. Rows `ads`/`adsbridge`/`adsconfig`
→ `F`/`U`/`FP` closed; only human `S` sign-off remains across the cluster.

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
| internal/launcher | 3196/898 | 1 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ☐ |
| internal/daemon | 277/365 | 1 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ☐ |
| cmd/gomc-server | 234/159 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| internal/config | 97/223 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| internal/pkgreg | 293/121 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| pkg/inifile | 606/966 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |

**Phase 3 tail reviewed 2026-07-21 (Tier 2 + inifile vs the 2.9 C oracle). Full findings in
`PHASE3_REVIEW_FINDINGS.md`.** `pkg/inifile` is the runtime-critical one (every customer INI
must load identically). Two CONFIRMED parity divergences vs `libnml/inifile` fixed: **I-1**
backslash line-continuation was not implemented (158 shipped lines use it — e.g.
`[DISPLAY]APP = sim_pin \` lost every argument); **I-2** inline `;` was stripped as a
comment, silently truncating `MDI_COMMAND = G0 Z25;X0 Y0;Z0` to `G0 Z25` (36 shipped
occurrences; the C parser never strips `;`, and 0 configs use it as a comment). The narrow
whitespace-`#` strip is kept (reproduces 2.9's `strtod` numeric tolerance with least risk).
I-2 reverses a behavior a test encoded as intended; taken as a bug fix under "load
identically" and **ruled keep-as-is by the user 2026-07-22 — finding closed**.
`internal/pkgreg`: **F1** a
typo'd `packages.conf` TYPE silently dropped a module (green build, module gone at runtime)
→ now a loud `file:line` build error; **F2** `_test.go`-only dirs no longer mis-discovered.
`cmd/gomc-server`/`internal/config` clean (2 LOW notes, no code change). Regression tests
added for every fix; `-race`/vet/gofmt/lint(0) green.

**Phase-3 `U` tail closed 2026-07-22** (`internal/daemon`, `cmd/gomc-server`, `internal/config`
— the three rows the review left at `☐`/`◐` for coverage). `pkg/inifile` **I-2 ruled
keep-as-is by the user** (`;` stays data; the narrow whitespace-`#` strip stays) — that
finding is closed. Three defects surfaced by writing the tests:
- **daemon D-4 (real bug):** `SyslogHandler.WithAttrs` appended into the parent's spare
  capacity, so two loggers derived from the same parent via `slog.With` shared one backing
  array and the second overwrote the first one's attrs (mutation-verified). `WithGroup` had
  the mirror defect — `groups` was recorded and never applied, so attrs from different groups
  collided under bare keys. Both fixed; handler attrs now also precede record attrs.
- **daemon D-1/D-2/D-3 + gomc-server F5 (pidfile ownership):** the parent and child both wrote
  the pidfile; a second daemon silently overwrote a live instance's file (orphaning it — nothing
  could stop it afterwards, while two servers fought over the same HAL shm and REST port); and
  `RemovePidFile` would delete a *replacement* instance's file. Now: parent is the sole writer,
  `Daemonize` refuses with `ErrAlreadyRunning` on a live PID (stale/malformed still overwritten,
  EPERM counts as alive), removal is ownership-checked, and `main.go` `defer`s the removal so
  **every** exit path drops it (it used to run only after a clean `Run()`).
- **config C-1 (dead `-X`):** `go build -ldflags -X pkg.Name=v` is a **silent no-op** when
  `Name` does not exist. The Submakefile injected `DefaultNmlFile`, which no Go code has ever
  declared (NML-era leftover). Removed, and a test now parses the Submakefile against
  `paths.go` and fails on any injection with no target — plus the reverse direction
  (declared-but-never-injected must document its empty-value fallback) and a check that every
  path var stays an uninitialised `string` (an initializer would make `-X` silently ineffective).
Coverage: `internal/daemon` 0 → 365 test lines (pidfile round-trip/malformed rejection,
`processAlive` self/reaped/EPERM, already-running refusal, child-does-not-rewrite, all four
`RemovePidFile` cases; syslog severity routing, live `Leveler`, attr ordering, the aliasing
regression, group qualification — against a new `syslogWriter` seam so no syslog daemon is
needed). `cmd/gomc-server` 0 → 159 (every argument path that returns before `launcher.Run`:
`-h` incl. a flag-documentation check, unknown flag → 2, missing INI, `-l`/`-` not-implemented,
`-H` missing-dir and not-a-dir, `-d` out-of-range, and the `multiFlag` accumulator).
`internal/config` 37 → 223 (the three checks above; the old hand-maintained default-value list
had drifted to 15 of 24 vars and is now driven off the parsed declarations).

**`internal/pkgreg` F3 closed 2026-07-22 (removed).** `ReadFile`/`WriteFile`/`Remove` had no
callers, and `WriteFile`'s round-trip dropped the `@GOMOD:TAG@` build-flag markers and every
comment `ReadConfIn` exists to interpret — a lossy writer beside a marker-aware reader is a
trap, not a "coherent API": wiring the two together silently strips the conditional-build
markers from `packages.conf` and drops optional modules from the build on a green compile.
Deleted (`internal/` package — no out-of-tree importer can exist). The companion
`hasInitFunc` note is closed no-change (the regex is already line-anchored; rationale
recorded at the regex).

**Phase 3 status: every module is `L R F U RC` ✅ and `FP` ✅ or n/a — only the human `S`
sign-off is open across the whole phase.**

### Phase 4 — HAL tooling

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/halcmd + cmd/halcmd | 3540+1932/364 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| internal/halparse | 1769/2330 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| internal/halfile | 343/400 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| internal/haljson | 876/151 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
| internal/modcompile + cmd | 2909+1636/393 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ◐ |
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

**halparse HP-1..HP-4 FIXED after user ruling (2026-07-21, commit `7bf02a484e`):**
the tokenizer now matches 2.9 exactly — per-line order `strip_comments →
replace_vars → tokenize` (HP-1: a `#` in a substituted value no longer truncates
the line; refs in comments no longer substituted), missing INI/ENV var → hard
error via a new `INILookup.Get` `found bool` (HP-2, 2.9 replace_vars -5/-4),
backslash is an ordinary char (HP-3), continuation joins with no separator (HP-4).
Validated by a full runtests round (all green); a `[SEC]KEY` that fails to
resolve now errors (exactly 2.9's behavior).

**HC-2 and HF-1 CLOSED 2026-07-22** by the server-side path-resolution ruling
(see the status log and `PATH_RESOLUTION_INVENTORY.md`): `resolveArgPath` is
deleted (paths are server-side paths, resolved + contained by `internal/pathres`),
and `.` is no longer a `LIB:` search root. Validated by a full 241-test runtests
round with zero path-resolver rejections. Still flagged: HJ-2 (drain contract —
ties to ADS A5 / pkg-hal H1), HP-6/HP-7 (`$(VAR)` syntax / dropped `getp`·`print`
output).

**`U` gaps CLOSED 2026-07-22 — every Phase-4 row now carries `U` ✅, except Tier-3 `internal/hallib` where it stays n/a (its Go surface is a 12-line cgo link shim)** (see the status
log entry for the detail and the one behaviour fix it surfaced). The unlock was
that a **live in-process HAL is available inside a test binary** (the pattern
`pkg/hal`'s keep-alive `TestMain` establishes), so the command surface could be
tested against real pins and signals instead of by compile-time signature
assertions. Coverage: halcmd 9.5→67.7 %, halparse 72→87 %, haljson 18.6→93.1 %,
modcompile 59.8→86.1 % (cross-package), cmd/halcmd 1.7→78.1 %.
HAL **thread** lifecycle is covered too, and getting there found a controller
hang — see the `thread_lock` entry in the status log. (The first attempt skipped
those tests on "no RT privileges"; that was a misdiagnosis. `app_policy` is a
static `SCHED_FIFO` until `rtapi_initialize_app()` runs its `SCHED_OTHER`
fallback, and the test binary had never called it. Calling it — exactly as the
launcher does — makes thread creation work unprivileged.)
**Residual, deliberately not unit-tested** — the *process-lifetime* entry points,
all called only from `internal/launcher`'s startup/shutdown sequence, never from a
command path. Refined 2026-07-22 after checking each one rather than deferring the
group wholesale; three turned out to be testable and now are:
`UnloadAll` (in-process it must be a **no-op** — the shim only SIGTERMs
components owned by *other* pids — so the test guards the pid check that, if
inverted, would make the controller SIGTERM itself), `SetExact` (refused once a
base period exists), and the `LockDLHandle`/`UnlockDLHandle` nil guard on the
cmod load path. What genuinely cannot be unit-tested, and why:
`RtapiAppCleanup` (`halpr_rtapi_app_exit` deletes every thread, then
`rtapi_shmem_delete` + `rtapi_exit` — HAL is not re-initialisable afterwards, so
one call ends the test binary's HAL for every remaining test);
`RtapiAppInit` (its partner — the binary's HAL is already up before the first
test runs); `SetLogRing`/`ClearMsgHandler` (they retarget the *global* RTAPI
message handler, and clearing it silently discards diagnostics for everything
that follows); and the sysfs topology readers `InitCPUPool`/`detectTopology`/
`cpuOnline`/`parseIsolatedCPUs`/`readSiblingsList`, whose answers are properties
of the host, not of the code — their one pure function, `parseCPUList`, **is**
tested, and `acquireCPU` is tested against an injected pool.
**These are covered end-to-end instead**: every one of the 241 runtests starts a
`gomc-server` or `halrun`, which is precisely this sequence — `RtapiInitializeApp`
→ `SetLogRing` → `RtapiAppInit` → `InitCPUPool` → … → `UnloadAll` →
`ClearMsgHandler` → `RtapiAppCleanup`. A regression in any of them does not
produce a subtle wrong answer; it fails to boot or fails to shut down, which the
suite catches immediately.
Separately, `cmd/modcompile` stays at ~10 % because the rest of it shells out to a
C compiler or rewrites the source tree — the build regenerates and compiles every
shipped `.comp` on each run, which is the stronger signal.

### Phase 5 — services & auxiliaries

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/apiserver | 2242/3627 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ☐ |
| internal/halrest | 663/849 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ☐ |
| internal/inirest | 90/553 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ☐ |
| internal/mqttbridge | 951/1151 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ☐ |
| internal/halscope | 1035/1096 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ☐ |
| internal/persist_sqlite | 397/471 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ☐ |
| internal/tooltable | 445/459 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ☐ |
| internal/emccalib, internal/calibreg | 393+46/844+53 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ☐ |
| internal/halstream | 125/125 | 3 | ✅ | ✅ | ✅ | ✅ | ✅ | — | ☐ |
| cmd/halsampler, cmd/halstreamer | 130+131/0 | 2 | ✅ | ✅ | ✅ | — | ✅ | — | ☐ |

LOC in *this* table refreshed 2026-07-22 after the coverage passes (the rest of the matrix is
still the 2026-07-11 snapshot). Three rows had drifted out of the table body into the prose
below it and are back where they belong. **`internal/halstream` is a new row** — it was factored out of
halsampler/halstreamer by the 2026-07-21 codegen-duplication audit (the shared WS stream
framing: `cfg:` header + 8-byte codec) and never got one. Tier 3: the authoritative
definition of the wire format is the C `hal_stream_common.h`, this is its Go reader/writer.

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
webapp traversal. **`N6` CLOSED 2026-07-22** (bookkeeping — the fix landed 2026-07-21): N6 *was*
the observation that halrest's load/unload makes launcher **L-3** remotely reachable, and L-3 got
the full locking fix (`arenaMu`/`modMu`/shutdown gate) the same day, so halrest's one and only
finding is fixed — row `F` → ✅ (still no halrest code change; the fix is in the launcher).
**Network half CLOSED 2026-07-22.** N7 (mqtt publish-count) and N9 (connection cap) are fixed,
and the `U`/`FP` tail with them — coverage halrest 0 → 87.1 %, mqttbridge 0 → 86.8 %, halscope
4.1 → 91.3 %, apiserver 45.6 → 96.2 %, all against a **real in-process HAL** rather than a mock
(keep-alive `TestMain`; halscope additionally needs `halcmd.RtapiAppInit()` for a component
`hal_export_funct` will accept). Writing them surfaced two more real defects, both fixed: **N10**
halrest's `GetStatus` compared the lock level against `"NONE"` while halcmd renders it lower-case,
so an HMI could never see HAL unlocked; **N11** the webapp SPA fallback was an infinite redirect
loop, so every deep link into a bundled app died after ~10 redirects — the exact case SPA fallback
exists for, unnoticed because the bare entry point worked. **`internal/inirest` closed 2026-07-22**, the last
of these rows: 57.5 → **100 %**, the whole gap being `GetParameterFile` at 0 % — the one method
here that reads a file off disk, at an INI-supplied path, and serves its contents over REST. Its
containment check is now tested for the right reason (see **I-3** and the method note in the
findings doc: the first version of that test passed on a "not found", which containment removal
would not have changed). The REST/WS surface still has **no authentication** (model is
"trusted local origin"; non-loopback needs a proxy/auth) — a cross-cutting item, not a Phase-5 row
blocker. build/vet/gofmt clean, lint 0, `-race` green.

**Services & auxiliaries (persist_sqlite/tooltable/emccalib+calibreg/halstream/halsampler+
halstreamer) — reviewed 2026-07-22 (Tier 2, adversarial). Full findings in
`PHASE5_REVIEW_FINDINGS.md`.** Same untrusted-wire lens (all four service IDLs are
`@rest_export`), plus a line-by-line diff of the two file-format parsers against the C they
replace. **Three HIGH, all fixed.** **E-1 (emccalib):** the tunable index stored
`&e.tunables[len-1]` taken *inside* the append loop, so every pointer captured before a
reallocation aliased an orphaned backing array — `Revert` read `iniValue` through it and kept
restoring the process-start value no matter how often the operator saved. Now an `int` position.
**T-1 (tooltable):** `parseTblLine` checked only `T` and `P`; every offset used the
discarded-error form, so an unparsable field silently became `0.0` and the line still imported —
and a zeroed **tool-length offset** is a tool driven into the work. The C does `if (!valid)
return -1` after each `sscanf`; that is now matched, and rejected lines are logged. **T-4
(tooltable):** `GetTool` of an unstored tool returned "unexpected end of JSON input" instead of
the zero entry — its not-found branch matched on an error string that can never arrive (see G-1).
Also fixed: **P-1/P-2** (persist_sqlite — REST-reachable `open` grew namespaces, fds and disk
without bound; `delete_all`/`open` cycling grew the handle slice), **T-2** (a lowercase `.tbl`
imported as an *empty* table; the C folds case), **T-3** (a transient read error at `Start` read
as "empty" and replayed the legacy `.tbl` over edited offsets), **T-5/T-6** (unsynchronised
publish + nil client in the runtime-REST-load window), **E-2/E-3/E-4/E-5** (use-after-unlock; a
stale line number overwriting an unrelated INI key; saves destroying inline comments — the split
rule now shared out of `pkg/inifile` as `SplitInlineComment`; hard-coded instance name),
**S-1..S-4** (a zero-pin `cfg:` header spun halsampler forever; unbounded `ReadRaw`; halstreamer
not skipping `#` lines; `httpToWS` duplicated). Coverage: persist_sqlite 10.3→86.0 %, tooltable
2.1→89.0 %, emccalib 9.1→43.2 %, halstream 100 %. T-1/T-2/P-1/P-2/E-3/E-4 mutation-verified.
build/vet/gofmt clean, `-race` green. **G-1/G-2 — FIXED 2026-07-22** (see the status-log entry):
the storage APIs were converted to the `@rc_error` shape, so a `persist`/`tooltable` failure now
reaches every consumer instead of arriving as a zeroed payload. `emcio.GetStatus` was ruled out of
scope and stays as it is. **emccalib's `U` closed 2026-07-22** (43.2 → 94.4 %): the reason it had
been ◐ — `GetTunables`/`SaveIni` read live HAL pins through `halcmd.GetP` — stopped being a reason
once the network pass established the in-process HAL pattern, so the whole REST surface is now
exercised against real pins rather than around them.

### Phase 6 — UI-adjacent

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/ngcpreview | 1302/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/pyvcpmodule | 1585/555 | 2 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ☐ |
| lib/python/gmi (classic `linuxcnc` API shim) | 2047/0 | 2 | ☐ | ☐ | ☐ | ☐ | — | ☐ | ☐ |
| AXIS gmi adaptation (diff-scoped) | Δ +1663/−3038 | 2 (diff only) | — | ☐ | ☐ | — | — | ☐ | ☐ |
| webapps ×5 (`src/webapp/`, excl. classicladder) | 7103/0 (Vue/TS) | 2 (write paths) / 3 (display) | ◐ | ☐ | ☐ | ☐ | — | ☐ | ☐ |

**`internal/pyvcpmodule` — reviewed 2026-07-22 as part of the widget-centric
fix-and-migrate** (`PYVCP_REVIEW_FINDINGS.md`; branch `pyvcp-restruct`, commits
`0ce9fa4583` + `f5f112f6ba`; LOC refreshed from the 749/0 pin-centric snapshot). The
pin-centric port this row originally pointed at was **replaced, not reviewed** — ruling:
reviewing code slated for deletion is negative-value work; the widget-centric rewrite
(server-authoritative, event-in / delta-state-out, multi-client) was reviewed, fixed and
adopted instead, and is now the **template for the remaining UI-framework ports** (see
the cross-cutting item). Headline findings, all fixed: a CRITICAL unload use-after-free
(an open watch pushLoop outliving the panel's HAL pins — goroutine-ownership class), all
HAL-input processing running only while a UI client watched (now a module-owned 100 ms
ticker), client auto-name counter desync, a raising callback permanently freezing the Tk
poll loop, min/max `0`-as-no-limit sentinel (now `f64?`), client-computed TIMER, dropped
meter/title/option/image widgets. `tests/pyvcp` runtests then caught two pin-inventory
regressions the review missed (dial `param_pin`, `halparam` naming) — the origin of the
template's mandatory parity-test rule. Deferred with rationale: watch snapshot/seq
marker (shared-infra wire change), `map<K,V>` IDL support — **the latter closed the
same day**: `map[string]T` + `@watch_delta` landed in gmicompile and `watch_state` is
now fully generated (findings doc D-2). **Caveat recorded:** the review ran inside the
fix-and-migrate session, not as a standalone adversarial pass. 19 unit + HAL-backed
tests `-race` clean, lint 0, runtests green; only human `S` remains.

**Phase-6 scope extended (ruling 2026-07-22).** The phase originally listed only the two
Go server modules; a survey of the actual UI surface added the hand-written client side.
Rationale for including client code at all: a webapp/UI cannot crash the controller (the
server side is hardened under the untrusted-wire posture), but it can **show or write
wrong values to an operator** — and `ngcpreview` was already in phase while the client
half of its contract was not.
- **`lib/python/gmi` — Tier 2, highest priority of the additions.** 2047 LOC hand-written
  (the generated `ini_client.py`/`manualtoolchange_client.py`/`ngcpreview.py` are Tier 3
  under the closed gmicompile review). This is the drop-in `linuxcnc.stat()/command()`
  replacement that AXIS *and every ported runtests driver* stand on — not UI code but the
  client half of the API contract, and it has already produced production-relevant bugs
  found only in passing (the no-op `poll()` over the 50 ms WS cache; RCS-error routing).
  Review lens: classic-API parity, the mm boundary, the jog dead-man contract.
- **AXIS — review the adaptation diff only** (+1663/−3038 vs merge-base `f5a72ff602`:
  ~1270 changed lines of `scripts/axis.py`, the gutted NML `extensions/emcmodule.cc`, new
  `glhelpers.cc` + `manualtoolchange_ui.py`). The NML→`gmi` backend swap is exactly where
  the documented client-contract traps live (2 s jog refresh, mm vs machine units,
  error-channel routing); the untouched Tk/GL bulk is proven 2.9 code, out of scope.
- **Webapps (6 Vue3/TS apps, zero automated tests) — risk-split, not line-by-line.**
  Tier 2 on the **write/command paths**: tooledit (a wrong offset written to the tool
  table is a tool driven into the work — same hazard class as tooltable finding T-1),
  emccalib tunable save/revert, halshow's pin-set path, halscope's trigger/control path.
  Tier 3 (type-check + manual smoke) for display-only rendering; `vue-tsc --force` is
  already clean from cold for all 6 (`1926c82ca8`), hence `L` ◐ (no ESLint gate). The
  **classicladder webapp defers** with its frozen backend (see the deferred table).
  Generated TS clients (~2.7k LOC) are Tier 3, covered by the closed gmicompile review
  (incl. the G-M4 64-bit-bigint sweep). The zero-test finding is tracked as its own
  cross-cutting item (webapp write-path tests) rather than per-row `U`.

### Deferred / frozen

| Module | Reason |
|---|---|
| internal/classicladder | **Mid-migration/reimplementation** — review after it settles; only lint + `-race` in CI until then. Its `src/webapp/classicladder` frontend (1919 LOC Vue/TS) defers with it (Phase-6 ruling 2026-07-22 — reviewing UI over a backend that will change is the negative-value work ruled out for pin-centric pyvcp) |
| internal/tasktest | Test scaffolding (Tier 3) |
| cmod/* (motion, tp, homing, components) | Inherited 2.9 C code — algorithm risk low; the **binding boundary** is covered in Phase 1; **RT-correctness** (effects-check, non-blocking) tracked in `RT_HARDENING_CHECKLIST.md` (motmod/tpmod/homemod already verified clean) |
| panelui, tracking-test, linuxcnclcd, motion logger cmod host, classicladder UI, all UIs except axis, qtvcp/gladevcp | Not (fully) ported — tracked in `MISSING_FEATURES.md`. **Ruling 2026-07-22:** future UI ports follow the widget-centric / server-authoritative template proven by pyvcp (`src/gmi/VCP_MIGRATION.md`); its three attach-conditions — `map[string]T` codegen (DONE 2026-07-22), a user-code ruling before gladevcp/qtvcp, a mandatory pin-parity test — are the cross-cutting item below |

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
   loss root-caused here; `--server-go` ptr truncation; mapper drift; fail-fast guards; **G-M4
   64-bit-as-JSON-string + G-L5 array-size `#define` consolidation, 2026-07-21**). The last two
   deferrals are now **also DONE** (`505e87d19f`, 2026-07-21): G-L1 landed as an additive
   `@rt_safe`→`_cb` `GOMC_API_NONBLOCKING` capability (no RT `@callback` exists today, so out of the
   RT-hardening bucket), G-L7 as `--client-c` fail-loud (silent field-drops → generate-time errors;
   only 5/16 `@rest_export` IDLs generate cleanly, so full recursive parity G-L7/A stays
   deferred-until-a-real-C-consumer). Tier-2 parser/AST also reviewed + closed (see the Phase-1
   note). **No open gmicompile findings remain** — only the final human sign-off.
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
   priority):** deep-review of the rarely-used `reg/sii/foe/soe` read-write commands. **Master-side
   follow-ups — RESOLVED 2026-07-22 (driver verified working on 5+ machines; these were cosmetic
   diagnostics fixed at the GMI/CLI boundary, master core untouched):**
   - **`version` (fixed):** the CLI decoded the ioctl ABI magic as a version → "0.0.37" (the magic is
     a flat ABI-compatibility counter, currently 37, not a packed version). Now the GMI `ModuleInfo`
     carries a real `version` string, populated in `gmi_ethercat.c` from the public `ecrt.h`
     RT-interface macros (`ECRT_VER_MAJOR`.`ECRT_VER_MINOR` → "1.6") — no master-core change. The CLI
     prints both facts: **`IgH EtherCAT master 1.6 (API Version 37)`**. (The full patch level "1.6.8"
     lives only in the master's private `config.h`; surfacing it would need a tool-API field —
     deferred as not worth a submodule change for a diagnostic.)
   - **`Phase: Idle` while active (fixed):** the in-process uspace master (`--enable-uspace-master
     --disable-kernel`) never runs the kernel-only `ec_master_enter_operation_phase()`, so it stays
     in the IDLE phase and signals "in use" via the `active` flag. The CLI now reports **Operation
     when `active`** — no master-core change.
   - **SDO dictionary (not a bug):** the master FSM *does* auto-fetch the dictionary
     (`fsm_master.c`), gated on `EC_WAIT_SDO_DICT = 3 s` after a CoE slave reaches PREOP. A `sdos`
     query inside that 3 s window sees `SdoCount = 0`; on a running machine it fetches fine (matches
     the "works perfectly on 5+ machines" evidence). Left as-is; the verified master FSM is not
     touched for a timing artifact.
   Both fixes covered end-to-end by new assertions in `tests/ethercat/sim-cli` (phase Operation while
   active; real version, not the ioctl magic).
   **`U`/`FP` closed (2026-07-22):** added `cmd/ethercat/output_test.go` — the command formatters and
   their error paths now run against an `httptest` server returning canned JSON (real generated REST
   client + real formatting/validation, stdout captured). **Formatters (U):** `version`, `master`
   (active/idle), `slaves`, `domains`, `sdos`, and the previously-uncovered read group
   `reg_read`/`sii_read`/`foe_read`/`soe_read`/`upload`; `pdos`/`cstruct`/`xml` stay covered by
   `sim-cli`. **Fault paths (FP):** transport failure (unreachable server), HTTP-5xx propagation,
   per-slave fetch error mid-iteration, in-band result errors (FoE/SoE result + SDO abort code), and
   9 argument-validation cases. Still un-unit-tested (minor, tracked under the existing deferral): the
   **write** commands `reg_write`/`sii_write`/`foe_write`/`soe_write` (the deferred deep-review set)
   and a few small display commands (`graph`/`ip`/`eoe`/`crc`/`alias`/`config`/`data`). vet/gofmt
   clean, pinned lint 0, `-race ×3` stable. Only human `S` sign-off remains for the row.
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

- [x] **Safety boundary document** — [`SAFETY_BOUNDARY.md`](SAFETY_BOUNDARY.md) (draft
  2026-07-22). Platform-general (gomc is a framework, not a machine control): states the core
  principle that **operator-safety functions must be certified hardware, independent of gomc**,
  sized to a per-machine hazard analysis (ISO 12100 → ISO 13849-1 / IEC 62061); asserts
  **per module** (RT motion, iocontrol, EtherCAT/lcec incl. the FSoE black-channel note, ADS,
  HAL, task/interp, REST/WS, MQTT) that the software is **not load-bearing** for any safety
  function; documents the software E-stop as a control convenience (the `ioControl.c`
  series-chain), and separates the unauthenticated-control-surface **security** boundary from
  the **safety** boundary. Open TODOs (machine-specific/policy) tracked at the end of that doc.
  <a name="safety-boundary"></a>
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
  Runtime REST module load/unload **is** a supported production path → launcher **L-3 got the
  full locking fix** (landed 2026-07-21; that also closes network finding N6). What remains open
  here is the auth deliverable itself, nothing in the launcher.
- [ ] **Widget-centric UI migration template** (ruling 2026-07-22, see
  `PYVCP_REVIEW_FINDINGS.md` + `src/gmi/VCP_MIGRATION.md`). The pyvcp widget-centric
  protocol — server owns pins/constraints/quantization/derivations, clients send gestures,
  server pushes delta-encoded per-widget state — is the template for every remaining UI
  framework port. Three conditions before it scales: (a) **`map[string]T` in the GMI
  IDL — DONE 2026-07-22** (`@watch_delta` too; findings doc D-2): a watch-only func may
  return a string-keyed map, confined by the checker to exactly that position (no C
  ABI — the C-provider surface skips it with an emitted comment, Go providers only),
  and pyvcp's `watch_state` is now fully generated, so a migrated framework has
  nothing left to hand-register; (b) **user-code ruling before
  gladevcp/qtvcp** — pyvcp is purely declarative XML, but GladeVCP/QtVCP embed arbitrary
  user Python with direct HAL access, and where that runs (server-side plugin vs.
  client-side against GMI only) must be decided before those ports start; (c) **a
  runtests-level pin/behavior parity test against the original framework is mandatory**
  per migration — pyvcp's review missed two pin-inventory regressions that only its
  parity test caught. The deferred watch snapshot/seq marker (shared-infra wire change,
  all watch consumers) is tracked in the findings doc's D-1.
- [ ] **Webapp write-path tests** (ruling 2026-07-22, Phase 6). ~9k LOC of hand-written
  Vue/TS across the 6 webapps has **zero automated tests**. Minimum bar for prototype
  delivery: request-level tests for the state-mutating paths (tooledit tool-table save,
  emccalib tunable save/revert, halshow pin-set, halscope trigger/control) asserting the
  emitted JSON against a mock server — the "fixed-but-untested" rule applied to the client
  side. A browser-e2e harness is explicitly out of scope for now; `vue-tsc` from cold
  stays the type gate.
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
- [x] **Surface RCS command errors to clients — DONE 2026-07-22.** A Go provider now serves
  REST and WS from the generated Go-native handler set; the C callbacks struct is still built
  and registered, because that is what lets a cmod call a gomod in-process. Routing REST
  through it was the bug: that ABI has no error channel, so the `//export` trampoline
  substituted a zero value (an `i32` became `-1`, a slice became empty, a struct became
  zeroed) and the client was told the call succeeded. A refused MDI is now an HTTP error
  carrying the machine's reason, and a provider errno maps to its status (EBUSY → 409).
  `RegisteredAPI.GoFuncs` + `RESTFuncs()` is the whole switch; C-provided modules are
  untouched. The generator emits `XxxHandlers` once and derives `XxxCommands` from it, so the
  two surfaces cannot drift apart again — two independently generated copies of one dispatch
  body is how REST and WS came to disagree in the first place. Clients: `gmi.Command` methods
  `return` the rc instead of discarding it (additive — they returned `None`), `gomc_test`
  translates the HTTP error into its `Timeout` so a failing test still reads the deadline and
  the machine's reason, and `bin/axis` wraps `gmi.Command` to route a refusal into the
  notification area operators already watch rather than a traceback on stderr. `linuxcnctop`
  is read-only and `manualtoolchange_ui` talks to a cmod, so neither was affected.
  Mutation-verified (disabling the switch fails all three contract tests); build, vet, gofmt,
  golangci-lint, `-race` and the whole Go suite clean.
  **Runtests caught a real over-correction, now fixed: 241/241.** Making *every* provider error
  a transport error was the opposite mistake to swallowing them all. A command the task
  **refuses** (wrong mode, not homed, busy) and one it **accepts, runs, and faults on** are
  different events: the tool-table tests deliberately issue `G10 L1 P0`, which the interpreter
  rejects with "P value out of range", and then read the resulting state — with the fault also
  raised as HTTP 500 the driver died mid-test, and the same event was reported twice (it was
  already on the error channel via `faultMDI`). `internal/task`'s `executedError` now marks the
  accepted-and-faulted class and `rcFor` maps it to **`RCS_ERROR` in a normal response**, which
  is what the `-> i32` return on every emccmd function was always for and what classic
  `linuxcnc.command()` does. Refusals stay transport errors. This is exactly the distinction
  `gomc_test.py` had documented all along ("tests legitimately issue commands that error … and
  then introspect the resulting state"), and the one place it was written down is what named
  the bug.
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
| 2026-07-22 | **Phase-6 scope extended to the hand-written UI client surface** (ruling, backed by a survey of the actual tree). Three rows added: **`lib/python/gmi`** (2047 LOC hand-written; the drop-in `linuxcnc.stat()/command()` shim every ported Python UI and every runtests driver stand on — Tier 2, highest priority: it already produced production-relevant bugs found only in passing, e.g. the no-op `poll()` over the WS cache); the **AXIS adaptation diff** (+1663/−3038 vs merge-base `f5a72ff602` — the NML→gmi backend swap reviewed diff-only against the documented client contracts: 2 s jog refresh, mm boundary, error routing; the untouched Tk/GL bulk is proven 2.9 code, out of scope); and the **5 non-deferred webapps** (7103 LOC Vue/TS, zero automated tests — Tier 2 on write/command paths only: tooledit/emccalib/halshow/halscope, where a wrong value written is machine damage; Tier 3 for display-only rendering; the classicladder webapp defers with its frozen backend). Generated TS + Python clients stay Tier 3 under the closed gmicompile review (incl. G-M4 bigint). New cross-cutting item: **webapp write-path tests** (request-level against a mock server; no browser-e2e for prototype delivery). Rationale: a UI cannot crash the hardened server — the hazard is showing or writing wrong values to an operator — and `ngcpreview` was already in phase while the client half of its contract was not. |
| 2026-07-22 | **`map[string]T` + `@watch_delta` land in the GMI IDL — pyvcp's `watch_state` is now generated, closing findings-doc D-2 and template condition (a) the day it was written.** The deferral's "large codegen feature" costing was stale: it dated from when REST went through the C dispatch, but after the Go-native handler switch (`9101c9ca4d`) the C ABI is the only place a map has no shape — and a watch never crosses it. So the feature is deliberately **narrow**: string keys only (a JSON object key IS a string; the parser rejects any other key), allowed *only* as the full return type of a watch-only func (`@watch`, no `@method`), no nested maps, no nullable values (a missing key already expresses absence) — every other placement (struct field, param, plain return, dual-purpose watch, map-in-slice) fails the checker with the reason. **The C-provider surface skips such a func entirely** — header typedef/vtable/`GMI_*_CALLBACKS` macro, cgo call wrapper, dispatch func, FuncMeta — with an emitted comment ("JSON-only watch (map return) — no C ABI, Go providers only"), so a C provider that tries to serve it fails at compile, not silently. **`@watch_delta true`** moves pyvcp's hand-set `Delta: true` (per-connection top-level-key diff) into the IDL; rejected on non-`@watch` funcs and on binary `[]u8` watches (no JSON keys to diff), and emitted only when set, so **every existing IDL's generated output is byte-identical** (no other IDL uses maps or `@watch_delta` — grep-verified — and `TestMapWatchDeltaOmittedWhenUnset` pins it). Type mappings: Go `map[string]T` (via the single `goTypeForDispatch`, honoring the four-copies lesson — the TS/Py/client-go copies each got the case too, the client-go one unreachable-but-mapped), TS `Record<string, T>` incl. the bigint reviver walking map values, Python `dict[str, T]` with per-value `from_dict` in the generated WS client. **pyvcp converted:** `pyvcp.gmi` declares `watch_state() -> map[string]WidgetState` `@watch_delta true`; `pyvcpmodule` implements the generated `PyvcpWatchCallbacks` (`WatchState() (map[string]pyvcp.WidgetState, error)`, closed-panel guard intact) and registers via `RegisterPyvcpWatch` — the manual `WatchAPI` block is deleted. Documented in `src/gmi/idl/README.md` (Type System row, "Maps", "`@watch_delta`") and `VCP_MIGRATION.md`. **Verified:** parser tests (map parse, non-string-key rejection), 9 checker cases (7 confinement + 2 `@watch_delta`), `cgen/map_watch_test.go` asserting all four surfaces (Go provider iface + `Delta: true`, C header/dispatch skip *and* ordinary funcs intact, TS/Py types); `go build ./...`, vet, gofmt clean; pyvcpmodule + gmicompile `-race` green; `gomc-lint-full` 0; **`tests/pyvcp` runtests green** — the untouched hand-written Python client passing against the generated registration proves the wire contract byte-compatible. |
| 2026-07-22 | **pyvcp replaced by the widget-centric rewrite; reviewed, fixed and adopted — row `L R F U RC FP` ✅, and the design is now the template for the remaining UI ports** (`PYVCP_REVIEW_FINDINGS.md`; branch `pyvcp-restruct` = `production-readyness` + `0ce9fa4583` gmicompile nullable codegen + `f5f112f6ba` migration; the 14-commit `migrate-vcp` range cherry-picked, both conflicts benign, then squashed — verified byte-identical to the pre-squash tip). **Ruling:** the pin-centric port this phase was slated to review is deleted by the rewrite, and reviewing throwaway code is negative-value work — so the rewrite was **fix-and-migrated** instead: server-authoritative (server owns HAL pins, min/max, quantization, derived pins, timer accrual), event-in / delta-state-out over a delta-encoded WS watch, multi-client sync for free, untrusted-wire posture consistent with `@rest_export`. **Findings, all fixed:** CRITICAL — an already-open watch pushLoop outlives `Destroy()` and reads freed HAL pins (`UnregisterByInstance` alone can't stop it; now a `closed` flag set under `mu` before `comp.Exit()` + nil-safe accessors, HAL-backed teardown test); HIGH — `scan()` (all HAL-input logic) ran only while a UI client watched and rate-coupled to N clients (now a module-owned 100 ms ticker), client auto-name counter desync (the server increments per-type on *different* conditions — unconditional for scale/spinbox/dial/jogwheel, only-when-empty otherwise — and the client now matches, pinned by `TestAutoNameCountersMatchClient`), one raising callback permanently killed the Tk poll loop (triggered by `value:null` → `int(None)`); MED — min/max `0`-as-no-limit sentinel → `f64?` (the first nullable floats through the C dispatch generator, which is what exposed the codegen gaps — completed additively + round-trip test, slots under the closed gmicompile Tier-1 review), TIMER accrual moved server-side, meter/title/option/image widgets restored. **`tests/pyvcp` runtests then caught two pin-inventory regressions the review missed** — dial `param_pin` (Python pyvcp always creates it; the rewrite gated it) and `halparam=` silently dropped for scale/spinbox/dial — which is why the template makes a pin/behavior parity test against the original framework *mandatory* per migration. **Deferred with rationale:** watch snapshot/seq marker (a shared-infra wire change for every watch consumer; ~nil practical exposure on ordered TCP + reconnect-fresh-snapshot) and `map<K,V>` IDL support (`watch_state` stays hand-registered until then — must be resolved before the *second* widget-centric framework, see the cross-cutting item). **Caveat recorded honestly:** the review ran inside the fix-and-migrate session, not as a standalone adversarial pass; depth reached Tier-2 practice (goroutine-ownership CRITICAL found, untrusted-wire event bounds, every fix mutation-style validated by runtests or a regression test). 19 unit + HAL-backed tests `-race` clean (keep-alive `TestMain` + `hallibtest` link shim), `gomc-lint` 0, gofmt clean, `gomc-server` builds, `tests/pyvcp` green. Only human `S` remains. |
| 2026-07-22 | **A refused command is a 409, not a 500** — the status contract for the REST surface, after the previous commit made provider errors reachable at all. `writeDispatchError` defaulted every un-errno'd error to **500**, and the task layer's refusals ("must be in MDI mode", "can't issue MDI command when not homed") are plain `fmt.Errorf`, so a machine correctly *declining* a command reported itself as a broken controller. That is not cosmetic: a 500 invites a retry against a machine presumed sick and is what monitoring escalates on. Fixed by making the provider state the *kind* of failure and the transport choose the status — `apiserver.Fault` with `FaultState` → **409 Conflict** (the machine's state forbids it; nothing happened, re-sending unchanged fails the same way), `FaultNotReady` → **503** (module not started or stopped; may succeed shortly), `FaultNotFound` → **404**, `FaultInternal` (the zero value, so unclassified errors keep the conservative 500) → **500**. `internal/task` classifies in one place: `rcFor` wraps an unclassified refusal as `FaultState`, an already-classified error keeps its kind (`errNotReady` stays 503), and all 34 command methods now route through it rather than the two that did. **Third defect found in passing:** the errno→status map used a bare `switch err`, so any provider that wrapped its errno for context — the normal thing to do — fell through to 500 anyway; it now uses `errors.Is`. The layering is deliberate: the task layer says what happened, not what HTTP status it deserves. Contract documented in `src/gmi/idl/README.md` ("Failure reporting"), including the rule that an *accepted-and-faulted* command is not a transport error at all. runtests 241/241; Go suite, vet, gofmt, golangci-lint clean. **The remaining four classified the same day**, each decided on its own merits rather than swept: `inirest`'s "INI file not loaded" → **409**, because an INI-less launcher (halrun mode) is *permanent* for that process and a 503 would invite a retry that can never succeed; its parameter-file failures (unset / unresolvable / unreadable) → **404**, because all three are one answer to a client — there is no parameter file — and the reason, including a containment refusal, still travels in the message for the operator debugging their INI; `persist_sqlite`'s invalid handle → **404** (a namespace never opened or already released); `emccalib`'s "not in tunable list" → **404** (the tunable list is the allow-list, so a key not on it does not exist as a tunable). `persist_sqlite`'s namespace cap got a **new kind, `FaultCapacity` → 503**, deliberately not reusing `FaultNotReady`: that module is running and healthy, it is simply full, and conflating the two would make the name lie in logs — they can also diverge later if one wants `Retry-After`. Mutation-verified (disabling the classification fails all four mapping tests; removing the errno unwrap fails the wrapped-errno one). |
| 2026-07-22 | **Audited every path-containment test by mutation; one was vacuous** (`internal/haljson`). The inirest pass had turned up a test that passed for the wrong reason, so the same question was put to the whole tree the only way that answers it: disable `pathres.contains()` — the containment check itself — and see which tests notice. **Five packages did** (`pathres`, `halfile`, `inirest`, `launcher`, `emccalib`); one that claims to did not. haljson's `TestNewHaljsonModuleErrors` case "config escaping the roots" used `config=../../etc/passwd` from a `t.TempDir()`, which reaches nothing — so it failed on "not found", asserted only `err != nil`, and would have passed with containment deleted outright. It was an exact duplicate of the "unresolvable config" case on the line above it. Fixed the way the inirest one was: config root and escape target as known siblings under one parent, a target that is real **and parseable** (so a containment failure builds a working module and fails the test loudly), and an assertion on the reason. Re-ran the mutation: six packages now catch it. Also added the reason assertion to emccalib's `TestSaveIniOutsideRoot` — it was already sound, because it asserts the out-of-root file is unchanged rather than merely that an error came back, but "any error will do" would equally pass on a typo'd path. **The other half of the question — modules that resolve but then use the unvalidated input — came back clean:** all nine `pathres.Resolve` call sites (`adsmodule`, `classicladder` ×3, `haljson`, `mqttbridge`, `persist_sqlite`, `pyvcpmodule`, `task`, `tooltable`) shadow the input variable with the resolved path, which makes using the unchecked value impossible rather than merely unlikely, and `PATH_RESOLUTION_INVENTORY.md` has categories A/B/C all done. **The transferable rule:** a containment test proves nothing unless the escape target actually exists and is reachable by the path under test, *and* the test asserts why the refusal happened — otherwise "not found" masquerades as "refused". Independent `t.TempDir()` calls are not predictably siblings, which is what made both of these miss. |
| 2026-07-22 | **I-3 fixed in gmi — `string?` is genuinely nullable now, and that closed a live EtherCAT bug** (ruling + write-up in `NETWORK_MODULES_REVIEW_FINDINGS.md`). User ruling: fix the emitter rather than delete inirest's dead branch, because a construct the IDL offers but a provider cannot express is a gap in gmi. **The severity was decided by what the fix investigation found.** `ethercat.gmi`'s `EoeIpRequest` has six `string?` fields and the C provider is written to exactly the documented contract — `gmi_ethercat.c` is a run of `if (req->mac_address)`, `if (req->hostname)`, where NULL means "not supplied, leave the device alone". But the REST dispatch is Go and `GoToC` emitted `C.CString("")` for an absent field, so every one of those checks was **true for a field the caller never sent**. Five survived by luck (`sscanf`/`inet_pton` reject `""`, leaving `*_included` clear); `hostname` has no parse step, so it took the empty name and set `name_included = 1` — **an EoE IP request setting only `ip_address` also wiped the slave's hostname**. `master_index: u32?` in the same call was correctly a NULL pointer, which is the proof only strings were broken. **Root cause: four copies of one rule.** `serverGoGen.toGoType`, `goTypeForDispatch`, `clientGoGen.toGoType` and `constraintEmitter.goIsPointer` each independently decided whether a type maps to a Go pointer, and three carried a `t.Name != PrimString` exception — so `string?` was `*string` in the standalone Go client and a plain `string` everywhere a provider could see it. Fixed by making `goTypeForDispatch` the single mapping (the server copy delegates, `goIsPointer` agrees) plus the four converter directions that follow: `CToGo` keeps NULL as nil, `GoToC` leaves the pointer NULL for nil (struct fields *and* parameters), and the provider bridge hands nil to the implementation for a NULL argument. Writing the emission test caught a bug in that last one — the generated nil check tested the Go variable rather than the incoming C pointer, so a nullable param would always have arrived nil. **Two things fell out:** `[]string?` binds the `?` to the *element* (a slice of nullable strings — never what anyone meant, and identical code to `[]string` while nullable strings were demoted); all three uses meant "optional slice", which needs no marker, and the checker now rejects it. And `@notnull` on a `string?` had been rejected with the reason "(string? maps to a non-pointer Go string)" — the bug, quoted — so it is now allowed. **Kept as `string?` deliberately:** the 12 *parameters* (`pattern`, `level`, `type`, `namespace`, `kind`), where empty and absent really are the same; demoting them would cost the generated TS/Python clients their optional-argument affordance, and one uniform rule beats 11 saved nil-checks. Guidance on which to use is now in `src/gmi/idl/README.md`. **This changes the REST wire, unlike G-1/G-2, and that is the point:** a present-but-empty value encodes as `{"value":""}` instead of being indistinguishable from absent. `inirest.Query` needed no logic change — its `keyExists` branch simply became reachable. Five mutations on the emitters/checker all caught; EtherCAT regression test on the wire contract; halscope dispatch test that an omitted optional parameter reaches the provider as nil. build/vet/gofmt/golangci-lint (0 issues)/`-race` clean, whole Go suite green. Full runtests owed at the Phase-5 checkpoint. |
| 2026-07-22 | **inirest `U` closed — 57.5 → 100 %, and the last Phase-5 coverage row with it** (`NETWORK_MODULES_REVIEW_FINDINGS.md`, "inirest coverage pass"). The entire gap was `GetParameterFile` at **0 %** — the one method in the package that touches the disk, taking a path out of operator-supplied INI content and returning that file's *contents* over REST, with a `pathres` containment check as the only thing between `[RS274NGC]PARAMETER_FILE = ../../etc/shadow` and any REST caller. Now covered: containment refusal for an absolute *and* a relative escape, plus that an in-root traversal still resolves (so the check cannot degenerate into "reject anything with `..`"); namespace override and global fallback on both methods; and the three failures that must stay distinct rather than collapsing into an empty string — PARAMETER_FILE unset, file missing, file unreadable — because an empty parameter table is a plausible-looking answer. Five mutations, all caught. **Method note worth carrying:** the first containment test passed for the wrong reason — two independent `t.TempDir()` calls are not predictably siblings, so the traversal missed and the refusal was an ordinary "not found"; asserting only `err != nil` would have stayed green with containment deleted outright. Any path-containment test that does not assert *why* it was refused is suspect. **New finding I-3 (open, needs a ruling):** a `string?` result field cannot express "not found", because the two Go emitters disagree — `cgen/server_go.go` `toGoType` *explicitly* excludes strings from the nullable-pointer rule (`t.Name != ast.PrimString`) so the **provider** sees a plain `string` with `omitempty`, while `cgen/client_go.go` maps the same field to `*string`. So `ini.gmi`'s documented "null if not found" is unrepresentable server-side: `Query`'s absent-key branch produces `IniQueryResult{}` and its present-but-empty branch produces `{Value:""}`, both marshal to exactly `{"values":null}`, and the `keyExists` call distinguishing them is dead. `bool?` is `*bool` on both sides, which is why sibling field `all` works. The generated Python client is built for the documented contract (`Optional[str]`, `d.get("value", None)`) and can never receive the distinction. 24 `string?` fields across four IDLs; result *fields* are where it bites. Deliberately not fixed inside a coverage pass — aligning the emitters moves a generated Go type in every API that uses `string?`, the same blast radius that correctly routed G-1/G-2 through a ruling. Pinned meanwhile by a test asserting the *actual* wire behaviour. |
| 2026-07-22 | **emccalib `U` closed — 43.2 → 94.4 %, against real HAL pins** (`internal/emccalib/module_hal_test.go`). This row had been ◐ with a stated reason: `GetTunables`/`SaveIni` read live HAL pins through `halcmd.GetP`, so only the pure logic around them was covered. The network pass killed that reason — the keep-alive `TestMain` pattern runs a real in-process HAL inside the test binary — and emccalib is a bad module to leave uncovered on a technicality, because *all four* of its REST methods exist to move numbers between HAL pins and the operator's INI file, and one of them (`SaveIni`) rewrites that file in place. Before this, `GetTunables`, `SetPin`, `SaveIni` and `Revert` were at **0 %**: the entire public surface, including the fix for **E-1** — the pointer-into-an-appending-slice bug that made `Revert` restore the *startup* value forever, throwing away a tune the operator had already saved. The E-1 regression test now exercises that at the API level (tune → save → nudge → revert, four tunables to force the append growth that stranded the early entries), not just through the index. Also newly covered: provenance discovery over a real parsed INI (a tunable with no INI entry gets no source file, and must not derail the ones that have it); the deliberate soft-fails (a vanished pin still lists, with a zero live value, rather than blanking the whole calibration panel; `SaveIni` skips it rather than writing a parse-failure `0` where a feed limit was); the hard-fails (`SetPin`/`Revert` on an untunable key are refused, so the endpoint cannot write arbitrary pins; a `setp` error is not reported as success; a failed save does not advance the in-memory INI value, which would make the retry think there was nothing left to save); and the `pathres` containment check on the write path, including that a refused save drops no `.bak` beside the out-of-root file. **Six mutations, all caught** (drop the `iniValue` write-back; ignore the `pathres` refusal; report `iniValue` as the live value; swallow the `setp` error; skip the backup; drop provenance). One pre-existing test-harness bug fixed while there: the helpers registered the API under `t.Name()`, but registration is process-global and never unregistered, so the package could not run under `-count=2` at all — now uniquified, and green under `-race -count=3`. `calibreg` was already 100 %. vet/gofmt/golangci-lint clean. |
| 2026-07-22 | **REST address conflicts now fail fast — in the server and in runtests** (`bed454f529`). A port conflict was the most expensive failure mode this suite had: not silent, but loud in the wrong place. One `gomc-server` left behind by an earlier run made **23 tests fail**, each reporting only its own "server did not become ready" timeout with the real reason (`bind: address already in use`) buried in a per-test `server.log` — it reads exactly like a code regression, and was mistaken for one before this fix. gomc-server already exited 1 on the bind failure (that path was added deliberately after the same bug), but it bound **last**, inside the serve goroutine at the end of startup — so the conflict surfaced only after realtime was running, motmod was loaded, HAL threads were started and the interpreter was initialised: a fully started machine, torn straight back down, on hardware whose drives that startup may have energised. `createAPIServer` now opens the listener where it already runs (**before realtime**) and `startAPIServer` serves it; the same conflict is two log lines and exit 1 instead of ~60 lines of startup and a full teardown. Consequences handled: `stopAPIServer` closes a listener that was never served (`http.Server.Shutdown` only knows about listeners it is *serving*, so a startup that failed a later step would leak the socket — and the next start would hit "address already in use" caused by its own predecessor); between bind and serve the socket listens unaccepted, so an early client queues in the backlog rather than being refused; the **bound** address is logged, which is what a `:0` ephemeral configuration needs. `TestStopAPIServer_IdempotentAndConcurrent` moved to an ephemeral port — the bind is real now, and a unit test must not fail because the machine happens to be running LinuxCNC. **runtests** gained the matching up-front gate in the idiom of the existing `lsof` check: if the REST address is already listening, name the port, the holding pid and the remedy, and exit 2 before the first test (honours `GMC_REST_ADDR`, reuses the `lsof` that gate already guarantees). A preflight cannot see a server leaked by an *earlier test*, so `tests/gomc-scale.sh` (sourced by every driver) gained `gomc_bind_failure`, which reads the reason out of the log the server already wrote; wired into `gomc_wait_ready`'s two failure paths and filestream-driver's. Tests: two launcher regressions (the bind error is reported and installs no listener; a never-served listener is released — asserted by re-binding the address, not by trusting `Close`). runtests 241/241. |
| 2026-07-22 | **G-1/G-2 fixed — the storage APIs can report a failure (`@rc_error`).** A GMI call that returned its payload by value had nowhere to put an error, so `--client-cgo` ended in a literal `return result, nil` and, symmetrically, the Go-provider trampoline collapsed `err != nil` into a zeroed struct: a sqlite failure and a missing row were the same event to every consumer. **New IDL annotation `@rc_error`** (documented in `src/gmi/idl/README.md`) declares the shape the fix needs — the `i32` return is the status channel, an `out` parameter carries the payload — and is deliberately opt-in, because a plain `i32` return next to an `out` param is a *value* the provider supplies itself (canon's `get_tool_by_number` returns `-1` for "not found" that way, and had to keep doing so). **G-2 (the emitter blocker) fixed:** `cgen/dispatch_c.go` had been unmarshalling an `out` param *from the request* and discarding what the provider wrote; it now declares the C variable, passes a pointer, checks the rc, and marshals the filled-in payload. **Slice payloads needed a new capability** — a callee-allocated slice cannot be a caller-provided buffer, so `[]T out` now travels in an owning `<api>_<elem>_slice_t {data, len}` (provider mallocs, caller frees, the same rule as a slice return), which is what let `get_entries`/`get_namespaces`/`list_tools` convert too rather than staying blind. **`persist.gmi` + `tooltable.gmi` fully converted (all 12 methods).** **Both Go signatures are unchanged on purpose** — consumer *and* provider stay `GetEntry(h, k) (Entry, error)` — so no Go call site moved; the churn is in C, where the payload became a pointer argument. **The REST/WS wire format is unchanged too**, and that is now proven mechanically: the emitters marshal a `restView(fn)` (payload out-param → return, out-param dropped from the request), and the regenerated `tooltable_client.ts` is **byte-identical** to the pre-conversion one. That check found a real break in passing: without the view, the TS client emitted `listTools(tools: ToolEntry[]): Promise<number>` — the status as the result and the payload as a query parameter. **C consumers updated — five, not the two the finding listed:** `emc/iotask/ioControl_v2.c` *and* `ioControl.c` (both tool-change paths; new `tt_get_tool`/`tt_put_tool` wrappers, and the four previously *unchecked* `put_tool` calls in `load_tool` are now checked), `internal/task/interp_param_io_persist.c` (a failed restore is no longer an all-zero parameter set — it becomes "Unable to restore parameters"), `internal/ngcpreview/module.go` and `internal/launcher/retain.go` (a failed read no longer reads as "nothing stored", which would have overwritten the retained values on the next save). The last three were found by the compiler, not by the review. **Consequential semantic decision:** persist's `GetEntry` now returns the zero entry with a nil error for a missing row instead of an error — the status channel carries one bit, and folding "no such key" into it would make an unwritten key indistinguishable from a broken database (the exact confusion T-4 was about). "Absent" stays distinguishable from "present and empty": a stored row echoes its `Key` back. halscope's first start and tooltable's unstored tool already relied on that convention. **Fail-loud guards:** the checker rejects `@rc_error` without an `i32` return or an out param, together with `@returns_value`, with more than one out param on a REST route, a slice out param on a non-`@rc_error` func, and more than one slice out param. **Tests:** generator tests for the dispatch/bridge/client emission and the REST-view invariants (**2 mutation-verified**), six new checker rejection cases, an end-to-end `tooltable` test that breaks the backing store and asserts each method reports it *with the rc* (proving the failure crossed the C callback boundary), and a `persist` REST-dispatch test pinning the response bodies and that an invalid handle is an error rather than an empty 200. Full build, `go vet`, gofmt clean; `-race` green; full runtests round owed by the ruling — run. Out of scope by the ruling and unchanged: `emcio.GetStatus` and the `@returns_value` contracts. |
| 2026-07-22 | **Network half of Phase 5 CLOSED — N7 + N9 fixed, the `U`/`FP` tail with them, and two more real defects found writing the tests** (`b5810a1055`, `9d8abf45fd`; findings in `NETWORK_MODULES_REVIEW_FINDINGS.md`). **N7 (mqttbridge):** the liveness `publish-count` pin advanced even when the publish had errored or the client was disconnected-and-buffering, so a supervisor watching it could be misled. Fire-and-forget is *kept* — waiting on a QoS≥1 token would stall `publishLoop` — but the token is now peeked (`select` on `Done()` with a `default`), and what counts as failure is what is knowable without blocking: a disconnected client, or a token already completed with an error. On failure the count does not advance, a new `publish-error-count` pin does, the log is throttled to one line per streak, and the change shadow is **not** updated so the next tick retries the value instead of swallowing it. **N9 (apiserver):** user ruling — cap both axes, INI-configurable. `REST_MAX_CONNECTIONS` (256) and `REST_MAX_WS_CONNECTIONS` (64); two caps because a WebSocket is a hijacked HTTP connection holding an accept slot for its whole life, so one cap lets watch clients starve plain REST. The original write-up had argued for no cap ("risks breaking legitimate multi-client use"); the ruling was that a generous blast-radius limit does not, and that no cap is the wrong default for a controller. **Coverage:** halrest 0 → 87.1 %, mqttbridge 0 → 86.8 %, halscope 4.1 → 91.3 %, apiserver 45.6 → 96.2 %, all against a **real in-process HAL** rather than a mock (keep-alive `TestMain`; halscope also needs `halcmd.RtapiAppInit()`, which sets hal_lib's `rtapi_pid` and is what makes `hal_init_ex(..., COMPONENT_TYPE_REALTIME)` produce a component `hal_export_funct` accepts — without it every scope fails `EINVAL`). **Two defects fell out of writing them:** **N10** halrest's `GetStatus` compared the lock level against `"NONE"` while halcmd renders the unlocked state lower-case, so `RtLock` was true for every level — an HMI could never see HAL unlocked; **N11** the webapp SPA fallback rewrote the path to `index.html` and handed it to `http.FileServer`, which redirects anything ending in `index.html` back to `./` — an infinite loop, so every deep link into a bundled app died after ~10 redirects, unnoticed because the bare entry point worked. Both fixed and covered. Also pinned as deliberate rather than bugs: `watchItemsFactory` accepts an unresolvable name (a pin may appear when its module loads), and a *failed command* returns `CmdResult{Success:false}` while a failed *lookup* returns a Go error (200-with-error vs 404 — collapsing them would hide failures or turn "not found" into a 500). Phase-5 rows apiserver/halrest/mqttbridge/halscope → `U`/`FP` ✅. `internal/inirest` (57.5 %) is the one Phase-5 module the pass did not cover. |
| 2026-07-22 | **Phase-5 second half reviewed — the five never-reviewed modules are now `L R F RC FP` ✅** (`PHASE5_REVIEW_FINDINGS.md`). persist_sqlite / tooltable / emccalib+calibreg / halstream / halsampler+halstreamer, Tier-2 adversarial, under the network pass's untrusted-wire lens (all four service IDLs are `@rest_export`) plus a line-by-line diff of the two file-format parsers against the C they replace. **Three HIGH:** **E-1** emccalib's tunable index stored `&e.tunables[len-1]` taken inside the append loop, so pointers captured before a reallocation aliased an orphaned array — `Revert` kept restoring the process-start value however often the operator saved; **T-1** tooltable's `.tbl` parser checked only `T`/`P` and discarded every offset's parse error, so an unparsable field became `0.0` and the tool still imported (a zeroed tool-length offset is a tool driven into the work — the C rejects the whole line, and now so does this); **T-4** `GetTool` of an unstored tool failed with "unexpected end of JSON input" because its not-found branch matched an error string that cannot arrive. Also fixed: P-1/P-2 (REST-reachable `open` grew namespaces/fds/disk without bound; `delete_all`+`open` cycling grew the handle slice), T-2 (a lowercase `.tbl` imported as an empty table), T-3 (a transient read error replayed the legacy `.tbl` over edited offsets), T-5/T-6 (unsynchronised publish + nil client in the runtime-REST-load window), E-2..E-5 (use-after-unlock; a stale line number overwriting an unrelated INI key; saves destroying inline comments — the split rule now shared out of `pkg/inifile` as `SplitInlineComment`; hard-coded instance name), S-1..S-4 (a zero-pin `cfg:` header spun halsampler forever; unbounded `ReadRaw`; halstreamer not skipping `#` lines; duplicated `httpToWS`). Coverage persist_sqlite 10.3→86.0 %, tooltable 2.1→89.0 %, emccalib 9.1→43.2 %, halstream 100 %; T-1/T-2/P-1/P-2/E-3/E-4 mutation-verified. **New open cross-cutting finding G-1:** a GMI data-returning call *cannot* report failure — `--client-go` emits a literal `return result, nil` for struct-returning funcs because the C callback returns the struct by value with no `rc` out-param, leaving 23 client methods across 5 generated packages structurally unable to signal an error and every in-process consumer of `persist` blind to storage failures. Needs a ruling; same class as the RCS-error item. Scope corrected from an initial 23 to 13 after tracing the emitter (the rest are `@returns_value`, a deliberate contract); REST is unaffected. The fix mechanism already exists (`out` param + `i32` return, as `motstat.get_status` uses) and preserves the Go client signature, but is gated on **G-2** — the dispatch emitter treats an `out` param as an input and drops the filled-in value. build/vet/gofmt clean, `-race` green. Full runtests owed at the phase checkpoint (`tests/ws-stream`, `configs/sim/axis/multiinst`, any config with a legacy `.tbl`). |
| 2026-07-22 | **Phase-5 matrix reconciled before starting the phase.** Three bookkeeping corrections, no code: (1) **N6 marked closed** — it was only ever the *reachability* half of launcher L-3 ("halrest's REST load/unload makes the unlocked module-slice race remotely reachable"), and L-3's full locking fix landed 2026-07-21, so halrest owes nothing further; row `F` `—` → ✅, and both the findings doc's "Still open" line and the auth cross-cutting item ("now the highest-priority open Tier-1/L item") were still claiming it open. (2) **`internal/persist_sqlite`/`tooltable`/`emccalib`+`calibreg` had drifted out of the table body** into the prose paragraph beneath it, where they render as literal pipe-text — the three never-reviewed modules were the easiest rows in the document to miss. Moved back in. (3) **New row `internal/halstream`** (94/56, Tier 3): factored out of halsampler/halstreamer by the 2026-07-21 codegen-duplication audit and never added. Phase-5 LOC refreshed to 2026-07-22 (the rest of the matrix stays the 2026-07-11 snapshot). Net remaining Phase-5 work after this: N7/N9 + the `U`/`FP` tail on the network half (halrest and mqttbridge are at 0 % coverage, halscope 4.1 %, apiserver 45.6 %), and a full Tier-2 review of the five never-reviewed modules. |
| 2026-07-22 | **pkgreg F3 closed — dead lossy API removed.** `ReadFile`, `Registry.WriteFile` and `Registry.Remove` had no callers anywhere (modcompile uses only `ReadConfIn`/`Add`/`GenerateImports`/`Discover*`/`ParseBuildFlags`), and `WriteFile`'s round-trip **dropped the `@GOMOD:TAG@` build-flag markers and all comments** that `ReadConfIn` exists to interpret — a live trap, since the first caller wiring the two together would silently strip every conditional-build marker from `packages.conf` (optional modules vanish from the build, green compile). Deleted; `internal/` package, so no out-of-tree importer can exist (same disposition as pkg/hal H-2). The companion `hasInitFunc` note is closed no-change: the regex is already line-anchored, so a comment/string mention cannot match, and a `go/parser` dependency would have to define what an unparsable file means for discovery — rationale recorded at the regex. build/vet/test green. |
| 2026-07-22 | **Launcher `U`/`FP` closed — row now `L R F U RC FP` ✅, only human `S` open** (237 → 898 test lines). Added the runtime-unload path (stop → unregister REST **and** watch APIs → destroy → remove from the slice; bystander untouched; a second unload is `ENOENT`, not a double Stop), the `EBUSY` dependency guard plus the two records that must NOT block it (self-reference, consumer no longer loaded), the fault paths (a mid-loop `startGoModules` failure — the scenario the Stop-without-Start contract exists for — still stops+destroys every loaded module exactly once; `fail()` keeps the FIRST error for `Run`'s return and triggers shutdown once under concurrent callers), and the config/CLI surface (REST addr precedence env > INI > loopback default, the `REST_ORIGINS` parse incl. the empty → same-origin-only N1 default, `initHalibPath`, `setConfigEnv`, the halrun tokenizer + its `loadusr`/`waitusr` rejection). **Found while writing them — L-8 (nil-HAL SIGSEGV, FIXED):** both unload paths called `halcmd.FindCompID` unconditionally, which dereferences `hal_data` (NULL before the first `hal_init`), so a runtime unload before HAL is up **segfaults** instead of erroring; the hooks are wired into halrest at the top of `Run()` before HAL init, which is what makes it reachable, and only the REST server starting later has kept it latent. Now routed through `halCompID()` (returns 0 without HAL — no HAL also means no RT functions to remove). `cmodules.go`/`retain.go` stay unit-untested by design (need a real cmod `.so` + a running RT thread; covered by runtests). build/vet/gofmt green, `-race ×3` stable, lint 0. |
| 2026-07-22 | **Phase-3 `U` tail closed — `internal/daemon`, `cmd/gomc-server`, `internal/config` (rows → `U` ✅); `pkg/inifile` I-2 ruled keep-as-is (closed).** Writing the missing tests surfaced three defects. **daemon D-4 (real bug):** `SyslogHandler.WithAttrs` did `append(h.attrs, attrs...)` into the parent's spare capacity, so two loggers derived from the same parent via `slog.With` shared a backing array and the second overwrote the first one's attrs (mutation-verified — both records logged `who=beta`); `WithGroup` had the mirror defect, recording `groups` that `Handle` never applied, so attrs from different groups collided under bare keys. Both fixed; handler attrs now precede record attrs per the stdlib convention. **daemon D-1/D-2/D-3 + gomc-server F5 (pidfile ownership):** parent and child both wrote the pidfile (two writers → a child that fails and removes it gets it recreated by the parent's later write, naming a dead process); a second daemon silently overwrote a LIVE instance's pidfile, orphaning it while both fought over the same HAL shm and REST port; and `RemovePidFile` would delete a replacement instance's file. Now the parent is sole writer, `Daemonize` refuses with `ErrAlreadyRunning` on a live PID (stale/malformed still overwritten; EPERM counts as alive so a root-owned daemon is not read as dead), removal is ownership-checked, and `main.go` defers it so every exit path drops it (previously only a clean `Run()`). **config C-1 (dead `-X`):** `-ldflags -X pkg.Name=v` is a SILENT no-op for an unknown `Name`; the Submakefile injected `DefaultNmlFile`, which no Go code has ever declared (NML-era leftover). Removed + a drift guard that parses the Submakefile against `paths.go` (mutation-verified), plus the reverse direction (never-injected vars must document their empty-value fallback) and a check that every path var stays an uninitialised `string`. Coverage 0→365 / 0→159 / 37→223 test lines. **Also fixed en route (pre-existing, from the 2026-07-22 adsbridge test addition): `internal/adsbridge` had no keep-alive `TestMain`,** so its per-test HAL create/exit cycles dropped the component count to zero and re-init hit pkg/hal **H-4** — the whole-module `-race` run failed 4 accessor tests with `hal_init_ex … (code -22)` and hung outright once (464 s), while the package alone or the suite with `-p 1` always passed. Added the same keep-alive `TestMain` pkg/hal uses. build/vet/gofmt green, `-race` green, lint 0. |
| 2026-07-22 | **Launcher LOW-findings tail closed (Phase 3, Tier-1 hotspot #4) — row `F` ✅.** **L-4** resolved as a *contract*, not a guard: `stopGoModules`/`unloadGoModule` deliberately call `Stop()` without a started-flag, because a mid-loop `startGoModules` failure leaves later modules loaded-but-not-started and they still must be stopped. Audited all 17 in-tree `gomc.Module` implementations against it (7 no-op Stops; every live one already guards — halscope `saverStarted`, stress_gc `startedOK`, classicladder modbus `running`, milltask nil/running guards + constructor-started mcode worker, ADS nil-listener+`stopOnce`, mqtt constructor-made `stopCh`) and wrote the lifecycle contract onto `gomc.Module` (factory completeness, Stop-without-Start, at-most-once Stop, Stop-before-Destroy) with the launcher rationale on `stopGoModules`. **L-7 (new, found while closing L-4):** `doCleanup`'s `halComp == nil` branch re-ran `stopCModules`/`stopGoModules` after steps 2/2b already had — a **second `Stop()` on every module**, which panics `close of closed channel` for mqttbridge / milltask's mcode worker; unreachable today (loading happens after HAL init) but exactly the trap the contract forbids — branch now owes only the destroys. **L-5:** `apiServer` guarded by `apiMu` + `apiServerRef()`; `stopAPIServer` removes the server from the field under the lock and runs the 2 s `Shutdown` outside it (no double-shutdown, no torn read when a restart path is added). **L-6:** the signal watcher — duplicated in `Run` and `RunHalFile` — blocked on a bare `<-sigCh` forever, leaking a goroutine + `signal.Notify` registration per Launcher; one `watchSignals()` helper now also selects on `shutdownCh` and `signal.Stop`s on exit, and `doCleanup` closes `shutdownCh` so an error return / one-shot halrun releases it too; `retainSync` spins 5 ms then backs off to 500 µs sleeps instead of burning a CPU for the full 1 s timeout. 5 regression tests (`lifecycle_test.go`), **all four mutation-verified** (duplicate stop → panic; blocking watcher → 2 s timeout ×2; unsynchronised field → `-race` DATA RACE). build/vet green, `-race` green, `make gomc-lint-full` 0, `gomc-fmt-check` clean. Row → L R F RC ✅; `U`/`FP` ◐, `S` open. |
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
| 2026-07-21 | **Full runtests GREEN** after the Phase-4 parser-semantics changes (halparse HP-1..HP-4) and the corpus-wide GMI ABI regeneration (nullable-scalar pointers) — validates both against the whole 232-test suite + shipped-config corpus. The two "needs a full runtests round" caveats below are now discharged. |
| 2026-07-21 | **Phase 0 `L` ticked — milltask lint-clean.** `golangci-lint v2.12.2 run ./internal/task/...` under the full `gomc-lint-full` linter set (`gomc/.golangci.yml`, pinned tool + make cgo env) = **0 issues**. Clears the last blocking item for `internal/task`; row → L R F U RC FP ✅, only `S` (final human sign) remaining. |
| 2026-07-21 | **Phase 1 gmicompile parser/AST — deferred design bucket worked through with user + CLOSED.** Four items landed (each gated: 33 IDLs clean + regen byte-identical + tests): **F1** structural type-existence pass in check.go (unknown named-type / misspelled-primitive → `file:line` error; `5dc76a891d`); **F2/F3** duplicate-name rejection — strict shared type namespace (type/enum/callback/import mutually unique) + per-scope field/param/enum-member uniqueness, duplicate enum *values* still legal (`78d1e05676`); **H4** post-parse `reclassifyForwardRefs` so a callback/import used before declaration gets the correct TypeKind, order-independent (`b49038cf23`); **unterminated-string** now fails loud via a scanner error sink Parse merges (`fc9158fd9b`). Declined by user ruling: hex literals (decimal-by-design, already errors) and the two doubled diagnostics (F6/F7, deemed correct). 12 new tests. Only the four Tier-1 emission deferrals (G-M4/L1/L5/L7) remain — separate session. gmicompile row unchanged (R RC ✅, F/U ◐ now only for emission G-*). |
| 2026-07-21 | **Phase 1 gmicompile parser/AST reviewed (Tier 2) — 4 fail-loud fixes.** The front-end (`parser/scanner.go`+`parser.go`, `ast/ast.go`, `check/check.go`, ~1383 lines; the emission side was hotspot #2). Two independent AI reads, adjudicated against source through the right lens — build-time tooling on trusted IDL, so the real hazard is *silent-wrong-that-compiles*, not uncompilable output (which fails loud at `cc`). `check.go` validates only `@constraints` (logic correct); structural checks live in the parser or nowhere. Fixed 4 discarded-error/missing-default sites that silently built a wrong AST (matching the parser's own `parseConst`/`parseConstraints` fail-loud precedent): enum-value `Atoi` error discarded (non-int→0); fixed-array size discarded parse error + no range check (`[0]`/`[-1]`→len 0); func-annotation `switch` had no `default` (typo'd `@methdo` silently dropped the method); duplicate `const` silently overwrote the resolution map. 4 regression tests; **all 33 shipped IDLs still parse clean, full `make` GMI regen byte-identical**. Deferred as design decisions (manual bucket): structural check.go pass for unknown-type-refs (F1) + duplicate names (F2/F3) — currently fail loud at `cc`, not silent; single-pass forward-ref callback (H4); low robustness items. Row → R RC ✅ (both halves reviewed), F/U ◐ (deferred + emission G-M4/L1/L5/L7), FP —, only `S`. |
| 2026-07-21 | **Phase 1 generated/gmi/* boundary spot-checked (Tier 3) — faithful.** Sampled IDL→generated fidelity (the generator itself was deep-reviewed in hotspot #2, so this is a spot-check not a re-audit). kins (const/enum/9-field struct/array/byref + reserved-word escaping `switch`→`switch_`, wire names preserved), ini (`string?`→`const char*`, NULL=absent, not double-pointered), halcmd (`newthread` `fp: bool?`/`cpu_id: i32?`→`const bool*`/`const int32_t*` — the issue-#265 nullable-scalar ABI fix correctly reflected in the committed tree). Sound alongside the generator's byte-identical-regen guards + full runtests green. Row → R RC ✅, L/U/F/FP —, only `S`. |
| 2026-07-21 | **Phase 1 internal/gmi (kinstest) reviewed (Tier 2) — clean, no findings.** The 376 "non-test" lines are a cgo test harness (`helpers.go`): it `dlopen`s real kins cmod `.so`s, stubs the `cmod_env`, and drives forward/inverse/type/switchable through C function pointers. Not imported by any production package (grep-confirmed); lives in a `.go` (not `_test.go`) file only because cgo+`//export`+`dlopen` can't go in test files. Faithfully mirrors production — the `//export` register/get stubs route through the real `apiserver.DefaultRegistry`; per-test fresh registry avoids contamination. Only smells are benign run-to-exit harness leaks (calloc / dlopen handle never freed). `go test`/`-race`/vet/lint(0) green. Row → L R U RC ✅, F/FP —, only `S`. |
| 2026-07-21 | **Phase 1 pkg/gomc + pkg/cmodule reviewed (Tier 2).** `pkg/gomc` module + log-hook registries. **GO-1 (HJ-1 + N2 class, FIXED):** `OnLogError` had no unregister path and `NotifyLogError` ran hooks synchronously on the recover-less log-drain goroutine — in `doCleanup`, `destroyGoModules()` runs before the ring's final flush (drain goroutine still live during destroy), so milltask's hook kept firing `Task.operatorError` into a torn-down task during **normal shutdown**, and any hook panic would kill the process. Fix: `OnLogError` returns an idempotent unregister func (milltask chains it into `apiCleanup`→`Destroy`); `NotifyLogError` isolates each hook behind `recover()`+`slog.Error`. Tests: unregister-stops-delivery, one-of-many-order, panic-isolation. `pkg/cmodule` (cmod C-ABI headers) spot-checked clean — bounded `vsnprintf`, documented mirror-constants, deliberate cross-referenced `nonblocking`-macro duplication, no TODO/FIXME. build/vet/`-race`/gofmt/lint(0) green. Row → L R F U RC ✅, FP —, only `S`. |
| 2026-07-21 | **Phase 1 pkg/hal `U`/`FP` closed (→ ✅).** Added the three test gaps the hotspot-#1 review named: `Component` lifecycle (Ready/Exit, non-idempotent second `Ready`, exit-then-reuse-name), `LookupValue` across the pin/signal/not-found arms, and the **linked** round-trip for scalars **and** ports (net out-pin + in-pin to a shared signal → value written on the writer read on the reader; exercises the netted double-pointer follow that `Get`/`Set` rely on, and the framed port write/peek over a real buffer vs the unlinked drop path). Netting primitives (`hal_signal_new`/`hal_link`/`hal_port_alloc`) — which pkg/hal intentionally doesn't expose and cgo forbids in `_test.go` — housed in a new test-support pkg `internal/hallib/halnettest` (mirrors `hallibtest`). Coverage 191→444 test lines. build/vet/`-race`/gofmt/lint(0) green. pkg/hal row → L R F U RC FP ✅, only `S` remaining. |
| 2026-07-21 | **gmicompile: nullable scalar params now carried as pointers through the C ABI** (commit `4e1f2ac387`; the root fix for the issue-#265 class, user ruling: C-ABI pointers over the Go-native-dispatch alternative). A nullable `T?` scalar param was flattened to a plain C scalar in the callback FFI, so "absent" was lost — the dispatch zero-filled it and the bridge trampoline always took `&local`, so a Go provider always saw a non-nil pointer to a fabricated 0 (`newthread cpu`→0/rejected, `newthread fp`→false/non-FP, `addf position`→0/front-insert). Now transits as `const T *` (NULL=absent) across api.h typedef / `call_X` / dispatch marshaling (malloc+`_freeList`) / bridge trampoline (nil-preserving `*T`); strings excluded (already `char*`). The one C provider with such params (`src/hal/drivers/ethercat/gmi_ethercat.c` — `master_index` ×32, `sdo_upload size?`, `soe_read mem_size?`) updated to pointer signatures (`resolve_master` takes the pointer, NULL→master 0; callers unchanged); gomc-server + ethercat.so rebuild **0 C warnings**. CLI band-aids (HC-4 -1 sentinels) reverted to nil — `tests/newthread-runtime` now exercises the omitted→nil→NULL→impl path end-to-end and passes. cgen regression tests (`nullable_param_test.go`). generated/gmi/* is gitignored (regenerated at build). Validated by a full runtests round (all green). Belongs to gmicompile hotspot #2. |
| 2026-07-21 | **GitHub issue #265 (`newthread` at runtime) FIXED** (commit `b4c7ffb74a`; Phase-4 halcmd, HC-4). `halcmd newthread <name> <period>` (no cpu) against a running server failed `cpu=0 is not an isolated CPU (isolated: [])` on a no-isolcpus box, while `newthread` in a HAL file worked. Root cause: the `.hal` parser defaults cpu to -1 (auto), but the CLI left it nil, and a nil `i32?` is flattened to 0 across the cgo REST dispatch (`int32_t` ABI has no "absent"; halcmd registers as C callbacks so the cgo path runs, not the nil-preserving Go bridge) → the impl gets `&0` → cpu 0 is non-isolated → rejected. Fix: CLI defaults cpu to -1 and sends it explicitly; explicit `cpu=0` still correctly rejected. Reproduced + verified live; regression runtest `tests/newthread-runtime` (resident server + runtime newthread) passes `scripts/runtests`. **Follow-up flagged:** the nullable-scalar-through-cgo flattening is a gmicompile codegen limitation affecting any `T?` scalar the cgo dispatch handles (e.g. `addf` optional `position` → 0/insert-at-front vs append) — the general fix belongs to gmicompile (hotspot #2), regenerating all packages. |
| 2026-07-21 | **halparse HP-1..HP-4 FIXED after user ruling** (commit `7bf02a484e`). Ruling: fix HP-1/HP-2, match 2.9 for HP-3/HP-4. The tokenizer now follows 2.9's per-line order `strip_comments → replace_vars → tokenize` (new quote-aware `stripComments`; a `#` in a substituted INI/ENV value no longer truncates the line; refs inside comments are stripped before substitution — which is why HP-2's blast radius is small, 0 non-comment env refs in the shipped corpus). A missing INI/ENV var is now a hard parse error (2.9 replace_vars -5/-4) via a new `INILookup.Get` `found bool` (adapter derives it from `GetAll`; env via `os.LookupEnv`; present-but-empty still OK). Backslash is an ordinary char everywhere (dropped gomc's `\n \t \" \\` escapes). Continuation joins with no separator. Tests reworked (mockINI 3-return Get, new TestStripComments + end-to-end HP-1/HP-2 tests, literal-backslash + no-separator-continuation cases); build/vet/gofmt/lint(0) green. Validated by a full runtests round (all green); a `[SEC]KEY` that fails to resolve now errors, exactly 2.9's behavior. halparse row → F ✅. |
| 2026-07-21 | **ADS cluster reviewed (Phase 2, Tier 2 adversarial)** — `ADS_REVIEW_FINDINGS.md`. Net-new code, no 2.9 oracle; server binds `0.0.0.0:48898` with no protocol auth. Two independent refutation passes (remote-DoS, concurrency/lifecycle). **Headline: a remote unauthenticated client could crash/OOM the motion controller with a single ~28-byte packet** — all fixed: A1 SumWrite `uint32` overflow → slice panic; A2 unbounded `make` from client sub-request count (≈137 GB → OOM); A3 unbounded process-image read `length` (≈4 GB, incl. notification `sendLoop` re-OOM every 10 ms); A4 no `recover()` in any goroutine. Bounds + `recover()` added; regression tests `internal/ads/dos_test.go`. Robustness: A6 write deadlines, A10 accept backoff, A11 idempotent `Stop()`, A12 construction-error HAL-component leak. A5 (partial): closed accept/register race + stage-2 read honors `quit`, narrowing the known ADS2 shutdown-UAF (full free-barrier contract still open, decide with pkg/hal H1). Refuted (locking correct): notifyManager races, SymbolTable lock model incl. suspected re-entrant-RLock deadlock. Open: A5 contract, A7 (conn/sub caps — HMI-count decision), A8 (`[0..N]` array silently mis-laid-out — fix proposed, separate commit), A9 (0.0.0.0/no-auth → safety-boundary doc), A13/A14 (low). build/vet/gofmt clean, lint 0, `-race` green. Rows → ads/adsbridge/adsconfig/adsmodule L R RC ✅, F ◐. |
| 2026-07-21 | **gmicompile Tier-1 emission deferrals — G-M4 + G-L5 landed (2 of 4).** **G-M4** (`d7d3e7fe7f`): 64-bit ints cross the wire as JSON **strings** (protobuf3 convention) across Go/Python/TS clients — Go native `json:",string"` (response fields + POST/PUT/PATCH body params, through pointers/nil), Python int↔str at from_dict/to_dict/body seam, TS `bigint` + recursive per-type revivers (BigInt() over nested structs+slices) wired into REST returns/WS subscribe/WS command results. Two **fail-loud** gmicompile guards (fire on no current IDL): reject a 64-bit REST **path/query** param (encodeParams→bare number→JS truncation), and reject `--client-python` for an API whose 64-bit field is reachable only through a **nested** named type (from_dict doesn't recurse). `newthread(period_ns)`→bigint; webapp consumers convert at display boundary. Decided with user: full clean solution over doc-footnote, hard-fail the unsupportable shapes. **G-L5** (`7d8d51408f`): all C array bounds route through one `#define`-aware helper (`cArraySizeStr`; `serverGen.arraySizeStr` delegates) so header/cgo-bridge/dispatch/external-client agree (kins bridge now `joints[KINS_MAX_JOINTS]` not `[16]`) and an unresolved `ArrayLenName` can't emit `[0]`; Go bounds stay numeric. Regenerated cgo recompiles clean; per-target tests added. **Bonus:** `vue-tsc --force` sweep of all 6 webapps flushed out pre-existing halscope type errors masked by stale incremental cache (missing trigger fields, `preTrig` on wrong type, dead var) — fixed separately (`1926c82ca8`); all 6 webapps now type-check from cold. gmicompile row F/U ◐ **now only for G-L1 + G-L7** (both → fresh/RT-hardening session). |
| 2026-07-21 | **G-M4 regression caught + root-caused → ethercat CLI de-duplicated onto the generated client** (`69c6bea407`). `,string` broke `tests/ethercat/sim-cli`: `cmd/ethercat` carried a **hand-written duplicate** of every ethercat wire type (no `,string`), so `unmarshal DeviceStats.tx_count` failed against the now-string server. (The G-M4 "no external client" assumption missed in-repo **hand-written Go consumers**; halcmd never broke because it consumes its *generated* client — the model.) Fixed the class, not the tag: added an ethercat `--client-go` target (→ `generated/gmi/ethercatclient`, gitignored) and refactored `cmd/ethercat` onto it with qualified names, **deleting the ~35KB hand-written client**. Two `--client-go` generator fixes this required: a numeric REST path param was passed bare to `url.PathEscape` (wants a string, wouldn't compile) → now `fmt.Sprintf("%v",…)` first (halcmd's are strings, never hit); and an additive `New<X>ClientInstance(baseURL, instance)` ctor (default delegates) so the CLI keeps a configurable instance (`EC_INST`) — existing callers unaffected. Instance of the **codegen-duplication risk class** (a hand-written mirror silently drifts from the generated wire format); the durable fix is that consumers use the generated client. 2 generator tests; all generated clients + `cmd/*` rebuild clean; sim-cli green. Sweep confirmed ethercat was the only wire-facing hand-written 64-bit consumer. |
| 2026-07-21 | **Codegen-duplication audit (whole gomc) + 3 remediations.** Prompted by the ethercat regression, swept all of `cmd/`+`internal/` (3 parallel auditors) for hand-written code that replicates GMI-generated wire structs/clients/providers. **Conclusion: ethercat was the ONLY ethercat-class latent duplicate** — everything else either routes through generated code (providers implement generated command tables and return generated types; C `#include`s the generated `*_api.h`) or is bespoke with no GMI peer (`TaskMessage`/`posPoint`), or is a separate protocol (ADS/AMS binary) or the apiserver framework itself. Name-intersection false-positives dismissed (`McodeCall` has a Go channel field; `Entry`/`Section`/`Symbol` are cross-domain coincidences). Three non-break-risk items fixed: (1) `internal/halcmd` `*Info` structs — dropped **dead json tags** (they're CGO-backed internal types converted to generated `halcmdapi.*` at the halrest seam before marshaling; tags never serialized, but misleading) (`bed039a80d`); (2) **`internal/halstream`** — the HAL WS stream framing (`cfg:` header + 8-byte codec + f/b/u/s) was hand-coded in *both* halsampler and halstreamer; factored into one shared package (the C `hal_stream_common.h` stays authoritative; no `--stream-client` generator exists) (`df1adb7e3c`); (3) **`--client-go` mis-emitted a broken REST method for `@watch`-only funcs** (halcmd `watch_items` → empty-path GET returning `[]PinInfo`, matching no transport) — extracted the server's `isCommandFunc` into a shared `isRESTCommandFunc` and gated the Go client on it, so client/server agree on the REST-callable set (`8401338ecc`). All build/vet/gofmt clean; new tests; sim-cli/hal-stream/ws-stream green. Closes the risk-class-3 "hand-written where generated should be used" concern with evidence. |
| 2026-07-22 | **Safety boundary document drafted** — `SAFETY_BOUNDARY.md` (cross-cutting item checked off). Platform-general per the user ruling (gomc is a framework, no concrete machine control): core principle is that operator-safety functions must **not** rely on gomc/LinuxCNC and must be implemented in **certified hardware independent of the software**, sized to a per-machine hazard analysis (ISO 12100 → ISO 13849-1 / IEC 62061). Asserts per module (RT motion, iocontrol, EtherCAT/lcec + FSoE black-channel, ADS, HAL, task/interp, REST/WS, MQTT) that the software is non-safety-rated and not load-bearing for any safety function; documents the software E-stop as a control convenience wired in series with certified E-stop hardware (the `ioControl.c` UEO/EEST chain); and explicitly separates the unauthenticated-control-surface **security** boundary (ADS/REST now loopback-default) from the **safety** boundary. Machine-specific/policy items (integrator note, explicit FSoE posture, standards-per-market, manual linkage) left as tracked TODOs in the doc. Wired into the cross-cutting list + linked. |
| 2026-07-22 | **ADS cluster Phase-2 — all remaining findings resolved (user rulings); cluster closed but for human sign-off.** Follows the 2026-07-21 ADS review. **A5 (shutdown free-barrier):** with **pkg/hal H1** now fixed (component-liveness barrier → a straggler pin access after `Exit()` returns zero/`ErrComponentExited`, not freed-memory corruption), ADS `Stop()` became a **true `wg.Wait()` join, no silent cap** — sound because bounded (listener+all conns closed first, read/write deadlines throughout), so `Destroy()` can never race live pin access; `shutdownTimeout` const removed. **A7 (resource caps):** default **8 conns / 256 subs-per-conn**, overridable via `$max-connections`/`$max-subscriptions` in the `.conf`; over-cap conns closed, over-cap subscribes return `ErrDeviceNoMemory (0x070A)`. **A9 (exposure):** default `$bind` → **`127.0.0.1`** (remote exposure now opt-in via `$bind 0.0.0.0`); the "exposed port has no auth" statement stays a Safety-boundary-doc line item. **A13:** `writeProcessImageRange` RMW now under a full write lock (concurrent overlapping writes no longer lose updates). **A14:** name-handle map capped at 65536. Regression tests `internal/ads/hardening_test.go` (conn/sub caps, true-join shutdown, concurrent-RMW — the last verified to fail under the old `RLock`) + `internal/adsconfig/serverconf_test.go` (loopback default, cap parse/validate). build/vet/gofmt clean, pinned lint **0**, `go test -race` green. Rows `ads`/`adsbridge`/`adsconfig` → `F`/`U`/`FP` ✅; only human `S` remains across the cluster. |
| 2026-07-22 | **ADS `adsbridge` + `adsmodule` unit tests added → `U` ✅ (closes a doc/reality gap).** The 2026-07-22 ADS-cluster entry below prematurely marked `adsbridge U ✅` while the matrix still showed `☐` and only `TestParseTypeInfo` existed — the risk-bearing **accessor byte↔pin conversions** were untested and `adsmodule` had **no** `_test.go` at all. Added `internal/adsbridge/accessor_test.go` (round-trips for bit / u32 at 1·2·4-byte wire sizes / s32 sign-extension / REAL+LREAL IEEE-754 / STRING(n) read layout + the unlinked-port write-drop error, i.e. `newBitAccessor`/`newU32Accessor`/`newS32Accessor`/`newFloatAccessor`/`newStringAccessor` — endianness, sign, float bits, wire-size truncation) and `internal/adsmodule/module_test.go` (`parseArgs` table + `newADSModule` missing-config / bad-path error paths) with a `link_test.go` HAL-symbol shim. Both packages: `-race`, vet, gofmt, pinned lint 0 green. Matrix rows `adsbridge`/`adsmodule` → `U` ✅; only human `S` remains cluster-wide. |
| 2026-07-22 | **Full runtests GREEN — 241 run / 241 successful / 0 failed / 0 expected / 0 skipped** (`runtests.log`), validating the path-resolution migration end-to-end against the shipped-config corpus. This was the risky part: with hard-fail containment an absolute `HALFILE` outside configDir/HALLIB_PATH now errors, `LIB:` no longer falls back to `.`, and every module config path is resolved + containment-checked. **Zero path-resolver rejections anywhere in the log.** |
| 2026-07-22 | **Path resolution unified server-side — Phase-4 HC-2 + HF-1 closed** (`PATH_RESOLUTION_INVENTORY.md`; commits `4e8c2f326b`, `22d8554169`, `970df1fe16`, `7e8e5c02e4`, `01dd6e2273`). User ruling: a path in a module argument or config file is a **server-side path** (the REST client's cwd is not part of the protocol and is meaningless for a remote client) — **allow-if-contained, roots = configDir + HALLIB_PATH without `.`, hard fail**. `cmd/halcmd`'s `resolveArgPath` heuristic (absolutize any arg containing `.`/`/`) is **deleted** — it mangled positional values like `3.14` and `<abs.0>`; args now go over the wire verbatim (HC-2). New **`internal/pathres`** owns the single rule (Read/Write/Dir modes, `EvalSymlinks` containment); `halfile.resolvePath` is a thin wrapper; dropping `.` as a search root also closes **HF-1**. Two rules fell out of the implementation: a relative *write* target resolves under the base only (so `outfile=core.hal` cannot overwrite the system `core.hal`), and non-regular files are refused in both directions (opening a FIFO blocks forever while `loadModuleNamed` holds `modMu` across a whole load → would wedge every load/unload/shutdown). **C modules got the same rule**: new `pkg/cmodule/gomc_path.h` + `env->path->resolve(ctx, name, mode, &err)` (trailing `cmod_env_t` field — in-tree rebuild, out-of-tree modules ignoring it still work). Migrated **at the `fopen`, not at argument parsing** — the placement that makes **nested** paths safe (ethercat's `<initCmds filename=>` lives *inside* the XML named by `config=`): ethercat conf.c + conf_icmds.c, filestream `infile=`/`outfile=`, `hm2_modbus mbccbs=` (its "path is not absolute" warning deleted — relative paths are first class now), `mb2hal config=`, `xhc-hb04 I=`, `z_level_compensation` probe map; Go side haljson/mqttbridge/ads/pyvcp/classicladder/persist_sqlite/emccalib/tooltable/task-COMP_FILE/inirest (six hand-rolled "join the INI dir" copies collapsed into one call). **G-code is user data, not configuration** (ruling): program paths resolve against **PROGRAM_PREFIX + SUBROUTINE_PATH + share** — `task.ProgramOpen` had **no containment at all** and now shares one definition (`pathres.ProgramDirs`) with ngcpreview's `get_file`, whose local `collectAllowedDirs` is gone. Verified-no-change: `load_tool_table`/`tool_load_table` open no file (both iocontrol impls are no-ops), `set_parameter_file_name` is not REST-reachable (`canon.gmi` has no `@path`). Device nodes / hardware config strings (`spidev_path=`, `port=`, every `hm2_* config=`, `/dev`, `/proc`, `/sys`) are deliberately exempt — inventory category D. |
| 2026-07-22 | **nil-INI crash class fixed — 7 sites** (`4e8c2f326b`). The launcher never sets `l.ini` in halrun mode (`halrun -f`, `gomc-server -f`) and `pkg/inifile`'s methods deref the receiver immediately (`parser.go:221`), so every unguarded `ini.Get/GetAll/SourceFile/WithNamespace` on that path segfaults the controller. **Three are `//export`ed cgo callbacks** — `gomc_ini_get`, `gomc_ini_get_all`, `gomc_ini_source_file` — where the panic unwinds into a C caller and kills the process outright (any cmod reading an INI value in halrun mode). Guard now lives in three `iniX` helpers on `Launcher` (unit-testable; cgo is not allowed in `_test.go`); no-INI reads as "key not found" (NULL / count 0), `source_file` keeps its always-a-valid-string contract and returns `""`; `gomc_ini.h` documents both. Four Go module factories: **adsmodule** `config=` (the other three copies already checked), **persist_sqlite** dbpath, **tooltable** legacy `.tbl` import, **emccalib** INI provenance, **ngcpreview** namespace + allow-list (falls back to the share dir only — narrower, never wider); **milltask** is INI-driven throughout so it now rejects a nil INI with a clear load error. Found while tracing module-arg path resolution for the HC-2 ruling. Regression test per site; `emccalib` gained the `hallibtest` link shim so it has a test binary at all. **8th site closed 2026-07-22:** `validateDependencies` (launcher) also read `l.ini.GetAll` raw. Latent, not live — only `Run()` calls it and `RunHalFile()` does not — but it was the exact shape of this crash class, so it now reads through `iniGetAll` and a nil-INI regression test pins it (mutation-verified: the raw form SIGSEGVs). Folded into `launcher.go` at the same time; `validate.go` (a whole file for one 6-line rule) is deleted. |
| 2026-07-22 | **cmd/ethercat `U`/`FP` closed → row `L R F U RC FP` ✅, only `S` open.** Added `cmd/ethercat/output_test.go`: the command formatters + error paths run against an `httptest` server returning canned JSON (real generated REST client + real formatting/validation, stdout captured) — no live server needed. **Formatters (U):** `version`, `master` (active/idle → Operation/Idle), `slaves`, `domains`, `sdos`, and the previously-uncovered read group `reg_read`/`sii_read`/`foe_read`/`soe_read`/`upload` (`pdos`/`cstruct`/`xml` stay under `sim-cli`). **Fault paths (FP):** unreachable-server transport error, HTTP-5xx propagation, per-slave fetch error mid-iteration, in-band result errors (FoE/SoE + SDO abort), 9 arg-validation cases. Still un-unit-tested (minor, under the existing deferral): the `*_write` commands (deferred deep-review) + small display cmds (`graph`/`ip`/`eoe`/`crc`/`alias`/`config`/`data`). vet/gofmt clean, pinned lint 0, `-race ×3` stable. |
| 2026-07-22 | **CRITICAL: `thread_lock` leaked locked on cooperative task exit → hung controller on `delthread`; fixed.** Found by unskipping the HAL thread-lifecycle tests (see the previous entry). **Scope: every deployment without RT privileges** — `rtapi_initialize_app()` sets `do_thread_lock = 1` whenever `harden_rt()` fails, i.e. the `SCHED_OTHER` fallback path, which covers sim/dev setups and any machine whose RT limits are not configured. **Mechanism:** `task_wrapper()` (`uspace_rtapi_lib.c`) acquired `thread_lock` at task start and *never released it*, relying on `task_wait()` to drop it on seeing the cooperative-exit flag. `task_wait()` does that on its two `task_exit` checks — but the flag can be set in the window *after* it re-acquires the lock and checks it, and the task loop's own `while (!self->task_exit)` condition then ends the task **holding** the lock. The mutex is left locked with its owner gone; the next `newthread` blocks forever on the acquire in `task_wrapper`, so it never reaches its loop, never observes its own exit flag, and the `pthread_join` inside `hal_thread_delete` → `rtapi_task_delete` never returns. **Symptom: `delthread` (or any module unload that deletes a thread) hangs the process forever, once a thread has already been created and deleted.** Confirmed with a native backtrace: one thread in `__lll_lock_wait` on `thread_lock` at `task_wrapper`, another in `pthread_join` at `hal_lib.c:2145`. **Fix:** lock ownership is now explicit — `struct rtapi_task.holds_thread_lock` (`rtapi/rtapi_task.h`, additive) is set/cleared at every acquire/release, and `task_wrapper()` releases the lock after the task body iff the task still holds it, so it is dropped exactly once on whichever path the task exits. The RT (`SCHED_FIFO`) path is unaffected — `do_thread_lock` is 0 there. This is the sibling of the earlier `task_wait()` shutdown-deadlock fix (same mutex, different escape path); that one covered the task that exits *inside* `task_wait`, this one the task that exits *after* it returns. **Regression test:** `internal/halcmd.TestThreadCreateDeleteCycles` — create/delete cycles with the delete walked across the thread period so some land mid-sleep and some just as the task re-takes the lock; **mutation-verified, hangs 5/5 with the fix reverted**, passes `-race -count=3` with it. (Two incidental facts pinned by the same work: the cycle counter advances from thread *creation*, not from `start` — `start`/`stop` gate funct execution and the `Running` flag, which is what the launcher's unload synchronisation relies on; and a thread faster than the established base period is rejected with EINVAL.) Owned by `RT_HARDENING_CHECKLIST.md`, updated there. |
| 2026-07-22 | **Phase 4 `U` gaps closed — halcmd, halparse, haljson, modcompile + both CLIs; every Phase-4 row now carries `U` ✅ (Tier-3 `internal/hallib` stays n/a).** The unlock: a **live in-process HAL runs inside a test binary** (the keep-alive `TestMain` pattern from `pkg/hal`), so the command surface is now tested against real pins/signals instead of compile-time signature assertions. **internal/halcmd (9.5→67.7 %)** — new `ops_test.go`/`threads_test.go`: signal lifecycle per type incl. `gets` formatting, setp/getp/ptype with the output-pin refusal, linkps/linksp/net incl. the two-writers and type-mismatch rejections, alias/unalias resolution, every `list`/`show` selector (HC-3 fnmatch parity re-asserted), `save` to slice and to file, the full lock-level matrix + classic `status` rendering, `parseCPUList`. **internal/halparse (72→87 %, logged as HP-8)** — **the executor had zero effective coverage in *any* build**: `executor_test.go` is `//go:build !cgo`, but `link_test.go` pulled in the cgo-only HAL shim unconditionally, so the nocgo test binary never compiled — and it had silently rotted (a removed `LoadToken.Params` field, plus stale expectations for `status`/`debug`/`load`). Fixed the rot, tagged `link_test.go` `cgo` so the nocgo suite runs again, and replaced the two skipped `haltest` placeholders with a **real cgo executor suite**: a parsed HAL file applied to live HAL (net/setp/sets/alias/link forms incl. deprecated `linkpp`, lock/unlock, save-to-file), `file:line` on a failing command with execution stopping there, `load` refused outside the launcher, and `IterLoads` default/multi-instance naming. Also added parser coverage for `newthread` options and the arity checks, and fixed a stale `IterLoads` doc comment (`[a,b]` → the `<a,b>` syntax `parseLoad` actually accepts). **internal/haljson (18.6→93.1 %)** — the JSON↔HAL core was untested: pin export incl. array expansion, the read snapshot shape, the POST round-trip for every type (u32 above the int32 range), the input-pin write guard, partial/short/overlong/malformed payloads, the REST GET/POST dispatch, and the WS watch (delta-only ticks, per-connection shadows, per-type change detection). **One behaviour fix (logged as HJ-6)**: the watch's first tick returned the structured snapshot without priming the shadows (pre-set to an impossible value), so **every subscriber immediately got a second, redundant full send** as a flat delta — now primed before `buildJSON` (that order deliberately re-sends rather than drops on a concurrent change). Also covered the module factory end-to-end through `pathres` + registry wiring, and its load-time rejections. **internal/modcompile (59.8→86.1 % cross-package)** — the risk-class-3 guard: `corpus_test.go` runs **all 132 shipped `.comp` sources** through parse → C generation → man-page generation on every test run, asserting the ABI contract (`New()` + the instance hooks), balanced preprocessor guards, no surviving format placeholders, the do-not-edit banner, and man-page structure — plus a determinism check (no map-order dependency). This alone took `cgen` 58.8→95.2 % and gave `docgen` 95.5 % / `ast` 67-80 %. **cmd/halcmd (1.7→78.1 %)** — `cli_test.go` on the ethercat `httptest` pattern: the line tokenizer (quoting/comments), arrow stripping, every command's dispatch + endpoint + arity check, in-band `{"success":false}` surfacing, transport and 5xx propagation, `-f`/`source` stream handling with and without `-k`, and the whole tab-completion engine incl. the type-matched completers (a completion may not offer a pin HAL would refuse to link) and the assertion that the typed fragment is sent to the server as a glob. **cmd/modcompile** gained tests for its pure logic (the non-compiling `processFile` modes, the filename/component-name guard incl. the `-`→`_` allowance, CC/CXX resolution, `copyFile`/`dirMirror`) but stays ~10 %: the rest shells out to a C compiler or rewrites the source tree, and the build already regenerates+compiles every shipped `.comp`. **Deliberate residual, not papered over:** the process-lifetime entry points called only from the launcher's startup/shutdown sequence — see the refined per-function breakdown in the Phase-4 section (`RtapiAppInit`/`Cleanup` end the binary's HAL irrecoverably, `SetLogRing`/`ClearMsgHandler` retarget the global RTAPI message handler, the sysfs topology readers answer host properties; all are covered end-to-end by every runtests boot). `UnloadAll`, `SetExact` and the `LockDLHandle` nil guard were re-examined and **are** now unit-tested. (This entry originally also deferred the thread-lifecycle tests as "needs RT privileges" — a misdiagnosis, corrected the same day; see the `thread_lock` deadlock entry.) All six packages: `go build`+`go vet` clean, gofmt clean, pinned golangci-lint **0 issues**, `-race -count=3` stable, and **both cgo and nocgo test binaries build and pass** (the nocgo one for the first time in halparse/halcmd/haljson). |
| 2026-07-22 | **cmd/ethercat master-side follow-ups resolved (`0d480e665e`, `7737a682a7`).** Cosmetic diagnostics surfaced by the sim-CLI harness; driver verified working on 5+ machines, so all fixed at the GMI/CLI boundary with the **verified master core untouched**. **`version`:** the CLI decoded the ioctl ABI magic (a flat compat counter, 37 — not a packed version) as "0.0.37"; the GMI `ModuleInfo` now carries a real `version` string from the public `ecrt.h` RT-interface macros (`ECRT_VER_MAJOR.MINOR` → "1.6"), and the CLI prints both facts: `IgH EtherCAT master 1.6 (API Version 37)`. **`Phase: Idle` while active:** the in-process uspace master (`--enable-uspace-master --disable-kernel`) never runs the kernel-only `ec_master_enter_operation_phase()`, so it stays IDLE and signals "in use" via `active`; the CLI now reports **Operation when active**. **SDO dictionary — not a bug:** the master FSM auto-fetches it, gated on `EC_WAIT_SDO_DICT = 3 s` after a CoE slave reaches PREOP; a query inside that window sees `SdoCount = 0` but it fetches fine on a running machine — the verified FSM is left untouched. Both fixes covered by new `tests/ethercat/sim-cli` assertions (phase Operation while active; real version + `(API Version` label, not the ioctl magic); runtest passes. Hotspot #3 prose updated. cmd/ethercat row unchanged (`U`/`FP`/`S` ◐ — the deferred `reg/sii/foe/soe` deep-review + human sign-off still stand). |
| 2026-07-21 | **gmicompile Tier-1 emission — final 2 deferrals DONE (G-L1 + G-L7); module now closed but for human sign-off** (`505e87d19f`; docs `a6b598240c`). **G-L1** landed as an *additive capability*, not the RT-session deferral it was filed as: investigation confirmed there is **no RT-invoked `@callback`** today — the four real ones (`interp_ext` oword/remap ×3, `mcode_handler` handler) are task/worker-level and *must* stay blocking (`mcode_handler.handler` blocks on `abort_fd`), and everything RT-invoked (mot/tp/hm2_serial `@rt_safe`) rides on already-annotated `_fn` typedefs. So nothing was mis-typed — but since gomc is a general framework, `@rt_safe` on a `@callback` now stamps the `_cb` typedef `GOMC_API_NONBLOCKING` (mirrors `_fn`; default-false → existing callbacks byte-identical). **Out of the RT-hardening bucket** — the clang check only bites when a real RT `@callback` appears. This *unblocks* `RT_HARDENING_CHECKLIST.md` item 1b (type hm2_serial's opaque-ptr BSPI callback), now a driver-side task, not an emitter one. **G-L7** landed as **fail-loud** (Option B, user ruling): `--client-c` is a published feature with zero in-tree consumers and no test that silently dropped fields in both directions (receive inlined one level of primitive-scalar nesting; send serialized primitives only, with a `// would go here` TODO stub emitting empty arrays). Added `failf` + `default:` guards at every silent-drop site across all 5 emitters. **The sweep is the finding: of 16 `@rest_export` IDLs only 5 generate cleanly, 11 fail loud** (narrow scalars like u8, enum fields, non-string slices, depth-≥2, slice-of-struct) — the generator was producing broken clients for ~69% of the real REST surface. `--help`+README now document the supported subset. Full recursive parity (**G-L7/A**) stays deferred-until-a-real-C-consumer. Tests: `callback_rtsafe_test.go`, `client_failloud_test.go` (synthetic fail-loud + supported-shapes-succeed + real-IDL characterization). gmicompile row → **L R F U RC ✅**, FP — (code generator), only `S` (human sign-off) open. |
