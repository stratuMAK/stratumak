# stratuMAK units consistency fix — spec / handoff

Status: **DONE** on branch `verify-motion-logger` (2026-07-14, two passes). All
linear length/velocity/accel/jerk config values are converted machine-units->mm
at load with a linear-only (axis-index / joint TYPE) guard; inch self-golds
re-captured; parity harness updated. A second pass fixed the canon's startup
modal units (G20 on inch machines — the first pass's "2.9 is unit-inconsistent"
parity adjudication was a misdiagnosis of this stratuMAK bug), the tool-length-offset
program-units conversion, and restored the full dynamics comparison in the
parity harness. See "Implementation notes" at the bottom, §5 subsection in
particular. Original brief preserved below.

Historical status: **partially done** (position limits, linear-only-unaware) on
branch `ci-trim`. The rest was a self-contained follow-up; this doc is the
complete cold-start brief so no re-derivation is needed.

---

## 1. The bug (root cause)

stratuMAK runs the motion controller (`motmod`, the same C module as 2.9) in
**millimetres internally**: the canon converts G-code targets to mm
(`CanonState.fromProg` × `unitScale`, `canon.go:139-159`) and emits mm to
`SetLine`; position feedback (`s.actual_position`) is mm; the whole gmi API is mm.

But length-dimensioned **config** values are handed to motion **raw from the INI
in machine units** (whatever `[TRAJ]LINEAR_UNITS` says), never converted to mm.
On an **inch** machine every such value is therefore **25.4× off**:

- **Position limits** (`config.go` `SetJointPositionLimits`/`SetAxisPositionLimits`)
  end up 25.4× too *tight* → `motmod` (`src/cnc/motion/command.c:216`,`:261`)
  **rejects legal moves**. This is what blocked `tests/abort/stop-button-crazy-move`
  (a `G21 G0 Z10` = 0.39 in move tripped a ±4 in Z limit).
- **Velocities / accelerations** end up 25.4× too *small* → motion on an inch
  machine runs ~25.4× too slow. Lower severity (no rejection, position-based
  tests still pass), but real for production hardware.

2.9 avoids all this by working in machine units end to end (targets *and* limits
in inch), so the numbers are self-consistent.

### Evidence (how it was proven)
Boundary-mapping via MDI on `tests/abort/stop-button-crazy-move` (inch config,
Z limit ±4 in). Before the partial fix the Z soft limit sat at **±4 mm**:
`G21 G0 Z3.9` ok, `Z4.1` rejected; `G20 G0 Z0.1` (2.54 mm) ok, `Z2` (50.8 mm)
rejected. The reproduction recipe (start `stmakd -r test.ini`, wait for the
`milltask` comp, drive with the `gmi` python client, home in `MODE_MANUAL`, then
MDI probe `G20`/`G21` Z moves and read `s.actual_position` / the error channel)
is the fastest way to re-verify.

---

## 2. Convention decision — go **mm-everywhere**

Two coherent end states; pick one before coding.

**A. mm-everywhere (RECOMMENDED).** Finish what the partial fix started: convert
every *linear* length/velocity/accel/jerk config value from machine units to mm
at load time. Localized to `config.go`. stratuMAK is already mm-internal to the bone
(positions, feedback, gmi API), so this flows with the grain.
- Cost: SET_LINE `vel`/`ini_maxvel`/`acc` output changes (×25.4) for inch
  configs → re-capture those golds; and the parity-vs-2.9 harness must
  unit-normalize (see §5).

**B. machine-units-everywhere (2.9-faithful, NOT recommended).** Make motmod run
in machine units like 2.9: apply `TO_EXT_LEN` (mm→machine) to move targets in the
canon, **revert** the position-limit scaling, and convert position feedback back
to mm at the stat boundary. Parity stays numerically clean and SET_LINE golds
don't churn — but it fights stratuMAK's mm-internal design across the whole emit +
feedback path. Much larger blast radius. Only choose this if a deliberate
decision is made to re-base stratuMAK on machine-units internally.

The rest of this spec assumes **A**.

---

## 3. Already done on `ci-trim` (and its gap)

