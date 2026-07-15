# Runtests disposition ledger (gomc)

One central, honest record of every test in `tests/` that does **not** run-and-pass
green against gomc, and every `expected` oracle re-baselined from classic-C to gomc output.
Purpose: a parity reviewer can see *what* diverged from LinuxCNC 2.9 and *why* without git
archaeology.

**Governing rule (user, 2026-07-12):** everything that exists must be tested. A test may be
**deleted** only when the capability it exercises is genuinely gone from the gomc architecture —
three such classes: (1) **TCL support for HAL**, (2) **Python support in the interpreter**, and
(3) **a mechanism the gomc model no longer has** (the rt/userspace split, NML transports,
`loadusr`/`rtapi_spawnv`, the default-channel-count concept, the `overrun` retry). Class (3) is
narrow: it covers a removed *mechanism*, not a feature that still exists by another means — each
class-(3) deletion is enumerated with its exact removed mechanism in §2c/§2d/§4a. Everything else
exists (the mechanism may have changed) → default **port** (adjust method) or **xfail** (adjusted
method hits a real gomc gap). Verify a replacement is truly absent before calling anything removed.
See `memory/runtests-only-two-removals`.

First run (2026-07-12, full suite): 216 run, 167 pass, 0 fail, 49 xfail, 37 skip (+1 XPASS: lathe).
*(historical — superseded)*

Current composition (2026-07-15, full run): **232 run / 224 pass / 0 fail / 8 xfail / 0 skipped**
(10 obsolete module-loading over-limit xfails removed by the gomc-side merge, see §3b).
The tool-change/lifecycle porting sweep (`../MILLTASK_LIFECYCLE_SWEEP.md`) un-xfailed 17 tests
(G43 Hn, the whole tool-tracking and RANDOM_TOOLCHANGER clusters, abort modal-state restore,
statbuffer-g5x-abort); earlier passes had already flipped startup-gcode-abort and the
on_abort/stop-button crazy-move pair. `scripts/runtests` now wipes each test's `db/` persistence
before running (interp params / tool table state leaked between runs). The stale-status /
sync-I/O cluster (`single-step`, `remap/remap-io`, `lathe`) was un-xfailed 2026-07-15
(see §3a-history).

---

## 1. Re-baselined `expected` (oracle: classic-C → gomc)

### 1a. Benign — format-only (gomc `halcmd`/loader output shape; no behavioral divergence)

| test | delta |
|---|---|
| alias.0 | one pin-name per line (was space-separated); dealias entry preserved |
| hal-backslash | new `halcmd show sig` column layout (drops "(linked to)" col) |
| loadrt.1 | pin/comp lines split one-per-line |
| loadrt.2 | line-split + gomc single-instance naming `streamer.0.*`→`streamer.*` |
| mb2hal/mb2hal.1b · mb2hal.2b | new `halcmd show pin` layout (drops owner col) |
| modparam.0 | space-separated line split one-per-line |
| pyvcp | new `halcmd show pin` layout; rows re-sorted by Dir then name |
| twopass · twopass-personality | drop 3 `twopass:invoked/found` announce lines + blanks; line-split (no TWOPASS in gomc) |
| save.0 | `halcmd save` re-baseline: `# component X (loaded by cmod)` headers; gomc naming; hex→decimal (round-trip verified, e252c17ed5) |

### 1b. Semantic — canon-call / motion-stream divergence (parity note in `../PRODUCTION_READINESS.md`)

