# pnptask — Pick-and-Place Task Module

Status: **draft for review** (2026-08-08)
Branch: `add-pnptask`

A task gomod for pick-and-place machines — not a `milltask` replacement, but
a sibling task implementation for a different use case. No NGC/G-code:
motion is generated dynamically from station definitions, tray grids and a
dead-zone route planner. Jobs are commanded purely over HAL pins so the module
integrates with a PLC/hardware world without any UI dependency.

Reference prototype for the route planner: `~/source/pnp-route-test/`
(visibility-graph + Dijkstra over clearance-offset dead zones, DXF input).

---

## 1. Decisions log (concept discussion, 2026-08-07/08)

| # | Topic | Decision |
|---|-------|----------|
| D1 | HAL params from Go | Missing feature — **add param support to `pkg/hal`** (Phase 0). Params are preferred over INI where runtime adjustment matters. |
| D2 | Settle/release times | HAL **params** (RW float, `halcmd setp`-adjustable). INI keys provide the initial values. |
| D3 | Picker x/y offsets | HAL **params** (RW float), symmetrical on both pickers (`picker.0` defaults 0/0). |
| D4 | Tray/station z-offsets | HAL **pins** (float, in), as originally specced — wireable (e.g. height sensor). |
| D5 | Alternating-picker enable | **Module load arg** `pickers=2` — no wiring auto-detection (Go HAL API has no writer/connectivity query, and an explicit arg is deterministic). |
| D6 | Tray/station state persistence | Optional, via `persist_sqlite` GMI API. Enabled by load arg `persistence_instance=<name>`; **no default lookup** — absent arg means in-memory only. |
| D7 | Dead zones | **Convex-only** for v1, hard validation error on concave input. |
| D8 | Tray reset pins | Two pins per tray station: `set-full` → all slots = current `process-step` pin value; `set-empty` → all slots = −1. (The earlier third pin `set-process-step` is merged into `set-full`.) |
| D9 | Slot search | Tracked slot state is authoritative for *which* slots to try; the picker material-present feedback (closed = gripped nothing) only validates and corrects it. |
| D10 | Pick Z | Per tray **station** in INI (like process stations). Both pickers share the machine Z axis; `picker.1` differs only by its x/y offset params. |
| D11 | Error reset | Dedicated `error-reset` input pin, edge-triggered (wireable to `machine-on` for auto-reset). |
| D12 | Job model | Strictly one job at a time via the `start-job` handshake. No queueing. |
| D13 | Planning latency | Plan time adds to cycle time (planning starts at the `start-job` edge). Budget: **< 100 ms**; precompute everything static (Phase 1). |
| D14 | Manual mode | `auto-enable` input pin: low → new jobs rejected, manual jog + manual picker control enabled; a running job **finishes first** (no abort) when it goes low. Picker `close` outputs keep their state across machine-off (held material stays held); they are cleared on estop. Manual picker control works even with the machine off, but not during estop. |
| D15 | Wait positions | No free-standing wait-point stations. A process station optionally defines `WAIT_X`/`WAIT_Y` and has a `busy` input pin; a job targeting a busy station waits there (holding the material) until `busy` clears. `auto-enable` going low aborts the wait (error `WAIT_ABORTED`). |
| D16 | Abort semantics | No abort request besides estop and machine-off. Externally clearing `start-job` mid-job is ignored. |
| D17 | Tray geometry | TRAYDEF `FIRST`/`LAST` are **absolute machine coordinates** (`LAST` optional — single-position tray); tray stations have no X/Y of their own, only `Z_PICK` and pins. A `tray-id` change resets all slots to −1 (the startup state). |
| D18 | Manual jog | Only when homed — jog pins are ignored while unhomed. |
| D19 | Release handshake | At action end `release` := 0, then wait for `released` to go **low** (RELEASE_TIMEOUT applies). |

---

## 2. Architecture

### 2.1 Instance stack

pnptask is a **peer of milltask**: it owns its own motion stack and talks to it
through the GMI clients (`motctl`/`motstat`), exactly like milltask does. No
`io`/`iocontrol` instance is needed (no toolchanger); estop/enable are
sequenced by pnptask itself against motctl.

