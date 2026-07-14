# Parity findings — gomc milltask vs LinuxCNC 2.9.8 (tests/motion-logger/basic)

Adjudication log for every diff surviving `./compare.sh`. Status one of:
**OPEN** (not yet decided), **FIX** (real gomc bug, to be fixed), **ACCEPT**
(benign difference, gomc gold stands). When all rows are FIX-and-fixed or
ACCEPT, the committed gomc gold is certified against 2.9.8.

Seeded from the first PoC runs (2026-07-14, checked-in gold on both sides).
Targets: `basic/{g0,g1,s}`, `mountaindew`, `m98m99-12`. Oracle: vendored
LinuxCNC 2.9 @ 50773b4353 (see `oracle-2.9/MANIFEST`).

Ranked most-severe first.

| # | Where | Difference | Status | Notes |
|---|-------|-----------|--------|-------|
| 5 | m98m99-12 | **Toolpath divergence in the M99 endless-main-program loop.** 2.9 replays the full triangle every iteration (returns to `x=1,y=0` at the top of each loop); gomc, from iteration 2 on, OMITS the `x=1,y=0` return leg and instead emits a spurious zero-distance `SET_LINE x=1,y=-4 ... ini_maxvel=0.6667, acc=0` | OPEN — likely **FIX** | Not a shape diff — the actual motion differs, and the injected `acc=0` zero-length move is degenerate. Prime suspect: the main-M99 loop re-entry (`interp.SetLoopOnMainM99(true)`) skips the program's leading move on re-loop. Investigate before freeze. |
| 1 | basic/g1, mountaindew, m98m99-12 | gomc emits `SET_VEL`+`SET_ACC`+`SET_TERM_COND` before **every** feed move; 2.9 emits them once / only on change | OPEN | Verbosity, not geometry — the resulting `SET_LINE` vel/acc are identical. Decide: is the per-move re-emission harmless (motion coalesces it) or should gomc suppress unchanged dynamics like 2.9? |
| 2 | basic/g0,g1,s, mountaindew, m98m99-12 | 2.9 emits a trailing `SET_SPINDLESYNC` before the final `SPINDLE_OFF`; gomc omits it | OPEN | Already noted in the milltask review (gomc omits trailing SET_SPINDLESYNC). Confirm whether motion needs the sync reset at program end. |
| 3 | basic/s | 2.9 emits redundant `SPINDLE_ON speed=0` commands (M3 S0 and a re-issue); gomc suppresses them | OPEN | gomc collapses no-op spindle commands. Likely a benign improvement — confirm no downstream consumer relies on the redundant edge, then ACCEPT. |
| 4 | mountaindew | gomc emits two leading zero-length rapids `SET_LINE ... motion_type=1, vel=0, ini_maxvel=0, acc=0` (move-to-current-position) that 2.9 does not | OPEN | Redundant no-op moves at program start. Harmless to the toolpath but extra motion queue traffic — decide whether the canon should suppress zero-distance moves. |

Findings #1–#4 are command-stream *shape* (extra/omitted commands) with matching
motion values. **#5 is a genuine motion divergence** — the one to chase first.

## Not yet certifiable

- `motion-logger/startup-gcode-abort` — gomc xfail, no gold
  (`RS274NGC_STARTUP_CODE` never executed). Nothing to compare until that gap is
  closed; then vendor it and add to `targets.sh`.