| test | delta | parity flag |
|---|---|---|
| interp/m98m99/12…/expected | +3 `ON_RESET()` (1 after SET_FEED_REFERENCE, 2 after PROGRAM_END) | ⚠ rs274 extra ON_RESET |
| interp/m98m99/12…/expected.motion-logger | real-motmod re-baseline: drop preamble, add FS/FH/SS_ENABLE + per-move SET_VEL/ACC/TERM_COND | ⚠ motion-logger stream diffs |
| motion-logger/basic/expected.g0 | prepend COORD, drop trailing SET_SPINDLESYNC, id renumber | ⚠ motion-logger stream diffs |
| motion-logger/basic/expected.g1 | prepend COORD, add per-move SET_VEL/ACC/TERM_COND, drop SET_SPINDLESYNC | ⚠ motion-logger stream diffs |
| motion-logger/basic/expected.s | prepend COORD, drop leading zero-speed SPINDLE_ON + trailing SET_SPINDLESYNC | ⚠ motion-logger stream diffs |
| motion-logger/basic/expected.builtin-startup (A) | gomc-native, replaces deleted `.in`; real-motmod startup dump (adds FS/FH/SS_ENABLE, FEED_SCALE, RAPID_SCALE) | ⚠ motion-logger stream diffs |
| motion-logger/mountaindew/expected.motion-logger | real-motmod re-baseline: drop preamble, add FS/FH/SS_ENABLE + FEED_SCALE + per-move SET_VEL/ACC/TERM_COND | ⚠ motion-logger stream diffs |

### 1c. Deleted `expected` (test removed — cross-ref §4)