```
load tpmod    <pnp.tp>   mot_instance=pnp.mot
load homemod  <pnp.home.0,pnp.home.1,pnp.home.2> mot_instance=pnp.mot
load trivkins <pnp.kins> coordinates=XYZ
load motmod   <pnp.mot>  num_joints=3 kins_instance=pnp.kins \
                         tp_instance=pnp.tp home_instance=pnp.home
load persist_sqlite <persist> db=/var/lib/stmak/pnp.db          # optional
load pnptask  <pnp.task> motion_instance=pnp.mot pickers=2 \
                         persistence_instance=persist
addf pnp.mot.motion-command-handler servo-thread
addf pnp.mot.motion-controller      servo-thread
```

Load args (parsed `key=value` like `internal/task/module.go`):

| Arg | Default | Meaning |
|-----|---------|---------|
| `motion_instance` | `motmod` | motctl/motstat provider instance |
| `pickers` | `1` | `1` or `2`; `2` enables the alternating-picker logic |
| `persistence_instance` | *(unset)* | persist API instance; unset = in-memory state only |

### 2.2 Source layout

```
src/stmak/pkg/hal/param.go              Phase 0: HAL param support (+ cgo.go additions)
src/stmak/pkg/pnproute/                 Phase 1: planner package (lifted from pnp-route-test)
    geom.go  plan.go  dxf.go  scene.go  scene_test.go plan_test.go testdata/
src/stmak/internal/pnptask/             Phases 2–6: the module
    module.go        registration, factory, load args, lifecycle
    config.go        INI schema parsing + validation
    pins.go          pin/param trees (global, per-picker, per-station)
    machine.go       estop/enable/homing state machine, motmod config push
    stations.go      station/tray model, slot state, direction modes
    persist.go       optional persist_sqlite backing
    job.go           job state machine, action selection
    actions.go       action-class sequences (pick/place/move)
    motion.go        motion streaming, waitMotionDone, limits
    errors.go        error-id table
src/stmak/internal/pnptasktest/         Phase 7: scripted integration tests (tasktest pattern)
tests/pnptask/                          sim config (ini/hal/dxf) for the integration run
```

Registered in `packages.conf` as `gomod internal/pnptask @GOMOD:PNPTASK_GO@`
(same pattern as milltask), plus the `imports_generated.go` regeneration.

---

## 3. Phase 0 — HAL parameter support in `pkg/hal`

`pkg/hal` today binds only `hal_pin_*_new`. Params are a straight parallel and
strictly simpler: `hal_param_*_new(name, dir, data_addr, comp_id)` takes a
**direct** data pointer (no double-pointer relink slot — params are never
linked to signals).

API (mirrors `Pin[T]`, minus HAL_PORT — there are no port params):

```go
type ParamValue interface{ bool | float64 | int32 | uint32 }

type ParamDirection int
const (
    RO ParamDirection = 64   // HAL_RO — read-only from halcmd
    RW ParamDirection = 192  // HAL_RW — writable via halcmd setp
)

func NewParam[T ParamValue](c *Component, name string, dir ParamDirection) (*Param[T], error)
func (p *Param[T]) Get() T
func (p *Param[T]) Set(value T)      // owner-side write (valid for RO and RW)
func (p *Param[T]) Name() string
func (p *Param[T]) Direction() ParamDirection
func (p *Param[T]) Type() PinType
```

Implementation notes:

- `cgo.go`: add `go_hal_param_new` dispatch wrapper (bit/float/s32/u32) and
  `halParamNew`; allocate the data cell via `halMalloc`, keep the single
  pointer.
- Same component liveness barrier (`comp.enter`/`leave`) and per-value mutex
  as `Pin[T]`.
- Tests alongside `pin_test.go`; verify `halcmd setp`/`getp` round-trip in the
  existing test harness style.

Deliverable: standalone PR — useful independent of pnptask.

---

## 4. Phase 1 — Route planner package `pkg/pnproute`