`src/stmak/internal/task/config.go`:
- Added `func (t *Task) machineToMM(v float64) float64` = `v / t.linearUnits`
  (guards `linearUnits <= 0`). `linearUnits` is machine-units-per-mm: `1.0` for
  mm, `1/25.4` for inch (`parseLinearUnits`), so dividing gives mm.
- Applied it to joint position limits (`loadJoint`, ~`config.go:208-209`) and
  axis position limits (`loadAxis`, ~`config.go:359-360`; `loadAxis` now takes
  `t *Task`, call site updated).

**GAP to fix in the follow-up: this scaling is applied to ALL joints/axes,
including angular (A/B/C) ones.** The canon never unit-scales angular coordinates
(`canon.go:400`: "angular feed ... never unit-scaled"; `toAbsolute`/`UpdateEndPoint`
run `fromProg` on X/Y/Z/U/V/W only, never A/B/C). So an inch config with a rotary
axis currently gets its ±deg limits wrongly multiplied by 25.4. Harmless for the
current linear-only (trivkins XYZ) test configs, but must be corrected: **scale
linear axes/joints only** (see §4).

---

## 4. What to change (mm-everywhere, linear-only)

Add linear/angular awareness, then scale only the **linear** entries by
`machineToMM`. Leave angular (degree) values untouched (their conversion factor
is effectively 1 via `angularUnits`; do NOT apply the linear factor).

**Determining linearity**
- Axes: by axis index / COORDINATES letter — `X,Y,Z,U,V,W` (indices 0,1,2,6,7,8)
  are linear; `A,B,C` (indices 3,4,5) are angular. stratuMAK already has the axis
  letter/index; add a small `axisIsLinear(index)` helper.
- Joints: `[JOINT_n]TYPE = LINEAR|ANGULAR` (default LINEAR). stratuMAK does **not**
  currently read `TYPE` — add it. (A joint's linearity ultimately follows the
  axis it drives, but reading `TYPE` matches the C config and is simplest.)

**Config values to scale — all in `src/stmak/internal/task/config.go` (line
numbers are post-partial-fix, verify before editing):**

Joint (`loadJoint`), scale iff joint is linear:
| value | line | dim |
|---|---|---|
| MIN_LIMIT / MAX_LIMIT | 208–209 | length (done, add linear guard) |
| BACKLASH | 215 | length |
| FERROR | 221 | length |
| MIN_FERROR | 225 | length |
| HOME | 231 | length |
| HOME_OFFSET | 232 | length |
| HOME_SEARCH_VEL | 233 | velocity |
| HOME_LATCH_VEL | 234 | velocity |
| HOME_FINAL_VEL | 235 | velocity |
| MAX_VELOCITY | 282 | velocity |
| MAX_ACCELERATION | 286 | accel |
| MAX_JERK | 290 | jerk |

Axis (`loadAxis`), scale iff axis is linear:
| value | line | dim |
|---|---|---|
| MIN_LIMIT / MAX_LIMIT | 359–360 | length (done, add linear guard) |
| MAX_VELOCITY | 372 | velocity |
| MAX_ACCELERATION | 378 | accel |

Per-index caches in `loadConfig` (used by jog clamping AND the canon's per-move
vel/acc blend — these feed SET_LINE `vel`/`ini_maxvel`/`acc`), scale iff linear:
| value | line |
|---|---|
| `t.jointMaxVel[j]` | 61 |
| `t.axisMaxVel[a]` | 74 |
| `t.axisMaxAcc[a]` | 75 |

TRAJ globals in `loadTraj` (linear by definition — `..._LINEAR_...` keys):
| value | line |
|---|---|
| DEFAULT_LINEAR_VELOCITY / DEFAULT_VELOCITY → `defaultVel` | 118–119 |
| MAX_LINEAR_VELOCITY / MAX_VELOCITY → `t.maxVelocity` | 120–121 |
| DEFAULT_LINEAR_ACCELERATION / DEFAULT_ACCELERATION → `defaultAcc` | 137–138 |
| MAX_LINEAR_ACCELERATION / MAX_ACCELERATION → `t.maxAcceleration` | 139–140 |

Also audit (same section) the extra reads at ~`config.go:420` (HOME_SEARCH_VELOCITY),
`:478` (MAX_ACCELERATION), `:502` (MAX_VELOCITY) — scale iff linear.

