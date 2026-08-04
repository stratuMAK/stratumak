# Parity findings — stratuMAK milltask vs LinuxCNC 2.9.8 (tests/motion-logger/basic)

Adjudication log for every diff surviving `./compare.sh`. Status one of:
**OPEN** (not yet decided), **FIX** (real stratuMAK bug, to be fixed), **ACCEPT**
(benign difference, stratuMAK gold stands). When all rows are FIX-and-fixed or
ACCEPT, the committed stratuMAK gold is certified against 2.9.8.

Seeded from the first PoC runs (2026-07-14, checked-in gold on both sides).
Targets: `basic/{g0,g1,s}`, `mountaindew`, `m98m99-12`. Oracle: vendored
LinuxCNC 2.9 @ 50773b4353 (see `oracle-2.9/MANIFEST`).

## ✅ CERTIFIED / FROZEN (2026-07-14)

Findings resolved: **#1 FIXED** (emit SET_VELOCITY/ACC/TERM the way 2.9 does —
per-move standalone dynamics fully eliminated, motion identical); **#4 FIXED**
(zero-distance-move guard — also erased #5's `acc=0` residual); **#2 FIXED**
(2026-07-15, part of the G95 velocity-sync port: SET_FEED_MODE(mode 0) now
calls StopSpeedFeedSynch like 2.9, emitting the trailing `SET_SPINDLESYNC`
at M2/M30 — golds re-baselined); **#3, #5 ACCEPT** (benign / stmak-better,
evidence recorded). No finding is a motion correctness defect. The compared
program segments now differ from 2.9 **only** by the accepted findings (#3
spindle-0 suppression, #5 M99-loop stub artifact). The committed stratuMAK
motion-logger golds (`basic`, `mountaindew`, `m98m99-12`) are **certified
against LinuxCNC 2.9.8**; the runtests self-regression golds guard drift.

**Re-certified 2026-07-24 after the mode-switch abort narrowing
(`8e36d676f6`).** That commit changed the bring-up/teardown stream — the
mode-switch abort no longer emits `SPINDLE_OFF` (2.9 `emcTaskAbort` parity;
the old golds encoded the pre-parity divergence) — and the three golds were
re-baselined accordingly. Harness re-run: identical picture, PARITY on
`basic/g0`, `basic/g1`, `mountaindew`, and the same two accepted divergences
(#3, #5) on `basic/s`, `m98m99-12` — no new findings, as expected: the
change lives entirely in the bring-up preamble the normalizer strips, and
every `SET_LINE` is byte-identical to the previously certified stream.
Re-baselining trap recorded while doing it: `out.motion-logger` gains the
SERVER-SHUTDOWN abort after the driver's in-run diff (test.sh kills the
server on exit), so a gold copied from the on-disk file post-run carries a
trailing `ABORT` the in-run comparison never sees — re-baseline from the
in-run `result.*` files or strip the post-mortem tail.

Ranked most-severe first.

## units (mm-everywhere)

stratuMAK converts every *linear* length/velocity/accel/jerk config value from the
INI's machine units to internal mm at load time (`src/stmak/UNITS_MM_CONSISTENCY_FIX.md`),
and emits the whole motion stream in mm; 2.9 emits it in machine units. The
oracle side is therefore normalized ×25.4 (inch configs) by `canonicalize.awk`
(`unit_factor`, per-target `units` column in `targets.sh`) — positions x..w and
the per-move dynamics `vel/ini_maxvel/acc` alike; angular a,b,c are degrees in
both trees and never scaled. After that the dynamics match **exactly**.

History — a misdiagnosis, kept for the record: an earlier revision of this
section claimed the dynamics were "not cross-tree comparable" because "2.9
blends mm displacements against inch/s limits" (basic/g1 move 1: 2.9
`ini_maxvel=1.66296` vs stratuMAK `2.49444`, ratio 1.5, reconcilable by no factor)
and stripped them from the diff. The real cause was a **stratuMAK bug**: the canon
hardcoded its startup modal units to mm, so the interpreter began unit-less
programs in G21 while 2.9 (whose INIT_CANON derives them from
[TRAJ]LINEAR_UNITS) began them in G20 — the two trees executed the corpus as
physically different moves, 25.4× apart, coincidentally printing the same
position numbers. 2.9's blend is unit-consistent (`FROM_EXT_LEN` on limits,
`toExtVel` on emission); both values above were "correct" answers to different
questions. With the stratuMAK modal-units fix, one plain ×25.4 reconciles positions
AND dynamics, and both trees pick the same limiting axis.

Caveat: for a PURE-angular move 2.9 emits its dynamics via `TO_EXT_ANG`
(factor 1) — the corpus has none, and the normalizer injects a loud marker
line if one ever appears rather than guessing (see canonicalize.awk).