Lift `geom.go` + `plan.go` + `dxf.go` from `~/source/pnp-route-test` (they are
already pure stdlib) into `src/stmak/pkg/pnproute`. Changes on top of the
prototype:

1. **Exported API around a precomputed scene** (this is what keeps job-time
   planning inside the <100 ms budget, D13):

```go
type Scene struct { ... }                      // parsed + validated DXF world

// Precomputed per (scene, clearance): offset obstacles, eroded boundary,
// static node set and the static-node visibility adjacency.
type Planner struct { ... }

func LoadDXF(r io.Reader) (*Scene, error)
func NewPlanner(s *Scene, clearance float64) (*Planner, error)   // startup cost, done once
func (p *Planner) Plan(start, goal Point) (*Route, error)        // job-time: insert 2 nodes + Dijkstra
```

   `NewPlanner` builds the full static visibility graph once; `Plan` only
   computes visibility for the two inserted nodes (O(2·V) segment tests) and
   runs Dijkstra on the cached adjacency. The prototype's lazy O(V²·E)
   recomputation goes away.
2. **Convex validation** (D7): reject concave dead zones and concave outer
   boundary with a descriptive error at load time.
3. **Constants become fields** on the planner (`arcSegments`, `segSamples`,
   `coreErode`, `offsetArcStep`, …) with the current values as defaults; INI
   can override the arc discretization if planning time demands it.
4. **Tests**: convert a representative subset of the `gen_testcases.sh`
   battery into table-driven fixtures (`testdata/*.dxf` + expected
   pass/self-check), plus a `Benchmark` for `NewPlanner`/`Plan` on a realistic
   scene to enforce the latency budget early.

Not in scope for v1 (documented as such in the package doc): concave zones,
dynamic/time-varying zones, arc output primitives (the TP's blending handles
corner smoothing — see §7.3).

---

## 5. Phase 2 — Module skeleton, config, HAL interface

### 5.1 INI schema

All sections resolve through `ini.WithNamespace(instanceName)`
(`[pnp.task:PNPTASK]` with fallback to `[PNPTASK]`). Standard `[TRAJ]`,
`[KINS]`, `[JOINT_n]`, `[AXIS_*]` sections are consumed for the motmod config
push exactly as milltask does (units: machine units in INI, **mm internally**).

```ini
[PNPTASK]
AUTOHOME = 1                  # home unhomed joints on first job
MOVE_HEIGHT = 30.0            # global Z movement height
CLEARANCE = 10.0              # planner clearance; must cover safety + BLEND_TOLERANCE
BLEND_TOLERANCE = 2.0         # TP term-cond tolerance for XY travel
MOVE_VEL = 0                  # XY travel vel/acc; 0 = use [TRAJ] defaults
MOVE_ACC = 0
Z_VEL = 50.0                  # Z stroke vel/acc (approach/retract)
Z_ACC = 500.0
POS_SETTLE_TIME = 0.1         # initial values of the RW params (D2)
PICK_SETTLE_TIME = 0.1
RELEASE_TIME = 0.1
RELEASE_TIMEOUT = 5.0         # wait for proc-station 'released'; 0 = forever
HOME_TIMEOUT = 30.0
DEADZONE_FILE = zones_a.dxf   # repeated key (GetAll); line order = selector value 0,1,…
DEADZONE_FILE = zones_b.dxf

[PNPTASK_TRAYDEF_0]           # tray *geometry* definition, selected by tray-id pin
ID = 1
ROWS = 4                      # ROWS=0 and COLS=0 -> endless tray, first pos only
COLS = 10
FIRST_X = 120.0               # slot (0,0), absolute machine coordinates (D17)
FIRST_Y = 400.0
LAST_X = 210.0                # slot (COLS-1, ROWS-1); optional — omit for a
LAST_Y = 430.0                #   single-position tray (reject bin, transfer)
ANGLE = 0.0                   # optional rotation of the grid around FIRST, degrees
DIR_MODE = C+R+~              # iteration order: C/R, +/-, optional ~ meander
MAX_UNPOPULATED = 3           # successive empty picks before tray declared empty

[PNPTASK_TRAY_0]              # tray *station* — no X/Y of its own (D17)
ID = 10                       # station id — unique across trays and procs
Z_PICK = 2.5                  # base pick Z (D10); z-offset pin adds to this

[PNPTASK_PROC_0]              # process station
ID = 20
X = 300.0
Y = 200.0
Z_PICK = 5.0
WAIT_X = 250.0                # optional wait position (D15); omit to wait in
WAIT_Y = 150.0                #   place while the station is busy

[PNPTASK_ROUTE_0]             # optional per-pair movement-height override
ORIGIN = 10
DEST = 20
MOVE_HEIGHT = 15.0
```

