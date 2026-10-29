# pnptask: the scene gate per leg (D45) and the end of the derived wait point (D46)

Status: **built on branch `pnp-simplify` (2026-10-29, d125d46371) as D45/D46; not yet run on the machine.** The config side
of the coating machine is specified in CoatV2conf
`docs/PNP_SIMPLIFY_SPEC.md` on the branch of the same name.

## 1. Why

A dead-zone scene can change while the portal moves: a sphere opens or closes,
a door swings. pnptask plans each leg against the scene of the moment (§7.4,
`dispatchLeg`) and nothing else ties the two together. Everything that keeps a
moving fixture and the head apart lives outside pnptask today, and it can only
work with what it can see from there:

- the fixture's own interlock sees where the head **is** (`deadzone.N.free`),
  not where the leg in flight will take it;
- the PLC sees jobs, not legs, and pnptask runs the legs of a job back to back
  with nothing in between it can hold.

On the coating machine that produced, on 2026-10-29: a park planned through
the open sphere's corridor while the halves had just begun to close (a crash,
siglog-20261029-182439); a job whose second leg passed under the sphere while
the halves were opening (same siglog, 204.5 s); and a config-side interlock
that had to reconstruct legs, the head's side of the sphere and the end of a
crossing from job ids and `deadzone.N.free` (CoatV2conf `coat_pnp_main`,
4be3dfc, 87ea061) — and which can still only hold a stroke for a whole job,
because the one place between two legs is inside pnptask.

D29 (the derived wait point, a two-leg approach streamed into the TP while a
station is polled) was the answer to "the sphere opens while the head is on its
way". Since CoatV2conf c0e3ca2 the sphere is open before the job to the camera
starts, D29's streamed approach is not used in steady state at all, and it is
the most intricate piece of pnptask: the only leg queued behind a moving one,
the only place a job can fail with motion still queued (`abortLeg`), its own
scene check and error id.

## 2. What

**D45 — the scene gate.** Before every travel leg, pnptask decides whether the
leg touches the **movable region** (§3). Such a leg starts only under a
two-flag lock with whatever moves the scene (§4): never while a scene change
is asked for or under way, and nothing may start changing the scene while such
a leg is in flight. The wait happens **between legs, standing**, never in the
middle of one. Legs that do not touch the region are not gated at all.

**D46 — no derived wait point.** `WAIT_DEADZONE` / `WAIT_CLEAR_DEADZONE`, the
streamed approach and everything only it needs are removed. A busy station
without a taught `WAIT_X/Y` is waited for where the head stands, at movement
height — the third shape `gatedTravel` already has.

Taught wait positions (D15) stay unchanged: they overlap travel with a
process (on the coating machine the coat chuck's, which saves ~1 s a part on
the critical path).

## 3. The movable region

Derived from the drawings at load, no new configuration:

- A zone that is **identical in every** `DEADZONE_FILE` (same vertices, same
  order after normalisation) is static.
- Every other zone of every drawing belongs to the movable region. The region
  is the set of those polygons (no union is computed; "touches the region" is
  "touches one of them").
- One drawing only: the region is empty, the gate never engages.

Measured against the **drawn** zones, not the planner's offset ones, for the
same reason as `deadzone_rt.go`: the drawn outline is the physical one (D28),
and the offset ring is the planner's margin for the scene the leg is planned
in, which says nothing about the other scene.

On the coating machine (`deadzones-sphere-close.dxf`, `-open.dxf`) the region
is the three strip zones — together the whole strip X −590…−385, Y −6…402.5.

## 4. The lock

### 4.1 Pins

| pin | dir | |
|---|---|---|
| `scene-hold` | in, bit | the scene is about to change, or is changing: start no leg that touches the region. Unwired it reads 0: the gate never waits. |
| `scene-busy` | out, bit | a leg that touches the region is in flight (or about to start, see 4.2) |

### 4.2 pnptask's side, in `dispatchLeg`

For a leg about to be dispatched (`travel`, `dispatchTravel`; park included):

