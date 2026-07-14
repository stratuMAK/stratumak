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

gomc now converts every *linear* length/velocity/accel/jerk config value from the
INI's machine units to internal mm at load time (`src/gomc/UNITS_MM_CONSISTENCY_FIX.md`).
The motion controller was already mm-internal for positions/feedback; this makes
the config consistent, so an inch machine no longer runs 25.4× slow or rejects
legal moves. The inch self-golds here were re-captured on the fixed binary.

Consequence for parity: the per-move dynamics `SET_LINE vel/ini_maxvel/acc` (and
the `SET_CIRCLE` equivalents) are **no longer cross-tree comparable** and are
stripped by `canonicalize.awk`. 2.9 keeps these limits in inch-based machine
units while gomc uses mm, and no single unit factor reconciles them: pure-linear
rapids differ ×25.4, G1 feeds not at all, and **mixed linear+angular moves
diverge non-uniformly** — 2.9 blends mm displacements against inch/s limits, which
mis-picks the limiting axis (e.g. basic/g1 `x1..w9 a4b5c6`: 2.9 reports the move
uvw-limited at `ini_maxvel=1.66296`; gomc reports it correctly angular-C-limited
at `2.49444`). gomc is physically correct here. Parity now certifies the portable
quantities (geometry, motion_type, move sequencing, spindle/IO); the full dynamics
remain regression-checked in the self golds (gomc-vs-gomc). This is why finding #1
below can no longer claim "identical vel/acc".

| # | Where | Difference | Status | Notes |
|---|-------|-----------|--------|-------|
| 5 | m98m99-12 | **Toolpath divergence in the M99 endless-main-program loop.** 2.9 replays the full triangle every iteration (returns to `x=1,y=0` at the top of each loop); gomc, from iteration 2 on, OMITS the `x=1,y=0` return leg and instead emits a spurious zero-distance `SET_LINE x=1,y=-4` | OPEN — likely **FIX** | Not a shape diff — the actual motion differs (`x=1,y=-4` where 2.9 has `x=1,y=0`), and the injected move is zero-distance/degenerate. Prime suspect: the main-M99 loop re-entry (`interp.SetLoopOnMainM99(true)`) skips the program's leading move on re-loop. Investigate before freeze. (The degenerate move's `acc=0` is now stripped from the diff — see "units" — but the position leg divergence is what matters.) |
| 1 | basic/g1, mountaindew, m98m99-12 | gomc emits `SET_VEL`+`SET_ACC`+`SET_TERM_COND` before **every** feed move; 2.9 emits them once / only on change | OPEN | Verbosity, not geometry. (The per-move `SET_LINE` dynamics are now stripped from the cross-tree diff — see "units" above — so this shows only the extra gomc `SET_VEL/ACC/TERM` lines.) Decide: is the per-move re-emission harmless (motion coalesces it) or should gomc suppress unchanged dynamics like 2.9? |
| 2 | basic/g0,g1,s, mountaindew, m98m99-12 | 2.9 emits a trailing `SET_SPINDLESYNC` before the final `SPINDLE_OFF`; gomc omits it | OPEN | Already noted in the milltask review (gomc omits trailing SET_SPINDLESYNC). Confirm whether motion needs the sync reset at program end. |
| 3 | basic/s | 2.9 emits redundant `SPINDLE_ON speed=0` commands (M3 S0 and a re-issue); gomc suppresses them | OPEN | gomc collapses no-op spindle commands. Likely a benign improvement — confirm no downstream consumer relies on the redundant edge, then ACCEPT. |
| 4 | mountaindew | gomc emits two leading zero-length rapids `SET_LINE x=0..w=0, motion_type=1` (move-to-current-position) that 2.9 does not | OPEN | Redundant no-op moves at program start (all-zero position; the `vel=0,ini_maxvel=0,acc=0` dynamics are now stripped from the diff — see "units"). Harmless to the toolpath but extra motion queue traffic — decide whether the canon should suppress zero-distance moves. |

Findings #1–#4 are command-stream *shape* (extra/omitted commands) with matching
motion values. **#5 is a genuine motion divergence** — the one to chase first.

## Not yet certifiable

- `motion-logger/startup-gcode-abort` — gomc xfail, no gold
  (`RS274NGC_STARTUP_CODE` never executed). Nothing to compare until that gap is
  closed; then vendor it and add to `targets.sh`.