Startup validation (fail the load, don't limp): duplicate ids; unknown
`DIR_MODE`; every proc/wait coordinate and every TRAYDEF slot position
(absolute machine coordinates, D17) must lie inside the eroded boundary and
outside every offset dead zone **of every configured dead-zone file**;
`CLEARANCE > BLEND_TOLERANCE`; tray-def grids with ROWS/COLS > 1 but no LAST.

### 5.2 HAL pins and params

Component name = instance name (e.g. `pnp.task.`). Spec's underscores are
normalized to HAL-conventional dashes.

**Global pins**

| Pin | Type | Dir | Function |
|-----|------|-----|----------|
| `estop-on` | bit | in | external estop chain state; high aborts + disables |
| `machine-on` | bit | in | request machine enable (level) |
| `machine-is-on` | bit | out | motion enabled and not estopped |
| `auto-enable` | bit | in | high = auto mode (jobs accepted); low = manual mode (§6.4) |
| `jog-<a>-pos`, `jog-<a>-neg` | bit | in | per axis `a` ∈ COORDINATES (`jog-x-pos`, …); jog while high, manual mode only |
| `jog-speed` | float | in | jog velocity, clamped to the axis MAX_VELOCITY; latched at jog start |
| `process-step` | u32 | in | latched at `start-job` edge |
| `origin-id` | u32 | in | latched at `start-job` edge |
| `dest-id` | u32 | in | latched at `start-job` edge |
| `start-job` | bit | io | rising edge starts a job; reset by module on finish/error; external clears mid-job are ignored (D16) |
| `busy` | bit | out | job executing |
| `error` | bit | out | latched error flag |
| `error-id` | u32 | out | error code (§7.5), 0 = none |
| `error-reset` | bit | in | rising edge clears error/error-id (D11) |
| `deadzone-select` | u32 | in | index into DEADZONE_FILE list, latched at job start |
| `homed` | bit | out | all joints homed |

**Global params (RW, initial values from INI)**

| Param | Type | Function |
|-------|------|----------|
| `pos-settle-time` | float | dwell after reaching pick/place Z |
| `pick-settle-time` | float | dwell after picker close/open command |
| `release-time` | float | dwell after successful place before retract |

**Per picker `n` ∈ {0, 1}** (picker 1 only with `pickers=2`)

| Name | Kind | Type | Dir | Function |
|------|------|------|-----|----------|
| `picker.N.close` | pin | bit | out | close command |
| `picker.N.opened` | pin | bit | in | opened feedback |
| `picker.N.closed` | pin | bit | in | fully-closed feedback (closed after pick ⇒ gripped nothing) |
| `picker.N.missing` | pin | bit | out | set when a pick found no material / tray exhausted; cleared on successful pick and on error-reset |
| `picker.N.manual-open` | pin | bit | in | rising edge: `close` := 0 (manual mode only, §6.4) |
| `picker.N.manual-close` | pin | bit | in | rising edge: `close` := 1 (manual mode only, §6.4) |
| `picker.N.x-offset` | param | float RW | | XY offset vs. machine position (picker.0 default 0) |
| `picker.N.y-offset` | param | float RW | | |

**Per tray station (id from INI), prefix `tray.<id>.`**

| Pin | Type | Dir | Function |
|-----|------|-----|----------|
| `tray-id` | u32 | in | selects the TRAYDEF; change resets all slots to −1 (D17) |
| `set-full` | bit | in | edge: all slots := `process-step` pin value (D8) |
| `set-empty` | bit | in | edge: all slots := −1 |
| `z-offset` | float | in | added to Z_PICK (default 0.0) |
| `empty` | bit | out | no slot matching a pick, or declared empty by probing |
| `full` | bit | out | no free (−1) slot |

**Per process station (id from INI), prefix `proc.<id>.`**

| Pin | Type | Dir | Function |
|-----|------|-----|----------|
| `z-offset` | float | in | added to Z_PICK (default 0.0) |
| `busy` | bit | in | station busy; gates the approach — see busy gating, §7.4 (D15) |
| `has-material` | bit | out | owned by pnptask; restored from persistence if configured |
| `release` | bit | out | request fixture release/unclamp |
| `released` | bit | in | fixture released feedback (state-checked, not edge) |

---

## 6. Phase 3 — Machine control

### 6.1 motmod config push

At `Start()`, after `GetAPIFor("motctl"/"motstat", motionInstance, 1)`:
push `[JOINT_n]`/`[AXIS_*]` position/vel/acc/jerk limits,
`SetJointHomingParams` from `[JOINT_n]HOME_*`, `[TRAJ]` vel/acc defaults and
`SetupArcBlends` from `ARC_BLEND_*` — the same push milltask does in
`internal/task/config.go` / `inihal.go`.

Preferred: extract the push into a shared `internal/motsetup` package used by
both milltask and pnptask. Fallback if the extraction turns out invasive:
copy the ~300 relevant lines and note the duplication. Decide at
implementation time; the extraction is a separate reviewable commit either
way.

### 6.2 Enable/estop state machine (`machine.go`)

Ported ordering from `internal/task/commands.go` `SetState` (minus emcio):

- `estop-on` high (any time): `Abort()`, `Disable()`, `machine-is-on` := 0;
  active job → error `ESTOP`; all picker `close` outputs := 0 (D14 — held
  material is released, this is the intended safe state).
- `machine-on` rising while `estop-on` low: `Enable()`, wait
  `MotionStatus.Enabled` (with the sequencer.go comm-failure watchdog
  pattern), `machine-is-on` := 1.
- `machine-on` falling: abort active job (error `MACHINE_OFF`), `Disable()`.

### 6.3 Homing / autohoming

On the first job (or any job with unhomed joints): if `AUTOHOME=1` —
`SetFree()`, wait `MOTION_FREE`, `JointHome(-1)` (motmod honors
`HOME_SEQUENCE`), poll `Joints[j].Homed` until all homed or `HOME_TIMEOUT`
(error `HOMING_FAILED`), then `SetCoord()`. If `AUTOHOME=0` and unhomed →
error `NOT_HOMED`. Template: `internal/tasktest/tests.go` `ensureHomed`.

### 6.4 Manual mode (`auto-enable`, D14)

Mode logic:

- `auto-enable` **high** (auto mode): `start-job` edges are accepted; all
  jog and `manual-open`/`manual-close` pins are ignored.
- Falling edge with a job running: the job **finishes normally** — no abort.
  `busy` stays high until it completes; manual controls activate only once
  the module is idle.
- `auto-enable` **low** (manual mode): a `start-job` edge is rejected with
  error `AUTO_DISABLED` (latched like any other error, `start-job` reset).

Jog (manual mode, idle, machine on, no estop):

- `jog-<a>-pos`/`-neg` high → `JogCont(axis, ±jog-speed, teleop)`; falling
  edge (or both pins high, or any enabling condition lost) → `JogAbort`.
  `jog-speed` is latched at jog start and clamped to `[AXIS_*]MAX_VELOCITY`.
- Jog requires homed joints (D18); while unhomed the jog pins are ignored.

Manual picker control (manual mode, idle):

- Rising edge on `picker.N.manual-close` → `close` := 1; on
  `picker.N.manual-open` → `close` := 0. Works **regardless of machine-on**
  (picker actuation is typically powered independently), but is inhibited
  while `estop-on` is high.
- Picker `close` outputs are *never* touched by machine-off — held material
  stays held. Only estop clears them (§6.2).
- Manual intervention can invalidate the tracked world state (slot states,
  `has-material`); the operator resyncs via `set-full`/`set-empty`.
  Manual picker-1 changes update the `altHeld` record (§8).

---

## 7. Phases 4/5 — Stations, trays, job engine

### 7.1 Tray model (`stations.go`)

Slot state per tray station: `[]int32`, `-1` = empty, `0` = unprocessed,
`>0` = processed at that step. Grid positions: bilinear interpolation of
FIRST→LAST by (col, row) index, optional ANGLE rotation around FIRST; all
coordinates are absolute machine coordinates — tray stations have no X/Y of
their own (D17). A missing LAST makes a single-position tray (always
pick/place at FIRST). Direction mode parsed into an iterator
(`C|R`, `+|-` each, optional `~` meander) — pure function, fully unit-tested.
Endless trays (ROWS=COLS=0): single position, state tracking reduced to the
probing counter; never "full", never "empty" by bookkeeping (only by probing).

State transitions: `set-full`/`set-empty` edges (D8); `tray-id` pin change →
all slots := −1, the startup state (D17).

Pick search (D9): iterate in direction-mode order over slots with
`state == process-step`; physical miss (closed-after-pick) marks the slot
`-1` and continues; `MAX_UNPOPULATED` successive misses → tray `empty` := 1,
`picker.N.missing` := 1, error `TRAY_EMPTY`. Place search: first slot with
`state == -1`, on success `state := process-step`.

### 7.2 Persistence (`persist.go`, optional per D6)

If `persistence_instance` is set: `open(namespace)` at Start with namespace =
instance name sanitized to `[A-Za-z0-9_]+` (dots → underscores, e.g.
`pnp_task`). Persisted on every change, restored at Start:

- `tray.<id>` → JSON `{tray_id, slots[]}` (restored only if the current
  `tray-id` pin matches the persisted one; mismatch → treat as tray change)
- `proc.<id>` → `has-material`
- `alt_picker` → held-material record (§8)

Single writer, plain `set_entry`, no optimistic-concurrency handling needed.

### 7.3 Motion streaming (`motion.go`)

- XY travel: `SetCoord()` once; per path `SetTermCond(TC_TERM_COND_PARABOLIC,
  BLEND_TOLERANCE)` (arc-blend optimizer active via the config push), then
  back-to-back `SetLine()` per planner waypoint at MOVE_VEL/MOVE_ACC. The
  planner's clearance-rounded corners + TP blending give smooth constant-Z
  travel; CLEARANCE ≥ safety + BLEND_TOLERANCE is enforced at config load.
- Z strokes: separate `SetLine()` at Z_VEL/Z_ACC with a queue drain
  (`Inpos && QueueDepth == 0`, sequencer.go idiom incl. the fresh-dispatch
  tick skip) before and after — picks/places always start from a full stop.
- Per-move vel/acc capped by axis maxima (port `internal/task/motionlimits.go`
  `straightLimits`).
- Route planning input: current commanded position (`GetPosCmd`) → target XY.
  Planner selected by the latched `deadzone-select` (invalid index → error).
  Movement height = global MOVE_HEIGHT unless a `[PNPTASK_ROUTE_n]` override
  matches the latched (origin, dest) pair. If the current Z is below the
  movement height (e.g. after an aborted job), the job starts with a Z retract.

### 7.4 Job state machine (`job.go`, `actions.go`)

`IDLE → LATCH → VALIDATE → [HOME] → PLAN → EXECUTE → FINISH/ERROR`

- LATCH on `start-job` rising edge: origin-id, dest-id, process-step,
  deadzone-select.
- VALIDATE: ids exist; action combo legal; preconditions (tray not
  empty/full, `has-material` state) — errors §7.5.
- EXECUTE runs the action sequence; every wait polls abort conditions —
  `estop-on` and `machine-on` falling only; an external clear of `start-job`
  is ignored (D16). Routes are planned per movement leg (to origin, optional
  wait leg, to dest) just before the leg executes (each within the <100 ms
  budget, D13). The busy-wait additionally aborts when `auto-enable` goes low
  (D15, error `WAIT_ABORTED`).
- FINISH: `start-job` := 0, `busy` := 0. ERROR: additionally `error` := 1,
  `error-id` set. `error-reset` edge clears both (only outside EXECUTE).

Action selection from latched ids:

| origin \ dest | tray | proc |
|---|---|---|
| **tray** | pick-tray + place-tray | pick-tray + place-proc |
| **proc** | pick-proc + place-tray | pick-proc + place-proc |

There are no free-standing wait stations (D15): waiting is part of a job
against a busy process station.

**Busy gating (D15):** when the dest is a proc station, its `busy` pin is
sampled once the pick leg completes: low → route directly to the station;
high → route to the station's wait position (or hold at movement height if
none is configured), wait for `busy` to go low, then route to the station.
The same gating applies before a pick-from-proc approach, sampled at job
start (**R1**). The release/released handshake remains the authoritative
synchronization; busy-waiting only keeps the head out of the station area
and saves travel time. `auto-enable` going low aborts the wait (error
`WAIT_ABORTED`) — the picker keeps holding its material for manual handling.

