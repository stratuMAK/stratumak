# Parity findings — gomc milltask vs LinuxCNC 2.9.8 (tests/motion-logger/basic)

Adjudication log for every diff surviving `./compare.sh`. Status one of:
**OPEN** (not yet decided), **FIX** (real gomc bug, to be fixed), **ACCEPT**
(benign difference, gomc gold stands). When all rows are FIX-and-fixed or
ACCEPT, the committed gomc gold is certified against 2.9.8.

Seeded from the first PoC runs (2026-07-14, checked-in gold on both sides).
Targets: `basic/{g0,g1,s}`, `mountaindew`, `m98m99-12`. Oracle: vendored
LinuxCNC 2.9 @ 50773b4353 (see `oracle-2.9/MANIFEST`).

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
| 5 | m98m99-12 | **M99 endless-main-loop position divergence.** Across the main-program M99 loop 2.9 restarts each iteration from `y=0` (full triangle); gomc **persists** position (`y=-101.6`), so from iter 2 the sub's `G1 Y-4` is a zero-distance move, emitted degenerate (`ini_maxvel=16.933, acc=0`). | OPEN — **likely NOT a gomc bug** (re-framed 2026-07-14) | Re-investigated: the interp's M99-loop code is functionally IDENTICAL in both trees (`interp_convert.cc:4570` gomc / `:4580` 2.9 — same `loop_to_beginning`+finish+`INTERP_EXECUTE_FINISH`; `loop_to_beginning` only `fseek(0)`+`sequence_number=0`, never resets position). So the divergence is NOT in milltask/interp. Confirmed against LIVE 2.9 (its `out.motion-logger` == its gold — 2.9 genuinely resets Y). **Strong hypothesis: a motion-backend artifact.** 2.9's m98m99-12 uses the FAKE `motion-logger` stub (reports position 0); gomc uses the interceptor + REAL `motmod` (reports actual position). If the loop's `EXECUTE_FINISH` re-syncs the interp from external position, 2.9 re-syncs to 0 (stub) while gomc re-syncs to the real position → gomc is MORE correct. **Verify** before adjudicating: confirm the external-position re-sync on the M99 loop, or run gomc's program against a fake/zero-position motion and check it then matches 2.9. If confirmed, ACCEPT (gomc correct; the 2.9 oracle for this test is a stub artifact) — but the degenerate `acc=0` zero-move (finding #4 class) is a separate real wart worth suppressing. |
| 1 | basic/g1, mountaindew, m98m99-12 | gomc emits `SET_VEL`+`SET_ACC`+`SET_TERM_COND` before **every** feed move; 2.9 emits them once / only on change | OPEN | Verbosity, not geometry — the `SET_LINE` vel/acc values themselves are identical cross-tree (re-verified after the modal-units fix). Decide: is the per-move re-emission harmless (motion coalesces it) or should gomc suppress unchanged dynamics like 2.9? |
| 2 | basic/g0,g1,s, mountaindew, m98m99-12 | 2.9 emits a trailing `SET_SPINDLESYNC` before the final `SPINDLE_OFF`; gomc omits it | OPEN | Already noted in the milltask review (gomc omits trailing SET_SPINDLESYNC). Confirm whether motion needs the sync reset at program end. |
| 3 | basic/s | 2.9 emits redundant `SPINDLE_ON speed=0` commands (M3 S0 and a re-issue); gomc suppresses them | OPEN | gomc collapses no-op spindle commands. Likely a benign improvement — confirm no downstream consumer relies on the redundant edge, then ACCEPT. |
| 4 | mountaindew | gomc emits two leading zero-length rapids `SET_LINE x=0..w=0, motion_type=1, vel=0, ini_maxvel=0, acc=0` (move-to-current-position) that 2.9 does not | OPEN | Redundant no-op moves at program start. Harmless to the toolpath but extra motion queue traffic — decide whether the canon should suppress zero-distance moves. |

Findings #1–#4 are command-stream *shape* (extra/omitted commands) with matching
motion values. **#5 was re-investigated and is likely NOT a gomc bug** — a
fake-vs-real motion-backend artifact (2.9's stub reports position 0; gomc's real
motmod reports actual), with gomc the more-correct side; see its row for the
one verification step remaining. The only real gomc wart it exposes is the
degenerate `acc=0` zero-distance move (finding #4 class).

## Not yet certifiable

- `motion-logger/startup-gcode-abort` — gomc xfail, no gold
  (`RS274NGC_STARTUP_CODE` never executed). Nothing to compare until that gap is
  closed; then vendor it and add to `targets.sh`.