**Consistency check to keep in mind:** `canon_getters.go:17` already does
`toProg(c.task.maxVelocity)` — it *assumes* `maxVelocity` is mm. After scaling
`t.maxVelocity` to mm at load, that getter becomes correct (it is currently fed
machine units, so it is wrong on inch configs today). Grep for other readers of
these caches to confirm none double-convert.

**Do NOT scale:** angular limits/vels/accels (A/B/C, ANGULAR joints), dimensionless
ratios (e.g. `OFFSET_AV_RATIO`, `config.go:351`), spindle speeds, feed overrides,
tolerances already run through `fromProg` in the canon, or anything the canon
already converts. Only the raw INI→motion config path is wrong.

**Also check these parallel limit paths** (same machine-unit bug may live there):
- `inihal.go` runtime limit/vel/accel HAL pins (`jointMinLimit`/`axisMinLimit`/…):
  values pushed from HAL/INIHAL to motion at runtime must be mm too.
- `stat.go:253-254` reports `MinPositionLimit`/`MaxPositionLimit` from axis config
  — after the fix these are mm (consistent with mm positions); verify no test
  asserts the old machine-unit value (only `tests/startup-state` does, and it is
  currently xfail).
- `configcheck.go:289-333` reads MIN/MAX_LIMIT for joint⊆axis validation — it
  compares raw-vs-raw so it stays correct without scaling; leave it, but do not
  let it and the load path diverge.

---

## 5. Parity harness (`tests/motion-logger/parity-vs-2.9`) — unit normalization

Under mm-everywhere, stratuMAK emits SET_LINE `x/y/z/u/v/w` and `vel/ini_maxvel/acc`
in **mm** while the vendored 2.9 oracle is in **machine units (inch)**. For inch
configs they now differ by 25.4× (same physical motion). Add a normalization step
to `canonicalize.awk`/`normalize.sh` so the comparison stays meaningful:

- Multiply the **2.9 (oracle) side's linear** SET_LINE fields (x,y,z,u,v,w and
  vel,ini_maxvel,acc) by the config's linear factor (25.4 for inch) to bring them
  to mm — OR divide stratuMAK's by it. Leave A/B/C untouched (angular, unscaled both
  sides). Positions like `turn`, `id` (already stripped), `motion_type` are not
  lengths.
- Simplest implementation: a per-target `unit_factor` (1.0 for mm configs, 25.4
  for inch) passed to the normalizer; apply only to the linear numeric fields of
  SET_LINE/SET_CIRCLE/SET_VEL/SET_ACC. mm-config targets (none currently — all
  parity targets are inch) need factor 1.
- After this, the existing 5 findings (per-move SET_VEL/ACC/TERM re-emission,
  trailing SET_SPINDLESYNC, spindle-0 suppression, leading zero rapids, the M99
  loop divergence) should re-appear unchanged; anything new is real.

Note: the **self-regression** runtests golds (`tests/motion-logger/*/expected.*`)
just get re-captured (see §6) — no normalizer needed there, they compare stratuMAK to
stmak.

---

## 6. Golds to re-capture (self-regression) after the fix

Inch motion-logger tests whose SET_LINE `vel/ini_maxvel/acc` (and limit preamble)
change once vel/accel go to mm. Re-run each and re-baseline:
- `tests/motion-logger/basic` (segments g0/g1/s + builtin-startup)
- `tests/motion-logger/mountaindew` (strip the trailing clean-shutdown `ABORT`
  when re-capturing from `out.motion-logger` — see the interceptor recipe)
- `tests/interp/m98m99/12-M99-endless-main-program`
- `tests/motion-logger/startup-gcode-abort` (once that feature lands; capture
  fresh on the fixed foundation)

The `parity-vs-2.9/oracle-2.9/` vendored files do NOT change (they are 2.9's
output); only the normalizer gains the unit factor.

---

## 7. Regression plan

1. `cd src/stmak && go test ./...` (fast; `internal/task` covers config/limits).
2. Rebuild: `cd src && make ../bin/stmakd`.
3. MDI boundary re-probe on `stop-button` (limits now ±config-in-mm; a linear
   move at the limit passes, just past it fails) AND confirm an **inch move now
   runs at the correct speed** (previously 25.4× slow) — e.g. time a fixed-length
   G1 and compare against the programmed feed.
