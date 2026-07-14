# gomc units consistency fix — spec / handoff

Status: **partially done** (position limits, linear-only-unaware) on branch `ci-trim`.
The rest is a self-contained follow-up meant for its own branch + full-suite
regression. This doc is the complete cold-start brief so no re-derivation is
needed.

---

## 1. The bug (root cause)

gomc runs the motion controller (`motmod`, the same C module as 2.9) in
**millimetres internally**: the canon converts G-code targets to mm
(`CanonState.fromProg` × `unitScale`, `canon.go:139-159`) and emits mm to
`SetLine`; position feedback (`s.actual_position`) is mm; the whole gmi API is mm.

But length-dimensioned **config** values are handed to motion **raw from the INI
in machine units** (whatever `[TRAJ]LINEAR_UNITS` says), never converted to mm.
On an **inch** machine every such value is therefore **25.4× off**:

- **Position limits** (`config.go` `SetJointPositionLimits`/`SetAxisPositionLimits`)
  end up 25.4× too *tight* → `motmod` (`src/emc/motion/command.c:216`,`:261`)
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
rejected. The reproduction recipe (start `gomc-server -r test.ini`, wait for the
`milltask` comp, drive with the `gmi` python client, home in `MODE_MANUAL`, then
MDI probe `G20`/`G21` Z moves and read `s.actual_position` / the error channel)
is the fastest way to re-verify.

---

## 2. Convention decision — go **mm-everywhere**

Two coherent end states; pick one before coding.

**A. mm-everywhere (RECOMMENDED).** Finish what the partial fix started: convert
every *linear* length/velocity/accel/jerk config value from machine units to mm
at load time. Localized to `config.go`. gomc is already mm-internal to the bone
(positions, feedback, gmi API), so this flows with the grain.
- Cost: SET_LINE `vel`/`ini_maxvel`/`acc` output changes (×25.4) for inch
  configs → re-capture those golds; and the parity-vs-2.9 harness must
  unit-normalize (see §5).

**B. machine-units-everywhere (2.9-faithful, NOT recommended).** Make motmod run
in machine units like 2.9: apply `TO_EXT_LEN` (mm→machine) to move targets in the
canon, **revert** the position-limit scaling, and convert position feedback back
to mm at the stat boundary. Parity stays numerically clean and SET_LINE golds
don't churn — but it fights gomc's mm-internal design across the whole emit +
feedback path. Much larger blast radius. Only choose this if a deliberate
decision is made to re-base gomc on machine-units internally.

The rest of this spec assumes **A**.

---

## 3. Already done on `ci-trim` (and its gap)

`src/gomc/internal/task/config.go`:
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
  are linear; `A,B,C` (indices 3,4,5) are angular. gomc already has the axis
  letter/index; add a small `axisIsLinear(index)` helper.
- Joints: `[JOINT_n]TYPE = LINEAR|ANGULAR` (default LINEAR). gomc does **not**
  currently read `TYPE` — add it. (A joint's linearity ultimately follows the
  axis it drives, but reading `TYPE` matches the C config and is simplest.)

**Config values to scale — all in `src/gomc/internal/task/config.go` (line
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

Under mm-everywhere, gomc emits SET_LINE `x/y/z/u/v/w` and `vel/ini_maxvel/acc`
in **mm** while the vendored 2.9 oracle is in **machine units (inch)**. For inch
configs they now differ by 25.4× (same physical motion). Add a normalization step
to `canonicalize.awk`/`normalize.sh` so the comparison stays meaningful:

- Multiply the **2.9 (oracle) side's linear** SET_LINE fields (x,y,z,u,v,w and
  vel,ini_maxvel,acc) by the config's linear factor (25.4 for inch) to bring them
  to mm — OR divide gomc's by it. Leave A/B/C untouched (angular, unscaled both
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
just get re-captured (see §6) — no normalizer needed there, they compare gomc to
gomc.

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

1. `cd src/gomc && go test ./...` (fast; `internal/task` covers config/limits).
2. Rebuild: `cd src && make ../bin/gomc-server`.
3. MDI boundary re-probe on `stop-button` (limits now ±config-in-mm; a linear
   move at the limit passes, just past it fails) AND confirm an **inch move now
   runs at the correct speed** (previously 25.4× slow) — e.g. time a fixed-length
   G1 and compare against the programmed feed.
4. Re-capture the §6 golds; confirm each test green via `scripts/runtests`.
5. Add the §5 normalizer factor; `parity-vs-2.9/compare.sh` should show the same
   findings as before the fix.
6. **Full suite**: `scripts/runtests tests/` (see `/tmp/gomc-fullreg.log` pattern).
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
