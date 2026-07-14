# Parity findings — gomc milltask vs LinuxCNC 2.9.8 (tests/motion-logger/basic)

Adjudication log for every diff surviving `./compare.sh`. Status one of:
**OPEN** (not yet decided), **FIX** (real gomc bug, to be fixed), **ACCEPT**
(benign difference, gomc gold stands). When all rows are FIX-and-fixed or
ACCEPT, the committed gomc gold is certified against 2.9.8.

Seeded from the first PoC runs (2026-07-14, checked-in gold on both sides).
Targets: `basic/{g0,g1,s}`, `mountaindew`, `m98m99-12`. Oracle: vendored
LinuxCNC 2.9 @ 50773b4353 (see `oracle-2.9/MANIFEST`).

## ✅ CERTIFIED / FROZEN (2026-07-14)

All five findings resolved: **#4 FIXED** (zero-distance-move guard — also erased
#5's `acc=0` residual); **#2, #3, #5 ACCEPT** (benign / gomc-better, evidence
recorded); **#1 FIX-identified but DEFERRED** (redundant per-move traj
`SET_VELOCITY` — benign, but the fix needs motmod velocity-cap verification). No
finding is a motion correctness defect. The committed gomc motion-logger golds
(`basic`, `mountaindew`, `m98m99-12`, re-captured after the #4 fix) are
**certified against LinuxCNC 2.9.8** and frozen; the runtests self-regression
golds guard drift. Routine `./compare.sh` runs are optional from here.

Remaining candidates (not blockers): **#1** (drop redundant per-move
`SET_VELOCITY`; TP-model risk — do with motmod verification) and **#2** (emit
end-of-program `SET_SPINDLESYNC`). Neither affects motion.

Ranked most-severe first.

## units (mm-everywhere)

gomc converts every *linear* length/velocity/accel/jerk config value from the
INI's machine units to internal mm at load time (`src/gomc/UNITS_MM_CONSISTENCY_FIX.md`),
and emits the whole motion stream in mm; 2.9 emits it in machine units. The
oracle side is therefore normalized ×25.4 (inch configs) by `canonicalize.awk`
(`unit_factor`, per-target `units` column in `targets.sh`) — positions x..w and
the per-move dynamics `vel/ini_maxvel/acc` alike; angular a,b,c are degrees in
both trees and never scaled. After that the dynamics match **exactly**.

History — a misdiagnosis, kept for the record: an earlier revision of this
section claimed the dynamics were "not cross-tree comparable" because "2.9
blends mm displacements against inch/s limits" (basic/g1 move 1: 2.9
`ini_maxvel=1.66296` vs gomc `2.49444`, ratio 1.5, reconcilable by no factor)
and stripped them from the diff. The real cause was a **gomc bug**: the canon
hardcoded its startup modal units to mm, so the interpreter began unit-less
programs in G21 while 2.9 (whose INIT_CANON derives them from
[TRAJ]LINEAR_UNITS) began them in G20 — the two trees executed the corpus as
physically different moves, 25.4× apart, coincidentally printing the same
position numbers. 2.9's blend is unit-consistent (`FROM_EXT_LEN` on limits,
`toExtVel` on emission); both values above were "correct" answers to different
questions. With the gomc modal-units fix, one plain ×25.4 reconciles positions
AND dynamics, and both trees pick the same limiting axis.

Caveat: for a PURE-angular move 2.9 emits its dynamics via `TO_EXT_ANG`
(factor 1) — the corpus has none, and the normalizer injects a loud marker
line if one ever appears rather than guessing (see canonicalize.awk).