4. Re-capture the §6 golds; confirm each test green via `scripts/runtests`.
5. Add the §5 normalizer factor; `parity-vs-2.9/compare.sh` should show the same
   findings as before the fix.
6. **Full suite**: `scripts/runtests tests/` (see `/tmp/stmak-fullreg.log` pattern).
   Pay attention to any inch config that (a) has a motion-timing assertion or
   (b) asserts a position/velocity value — those may shift. rotary-axis inch
   configs are the key new-coverage area for the linear-only guard.

---

## 8. Related session context

- The blocker this came from: `tests/abort/stop-button-crazy-move` (now passing
  with the partial limits fix + gmi driver port). See its git history on
  `ci-trim`.
- The gmi python `Stat` client gained `queue`/`queue_full` (`lib/python/gmi/stat.py`)
  as part of that port — unrelated to units, keep.
- Parity harness lives in `tests/motion-logger/parity-vs-2.9/` (README there).

---

## Implementation notes (what actually landed, 2026-07-14)

Convention **A (mm-everywhere)** as recommended. Changes:

- `config.go`: added `axisIsLinear(index)` (X/Y/Z/U/V/W linear; A/B/C angular),
  `jointTypeIsLinear([JOINT_n]TYPE)` (default LINEAR), `machineToMMLinear(v, linear)`,
  and `poseLinearToMM`. Every linear length/vel/accel/jerk value in `loadJoint`,
  `loadAxis`, the `loadConfig` per-index caches, and the `loadTraj` globals is now
  scaled iff linear; angular values pass through. `jointLinear[]` cached on `Task`.
  World home (`[TRAJ]HOME`) also gets its linear components scaled (a position
  handed to motion; not in the original §4 tables but the same bug class).
- Spindle velocities and `HOME_SEARCH_VELOCITY` (rev/s), `OFFSET_AV_RATIO`
  (dimensionless), and anything the canon already converts are left unscaled, as §4 said.
- `inihal.go`: **no functional change** — its length pins are stmak-internal mm by
  design (traj pins initialised from the now-mm `t.maxVelocity`; joint/axis limit
  pins are HAL-driven and pushed raw to mm-internal motion; no stratuMAK code feeds INI
  machine units into them). Added a doc comment stating the mm contract so the bug
  isn't reintroduced. If you ever add INI->pin substitution, it must emit mm.
- Tests: `internal/task/units_test.go` covers linear-scaled vs angular-unscaled
  joints/axes and the two helpers.

### §5 deviation — parity normalizer (CORRECTED 2026-07-14, second pass)

The first pass of this section claimed the per-move dynamics (`vel`,
`ini_maxvel`, `acc`) were not cross-tree comparable ("2.9 blends mm
displacements against inch/s limits", basic/g1 move 1 `1.66296` vs `2.49444`,
ratio 1.5) and stripped them from the diff. **That was a misdiagnosis.** 2.9's
blend is unit-consistent — `getStraightVelocity` applies `FROM_EXT_LEN` to the
axis limits and `toExtVel`/`TO_EXT_LEN` on emission — and its INIT_CANON starts
the interpreter in the MACHINE's modal units (G20 on inch). stratuMAK's canon
hardcoded its startup modal units to mm, so the same unit-less corpus programs
ran as inches in 2.9 and as millimetres in stratuMAK: physically different moves,
25.4× apart, coincidentally printing identical position numbers (which is why
geometry "matched" while dynamics didn't — both trees blended correctly, for
different moves). Verified arithmetically: a consistent G20 blend reproduces
2.9's `1.66296`/`415.74` exactly; a consistent G21 blend reproduces the interim
stratuMAK `2.49444`/`623.61` exactly.

Fixes (this branch, follow-up commit):
- `canon.go`: `machineCanonUnits(linearUnits)` — `NewCanon`, `InitCanon` and
  `loadTraj` (canon exists pre-config) now start/reset the modal length units
  from `[TRAJ]LINEAR_UNITS`, mirroring INIT_CANON. Unit-less G-code on an inch
  machine no longer runs 25.4× small.