Sequences (normalized from the spec; all Z values = station `Z_PICK` +
`z-offset` pin; "settle" = the respective param):

**pick from tray** — validate tray has a matching slot; retract Z to movement
height; route XY to candidate slot; Z down; pos-settle; `close` := 1;
pick-settle; `opened` still high → error `PICKER_CLOSE_FAILED`; `closed`
high → slot physically empty: `close` := 0, wait `opened` (pick-settle
timeout), Z up, next candidate per §7.1 probing; success → `missing` := 0,
Z up.

**pick from proc** — validate `has-material`; busy gating (see above);
retract; route XY to station; Z down; pos-settle; `close` := 1; pick-settle;
`opened` high → error `PICKER_CLOSE_FAILED`; `closed` high → material
vanished, error `PROC_NO_MATERIAL`; `release` := 1, wait `released` high
(RELEASE_TIMEOUT → error `RELEASE_TIMEOUT`); `has-material` := 0; Z up;
`release` := 0, wait `released` low (RELEASE_TIMEOUT, D19).

**place to tray** — validate free slot; retract; route XY to free slot;
Z down; pos-settle; `close` := 0; pick-settle; `opened` low → error
`PLACE_FAILED`; release-time dwell; slot := process-step; Z up.

**place to proc** — validate `!has-material` (single-picker mode; §8 for
alternating); busy gating (see above); `release` := 1; route XY to station;
wait `released` high (RELEASE_TIMEOUT); Z down; pos-settle; `close` := 0;
pick-settle; `opened` low → error `PLACE_FAILED`; release-time dwell;
`has-material` := 1; Z up; `release` := 0, wait `released` low
(RELEASE_TIMEOUT, D19).