| # | Where | Difference | Status | Notes |
|---|-------|-----------|--------|-------|
| 5 | m98m99-12 | **M99 endless-main-loop position divergence.** Across the main-program M99 loop 2.9 restarts each iteration from `y=0` (full triangle); stratuMAK **persists** position (`y=-101.6`), so from iter 2 the sub's `G1 Y-4` is a zero-distance move, emitted degenerate (`ini_maxvel=16.933, acc=0`). | **ACCEPT** — verified NOT a stratuMAK bug (2026-07-14) | VERIFIED as a fake-vs-real-motion oracle artifact; stratuMAK is the correct side. Chain: (a) the interp M99-loop code is identical in both trees (`interp_convert.cc:4570`/`:4580`; `loop_to_beginning` = `fseek(0)`+`sequence_number=0`, never resets position). (b) BOTH trees re-sync the interp from external position on continuation — 2.9 via `taskintf.cc:1600` (`stat->position = emcmotStatus.carte_pos_cmd`), stratuMAK via `sequencer.go:864` `interp.Synch()`. (c) 2.9's `motion-logger` STUB never updates `carte_pos_cmd` (it only sets `joint_status->pos_cmd`), so `GET_EXTERNAL_POSITION` returns **0** → 2.9's interp resets to `y=0` each loop. (d) stratuMAK's real `motmod` reports actual `carte_pos_cmd` → stratuMAK's interp syncs to the real position → `y` persists. So the divergence is entirely the 2.9 stub lying about position; the m98m99-12 2.9 gold is NOT a valid parity oracle for post-loop moves — cannot certify against it. The residual `acc=0` zero-distance move stratuMAK emits (because Y correctly persists) is real but folds into **finding #4** (suppress degenerate zero-length moves). |
| 1 | — (resolved) | stratuMAK emitted `SET_VEL`+`SET_ACC`+`SET_TERM_COND` before **every** feed move; 2.9 emits them once / on change | **✅ FIXED** (2026-07-14) | The motmod-cap worry was unfounded — stratuMAK links the *same* C motmod as 2.9, so replicating 2.9's stream is safe by construction. Three changes, all matching 2.9's emission architecture: (a) removed the per-move traj `SET_VELOCITY` (`SetMotionParamsCmd` no longer sets vel — the feed rides in `SET_LINE.vel`); (b) `SET_TERM_COND` now emitted from `SetMotionControlMode` on change (at the G61/G64 parse), cached on the `Canon` so it persists across programs; (c) removed `enqueueMotionParams` — `SET_ACC` is emitted once at startup like 2.9. Result: **zero** standalone `SET_VEL`/`SET_ACC`/`SET_TERM` in the program segments — g0/g1/s/mountaindew/m98m99-12 now match 2.9 exactly modulo the other findings. Motion verified identical (all `SET_LINE` unchanged); full suite clean. |
| 2 | basic/g0,g1,s, mountaindew, m98m99-12 | 2.9 emits a trailing `SET_SPINDLESYNC` before the final `SPINDLE_OFF`; stratuMAK omits it | **✅ FIXED** (2026-07-15) | Root cause was deeper than fidelity: stratuMAK's `SET_FEED_MODE` never called `StopSpeedFeedSynch` on mode 0 (2.9 emccanon.cc:522 does), and `SetFeedRate` omitted 2.9's G95 velocity-mode sync start (emccanon.cc:529). Both ported as part of the G95 velocity-sync fix; the M2/M30 feed-mode reset now emits the trailing `SET_SPINDLESYNC(0)` exactly like 2.9. Golds re-baselined (basic g0/g1/s, mountaindew, m98m99-12). |
| 3 | basic/s | 2.9 emits redundant `SPINDLE_ON speed=0` commands (M3 S0 and a re-issue); stratuMAK suppresses them | **ACCEPT** | stratuMAK collapses no-op spindle commands — a strict improvement; nothing downstream relies on the redundant edge. |
| 4 | mountaindew, m98m99-12 | stratuMAK emitted zero-length moves `SET_LINE ... vel=0, ini_maxvel=0, acc=0` that 2.9 does not; also #5's `acc=0` degenerate loop move | **FIXED** (2026-07-14) | Mirrored 2.9's `if(vel && acc)` guard: `StraightTraverse`/`StraightFeed` now skip a move when `acc==0` (zero distance). Verified gone from the parity diff for mountaindew and m98m99-12, and it also removed **#5's residual `acc=0` move**. Golds re-captured; motion-logger tests green. |

Findings #1–#4 are command-stream *shape* (extra/omitted commands) with matching
motion values. **#5 is VERIFIED NOT a stratuMAK bug** — a fake-vs-real motion-backend
artifact (2.9's stub never updates `carte_pos_cmd`, so its interp resets to 0 on
the M99 loop; stratuMAK's real motmod reports the actual position and correctly
persists); see its row. The only real stratuMAK wart it exposes is the degenerate
`acc=0` zero-distance move, folded into finding #4.

## Not cleanly certifiable

- `motion-logger/startup-gcode-abort` — now un-xfailed and passing with a stratuMAK
  gold (`RS274NGC_STARTUP_CODE` executes as of the startup-code commit), BUT not a
  clean parity target: 2.9 defers the startup move via `interp_list` and emits
  **0** SET_LINE, while stratuMAK dispatches it to motion at estop (rejected) and emits
  **1**. Architectural difference (canon→motion direct vs deferred interp_list),
  documented; leave it OUT of `targets.sh` rather than diff against an
  incomparable oracle.