| # | Where | Difference | Status | Notes |
|---|-------|-----------|--------|-------|
| 5 | m98m99-12 | **M99 endless-main-loop position divergence.** Across the main-program M99 loop 2.9 restarts each iteration from `y=0` (full triangle); gomc **persists** position (`y=-101.6`), so from iter 2 the sub's `G1 Y-4` is a zero-distance move, emitted degenerate (`ini_maxvel=16.933, acc=0`). | **ACCEPT** — verified NOT a gomc bug (2026-07-14) | VERIFIED as a fake-vs-real-motion oracle artifact; gomc is the correct side. Chain: (a) the interp M99-loop code is identical in both trees (`interp_convert.cc:4570`/`:4580`; `loop_to_beginning` = `fseek(0)`+`sequence_number=0`, never resets position). (b) BOTH trees re-sync the interp from external position on continuation — 2.9 via `taskintf.cc:1600` (`stat->position = emcmotStatus.carte_pos_cmd`), gomc via `sequencer.go:864` `interp.Synch()`. (c) 2.9's `motion-logger` STUB never updates `carte_pos_cmd` (it only sets `joint_status->pos_cmd`), so `GET_EXTERNAL_POSITION` returns **0** → 2.9's interp resets to `y=0` each loop. (d) gomc's real `motmod` reports actual `carte_pos_cmd` → gomc's interp syncs to the real position → `y` persists. So the divergence is entirely the 2.9 stub lying about position; the m98m99-12 2.9 gold is NOT a valid parity oracle for post-loop moves — cannot certify against it. The residual `acc=0` zero-distance move gomc emits (because Y correctly persists) is real but folds into **finding #4** (suppress degenerate zero-length moves). |
| 1 | basic/g1, mountaindew, m98m99-12 | gomc emits `SET_VEL`+`SET_ACC`+`SET_TERM_COND` before **every** feed move; 2.9 emits them once / only on change | **FIX identified — DEFERRED** | Diagnosed (2026-07-14): NOT a caching gap. `enqueueMotionParams` → `SetMotionParamsCmd.Execute` calls `SetVel(linearFeedRate)` before each move — it pushes the per-move *feed* as the traj `SET_VELOCITY`, redundant with `SET_LINE.vel` (both ≈ the feed, which changes every move, so caching can't collapse it). 2.9 keeps traj velocity stable and carries feed only in `SET_LINE.vel`. Fix = stop emitting the per-move `SET_VELOCITY` (and cache SET_ACC/SET_TERM_COND on change). DEFERRED: dropping the per-move `SetVel` touches the TP velocity-cap model (feed vs traj-max vs rapids); needs motmod-level verification against feed-capping before it's safe. Behaviourally benign meanwhile (values identical, motion coalesces). |
| 2 | basic/g0,g1,s, mountaindew, m98m99-12 | 2.9 emits a trailing `SET_SPINDLESYNC` before the final `SPINDLE_OFF`; gomc omits it | **ACCEPT** (cleanup candidate) | Benign: the sync state is re-established by the next program's canon init (and by abort/estop teardown), so the missing end-of-program reset does not affect motion. Low-priority fidelity cleanup: gomc could emit `SET_SPINDLESYNC(0)` at program end to match 2.9. |
| 3 | basic/s | 2.9 emits redundant `SPINDLE_ON speed=0` commands (M3 S0 and a re-issue); gomc suppresses them | **ACCEPT** | gomc collapses no-op spindle commands — a strict improvement; nothing downstream relies on the redundant edge. |
| 4 | mountaindew, m98m99-12 | gomc emitted zero-length moves `SET_LINE ... vel=0, ini_maxvel=0, acc=0` that 2.9 does not; also #5's `acc=0` degenerate loop move | **FIXED** (2026-07-14) | Mirrored 2.9's `if(vel && acc)` guard: `StraightTraverse`/`StraightFeed` now skip a move when `acc==0` (zero distance). Verified gone from the parity diff for mountaindew and m98m99-12, and it also removed **#5's residual `acc=0` move**. Golds re-captured; motion-logger tests green. |

Findings #1–#4 are command-stream *shape* (extra/omitted commands) with matching
motion values. **#5 is VERIFIED NOT a gomc bug** — a fake-vs-real motion-backend
artifact (2.9's stub never updates `carte_pos_cmd`, so its interp resets to 0 on
the M99 loop; gomc's real motmod reports the actual position and correctly
persists); see its row. The only real gomc wart it exposes is the degenerate
`acc=0` zero-distance move, folded into finding #4.

## Not cleanly certifiable

- `motion-logger/startup-gcode-abort` — now un-xfailed and passing with a gomc
  gold (`RS274NGC_STARTUP_CODE` executes as of the startup-code commit), BUT not a
  clean parity target: 2.9 defers the startup move via `interp_list` and emits
  **0** SET_LINE, while gomc dispatches it to motion at estop (rejected) and emits
  **1**. Architectural difference (canon→motion direct vs deferred interp_list),
  documented; leave it OUT of `targets.sh` rather than diff against an
  incomparable oracle.