1. Plan the route as today, against `deadzone-select` of the moment.
2. Does the leg touch the region? The polyline from the start point (the
   machine's current XY) through every waypoint, including both ends: any
   point inside, or any segment crossing a boundary, of any region polygon
   (`pnproute`: `Contains` plus `firstCrossing`, exported as
   `pnproute.PathTouches(path, poly)`). No → dispatch, no lock.
3. Yes → **wait until `scene-hold` is low** (a wait like D15's: `auto-enable`
   low and `wait-abort` end it with `WAIT_ABORTED`, estop and machine-off as
   always), then raise `scene-busy`.
4. **One control cycle later, read `scene-hold` again.** High → drop
   `scene-busy` and go back to 3. Low → **re-plan** (the scene may have changed
   while waiting; a route planned before the wait is not trusted) and, if the
   re-planned leg still touches the region, dispatch it with `scene-busy` held.
   If it no longer touches, drop `scene-busy` and dispatch.
5. Drop `scene-busy` once the leg has drained (`waitMotionDone`), and on every
   way out of the leg (error, estop, abort).

`scene-busy` is published every control cycle like the other outputs, and is
low whenever no job or park is running.

### 4.3 The other side (a fixture, in its own component)

In automatic, before starting a stroke that changes the scene:

1. Raise `scene-hold` and keep it raised until the stroke has ended (end
   positions reached, or the fixture faulted).
2. Start the stroke only once `scene-busy` has been **low for at least one
   servo cycle after** `scene-hold` went up. While it is high, wait — a leg in
   flight runs to its end.

### 4.4 Why it cannot let both start

pnptask starts a gated leg only if `scene-hold` was low at a sample taken at
least one cycle **after** it raised `scene-busy`. The fixture starts only if
`scene-busy` was low at a sample taken **after** it raised `scene-hold`, which
then stays up. If both started, the fixture's sample would have to precede
pnptask's raise, and pnptask's sample would then follow the fixture's raise —
and see it. Raised at the same moment, pnptask yields (4.2 step 4) and the
fixture goes first: a stroke is short and must not be starved by a stream of
legs; legs are starved at most for one stroke.

### 4.5 What the lock does not cover, and what covers it

- **A head standing inside the region** between legs (at the camera chuck,
  during its Z strokes and the swap) is not a leg. The fixture's own position
  interlock (`deadzone.N.free`) keeps covering that, unchanged.
- **Z strokes** are not legs: vertical, XY unchanged; covered as above.
- **Manual jogs** are not gated; the position interlock covers them, as today.
- **Homing** keeps its own rule on the coating machine (only with the sphere
  closed); not gated here.

## 5. Interaction with existing decisions

- **D15 taught wait positions**: unchanged. The leg to the wait position and
  the leg in are each gated on their own.
- **D15 auto-enable low / D33 wait-abort**: also end a wait at the gate
  (4.2 step 3), with `WAIT_ABORTED`, the picker keeping its material, exactly
  as a busy-station wait.
- **D34 park**: a park is a leg; it is gated like any other.
- **D37 stay-near / D38 low moves**: unchanged; a move that stays at a tray
  does not touch the region.
- **D40 exact stop**: unchanged — the gate only ever waits with the queue
  drained.
- **Scene read per leg** (§7.4): unchanged, plus the re-plan after a wait
  (4.2 step 4).

## 6. D46: what is removed

- Config: `WAIT_DEADZONE`, `WAIT_CLEAR_DEADZONE` (now a load error naming D46,
  not silently ignored), their validation (`checkWaitZone`, `HasWaitZone`,
  `WaitZoneIdx`).
- `gatedTravel`'s `HasWaitZone` branch, `streamedApproach`, `waitPoint`,
  `dispatchLeadingLeg` / `splitTail` / `withTriangleTail`, `checkClearScene`,
  `abortLeg` (no leg is ever queued behind a moving one any more).
- `errWaitSceneMismatch` (25): the id stays reserved, never raised.
- The `wait-stops` pin.
- `pnproute.EntryPoint` if nothing else uses it.
- Tests: `tests/pnptask/waitzone`, the D29 cases in `job_test.go`,
  `altpicker_test.go`, `config_test.go`, `planners_test.go`; `tests/pnptask-blend`
  only where it exercises the streamed approach (to be checked — the blend of a
  route's own corners stays).
- Docs: D29's row becomes "superseded by D46"; CoatV2conf
  `docs/PNPTASK_ZONE_WAIT_SPEC.md` is archived.

A busy station without `WAIT_X/Y` is then waited for where the head stands,
at movement height (`gatedTravel`, third case, unchanged).

## 7. Errors and diagnostics

- No new error id. A gate wait that never ends is ended by the PLC
  (`wait-abort`, auto-enable) or by its own job timeout, like any station wait.
- Log: `pnptask: leg waits for the scene` (info, once per wait, with the leg's
  target), `pnptask: leg yields to a scene change` (debug, 4.2 step 4).
- `scene-busy` and `scene-hold` are plain pins: visible in the siglog.

## 8. Configuration

- **No switch.** The gate is always there and turns itself off where it has
  nothing to do: with one drawing the movable region is empty and no leg
  touches it; with `scene-hold` unwired (reads 0) no leg ever waits — what is
  left is `scene-busy` reporting legs that touch the region, and the one-cycle
  confirmation before such a leg (~10 ms). A switch would buy those 10 ms with
  a way to turn a safety interlock off in the INI, silently, on a machine whose
  drawings and wiring still say it is needed — and a second path to test. What
  decides is what the config already shows: the drawings (is there a region?)
  and the wiring (does anything raise `scene-hold`?).
- No margin parameter: the drawn zones are the physical outline (D28). If a
  fixture sweeps beyond its end positions, the drawing is what changes.

## 9. Tests

Unit (`pnptask`):

- region derivation: identical zones static, differing zones movable, one
  drawing → empty;
- `PathTouches`: inside, crossing, touching an edge, endpoint inside, clear;
- the lock: a leg waits while `scene-hold` is high; yields when it rises during
  the confirmation cycle; re-plans after a wait; `scene-busy` drops on drain,
  error, estop, wait-abort;
- a leg that does not touch the region never waits and never raises busy;
- D46: `WAIT_DEADZONE` refused at load; a busy station without `WAIT_X/Y` is
  waited for standing.

Runtests (`tests/pnptask/scenegate`, two drawings like the machine's):

- a stroke asked for while a crossing leg runs starts only after the leg;
- a crossing leg asked for during a stroke waits at the boundary, standing
  (no motion between the two legs), and plans in the new scene;
- the 2026-10-29 shapes: a park from east of the region while a close is asked
  for at the same moment; a job whose first leg leaves the region and whose
  second crosses it, with a close asked for in between — the close runs during
  the pick, the second leg waits for its end.

## 10. Order of work

1. `pnproute.PathTouches` and the region derivation, with tests.
2. The lock in `dispatchLeg`, pins, config switch, unit tests, runtest.
3. D46 removal, tests and docs adjusted.
4. Design doc rows D45, D46; D29 superseded.