halcompile/command_line_flags (f3cd5a61c8), halcompile/personalities_mod/{4count_2pers,4names_2pers} (6bc8f606ff),
halcompile/userspace-count-names/*.expected ×6 (f3cd5a61c8), motion-logger/basic/expected.builtin-startup.in +
expected.reset (047c4962a5), motion-logger/startup-gcode-abort/expected.motion-logger.in (3e3fe9c93c).

---

## 2. Removals (genuine feature removals — tests DELETED)

These tests exercised features gomc removed for good (embedded Python interpreter,
TCL-for-HAL, custom kernel `rtapi_vsnprintf`). After confirming each (#3) against
the exact removed mechanism, the test dirs were **deleted** (2026-07-13) rather than
left as permanent skips — the features are not coming back, the per-test rationale
below is the record, and the classic tests remain in `linuxcnc-2.9` + git history if
ever needed. Result: **zero skipped tests remain** in the suite.

### 2a. DELETED — Python interpreter removed (all CONFIRMED #3, then removed)

interp/compile (`Python.h`), interp/plug/{absolute,filename,relative} (`canterp`),
interp/{pymove,python/error,python-self},
remap/fail/{body-py,canon_error}, remap/{predefined-named-params,remap-reentry}.
(remap/spindle, remap/fail/{prolog,epilog}, remap/oword-pycall, remap/introspect, remap/variable-injection, m70-m73/m73-flood-mist-restore.0 moved out — re-expressed; see §2e.)

**#3 (2026-07-13): each verified against the exact removed mechanism, not blanket "python gone"; per-test reason lives in each `skip` file. Summary:**

| test | exact removed mechanism it needs | why not re-expressible |
|---|---|---|
| interp/compile | external C++ program embedding the interp: `#include <Python.h>` + `$PYTHON_LIBS`, Python types in the public interp API (`struct inttab`) | Python.h/$PYTHON_LIBS not in the build; rs274ngc.hh no longer carries Python types. (Linking an external client is covered by build/ui via libgmi.) |
| interp/plug/{absolute,filename,relative} | `rs274 -p canterp.so` — the Python "canonical interpreter" plugin, resolved by absolute / bare-filename / relative path | canterp.so (a Python C-extension) isn't built; no non-Python interpreter plugin exists to exercise `-p`. |
| interp/python/error | `python3 -mcanon` + `import gcode` — drives the interp through the Python `gcode` binding to catch an arc error | the Python `gcode`/`emc` binding is removed (preview = Go ngcpreview/REST). Arc-radius-mismatch detection itself stays covered by standalone-interp arc tests. |
| interp/python-self | Python `self.param1 = x` on the interp object + `interpreter.this` alias, persisting across `;py,`/o-word calls | interp_ext handlers are stateless C callbacks; no interpreter-bound Python `self`/`this`. |
| interp/pymove | `emccanon.STRAIGHT_FEED/STRAIGHT_TRAVERSE` — direct canon **motion** emission from a handler | interp_ctx exposes only canon_enqueue_set_spindle_speed/_feed_rate + tool calls, no motion emit; motion from a handler is via an `ngc=` body. |
| remap/fail/body-py | `REMAP=M400 py=interp_error` — a pure Python remap **body** returning INTERP_ERROR | gomc rejects `py=`/`python=` at parse. Handler-fails-conveys-error is covered by remap/fail/{prolog,epilog}; only the py= body form is gone. |
| remap/fail/canon_error | prolog calls `emccanon.CANON_ERROR("…%s…")` (literal-%s safety of the canon error path) | no `canon_error` accessor on interp_ctx (C handlers use set_error, a different path). Prolog-fail path covered by remap/fail/prolog. |
| remap/predefined-named-params | Python-registered **predefined named params** (`_pi`, `_py_motion_mode`, read-only #<_name>) | interp_ext has no register-named-param; interp_ctx get/set only touch existing params. |
| remap/remap-reentry | Python **generator** handler bodies doing `self.execute("G0 …")` + repeated `yield INTERP_EXECUTE_FINISH` | interp_ctx has no execute-string accessor; interp_ext's single EXECUTE_FINISH can't reproduce the multi-yield coroutine + self.execute pattern; py= rejected at parse anyway. |

### 2b. DELETED — TCL-for-HAL removed

tclsh-extensions, tcllibpath-separator — tested the classic `tclsh`/`wish`
(LINUXCNC_EMCSH) HAL extension commands and the Tcl `TCLLIBPATH` separator
convention. gomc's UIs are REST/WS web apps; there is no wish-like emcsh and the
Tcl GUI stack is not ported. Deleted.

### 2c. RECLASSIFIED to must-test (was skip; the capability still exists)

| test | capability | adjusted method | class / status |
|---|---|---|---|
| hal-link-unlink | HAL link/unlink value preservation | resident server + `tristate_float` pins + halcmd | ✅ **PASS** — ported; both hal_lib invariants verified green |
| rtapi_printf.0 | custom `rtapi_vsnprintf` %f formatter | 🗑 **DELETED** — custom kernel-safe `rtapi_vsnprintf` removed (gomc uspace-only → libc); no meaningful re-expression. rtapi_print/rtapi_print_msg remain but formatting is libc's now. (Was a precise skip; removed 2026-07-13 as a genuine feature removal.) |
| build/header-sanity | headers compile standalone | ✅ **PASS** — found + **fixed** 2 header-packaging bugs: removed internal `axis.h` from public SRCHEADERS; installed `iniparse.h` (dep of the public `inifile.h`). All 61 headers now compile standalone. |
| build/ui | external program links the control API | ✅ **PASS** — re-expressed as a minimal gmi C client compiled/linked against `libgmi` (+`-lcurl -lcjson`, which libgmi fails to declare — noted in PRODUCTION_READINESS). |
| overrun | runtests overrun-*retry* workaround | ⛔ **DROPPED** — it tested `run_without_overruns` (re-run a `test.hal` up to 10× if it prints `overrun`), a flakiness-masking retry that was dormant in gomc (nothing emits `overrun`) and is a workaround, not behavior. Removed `run_without_overruns` from `runtests.in` (a `.hal` now runs once); deleted the test. Tests must be deterministic, not retried. |
| halmodule.0 | pin type/range coercion | ✅ **PASS** — ported: haljson creates s32/u32/float pins, gmi client POSTs values over REST (`haljson.writePin` range-coerces, matching the classic oracle line-for-line). Found+fixed a real gomc bug: haljson nil-INI deref under `-f` (mirrors the pyvcp fix). Binding-object introspection (is_pin/getitem) dropped — that's the removed userspace-Python-binding API. |
| pyhal | Python HAL binding (scalar + PORT) | ✅ **PASS** — scalar s32/u32/float/bit signal-propagation via haljson pins + net links + gmi-client REST. **PORT** read/write/peek omitted → deferred+documented (HAL_PORT exists in hal_lib core but not exposed via haljson/REST). |
| hal-stream | stream cfg validation (Python `hal.stream(...,"xx")` must reject) | ✅ **PASS** — re-expressed as the `filestream` cmod refusing `sample_cfg=xx` (`hal_stream_parse_cfg` rejects any non-f/b/u/s type); the load fails. |
| halmodule.1 | stream ring overrun/underrun/sampleno (Python `hal.stream`) | ✅ **PASS** — re-expressed via `filestream` HAL pins: replay 9 bfsu samples through a depth-10 ring clocked 12 ticks → round-trips the 9, `sample-num`=12, `underruns`=3 (empty clocks), `overruns`=0. The Python write-raises-on-full / read-returns-None API is the removed binding; the ring counters are the gomc equivalent. |
| tooledit | tool-table float fidelity | ✅ **PASS** — classic drove the Tk tooledit to round-trip a `.tbl`; gomc has no Tk tooledit and no `.tbl` writer, so import the 21-tool `.tbl` via a minimal `persist_sqlite`+`tooltable` server and assert every offset/diameter/pocket/comment survives the import→sqlite→REST round-trip exactly. (INI needs `[EMC]VERSION` or gomc treats it as a convert-me config.) |
| mb2hal/mb2hal.1a · mb2hal.2a | mb2hal cmod (loads/creates pins) | ✅ **skip→xfail** — reclassified: they now RUN (loadrt mb2hal via resident server) and xfail on the format gap, rather than skipping. gomc mb2hal logs progress to the server log via slog ("parse_transaction_section N OK") instead of the classic INI-DEBUG per-key `[SECTION] [KEY] [VALUE]` stdout dump, so the classic `expected` can't be reproduced. (Pin creation covered by 1b/2b.) |

**Item 7 (HAL streaming) — DONE.** The WS sampler/streamer decision was kept for live/GUI use (panelui/qtvcp/gladevcp later) but a new **`filestream`** cmod (`src/hal/components/filestream.c`, file-backed replay+capture, deterministic one-line-per-thread-cycle, byte-identical to halsampler) now backs the tests. The 26 streaming tests migrated off the WS driver to `filestream` + `tests/filestream-driver.sh` (`fs_run`), expected files unchanged; `tests/ws-stream` is the new dedicated WS-path coverage; `hal-stream`/`halmodule.1` re-enabled (above); `multiclick` and `mux` both **xfail→pass** (filestream's one-per-cycle pacing fixed the timing multiplicity that the classic streamer-FIFO-overflow golden encoded — both `xfail` files deleted). The 16 resident-server-only tests still use `hal-stream-driver.sh`'s `hal_start_server`.

### 2d. Ruled (user, 2026-07-12)

| test | mechanism | disposition |
|---|---|---|
| linuxcncrsh | telnet remote-shell + bulk-MDI g-code → canon output | ✅ **PASS** — migrated to REST: the rsh command stream (hello/enable/mode/estop/machine/mdi + 201 M100 MDI calls) is translated to gmi by `rsh2gmi.py`; M100 captured by `mcode_coord_log format=raw`. Output matches the classic `expected-gcode-output` exactly. |
| linuxcncrsh-tcp | same test, forced onto NML-over-TCP (`tcp.nml`) | ⛔ **REMOVED** — NML gone; REST has one transport, nothing distinct left |
| uspace/spawnv-root | userspace `rtapi_spawnv` (build+spawn a `.c` userspace binary as root) | ⛔ **REMOVED** — no userspace binaries / `rtapi_spawnv` |
| halrun-getopt-reset | `halrun` getopt-reset across repeated `loadusr` | ⛔ **REMOVED** — no `loadusr`; `halrun` is a shim |
| module-loading/{encoder,encoder_ratio,pid,siggen,sim_encoder}/num_chan=0 | `num_chan=0` = load with the *default* channel count (1 instance) | ⛔ **REMOVED** — explicit-names-only; the 1-instance case is already covered by the `count=1` test |

**Resolved — `mdi-while-queuebuster-waitflag` re-expressed non-Python (✅ PASS).** The classic `M400`
queue-buster was a Python remap doing `M66 E0 L0` + `yield INTERP_EXECUTE_FINISH`. Re-expressed as a
pure-NGC remap body (`REMAP=M400 … ngc=m400`, body does `M66 E0 L0`) — M66 (synchronised input read)
is itself a queue-buster, so the MDI-vs-queue-buster race the test guards is preserved. Python files
deleted; xfail removed. The 20-iteration crash check passes (verified the M400 body runs and reads
`#5399`).

### 2e. Re-expressed against the C interp_ext / mcode_handler mechanism (#2, was §2a Python skip)

The gomc replacement for embedded-Python interpreter extensions is the C interp_ext API
(register_oword / register_remap_prolog / register_remap_epilog) plus mcode_handler — all
now wired + tested (tests/interp-ext, tests/mcode-handler). Python remap/O-word tests whose
*capability* still exists are being re-expressed against it rather than skipped.

| test | classic mechanism | re-expression | status |
|---|---|---|---|
| interp/value-returned | Python O-word sub returning a value | NGC-only (endsub/return observable via `g0 x#<_value>` canon moves) | ✅ **PASS** |
| remap/spindle | `M500 py=m500` reads `self.speed[]`/`self.active_spindle` | `REMAP=M500 prolog=m500_prolog` C cmod (`test_spindle_remap.so`) reads per-spindle speed via interp_ctx `get_speed()`; full-instance MDI run, checkresult greps the prolog's logged speeds ([0,0,0]→[1000,0,0]→[1000,2000,0]) | ✅ **PASS** |
| remap/fail/prolog | Python prolog returns INTERP_ERROR; must abort + convey error, NGC body not run | `REMAP=M400 prolog=failingprolog` C cmod (`test_remap_fail.so`) `set_error()`+INTERP_EXT_ERROR; checkresult confirms prolog failed, error text conveyed, body (`o<mark_body>`) NOT run | ✅ **PASS** (found+fixed the pycall message-clobber bug below) |
| remap/fail/epilog | Python epilog returns INTERP_ERROR after the NGC body ran | `REMAP=M400 ngc=mustbecalled epilog=failingepilog` (same cmod); checkresult confirms body ran, epilog failed, error conveyed | ✅ **PASS** |
| remap/oword-pycall | Python O-word subs (o<square>, o<multiply>) w/ fixed+variable args and #<_value> return | C interp_ext O-words (cmod `test_oword_math.so`, register_oword); MDI feeds a prior call's #<_value> back as an arg to prove the return round-tripped. checkresult greps args+result: square(5)=25, multiply(25,2)=50, multiply(5,6,7)=210 | ✅ **PASS** |
| remap/introspect | Python O-word reads args + live interp state (feed/speed/named/INI/global params) | C interp_ext O-word (cmod `test_introspect.so`) via interp_ctx get_feed_rate/get_speed/get_param; checkresult greps args [1,2,3,3.14159], feed=200, rpm=3000, global=47.11, ini=3.14159. Python-binding-only bits (block param arrays, sub_context iteration, params.locals()/globals(), self.remaps) dropped — removed embedded-Python API | ✅ **PASS** |
| remap/variable-injection | Python prolog injects a var, NGC bumps it, epilog retrieves it; per-remap scoping | C interp_ext prolog/epilog (cmod `test_var_inject.so`) via interp_ctx set_param/get_param; M405/406/407 run singly + all-in-one-block. checkresult confirms each prolog injected #<fooNNN>=42, NGC bumped to 43, epilog retrieved 43, and no abort (sibling-remap vars not visible — local scoping intact) | ✅ **PASS** |
| interp-ext-finish | (net-new) a *C* handler returning INTERP_EXECUTE_FINISH — the `mdi-while-queuebuster` re-expression covers only the NGC-side queue-buster, not the C return path | `REMAP=M510 prolog=finish_prolog` C cmod (`test_interp_ext_finish.so`) returns INTERP_EXT_EXECUTE_FINISH on phase 0, INTERP_EXT_OK on the post-drain phase 1 (read via interp_ctx `get_phase()`); checkresult confirms the two-phase finish cycle fired exactly once each with no interp error/crash | ✅ **PASS** (found+fixed the MDI remap-finish spin/crash below) |
| m70-m73/m73-flood-mist-restore.0 | M73 auto-restore of M7/M8; verified with `;py,assert this.params[...]` | NGC-only (standalone rs274, like sibling m73autorestore.0): drop the py-asserts, surface restored state via `(debug, _mist=#<_mist> _flood=#<_flood>)`; MIST_ON/FLOOD_ON reappear in the canon trace after the sub returns | ✅ **PASS** |

**gomc bug fixed here (interp error conveyance):** a C interp_ext prolog/epilog/O-word handler that called `ctx->set_error()` and
returned INTERP_EXT_ERROR had its saved message clobbered with a generic "pycall(...) failed" / "handler not registered".
Root cause: gomc's `Interp::pycall` returned the handler's mapped status directly, tripping the caller's
`CHKS(status==INTERP_ERROR,...)` (and the O-word caller's not-registered ERS) *before* `handler_returned()` could convey it.
Classic Python left pycall's own status INTERP_OK and surfaced the handler's return via `handler_returned`. Fixed: pycall now
detects genuine not-registered via `ext_has_*` (clear error), and otherwise returns INTERP_OK with the handler's status in
`last_status`; the O-word caller conveys it through `handler_returned` (interp_python.cc, interp_o_word.cc).

**gomc/interp bug fixed here (MDI remap EXECUTE_FINISH hang + teardown crash):** a C remap prolog (or body) returning
INTERP_EXECUTE_FINISH from a top-level MDI M-code left the interpreter spinning at call_level 1 — the MDI never drained
(rsh2gmi 30s timeout, interp stuck in READING) and the lingering finish goroutine segfaulted against the torn-down interp at
shutdown. Root cause in the *shared* interpreter: `Interp::_execute` relinquishes on the remap handler's EXECUTE_FINISH at the
remap-kick `convert_control_functions` call *before* the `while(MDImode && call_level)` loop, without arming
`_setup.mdi_interrupt`. The continuation `execute(0)` then re-enters with MDImode=0 and never drives the remap's replacement
sub to completion. Fixed by arming `mdi_interrupt` on EXECUTE_FINISH there too, mirroring the loop's own handling
(rs274ngc_pre.cc). The o-word queue-buster path was unaffected (it arms mdi_interrupt inside the loop); classic milltask
shares the same `execute(0)` continuation, so this fixes a latent hang there as well.

**#2 COMPLETE for the re-expressible set.** Remaining §2a Python skips are genuine removals (no C interp_ext / interp_ctx
equivalent): remap/predefined-named-params (Python-computed predefined named params), remap/remap-reentry (python
`yield INTERP_EXECUTE_FINISH` generator body), interp/pymove (direct `emccanon` motion emission from a handler),
remap/fail/{body-py,canon_error}, interp/{compile,python-self,python/error}, interp/plug/* (canterp).

---

## 3. Xfails (8)

### 3a. Legit — runnable, fail on a documented gomc bug (`../PRODUCTION_READINESS.md`)

| bug | tests |
|---|---|
| jog/teleop + joint-mode + limit status | hard-limits, halui/jogging |
| gmi.Stat client field gaps | startup-state, mdi-queue-length |
| rtapi_shmem_delete not exported to cmods | rtapi-shmem |
| stepgen array module-param instance count | modparam.0 |
| mb2hal debug output routing | mb2hal/mb2hal.{1a,2a} |
| operator-message loss (emcerror watch: destructive flush + dedup), probable | interp/oword-mdi-sub-update |

### 3a-history. Fixed by the 2026-07-15 lifecycle sweep (xfail files removed, tests green)

G43 Hn (rs274ngc-startup, tlo) · RANDOM_TOOLCHANGER startup (io-startup/random/*,
t0/random-*, tool-info/random-*) · tool tracking M6 #5400 / M61 Q (t0/nonrandom,
tool-info/non-random, toolchanger/m61, toolchanger/reload-tool/*,
toolchanger/toolno-pocket-differ/*, mdi-queue/oword-queue-buster) · abort modal-state
restore + g5x desync (statbuffer-g5x-abort; abort/g64's modal checks) — see
`../MILLTASK_LIFECYCLE_SWEEP.md`. Earlier fixes: RS274NGC_STARTUP_CODE
(motion-logger/startup-gcode-abort), ON_ABORT_COMMAND + queue depth
(abort/{on_abort_command,stop-button}-crazy-move), streaming multiplicity
(mux, multiclick via filestream, §2c).

**2026-07-15, G64 blending parity (abort/g64):** all extent checks now match 2.9
exactly (G61 5.000 / G64P0.5 4.500 / G64 3.725 / G64Q6 0.000). Three stacked fixes
(see `../PRODUCTION_READINESS.md`): the 2.9 naive-CAM detector ported to the canon
(`canon_naivecam.go`, merged segments pin their own line/tag/status codes via
`Interp::active_modes`); arc blending had been silently OFF machine-wide (no
`EMCMOT_SETUP_ARC_BLENDS` sender existed — new IDL `setup_arc_blends`, pushed from
loadTraj with 2.9 defaults); and `pushDefaultTermCond` wiped the operator's modal
G64 P from the TP on every teardown/mode-switch (now re-asserts the canon's current
modal term cond). The mid-run P/Q readback checks pass because merged segments
report tag-decoded settings, not the readahead's never-executed `G64 P1 Q2`.

**2026-07-15, M67/M62 sync-I/O + stale-status cluster (single-step, remap/remap-io,
lathe):** the sync-I/O loss itself was the motctl single-slot send race, fixed
2026-07-14 (`104f633164`, concurrent senders could overwrite the shared command slot
before the RT side consumed it — the sequencer's SET_AOUT was silently dropped during
read-ahead). Verified with position-correlated pin sampling: synced outputs apply at
the correct segment activation in plain AUTO and in single-step. The remaining
failures were client-side: (1) `gmi.Stat.poll()` was a no-op over the 50 ms WS push
cache, so a driver polling right after a command could observe pre-command state
(single-step saw `interp_state==IDLE` right after AUTO_STEP and declared the program
finished; lathe's jog-overshoot flake was the same lag) — poll() now does a
synchronous fresh REST GET (classic `linuxcnc.stat.poll()` semantics, benefits every
ported driver); (2) drivers compared gomc-mm positions against inch goals
(single-step, lathe — fixed in the drivers); (3) lathe's continuous jog was killed
mid-travel by gomc's INTENDED 2 s jog dead-man watchdog (`task.go jogTimeout`,
runaway protection for disconnected clients — classic NML jogs had no such contract):
`linuxcnc_util.jog_axis` now refreshes the jog inside its wait loop. Note the old
lathe xfail's "jog overshoot" diagnosis was doubly stale: after mm-everywhere landed,
the test actually failed deterministically on units, and at the mm client boundary
`vel=5` is 5 mm/s (slow) so the watchdog, not overshoot, was the stopper. Validation:
remap-io 5/5, lathe 19/20 (single early flake never reproduced — investigate if it
recurs in CI), single-step 4/4, full suite green.

### 3b. Reclassified out of xfail (→ §2d, ruled)

module-loading/*/num_chan=0 → **removed** (default-channel-count concept gone, covered by `count=1`);
module-loading array-count (encoder/encoder_ratio/sim_encoder `9-names`+`num_chan=9`, pid/siggen `17-names`+`num_chan=17`) → **removed** (user ruling). These asserted that loading one instance past the classic `MAX_CHAN` static-array cap is *rejected* (`RESULT=1`, `NUM_PINS=0`). That cap was an artifact of fixed-size C arrays; the gomc ports are genuine multi-instance comps with no such array, so the over-limit load correctly *succeeds*. This is the same no-cap behaviour `or2` (never array-backed) already expected upstream (`count=17` → `RESULT=0`). The surviving `count=1`/`8`/`16` variants still cover multi-instance loading + atomic pin creation. **Correctly removed — do not restore.**
mdi-while-queuebuster-waitflag → **re-expressed non-Python, now PASS** (§2d).