### 7.5 Error-id table (`errors.go`)

| Id | Name | Raised when |
|----|------|-------------|
| 0 | `NONE` | |
| 1 | `ESTOP` | estop during job |
| 2 | `MACHINE_OFF` | machine-on dropped / job started while off |
| 3 | `NOT_HOMED` | unhomed joints, AUTOHOME=0 |
| 4 | `HOMING_FAILED` | autohoming timeout/fault |
| 5 | `INVALID_ORIGIN` | unknown origin-id |
| 6 | `INVALID_DEST` | unknown dest-id |
| 7 | `INVALID_ROUTE` | reserved — currently unused (every tray/proc pairing is legal) |
| 8 | `INVALID_DEADZONE_SELECT` | selector out of range |
| 9 | `PLANNING_FAILED` | no route (start/goal blocked) |
| 10 | `INVALID_TRAY_ID` | tray-id pin matches no TRAYDEF |
| 11 | `TRAY_EMPTY` | no matching slot / probing exhausted |
| 12 | `TRAY_FULL` | no free slot |
| 13 | `PROC_NO_MATERIAL` | pick-proc precondition or material vanished |
| 14 | `PROC_HAS_MATERIAL` | place-proc precondition (single-picker) |
| 15 | `PICKER_CLOSE_FAILED` | opened still high after pick-settle |
| 16 | `PLACE_FAILED` | opened stayed low after release |
| 17 | `RELEASE_TIMEOUT` | proc `released` wait timed out |
| 18 | `MOTION_ERROR` | motctl/motstat comm failure or fault |
| 19 | `ALT_PICKER_SEQUENCE` | next job must originate from held station (§8) |
| 20 | `AUTO_DISABLED` | start-job while auto-enable is low (§6.4) |
| 21 | `WAIT_ABORTED` | busy-wait aborted by auto-enable going low (D15) |

