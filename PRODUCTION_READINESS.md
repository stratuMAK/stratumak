# GOMC Production Readiness

Goal: prototype machines can be delivered to customers. Assumptions:

- Short access paths exist to debug/fix issues in the field (observability + deployment story required).
- All hard functional safety (protection of human life/health) is handled by **external certified hardware**.
  The software must not be silently load-bearing for any safety function — see [Safety boundary](#safety-boundary).

This document tracks the per-submodule verification pipeline. Pattern proven with milltask:
AI review vs. LinuxCNC 2.9 → findings doc → fix PRs → tests → sign-off
(see `MILLTASK_REVIEW_FINDINGS.md`).

---

## Immediate next steps

1. **Runtests against gomc** — DONE (2026-07-13; updated 2026-07-15 after the lifecycle sweep,
   the sync-I/O/stale-status un-xfail sweep, and the G64 blending-parity work): full suite green,
   232 run / 224 pass / 0 fail / 8 xfail / 0 skipped
   (10 obsolete module-loading over-limit xfails removed, user ruling — see the ledger §3b);
   `tests/DISPOSITION.md` is the authoritative ledger.
2. **CI gates** — DONE (2026-07-13; runtests dedup 2026-07-15): `ci.yml` `gomc` job = build +
   C-warning gate (owned paths, `scripts/check-gomc-cwarnings`) + `make gomc-check` (vet, tests,
   pinned golangci-lint v2.12.2 with a no-NEW-findings merge-base gate, fmt). The full runtests
   suite runs once per PR in `rip-and-test` (as the name says), which now uploads the
   failure-log artifacts; both jobs are intended required checks.
   `nightly-gomc.yml` = `gomc-test-race` + runtests against a race-built gomc-server.
   First `-race` sweep over the full module found+fixed a data race (ads notification test mock).
   **Lint burn-down (legacy baseline, `make gomc-lint-full`, 69 findings):** 50 errcheck
   (meaningful ones: unchecked `mc.Set*` motion-limit setup calls, `task.SetState/SetMode`,
   `reg.Register`) + 19 unused (dead code — incl. `task/guards.go` requireState/requireMode,
   which look like guards that SHOULD be called; review before deleting). Also: migrate
   `nhooyr.io/websocket` → `github.com/coder/websocket` (drop-in re-home; touches the generated
   go.mod template), then drop the SA1019 exclusion in `src/gomc/.golangci.yml`.
   Still open: branch protection on `gomc` (required checks: gomc + rip-and-test).
   Original plan for reference:
   - `go build ./...` + `go test -race ./...` in `src/gomc`
   - `go vet` + `golangci-lint` (incl. `staticcheck`, `unused`) — baseline first, then ratchet
   - gomc runtests subset from step 1
3. **Parity check for failing/fault paths** — the capture corpus only yielded 3 trustworthy
   oracles (lines, arcs, spindle — see `tests/milltask-parity/`). Aborts, estop, dwell-drain,
   feed-per-rev, tool-change and the sync/m66/dio programs have **no usable C oracle**.
   These need written-spec tests verified against the 2.9 C source
   (reference tree: `~/source/linuxcnc-2.9` old code), not capture conversion.

**Bug FIXED this pass (production-relevant): shutdown deadlock with ≥2 HAL threads.** Any config with `BASE_PERIOD>0` (base + servo thread — most stepper configs) hung forever on shutdown: `task_wait()` re-acquired `thread_lock` on the cooperative-exit path, so the first HAL task to be deleted exited holding it and the next task's `pthread_join` blocked. Fixed in `src/gomc/internal/hallib/uspace_rtapi_lib.c` (leave `thread_lock` released on cooperative exit). This was also the root cause of the runtests full-instance flakiness (hung shutdown → leaked gomc-server → shared-REST-port collision → stalled suite). Verified: lathe/abort-g64 now shut down ~0s; 0 leaked servers.

**Runtests progress (branch `reenable-runtests`):** Category C (standalone interp)
and the HAL `test.hal` bucket are re-enabled and green — interp 71 pass / 9
Python-skip / 1 xfail; HAL 30 pass / 0 fail / 10 skip. Infra added:
`gomc-server -f` one-shot + `-f --serve` resident HAL modes, `scripts/halrun`
shim, `tests/hal-stream-driver.sh`. Remaining: ~15 `halrun`-in-`test.sh`, the
halcompile/build tests, and the ~46 full-instance (Category D) tests (need the
Python NML→`src/gmi/python` REST port).

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
- **Operator messages lost — PRODUCTION-RELEVANT.** The emcerror WS watch destructively flushes queued messages (`PublishErrorDrain.Flush` in the generated drain) and `pushLoop` (apiserver/ws_handler.go) suppresses byte-identical consecutive payloads — a DEBUG/error message that repeats the previous one within a watch tick is silently destroyed, and with multiple subscribers each message reaches only one of them. Queue semantics need per-connection cursors over a retained seq'd buffer (the `WatchFuncMeta.Factory` hook fits). Test drivers mitigate with 0.3 s pacing + retries (tlo, toolchanger/m61); likely the real cause of interp/oword-mdi-sub-update's "(other)" xfail.
- **`motion-logger` interceptor — DONE (cmod built + 2 tests green).** Implemented as an **interceptor/proxy** cmod (`src/emc/motion-logger/motion_logger_cmod.c` → `cmod/motion-logger.so`): registers `motctl`/`motstat` under its own instance name (milltask's `[EMCMOT]EMCMOT=motion-logger`), looks up the real motmod by `mot_instance=`, logs + forwards every call (real motmod = real motion + real status; no faking). milltask picks it up via a new `[EMCMOT]MOTION_INSTANCE` INI fallback (module.go). Converted + passing via runtests: `tests/motion-logger/{basic,mountaindew}`. Remaining: `tests/interp/m98m99/12`, `tests/abort/*crazy-move` (timing-dependent), and `tests/motion-logger/startup-gcode-abort` (blocked on the STARTUP_CODE gap below). Still TODO (under human review, `GOMC_PORT_SPEC.md` steps 2-3): rewire `tests/milltask-parity` to the interceptor, then **delete the `#ifdef MILLTASK_PARITY_TRACE` `motcmd_trace()` hook from `src/emc/motion/command.c`** so production RT carries no test instrumentation. Known gomc-vs-classic stream diffs (for the parity review): gomc omits `JOG_ABORT` for non-existent joints and the trailing `SET_SPINDLESYNC`; decoded-motctl format differs from the classic raw dump.
- **FIXED: main-program `M99` now loops in task.** gomc had the `interp_set_loop_on_main_m99` binding (`interp.go:203`) but milltask never called it, so `M99` at main level ended the program instead of looping (classic sets it in `emctask.cc:461`). Added `interp.SetLoopOnMainM99(true)` to milltask `initInterpreter`. (unblocks tests/interp/m98m99/12)
- **Minor: gomc `rs274` standalone emits extra `ON_RESET()` canon calls** vs the classic dump (one after `SET_FEED_REFERENCE`, two at `PROGRAM_END`). Benign-looking (interp reset lifecycle) but breaks byte-exact `expected` comparison; re-baselined where it appears. Worth a look if canon-call parity matters. (tests/interp/m98m99/12)
- **FIXED (fault-path parity, PR #259): `ON_ABORT_COMMAND` now wired.** `interp on_abort` runs on every abort path (including `recoverSeqFault` for producer-less sequencer faults) with the classic reason enum as the numeric argument to a configured `[RS274NGC]ON_ABORT_COMMAND` (`internal/task/commands.go`). (tests/abort/on_abort_command-crazy-move remains gated on the `gmi.Stat` queue-depth gap below)
- **`rtapi_shmem_delete` not exported to cmods.** A cmod calling `rtapi_shmem_delete` fails to dlopen ("undefined symbol: rtapi_shmem_delete"), though `rtapi_shmem_new`/`rtapi_shmem_getptr` ARE exported (shmem allocates) and delete is used internally in hal_lib.c. Add it to the cmod symbol exports. (tests/rtapi-shmem — after the .comp was fixed to proper multi-instance)
- **gmicompile `--server-go` mis-types callback/ptr params.** For an API whose func takes a callback (e.g. `handler`) or `ptr` (e.g. `user_data`) argument, the generated `_bridge.go` //export trampoline types them as `C.int`, truncating pointers. This is why `mcode_handler` had to be given a hand-written Go provider (`internal/task/mcode_provider.go`) instead of a generated bridge, and why its codegen rule still uses only `--server-c`/`--server-meta`. Fix the generator, then switch the rule to `--server-go` and drop the hand-written provider.
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
- **`stepgen` module-param instance count** — `load stepgen <stepgen.0> step_type="2,2,2"` creates 1 instance, not 3; array module-param count doesn't drive instance count. (modparam.0)
- **`mux_generic` single-instance only** — rejects the classic multi-instance comma config (`mux-gen.NN`); errors `invalid character ',' in config string`. (mux, multiclick)
- **mb2hal debug output routing** — mb2hal INI-DEBUG dump goes to the server log, not a capturable stdout stream. (mb2hal.1a/2a)
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
| internal/task (milltask) | 12445/4839 | 1 | ☐ | ✅ | ✅ | ✅ | ✅ | ◐ | ◐ |

Milltask review closed and merged. Remaining: fault-path parity tests from
[Immediate next steps](#immediate-next-steps) §3, then sign-off.

### Phase 1 — foundation (bugs here multiply into everything else)

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| pkg/hal | 1174/54 | 1 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/gmicompile | 10755/2141 | 1 (emission logic) / 2 (rest) | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| generated/gmi/* boundary | n/a | 3 (spot-check vs IDL) | ☐ | ☐ | ☐ | — | ☐ | — | ☐ |
| internal/realtime | 80/43 | 1 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/gmi | 376/262 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| pkg/gomc, pkg/cmodule | 94/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |

### Phase 2 — field I/O (drives real iron; highest risk per untested line)

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| cmd/ethercat | 3867/0 | 1 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/ads | 1763/1700 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/adsbridge | 498/47 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/adsconfig | 1473/2988 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/adsmodule | 163/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |

### Phase 3 — supervision & startup (first thing a field tech touches)

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/launcher | 2599/237 | 1 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/daemon | 157/0 | 1 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| cmd/gomc-server | 266/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/config | 86/37 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/pkgreg | 353/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| pkg/inifile | 606/966 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |

### Phase 4 — HAL tooling

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/halcmd + cmd/halcmd | 3540+1932/364 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/halparse | 1769/2330 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/halfile | 343/400 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/haljson | 876/151 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/modcompile + cmd | 2909+1636/393 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/hallib | 30/0 | 3 | ☐ | ☐ | ☐ | — | ☐ | — | ☐ |

### Phase 5 — services & auxiliaries

| Module | LOC | Tier | L | R | F | U | RC | FP | S |
|---|---|---|---|---|---|---|---|---|---|
| internal/apiserver | 2174/2446 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/halrest | 659/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/inirest | 87/171 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/mqttbridge | 791/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| internal/halscope | 939/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
| cmd/halsampler, cmd/halstreamer | 146+174/0 | 2 | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ | ☐ |
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
| cmod/* (motion, tp, homing, components) | Inherited 2.9 C code — algorithm risk low; the **binding boundary** is covered in Phase 1 |
| panelui, tracking-test, linuxcnclcd, motion logger cmod host, classicladder UI, all UIs except axis, qtvcp/gladevcp | Not (fully) ported — tracked in `MISSING_FEATURES.md` |

---

## Tier 1 hotspots

Human review mandatory, in this order:

1. **pkg/hal** — the binding layer every realtime interaction crosses; 54 test lines.
   Focus: pin/signal lifecycle, type conversions, thread interaction, error propagation.
2. **gmicompile emission logic** (`internal/gmicompile/cgen`) — one wrong emission pattern
   replicates into 39 generated packages. Review generator + diff a sample of generated
   output against the IDL by hand. The parser/AST side is Tier 2.
3. **cmd/ethercat** — commands real drives, zero tests. Focus: state machine
   (INIT/PREOP/SAFEOP/OP transitions), error/timeout handling, watchdog behavior,
   behavior on slave loss/rejoin.
4. **internal/launcher + internal/daemon** — process supervision, startup/shutdown ordering,
   restart-after-crash. Focus: goroutine ownership, orphan handling, partial-startup failure.
5. **State machines & abort paths across modules** — wherever a Tier 2 AI review flags a
   state machine or abort/estop path, that section gets human eyes regardless of module tier.
6. **internal/realtime** — small, but sits on the RT boundary; verify no GC-managed
   allocation in cyclic paths.

---

## Cross-cutting work items

Not per-module; each needs an owner and a done-definition.

- [ ] **Safety boundary document** — list exactly which functions the external certified
  hardware covers (estop chain, limits, spindle stop, interlocks) and assert per module that
  the software is not load-bearing for any of them. <a name="safety-boundary"></a>
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
  cannot stall any cyclic path.
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
- [ ] **gomc-server dies with a Go runtime GC fault in the generated `persist` GMI client —
  PRODUCTION-RELEVANT (uncontrolled controller death), found 2026-07-17.** Not a hang and
  not an M-code bug: the process is *killed by the Go runtime*, after which every client
  poll gets `Connection refused` and the test driver blames its own 30s drain deadline —
  which is what made this look like the M-code wedge. Verbatim:

      runtime: bad pointer in frame
        github.com/sittner/linuxcnc/src/gomc/generated/gmi/persist.(*PersistClient).GetEntry
        at 0xc0004e10a0: 0x8
      fatal error: invalid pointer found on stack
      runtime.adjustpointers -> adjustframe -> copystack -> shrinkstack -> scanstack

  A stack slot the GC believes is a pointer holds `0x8` (below `minLegalPointer`), so the
  stack scan throws. `persist_entry_t` is `{const char *key; const char *value; int64_t
  updated;}` — cgo types those `char*` fields as Go pointers **and the GC scans them**, so
  any garbage the C side leaves in the by-value return struct is a fatal error, not a
  benign bad read. Hit during the tool-change path (`tooltable`/`ngc_vars` persist lookups
  — the last log line before the fault is `tool change complete`), so it is reachable from
  any config that changes tools, independent of user M-codes.
  Reproduces on `tests/mdi-queue/simple-queue-buster` roughly **1 run in 25** (2 in ~47).
  The M100 log just stops mid-run (e.g. at `P is 374` of 1001) with nothing unexpected
  after it — a clean decapitation, no partial line. **The evidence is in the test's
  `stderr` file, NOT in the runtests output**, which is why previous passes over this test
  never saw it.
  Ruled out as a consequence of the 2026-07-17 M-code fix: that diff touches only
  `internal/task/{mcode_handler,sequencer}.go` and adds no C pointer handling; this fault
  is a GC stack scan of a *generated persist client* frame on the tool-change path.
  Not yet root-caused. `entryGoToC`/`entryCToGo` and the zero-struct error path all look
  correct on inspection (`C.CString` memory is valid malloc'd C memory), and both ends are
  Go — provider `internal/persist_sqlite` (`module.go` `GetEntry`, registered via
  `persist.RegisterPersistAPI`), consumers `internal/tooltable` and `internal/halscope`
  via `persist.NewPersistClient` — so a struct-layout mismatch is ruled out.

  **Leading hypothesis (UNPROVEN — verify before acting on it): the by-value struct return
  hands C an sret pointer into the Go stack, and the callback into Go then moves that
  stack.** `persist_entry_t` is 24 bytes (two pointers + int64); on x86-64 SysV a >16-byte
  struct is returned through a hidden caller-allocated pointer, so cgo passes C the address
  of the Go-stack local `out` in `PersistClient.GetEntry`. That C function immediately
  calls back into Go (`persist_bridge_get_entry`), which can grow/move the goroutine stack
  — leaving the sret pointer dangling at the old location. Fits the evidence: the fault is
  thrown from `copystack`/`shrinkstack` with `GetEntry`'s frame live, it is intermittent
  (only when the stack actually moves during the callback), and the corrupted slot holds a
  small non-pointer value. If it holds, this is a **generator** bug affecting EVERY gmi API
  whose Go→C→Go call returns a >16-byte struct by value, not just persist — check the other
  generated clients before fixing this one by hand. Only 5 APIs have generated clients, but
  they hold 12 by-value struct returns; the ones big enough to go through sret (and so at
  risk) are `persist_entry_t` (24B — the one that crashes), `tooltable_tool_entry_t` and
  `emcio_io_status_t`. Small results (`persist_set_result_t` = `{bool}`, etc.) come back in
  registers and are safe — which predicts the fault concentrates on tool-table/status reads,
  matching where it was seen. Cheap first probe: make the client pass
  an out-param instead of returning by value, or force the callback path to not re-enter Go
  on the same goroutine, and see if the fault stops.

  Note en route: `persist_bridge_get_entry` builds `_retAllocs` ("caller owns returned
  data") and then **discards it** — the returned `CString`s are never freed by bridge or
  client, so every `GetEntry` leaks key+value. Separate bug, fix alongside.
  Repro: loop the test; on the first failure read `tests/mdi-queue/simple-queue-buster/
  stderr` and grep for `fatal error` — and copy it out immediately, a later green run
  overwrites it. `GODEBUG=cgocheck=2` / `-race` on gomc-server are the obvious next
  instruments.
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