---

## 4. Vanished dirs (12) — deleted on gomc

### 4a. Deleted — INTENTIONALLY removed (user ruling): rt/userspace-split model is gone

Deleted (f3cd5a61c8 / 6bc8f606ff). These test the classic `halcompile` **rt/userspace split** —
compiling a component as a separate userspace program, `rtapi_app` main, `RTAPI_MP_ARRAY_INT`
personality arrays, `--personalities` count-cycling, userspace `count=`/`names=` instancing. gomc has
**no** such split (one cmod does both realtime and userspace), so the features these address are gone
or handled differently in the single-cmod model. **Correctly removed — do not restore.**

| test | addressed a feature that is now… |
|---|---|
| halcompile/command_line_flags | halcompile CLI-flag surface of the removed model |
| halcompile/extralib | userspace-comp extra-lib linking — no rt/userspace split |
| halcompile/relative-header-user | the *userspace* variant of relative-header (the RT/single-cmod variant is covered + green) |
| halcompile/userspace | "compile a userspace comp" — no separate userspace build |
| halcompile/userspace-count-names | userspace `count=`/`names=` instancing — replaced by explicit instance names |
| halcompile/personalities_mod | personality arrays + `--personalities` cycling — no equivalent by design |

### 4b. To restore or disposition

| test | action |
|---|---|
| threads.0 · threads.1 | ✅ **PASS** — ported: `newthread` fast/slow + `threadtest` counter, captured by `filestream` (gomc has no userspace `halsampler`). threads.0 verifies the 10:1 period-ratio scheduling over 3500 samples; threads.1 verifies per-function `tmax` is nonzero (read via `show param` — gomc `getp` doesn't resolve RW params, noted in PRODUCTION_READINESS). |
| module-loading/rtapi-app-main-fails | ✅ **PASS** — ported to the cmod/`load` model: a comp fails its init via a failing `EXTRA_SETUP` (`-ERANGE`), and `load` correctly fails (`factory returned error code`). Classic used `option rtapi_app no` + custom `rtapi_app_main`. Note: gomc flattens the errno to `-1` (documented gap). |
| mdi-queue/simple-queue-buster | ✅ **PASS** — ported to REST/gmi (via `rsh2gmi.py`, M100 captured by `mcode_coord_log format=p`, a new P-only mode). Bulk MDI interleaving `t1 m6`/`t2 m6` tool-change queue-busters with `m100 p<i>`; all 1001 M100s logged in order. gomc's `mdi()` is synchronous so the queue-buster serialises rather than races, but the tool-change-vs-MDI sequencing is exercised. |
| mdi-queue/oword-queue-buster | ⚠ **xfail** — same port, but the queue-buster is an O-word sub that logs the *current tool* via `m100 p-#5400`; hits the documented M6/#5400 tool-tracking bug (reads `-0` not the tool number). Every other line matches. Flips green with the tool-tracking fix (§3a). |
| mqtt | ✅ **PASS** — ported to the `mqtt-bridge` module (`internal/mqttbridge`). Added a `dryrun` load arg + `publish-count` liveness pin (mirroring the classic `mqtt-publisher --dryrun`/`lastpublish`); test drives motion and asserts publish-count advances. Fixed 3 bridge bugs found doing so (nil-INI segfault, double-prefixed pin names, no offline test path) — see PRODUCTION_READINESS. |

---

## 5. Orphaned (no `test.sh` — invisible to the runner)

| test | action |
|---|---|
| trajectory-planner/circular-arcs | ✅ **PASS** — added an automated regression test (the classic dir was a developer profiling/tuning harness — operf/octave/interactive — with no `test.sh` in either branch). New `test.sh`/`checkresult`/`arc.ngc`/`arc.ini`/`capture.hal`/`run-arc.py`: run a full-circle G3 through the gomc TP, capture the commanded X/Y joint path every servo cycle with `filestream`, and assert every sample lies on the commanded circle (centre (0,10), r=10) within 0.05 mm and that the arc swept the whole circle. gomc traces it with 0.00000 mm deviation. The legacy profiling files are kept as dev tooling (see `README.gomc`). |