- `UseToolLengthOffset` now converts program units→mm on receipt (the interp
  passes `USER_TO_PROGRAM_LEN` values; C canon did `FROM_PROG_LEN`), and the
  `GetExternalToolLength*offset` getters hand back `toProg` (C: `TO_PROG_LEN`).
  Angular components stay degrees. Stat/halui now report the tool offset in mm,
  consistent with the rest of the gmi API.
- `configcheck.go`: warns on `LINEAR_UNITS != mm` that motion's HAL pins carry
  mm — 2.9-ported HAL scale/gain values (per-inch) must be converted.
- Parity normalizer: §5's original plan implemented after all — per-target
  `units` factor (25.4) in `targets.sh`, applied by `canonicalize.awk` to the
  ORACLE side's linear positions AND dynamics (`normalize.sh --units-factor`;
  rounding switched from %.4f absolute to 5 significant digits to match the
  logs' %.6g precision at mm magnitudes; LC_ALL=C forced). Pure-angular moves
  would need factor 1 (TO_EXT_ANG) — none in the corpus; the normalizer injects
  a loud marker if one appears. After this, positions and dynamics match
  EXACTLY; `compare.sh` shows precisely the pre-existing findings #1–#5.
- `tests/*/test.sh` (motion-logger + m98m99): added explicit `set -e` —
  runtests invokes `bash -x test.sh`, which bypasses the shebang's `-e`, so
  m98m99-12's motion-logger diff had been silently advisory (false pass).
- Golds re-captured a second time (G20 semantics): basic g0/g1/s (builtin-
  startup unchanged — bring-up is modal-units-independent), mountaindew,
  m98m99-12. `internal/task/units_test.go` covers the modal init and the tool
  offset round-trip.

### §6 golds re-captured
`motion-logger/basic` (builtin-startup, g0, g1, s), `motion-logger/mountaindew`
(trailing ABORT stripped), `interp/m98m99/12-M99-endless-main-program`. All pass.

### §7 test fallout — mm HAL pins
stratuMAK feeds motmod mm, so HAL `joint.N.pos*` pins are mm (not 2.9's inch). Three
`tests/motion` inch tests that read those pins and compared against inch INI/values
broke — the fix is correct; the tests assumed machine-unit pins. Made them
"mm-aware" (interpret the HAL values as mm) WITHOUT migrating the test:
- `motion/g0/checkresult`: convert [AXIS_X] MAX_VELOCITY/MAX_ACCELERATION to mm via
  [TRAJ]LINEAR_UNITS; break the accel phase on accel<=0 (the correct mm limits make
  the 1 mm move triangular, no cruise); widen the accel epsilon to the 6-decimal
  sample quantization at mm scale. Move stays 1 mm.
- `motion/jogwheel-{axis,joint}`: relax close_enough epsilon 1e-6 -> 1e-4 mm. The
  1e-6 was an inch tolerance matching simple_tp's arrival deadband TINY_DP =
  max_acc*period^2*0.001 (2.54e-5 mm, scale-invariant); in mm it was 25.4x too
  tight so a jog that legitimately stops within the deadband "failed". jog-scales
  unchanged; not a motmod bug.
Not caused by the fix (flaky under batch load, pass alone): `linuxcncrsh`,
`remap/introspect`, `remap/fail/prolog`. Watch for other inch tests that read HAL
joint pins as machine units — apply the same mm-aware treatment.

---

## Update (2026-07-16): opt-in machine-units view on the client

The mm-everywhere decision stands: the server API and the base `gmi.Stat`
continue to report all linear quantities in millimetres. For consumers that
need the machine's *configured* units instead (a `linuxcnc.stat()` drop-in for a
UI, or a parity test that mirrors classic inch values), `gmi.Stat` now offers an
opt-in read-through view:

    s = gmi.Stat().machine_units()   # or gmi.MachineUnitsStat(gmi.Stat())

It converts linear position/offset/limit/velocity/accel fields from mm to the
config units (per-joint via each joint's `units` scale; positions per-component
linear-vs-angular; a no-op on a mm machine). The base Stat is unchanged, so the
mm-adapted tests keep working. First user: `tests/startup-state` (inch config).