---

## 8. Phase 6 — Alternating picker (`pickers=2`)

Targeting: to bring picker N over (x, y), command
(x − `picker.N.x-offset`, y − `picker.N.y-offset`); both pickers share Z (D10).

Behavior on **place to proc** when `has-material` is set: instead of erroring,
route picker 1 over the station, run the pick-proc sequence with picker 1
(including the release handshake), retract, then place picker 0's part
normally. The module records `altHeld = {stationID}` (persisted if
persistence is on).

While `altHeld` is set, the next job **must** use that station as origin
(else error `ALT_PICKER_SEQUENCE`). Such a job skips the physical pick —
the material is already in picker 1 — and executes its place sequence with
picker 1's offsets, then clears `altHeld`.

Manual interplay (§6.4, resolved O8): a manual open of picker 1 marks the
held material as removed but retains the station id; a following manual
close samples the picker feedback after pick-settle-time — material gripped
again (not fully closed) → the `altHeld` record is restored with the
retained station id; gripped nothing (`closed` high) → `altHeld` is cleared.

---

## 9. Phase 7 — Testing

- **Unit** (no HAL): direction-mode iterator, grid interpolation, slot
  search/probing, action selection, error mapping, persistence codec;
  `pkg/pnproute` fixtures + latency benchmark (Phase 1).
- **Integration**: `internal/pnptasktest` gomod (tasktest pattern) driving a
  sim config in `tests/pnptask/` — trivkins XYZ sim stack, pickers simulated
  with `timedelay`-based HAL logic (close → closed/opened feedback with
  configurable delay and scriptable "slot empty" injection via test pins).
  Scenarios: full pick→place cycle, empty-slot probing to tray-empty,
  autohome-on-first-job, busy-gated wait (incl. auto-enable abort),
  error+error-reset, estop mid-move, persistence restore,
  alternating-picker swap.
- **Latency check**: assert plan time < 100 ms in the integration run (D13).

## 10. Delivery order

Each phase is a separately reviewable PR against `add-pnptask`/`main`:

0. `pkg/hal` params (independent)
1. `pkg/pnproute` (independent)
2. module skeleton + config + pins (loads in sim, no motion)
3. machine control + config push + autohoming (moves in sim)
4. stations/trays/persistence
5. job engine, single picker, end-to-end sim green
6. alternating picker
7. integration suite + docs polish

## 11. Review resolutions and remaining points

Open points O1–O8 of the first draft were resolved in review (2026-08-10):
O1 → split confirmed (D2–D4); O2 → D17 (absolute TRAYDEF coordinates, no
station X/Y); O3 → D17 (tray-id change resets to empty); O4 → D16 (no
external abort); O5 → D15 (wait positions folded into proc stations with a
`busy` pin, replacing free-standing wait stations); O6 → D19; O7 → D18;
O8 → manual picker-1 handling in §8.

Remaining:

- **R1** Busy gating is also applied to pick-from-proc approaches (sampled
  at job start), not only before placing — confirm.
- **R2** Proposed job-start validation: picker 0 must be free (close output
  low, `opened` feedback high) before a pick action — a manually closed
  picker 0 holding material would otherwise look like a successful pick and
  double-pick. Would add error id 22 `PICKER_NOT_READY`.
