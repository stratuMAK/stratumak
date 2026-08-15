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
| D6 | Tray/station state persistence | Optional, via `persist_sqlite` GMI API. Enabled by load arg `persist_instance=<name>` (the spelling every other module uses for the persist API); **no default lookup** — absent arg means in-memory only. |
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
| D17 | Tray geometry | TRAYDEF `FIRST`/`LAST` are **absolute machine coordinates** (`LAST` optional — single-position tray), `LAST` being the *taught* position of the far corner slot (D24); tray stations have no X/Y of their own, only `Z_PICK` and pins. A `tray-id` change resets all slots to −1 (the startup state). |
| D18 | Manual jog | Only when homed — jog pins are ignored while unhomed. |
| D19 | Release handshake | At action end `release` := 0, then wait for `released` to go **low** (RELEASE_TIMEOUT applies). |
| D20 | Alternating pickers | Picker roles are not fixed: the free picker performs the next pick/removal, the picker holding the job's material places. Only **one** free picker is required for a pick action; per-picker held-material records replace the single `altHeld`. See the reference flow in §8. |
| D21 | Position teach | Per-picker `pos-x`/`pos-y` output pins report the picker's position (feedback position + picker offset), for UI display and manual position teaching: the user mounts material in a picker, jogs onto the target, and reads the station/slot coordinates off these pins. **Teaching into the INI uses the `-mu` siblings** (D26): the INI is written in machine units. |
| D22 | DXF shape rules | Convexity is the *only* shape rule (Phase 1, 2026-08-10). The prototype's extra horizontal/vertical edge requirement is dropped for both the outer limit and dead-zone polylines — with D7 in force it would have meant "axis-aligned rectangles only", and its dead-zone half was a stderr warning a library cannot emit. |
| D23 | DXF units | The dead-zone drawings are in **machine units**, like the INI — they describe the same coordinates as `FIRST_X`/`PROC X`. The loaded scene is scaled to mm when the planners are built; `CLEARANCE`, like every INI length, is already mm by then (converted at parse time — scaling it again would square the factor). Everything internal stays mm and every HAL float pin carries mm, the way milltask's halui publishes raw internal positions. |
| D24 | Tray geometry fit | The grid is built from its **step widths**, not interpolated between two taught corners: `slot(c,r) = FIRST + R(θ)·(COL_STEP·c, ROW_STEP·r)`. The tilt θ is **derived, never configured** — it is the angle from the corner the steps compute, `(COL_STEP·(COLS−1), ROW_STEP·(ROWS−1))`, to the taught `LAST−FIRST`, bearing on `FIRST`. A rotation cannot change a vector's length, so whatever separation is left between the rotated corner and `LAST` is the two descriptions disagreeing (a mistyped step, a mis-taught `LAST`, the wrong `ROWS`/`COLS`): more than `[PNPTASK]POS_TOLERANCE` of it fails the load. Steps are positive and at least 0.1 mm; which way the tray runs on the table is θ's business. A tray without `LAST` has nothing to fit and stays at θ = 0. |
| D25 | Homing request | Global `home` input pin, **rising edge**, machine on and no estop, accepted in *both* modes. §6.3's autohoming only fires at the first job (phase 5), which left `AUTOHOME = 0` machines with no way to home at all — jobs refuse with `NOT_HOMED` and the jog pins are ignored while unhomed (D18). Not gated on manual mode: a PLC that wants the machine homed before its first job should not have to drop `auto-enable` to ask. |
| D26 | Unit pins | Every float pin carries the internal **mm** (D23). Where a pin's value is meant to round-trip into the INI — which is written in **machine units** — a sibling pin with the `-mu` suffix carries the machine-unit value (phase 3 review: the teach pins `picker.N.pos-x-mu`/`pos-y-mu`; on a metric machine both pairs are equal). One-shot request pins (`home`, `error-reset`, `manual-open`/`-close`) are edge-triggered against their *startup* state: a level held high across a stmakd restart is not a new request. `machine-on` is the deliberate exception — it is the standing request for the machine, and holding it high across a restart re-enables. |
| D27 | Integration harness (2026-08-12) | **No test gomod.** Phase 7 uses a Python driver per scenario plus one shared simulation **cmod**, under the standard runtests harness. The tasktest-gomod pattern was inherited from milltask, whose surface is GMI; pnptask's whole surface is HAL pins (D12), so a gomod buys nothing pin driving cannot do — and a `@GOMOD:*@`-gated test module would make runtests depend on a build flag, while cmods compile unconditionally. The sim cmod owns the machine physics on the servo thread (gripper close→opened/closed with settling delay, fixture release/released, busy scripting, miss injection), its knobs as pins the driver flips between jobs; the Python side owns sequencing and assertions. |

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
                         persist_instance=persist
addf pnp.mot.motion-command-handler servo-thread
addf pnp.mot.motion-controller      servo-thread
```

Load args (parsed `key=value` like `internal/task/module.go`):

| Arg | Default | Meaning |
|-----|---------|---------|
| `motion_instance` | `[EMCMOT]MOTION_INSTANCE`, then `motmod` | motctl/motstat provider instance (INI fallback like milltask's) |
| `pickers` | `1` | `1` or `2`; `2` enables the alternating-picker logic |
| `persist_instance` | *(unset)* | persist API instance; unset = in-memory state only |

### 2.2 Source layout

```
src/stmak/pkg/hal/param.go              Phase 0: HAL param support (+ cgo.go additions)
src/stmak/pkg/pnproute/                 Phase 1: planner package (lifted from pnp-route-test)
    geom.go  plan.go  dxf.go  scene.go  scene_test.go plan_test.go testdata/
src/stmak/internal/motsetup/            Phase 3: shared INI -> motmod config push
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
src/hal/components/pnpsim.comp          Phase 7: the shared simulation cmod (D27)
tests/pnptask/                          sim config (ini/hal/dxf) + per-scenario
                                        Python drivers for runtests (D27)
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

## 4. Phase 1 — Route planner package `pkg/pnproute` *(implemented)*

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

**As built** (2026-08-10) — `doc.go geom.go dxf.go scene.go plan.go` plus
`geom_test.go scene_test.go plan_test.go` and `testdata/{cad_export,mixed}.dxf`:

```go
func LoadDXF(r io.Reader, opts ...LoadOption) (*Scene, error)   // WithArcSegments
func LoadDXFFile(path string, opts ...LoadOption) (*Scene, error)
func NewPlanner(s *Scene, clearance float64, opts ...Option) (*Planner, error)
                        // WithSegmentSamples / WithCoreErode / WithOffsetArcStep
func (p *Planner) Plan(start, goal Point) (*Route, error)
func (p *Planner) Metrics(r *Route) *Route     // fills Curv/MinRadius/MinClear on demand (§12)
func (p *Planner) CheckPoint(pt Point) error   // for the §5.1 position validation
func (p *Planner) Boundary() Polygon           // eroded limit, for UI/diagnostics
func (p *Planner) OffsetZones() []Polygon
func (p *Planner) Scene() *Scene; Clearance() float64; NodeCount(); EdgeCount() int
var ErrOutsideLimit, ErrInDeadzone, ErrNoRoute error   // all → PLANNING_FAILED (§7.5)
```

Deltas worth carrying into later phases:

- Shape rules follow D22: closed + convex, no axis-alignment requirement.
  Overlapping dead zones are allowed (shortest paths only bend at convex
  corners, so the individual offset rings still carry every usable corner).
- Offsets and arc discretization are deliberately **conservative**: corner arcs
  circumscribe the true offset and circles/ellipses circumscribe the drawn
  shape, so the achieved margin is never smaller than the requested clearance.
- The loader rejects rather than guesses: unsupported entities on a recognized
  layer, bulge (arc) polyline segments and mirrored-OCS entities are errors, so
  a drawn zone can never be silently dropped or shrunk. Old-style
  `POLYLINE`/`VERTEX` geometry is read as well as `LWPOLYLINE`.
- `Planner` is immutable after construction and safe for concurrent use.
- Measured on an i5-8250U: `NewPlanner` ≈ 20 ms (once per dead-zone file at
  startup), `Plan` ≈ 1 ms — two orders under the D13 budget. `go test` carries
  the 190-route battery, a shortest-path cross-check against the prototype's
  lazy-visibility Dijkstra, a median-latency assertion and benchmarks.

---

## 5. Phase 2 — Module skeleton, config, HAL interface *(implemented)*

### 5.1 INI schema

All sections resolve through `ini.WithNamespace(instanceName)`
(`[pnp.task:PNPTASK]` with fallback to `[PNPTASK]`). Standard `[TRAJ]`,
`[KINS]`, `[JOINT_n]`, `[AXIS_*]` sections are consumed for the motmod config
push exactly as milltask does (units: machine units in INI, **mm internally**).
`[KINS]JOINTS` is **required** here rather than defaulted: it decides which
joints are activated and which have to report homed.

The suffix of a `[PNPTASK_TRAYDEF_x]`, `[PNPTASK_TRAY_x]`, `[PNPTASK_PROC_x]`
or `[PNPTASK_ROUTE_x]` section is a **free-form name**, not an index — letters,
digits, `_` and `-`. Nothing downstream reads it: a station is identified by
its `ID` everywhere it matters (`origin-id`/`dest-id`, the route overrides, the
HAL pin names, the persisted slot state) and a tray definition is selected by
the value a `tray-id` pin carries. So a config may write
`[PNPTASK_TRAYDEF_MATERIAL]` or `[PNPTASK_PROC_COATER]` and describe the
machine in the machine's own words; the name is what the diagnostics quote
back. Numeric names still work — they are just names now, so they need not
start at 0 or run without gaps. The example below stays numbered because the
rest of this document refers to its stations by number.

```ini
[PNPTASK]
AUTOHOME = 1                  # home unhomed joints on first job
MOVE_HEIGHT = 30.0            # global Z movement height
CLEARANCE = 10.0              # planner clearance; must cover safety + BLEND_TOLERANCE
BLEND_TOLERANCE = 2.0         # TP term-cond tolerance for XY travel
POS_TOLERANCE = 0.1           # how far a computed position may sit from a
                              #   taught one before the load fails (D24)
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
LAST_X = 210.0                # *taught* position of slot (COLS-1, ROWS-1);
LAST_Y = 430.0                #   optional — omit for a single-position tray
                              #   (reject bin, transfer). It fixes the tray's
                              #   tilt (D24), it does not stretch the grid
COL_STEP = 10.0               # slot pitch along the column axis; required
ROW_STEP = 10.0               #   wherever that axis has more than one slot
DIR_MODE = C+R+~              # iteration order: C/R, +/-, optional ~ meander
MAX_UNPOPULATED = 3           # successive empty picks before tray declared empty

[PNPTASK_TRAY_0]              # tray *station* — no X/Y of its own (D17)
ID = 10                       # station id — unique across trays and procs
Z_PICK = 2.5                  # base pick Z (D10); z-offset pin adds to this
DEFAULT_TRAYDEF = 1           # optional: seeds the tray-id pin, for a station
                              #   that only ever holds this one geometry

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
`DIR_MODE`; a `DEFAULT_TRAYDEF` that no TRAYDEF's ID matches; every proc/wait
coordinate and every TRAYDEF slot position (absolute machine coordinates, D17)
must lie inside the eroded boundary and outside every offset dead zone **of
every configured dead-zone file**; `CLEARANCE > BLEND_TOLERANCE`; tray-def
grids with ROWS/COLS > 1 but no LAST, or no step width for such an axis; and
the D24 fit — the grid a TRAYDEF's step widths describe has to reach its taught
LAST within `POS_TOLERANCE`.

`DEFAULT_TRAYDEF` is a **pin seed**, not a fallback value: it is written to the
station's `tray-id` pin when the pins are exported (before the instance's `net`
lines, so a wired selector overwrites it and an unwired one keeps it), exactly
as a `halcmd setp` would. Defaulting the *value* instead would cost id 0 its
meaning — a PLC dropping its selector has to park the station, not silently
fall back to a tray (§7.1).

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
| `plan-time` | float | out | slowest route plan of the current/last job, seconds; reset at the `start-job` edge (phase 7 — D13's budget, made observable) |
| `error` | bit | out | latched error flag |
| `error-id` | u32 | out | error code (§7.5), 0 = none |
| `error-reset` | bit | in | rising edge clears error/error-id (D11) |
| `deadzone-select` | u32 | in | index into DEADZONE_FILE list, read per movement leg (§7.3) |
| `home` | bit | in | rising edge homes all joints (D25); machine on, no estop, both modes |
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
| `picker.N.pos-x` | pin | float | out | picker position, mm: feedback X + `x-offset` (D21/D23) |
| `picker.N.pos-y` | pin | float | out | picker position, mm: feedback Y + `y-offset` (D21/D23) |
| `picker.N.pos-x-mu` | pin | float | out | same position in machine units — the value to paste into the INI (D26) |
| `picker.N.pos-y-mu` | pin | float | out | same position in machine units (D26) |
| `picker.N.x-offset` | param | float RW | | XY offset vs. machine position (picker.0 default 0) |
| `picker.N.y-offset` | param | float RW | | |

**Per tray station (id from INI), prefix `tray.<id>.`**

| Pin | Type | Dir | Function |
|-----|------|-----|----------|
| `tray-id` | u32 | in | selects the TRAYDEF; change resets all slots to −1 (D17). Seeded from `DEFAULT_TRAYDEF` if the section has one, so an unwired selector can still name a geometry |
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

### 5.3 As built (2026-08-10)

`internal/pnptask/`: `module.go config.go stations.go pins.go` plus
`config_test.go stations_test.go module_test.go link_test.go`, registered in
`packages.conf` as `gomod internal/pnptask @GOMOD:PNPTASK_GO@` with the
matching `--enable-pnptask-go` configure flag (default yes),
`BUILD_PNPTASK_GO` and the `STMAK_BUILD_FLAGS` entry. Verified against a real
`stmakd` run: `load pnptask <pnp.task> pickers=2` exports the whole §5.2 tree
(48 pins, 7 params) and `halcmd show param` reads the INI-seeded values back.

- The HAL component is created in the **factory**, not in `Start`: the `net`
  lines that wire an instance run immediately after its load line. A factory
  that fails after creating the component exits it again — the launcher only
  tears down modules whose factory returned one.
- Load args are validated strictly: an unknown key, a missing `=`, `pickers`
  outside {1,2} or an empty instance name all fail the load. A mistyped
  `picker=2` would otherwise surface much later as a missing HAL pin.
- Config parsing is strict too — a malformed number, a missing required key or
  an unreadable `DEADZONE_FILE` fails the load rather than defaulting. Beyond
  the checks §5.1 lists, a section name must be a plain identifier (a header
  that came out as `[PNPTASK_TRAY_MATERIAL IN]` is a typo, not a station), the
  ids are unique across trays *and* procs, id 0 is refused (an unconnected u32
  pin reads 0), `LAST_X`/`LAST_Y` and `WAIT_X`/`WAIT_Y` are all-or-nothing,
  `ROWS`/`COLS` must both be 0 or both positive, an endless tray defines neither
LAST nor step widths, a route override must name
  known stations and may not repeat a pair, and `[TRAJ]COORDINATES` must carry
  at least X, Y and Z.
- Defaults for the optional keys: `AUTOHOME` off, `BLEND_TOLERANCE` and all
  three settle/release times 0, `MOVE_VEL`/`MOVE_ACC`/`Z_VEL`/`Z_ACC` 0
  (= use the `[TRAJ]` defaults), `RELEASE_TIMEOUT` 5 s, `HOME_TIMEOUT` 30 s,
  `MAX_UNPOPULATED` 1, `DIR_MODE` `C+R+`, `ROWS`/`COLS` 1 (single position).
  `MOVE_HEIGHT`, `CLEARANCE` and `POS_TOLERANCE` are required.
  A timeout defaulting to "forever" would turn a stuck fixture into a hung job
  with nothing on the error pin.
- Lengths and linear velocities convert machine units → mm at parse time
  (D23) — step widths and `POS_TOLERANCE` included; times stay seconds. The
  tray tilt is derived in radians and never read from the INI (D24).
- `Start` resolves the motion stack — `GetAPIFor("motctl"/"motstat",
  motion_instance, 1)`, wrapped in the generated clients — so a load line
  naming an instance no motmod provides, or one at another API version, fails
  startup instead of surfacing as a nil client at the first job. **This lookup
  belongs in `Start`, not in the factory:** the launcher runs every module's
  constructor (where a provider registers its API) before it starts any of
  them, so a motmod loaded on a *later* HAL line is registered by the time
  `Start` runs — verified by loading pnptask ahead of its own motion stack.
  Resolving it in the factory would instead impose a HAL-file ordering rule
  and fail on any config that did not happen to obey it. The pins go the other
  way, created in the factory because the `net` lines execute right after the
  load line, so the two halves of the module deliberately live in different
  lifecycle stages. Pushing the motmod configuration (§6.1) stays phase 3.
- Deferred per the phase split: the `errors.go` id table (§7.5, with the job
  engine). `error-id` exists and reads 0.

**Startup planner construction** (`planners.go`, added on top of the above):
every `DEADZONE_FILE` is loaded and its `Planner` built at load time, which
also completes the §5.1 validation — the geometric half needs the eroded
boundary and the offset zones, so it could not run before.

- The drawings are read in machine units and the scene is scaled to mm in
  place before `NewPlanner` (D23). The clearance is *not* scaled there — it
  is an INI length, already converted at parse time. Scaling the geometry
  rather than the query points keeps every later comparison — `CheckPoint`,
  `Plan`, the route it returns — in one unit system. A dead zone drawn as a
  circle carries `Center`/`Radius` that `NewPlanner` offsets analytically, so
  those scale with the polygon or the planner would guard a circle somewhere
  else entirely.
- Validated against **at least one** configured drawing, not all of them: a
  station may deliberately sit inside a dead zone of one scene and be reachable
  only in another (a fixture inside an enclosure that has to open first), and
  `deadzone-select` is how the PLC says which scene applies right now. A
  position usable in *no* scene is the one that can never be driven to, and
  that fails the load. Checked are each proc `X/Y`, each `WAIT_X/WAIT_Y`, and
  every tray slot — all of them, because a dead zone can sit inside a tray's
  footprint without touching a corner. The error names the INI section and
  keys, the coordinate, and one drawing that rejected it.
- This is *position* validation, not route validation: whether a given pair of
  stations has a collision-free route between them is only knowable per pair
  and stays a job-time `PLANNING_FAILED`.
- `TrayDef.SlotPos`/`SlotCount` (the D24 grid layout) land here because
  the slot check needs them. Slot *state* — the `[]int32`, the direction-mode
  iteration and the probing counters — stays in phase 4.
- Cost on the sim config: ~3 ms for a 36-node scene, once per file at load.

**Sim config** (`tests/pnptask/`): `pnptask.ini`, `pnptask.hal`, `zones.dxf`
and `zones_alt.dxf` — a 600×500 mm gantry with two pickers, two tray stations
(a 10×4 grid and a single-position bin), two process stations and two
dead-zone drawings, motion looped back and home switches always made. It is
the config phase 7's integration suite drives; what it exercises today is the
load path end to end (59 pins, 7 params, both drawings validated). The job
handshake and the picker/fixture feedback are deliberately unwired — those are
PLC signals, and simulating them is phase 7. The directory carries no
`test.hal`/`test.sh`, so `runtests` ignores it.

---

## 6. Phase 3 — Machine control *(implemented)*

### 6.1 motmod config push

At `Start()`, after `GetAPIFor("motctl"/"motstat", motionInstance, 1)`:
push `[JOINT_n]`/`[AXIS_*]` position/vel/acc/jerk limits,
`SetJointHomingParams` from `[JOINT_n]HOME_*`, `[TRAJ]` vel/acc defaults and
`SetupArcBlends` from `ARC_BLEND_*` — the same push milltask does in
`internal/task/config.go` / `inihal.go`.

Decided at implementation time (2026-08-10) in favour of the preferred option:
the push lives in a shared **`internal/motsetup`** package, extracted from
milltask in its own commit. `motsetup.Push(ini, Options, MotionConfig)` takes
the joint/spindle counts, the axis mask and the unit scale, and returns the
derived values the caller needs afterwards — the mm trajectory limits and the
per-joint/per-axis maxima for jog clamping and vel/acc blending — rather than
leaving each caller to re-derive them from the INI, which is how two unit
systems drift apart. `Options.NumSpindles = 0` skips the spindle push, which
is what pnptask passes. What stayed in milltask is what is task policy rather
than machine configuration: the canon's modal units, the interpreter startup
code, the tool-change position and the MDI queue depth.

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

The same sequence is what the `home` pin runs (D25) — that one homes on an
explicit request and so ignores `AUTOHOME`, which governs only whether a *job*
may home the machine on its own.

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
- **Position teach (D21):** `picker.N.pos-x`/`pos-y` continuously report
  `GetPosFb()` + the picker's offset params, updated in the module's poll
  loop (in all modes, not just manual). Teach workflow: mount material in a
  picker, jog it onto the target position, read the coordinates off these
  pins (UI display) and enter them as station/TRAYDEF values in the INI.
  Consistent with the targeting convention in §8: command = target − offset,
  hence picker position = feedback + offset.
- Manual intervention can invalidate the tracked world state (slot states,
  `has-material`); the operator resyncs via `set-full`/`set-empty`.
  Manual picker-1 changes update the `altHeld` record (§8).


### 6.5 As built (2026-08-10)

`internal/pnptask/machine.go` (the control loop) and `errors.go` (the §7.5 id
table), plus `machine_test.go`; `internal/motsetup/` for §6.1. Verified against
a real `stmakd tests/pnptask/pnptask.ini` run: the machine enables itself from
the held `machine-on` level, homes on a `home` pulse, jogs under `auto-enable`
low with `picker.N.pos-x` tracking the motion (`122` on X, `162` on picker 1
with a 40 mm offset param), stops a Y jog at the pushed soft limit, releases a
manually closed picker on estop, and refuses to come back until `machine-on`
is cycled.

- **One goroutine owns everything.** The module runs a single 100 Hz control
  loop; no state in it is locked because nothing else touches it. Long
  operations (the enable handshake, homing, and the job actions of the later
  phases) do not run *beside* the loop — they run *inside* it, ticking the
  same cycle through `waitUntil`, so every one of them re-samples the inputs
  and re-runs the abort check every 10 ms. That is what makes "an estop ends
  any wait" structural rather than something each wait has to remember.
- **Every input is sampled once per cycle** into one snapshot, and the
  decisions of that cycle read only the snapshot. The first version read the
  jog pins live inside the jog handler while `auto-enable` came from the
  snapshot, and the race detector duly caught a cycle that jogged in auto
  mode: it had a stale `auto-enable` and a fresh jog pin.
- **Clearing estop does not re-enable the machine.** `machine-on` has to be
  cycled: a level still high from before the estop is not a request to start
  moving again. A level already high at *startup*, on the other hand, is —
  the first cycle compares against a zeroed previous reading, so a PLC that
  holds `machine-on` across a stmakd restart gets its machine back.
- **`machine-is-on` is only set once motion reports itself enabled.** The PLC
  interlocks on that pin; setting it on the `Enable()` ack would advertise a
  machine that motion refused (a tripped limit, say). An enable that never
  confirms disables again and raises `MOTION_ERROR`.
- Faults are latched **first-error-wins**: a follow-up caused by the first one
  must not overwrite the cause the operator needs. `error-reset` clears the
  latch and the per-picker `missing` flags with it.
- Homing watches for the sequence *stopping* as well as for the timeout, so a
  joint that faults or never makes its switch fails now rather than one
  `HOME_TIMEOUT` later.
- Motion mode is managed per operation: free to home, teleop to jog, coord to
  rest (and, in phase 5, to move). The switch is re-sent every cycle while
  waiting, because motion rejects one while it is not in position.
- `Start` splits at the C ABI boundary: it resolves the callback tables and
  then calls `startControl`, which pushes the configuration and starts the
  loop through the `motionControl`/`motionStatus` interfaces. That split is
  what lets the whole state machine be unit-tested against a scripted motion
  stack — a fake provider's callback table cannot be called through cgo.
- On `Stop` the loop aborts any jog, aborts and disables motion, but leaves
  the picker outputs alone (D14): whatever is held stays held.

---

## 7. Phases 4/5 — Stations, trays, job engine

### 7.1 Tray model (`stations.go`)

Slot state per tray station: `[]int32`, `-1` = empty, `0` = unprocessed,
`>0` = processed at that step. Grid positions: `COL_STEP`/`ROW_STEP` stepped
by (col, row) index and turned by the tilt the load derived from the taught
LAST, bearing on FIRST (D24); all coordinates are absolute machine coordinates
— tray stations have no X/Y of their own (D17). A missing LAST makes a single-position tray (always
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

If `persist_instance` is set: `open(namespace)` at Start with namespace =
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
  Planner selected by `deadzone-select` **as read when the leg starts**, not as
  latched at job start (invalid index → error on that leg): the selector
  describes the machine now, and the machine can change under a job — a job
  waits out a busy station at its wait position, the enclosure around it opens,
  the PLC selects the other drawing and clears busy, and the leg into the
  station is planned against the scene that now applies. Like the picker
  offset it is one snapshot per leg, never per waypoint.
  Movement height = global MOVE_HEIGHT unless a `[PNPTASK_ROUTE_n]` override
  matches the latched (origin, dest) pair. If the current Z is below the
  movement height (e.g. after an aborted job), the job starts with a Z retract.

### 7.4 Job state machine (`job.go`, `actions.go`)

`IDLE → LATCH → VALIDATE → [HOME] → PLAN → EXECUTE → FINISH/ERROR`

- LATCH on `start-job` rising edge: origin-id, dest-id, process-step.
  `deadzone-select` is deliberately not latched — it is range-checked in
  VALIDATE and read again per movement leg (§7.3).
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
`PLACE_FAILED`; **slot := process-step** (the records commit at the `opened`
confirmation — the physical commit point, since an open picker cannot take
the part back; an abort during the dwell must find a world that already says
where the part is); release-time dwell; Z up.

**place to proc** — validate `!has-material` (single-picker mode; §8 for
alternating); busy gating (see above); `release` := 1; route XY to station;
wait `released` high (RELEASE_TIMEOUT); Z down; pos-settle; `close` := 0;
pick-settle; `opened` low → error `PLACE_FAILED`; **`has-material` := 1**
(records at the `opened` confirmation, as above); release-time dwell; Z up;
`release` := 0, wait `released` low (RELEASE_TIMEOUT, D19).

**Release cleanup (phase 4/5 review):** no error path may leave a station's
`release` request standing — every proc action withdraws it on the way out of
a failed sequence (without waiting for feedback), and estop/shutdown drive all
`release` outputs low alongside the picker releases. A pick-from-proc whose
release wait fails *after* a confirmed grip opens the picker again: the
fixture never confirmed letting go, so the part belongs to the fixture and
`has-material` stays true.

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
| 22 | `NO_FREE_PICKER` | pick or swap needed but no picker is free (§8) |
| 23 | `PICKER_OPEN_FAILED` | close withdrawn on the pick side but `opened` never came back (§12) |

### 7.6 As built — the model (2026-08-11)

`internal/pnptask/stations.go` (the runtime half, on top of the phase-1 grid
geometry) and `persist.go`, plus `stations_test.go`/`persist_test.go`. The world
model — tray contents, process-station occupancy, per-picker held material — is
built in the factory next to the pins it publishes on, seeded from those pins
and from persistence when the control loop starts, and owned by the control
goroutine from then on like everything else in §6.5.

- **Slot states are `int64`, not the design's `int32`.** The step comes off a u32
  pin, so with `int32` a step above 2^31 would wrap negative and 2^32−1 would
  collide with the −1 that means "empty" — a PLC word read as "this slot is
  free". Four bytes per slot is not worth that.
- **The DIR_MODE order is precomputed** per tray geometry as a permutation of the
  linear slot indices, once at load. That is also what makes the probing
  correction of D9 cheap to express: "continue after the slot that turned out to
  be empty" is a position in a list rather than a resumed nested loop. Meander
  flips on the *pass number*, not the geometric row index, so a pass always ends
  where the next one starts whichever end the secondary direction started from.
  Places take the first free slot **in the same order** — a place that ignored it
  would scatter material across a tray a later pick then travels back and forth
  over.
- **A tray with no geometry reports neither `empty` nor `full`.** tray-id 0 (the
  value an unwired u32 pin reads, and the reason config.go refuses station id 0)
  means "not told yet": there is no slot state to report on, and a pin that
  guessed either way would send a PLC refilling a tray or diverting production.
  The actionable signal for that state is the `INVALID_TRAY_ID` a job raises.
- The `empty` pin is measured against the **live** `process-step` pin, not a
  job's latched copy: between jobs there is no latched step, and this pin is what
  the PLC reads to decide what to command next. `set-full` uses the same live
  value (D8).
- `set-full`/`set-empty` join `home`, `error-reset` and the manual picker pins as
  edges primed against their startup state (D26) — a level latched across a
  stmakd restart would otherwise wipe or forge the slot state that was just
  restored. `tray-id` is primed for the mirror-image reason: `world.start` has
  already selected the geometry it names, so treating the boot value as a
  *change* would reset what the restore just installed.
- **The held-material records are per picker from the start** (D20), and the
  engine asks `freePicker()`/`holderOf(station)` rather than indexing 0.
  Estop clears them all: the close outputs have just dropped (D14), so whatever
  was gripped is on the table and the records would be fiction. A manual open
  clears the one picker's record for the same reason; §8's refinement (retain the
  station id so a following manual close can restore it) belongs to phase 6.
- **Persistence** (`persist.go`, D6): namespace = instance name with everything
  outside `[A-Za-z0-9_]` replaced (`pnp.task` → `pnp_task`), keys `tray.<id>`,
  `proc.<id>` and `held_material`. The last one is the design's `alt_picker`
  renamed after what D20 made it store — one record per picker, each naming its
  picker explicitly so a load line changed between `pickers=1` and `pickers=2`
  cannot shift a record onto the wrong picker.
  - Writes are **coalesced per control cycle** rather than issued per assignment:
    the several changes of one pick cost one write, and nothing is ever more than
    10 ms behind. The flush hangs off `tick`, not `step`, so it keeps running
    through the long actions of a job.
  - A storage failure is logged and swallowed. The machine's state is the tracked
    model, not the database; losing a write costs state at the next restart,
    which is strictly better than aborting a cycle with a part in the picker over
    a locked sqlite file. Failing to *open* the namespace does fail `Start` —
    that one was asked for on the load line.
  - The probing state (the successive-miss counter and the "declared empty by
    probing" flag) is deliberately **not** persisted, as §7.2's record shape
    implies: it is a conclusion drawn from physical feedback, and re-deriving it
    costs one probing pass, while restoring it could leave a refilled tray
    declared empty with nothing in the record to justify it.
  - A record is only adopted for the geometry it was written under, and only if
    it still fits it: an unknown TRAYDEF or a changed slot count discards it
    rather than stretching stored slots over a grid they no longer describe.
  - **One refinement over §7.2:** a `tray-id` pin reading 0 at restore time is
    treated as "not told yet", not as a mismatch. The PLC that drives that pin
    has typically not written it when stmakd starts, so comparing against it
    would discard the tray state at every single restart — the one thing
    persistence exists to prevent. The record is held pending and adopted the
    moment `tray-id` names the geometry it was recorded under; any *other* id
    discards it immediately, as specified.

### 7.7 As built — the engine (2026-08-11)

`internal/pnptask/motion.go job.go actions.go` plus `job_test.go`, wired into the
§6.5 control loop: a job runs *inside* it, ticking the same cycle through every
wait, so estop and machine-off end it wherever it stands without any action
having to remember that. Verified against a real `stmakd tests/pnptask/pnptask.ini`
run: `set-full` fills the component tray, a `start-job` for 10 → 20 autohomes the
machine and completes the pick/place cycle (`has-material` set, a slot consumed,
`error-id` 0), and 20 → 11 empties the press into the single-position output tray.

- **Nothing is addressed as "picker 0".** The pick asks `world.freePicker()`, the
  job records which picker came away holding the material, and the place uses
  that record with that picker's offsets (D20). `TestJobUsesTheHoldingPicker`
  runs a whole job on picker 1 — offsets applied, picker 0 untouched — on a
  `pickers=2` instance where picker 0 was loaded by hand. Phase 6 adds the
  *decisions* (skip the pick when a picker already holds the origin's material,
  swap at an occupied dest) without moving any of this.
- **One frame, converted once.** Station coordinates, tray corners and the
  dead-zone drawings all describe where a *picker* is; `commandFor` subtracts the
  offset at the last step before a move is dispatched (§8), and route planning
  and station geometry never learn which picker is moving.
- **The eroded outer limit has to contain wherever the machine can be parked.**
  A route's start point is checked like any other (`Plan` → `ErrOutsideLimit`), so
  a machine homed at the corner of its axis range has no valid start and its
  first job dies with `PLANNING_FAILED`. That is the drawing's job to prevent:
  `tests/pnptask/zones.dxf` now draws the envelope wider than the axis soft
  limits (−20..620 × −20..520 against 0..600 × 0..500) so the 12 mm erosion still
  covers the whole reachable range. Documented in the sim README and covered by
  `TestJobPlanningFailed`.
- **The commanded position is tracked, not re-read.** A route's segments are
  dispatched back to back so the TP can blend them, so status still describes the
  first while the last is queued; `cmdPos` carries the module's own notion and is
  re-anchored from `GetPosCmd` once per job, because a manual jog moves the
  machine without going through motion.go.
- **A zero settle time still costs one control cycle.** Every dwell is followed by
  a check of the picker or fixture feedback, and that feedback comes out of the
  input snapshot — returning without ticking would have the check judge a sample
  taken before the command it is checking, so a machine with the settle times
  left at 0 would fail every pick with `PICKER_CLOSE_FAILED`.
- **`start-job` is armed, not edge-detected.** It has to be seen low before it
  counts as a request: a level latched across a stmakd restart is not a job
  (D26), and a pin linked to a signal the PLC keeps driving high reads high again
  on the cycle after the module clears it — still the same request.
- **`start-job` is sampled before the parameters it carries.** The PLC writes
  origin-id/dest-id/process-step/deadzone-select and *then* raises the request;
  this loop is not synchronised with whatever writes those pins, so reading the
  request first is what guarantees a job is never latched with the previous job's
  parameters.
- **Rising edges are latched, not per-cycle.** This was a real bug the job engine
  exposed: the long operations tick the loop themselves, so they advance the edge
  detector many times before `step()` looks again, and any button pressed during a
  job or a homing sequence was sampled by a nested tick and silently gone. Edges
  now accumulate and each consumer clears the one it acts on; auto mode
  *discards* the manual-control edges rather than storing them up (§6.4). The
  tray-id selector is compared against the geometry the model has actually
  selected, for the same reason.
- **The diagnosis is published before the handshake completes.** `busy` low with
  `start-job` cleared is the PLC's cue to look at `error-id`, so the error pins
  are written first.
- **A job is refused while an error is latched.** Faults latch first-error-wins
  (§6.5), so a job started with the latch set could fail with nothing to show for
  it — its id would be swallowed by the one already there. `start-job` is cleared
  with the original error still on the pin, which says exactly what happened, and
  `error-reset` is what the PLC owes before the next job.
- Homing runs *after* validation: a job naming an unknown station is refused
  without the machine having moved first.
- `has-material` and the held record are written together and before the retract,
  so a job aborted mid-lift leaves a world that still says where the part is. A
  pick from a proc station that finds the fixture empty clears `has-material` on
  its way to `PROC_NO_MATERIAL`: the next job must not be sent for a part that is
  not there.
- The picker-open wait of the probing retry reports `PICKER_OPEN_FAILED` (23;
  §12 — this reverses the first build's reuse of `PLACE_FAILED`): both callers
  of that wait are on the pick side, where "opened stayed low" is a jammed
  gripper at the *origin*, and a PLC keyed on the documented place-side id
  would send the operator to inspect the wrong station.
- **The tray reset requests survive a job.** A `set-full` pressed while a job runs
  is applied as soon as the loop is back to its own decisions — never in the
  middle of a slot search, and never dropped.

---

## 8. Phase 6 — Alternating picker (`pickers=2`) *(implemented)*

Targeting: to bring picker N over (x, y), command
(x − `picker.N.x-offset`, y − `picker.N.y-offset`); both pickers share Z (D10).

The pickers are symmetrical (D3); roles are **not** fixed (D20). The module
tracks a held-material record per picker: `held[N] = none | {stationID}`
(persisted if persistence is on). Reference flow (from the concept review;
"picker A/B" because either physical picker can take either role):

1. Both pickers empty. Job tray→procA: picker A picks from the tray.
2. At procA (occupied): picker B removes procA's finished part — `held[B] =
   {procA}` — then picker A places its part. Next job must originate at procA.
3. Job procA→procB: physical pick skipped (picker B already holds procA's
   part). At procB (occupied): picker A removes procB's part — `held[A] =
   {procB}` — then picker B places. Next job must originate at procB.
4. Job procB→tray: pick skipped (picker A holds it); picker A places the
   processed part into a free tray slot. Both pickers empty again.

Rules:

- **Pick phase**: if a picker holds material from the job's origin station,
  the physical pick is skipped and that picker becomes the placer.
  Otherwise a free picker (picker 0 preferred when both are free) runs the
  pick sequence at the origin with its own offsets.
- **Swap at an occupied dest proc** (`has-material` set): the other, free
  picker first removes the occupant — pick-proc sequence with its offsets;
  the `release` handshake stays asserted from the removal through the
  following place (no re-clamp in between) — and records `held = {dest}`.
  Then the placer places the job's material; `has-material` stays true.
- **Place phase**: always executed by the picker holding the job's
  material, with its offsets.
- **Sequence constraint**: while any picker holds swap-removed material,
  the next job must use that station as origin (error
  `ALT_PICKER_SEQUENCE`).
- **Free-picker requirement**: a physical pick or a swap needs *a* free
  picker at that moment — any one, not a specific one (resolved R2). None
  free → error `NO_FREE_PICKER`. A well-formed job sequence can never hit
  this; it guards against manual intervention.

Manual interplay (§6.4, resolved O8): a manual open of a picker with a held
record marks the material as removed but retains the station id; a
following manual close samples the picker feedback after pick-settle-time —
material gripped again (not fully closed) → the record is restored with the
retained station id; gripped nothing (`closed` high) → the record is
cleared.

### 8.1 As built (2026-08-12)

No new file: the phase is the three §8 decisions added to `job.go`
(`validateJob`/`checkDest`/`runPick`), the swap sequence in `actions.go`, the
retained-record half of `manualPickers` in `machine.go` and the record shape in
`stations.go`/`persist.go`, plus `altpicker_test.go`. Phases 4/5 had already put
the per-picker held record and `freePicker`/`holderOf` in place (§7.7), so this
adds *decisions* rather than plumbing — no call site learned which picker it is
working with. Verified against a real `stmakd tests/pnptask/pnptask.ini` run:
the §8 reference flow end to end on the sim's two process stations
(`10→20`, `10→20` swapping, `20→21`, `10→20`, `20→21` swapping, `21→11`), the
`ALT_PICKER_SEQUENCE` refusal in between, and the manual open/close round trip
restoring a swap record.

- **`removeFromProc` is the pick-from-proc sequence, shared with the swap.** The
  two differ in exactly two places: the busy gating (the swap's station was
  already gated by the place it belongs to) and what happens to the release
  request at the end. Writing the swap as its own sequence would have meant two
  copies of the grip/unclamp/lift order, which is the part of this module a
  divergence would be most expensive in.
- **The fixture is opened once for both halves of a swap.** The removal leaves
  `release` standing (§8: "no re-clamp in between") and the place's own
  `requestRelease` is then a no-op whose `waitReleased` returns at once. A clamp
  cycling shut on an empty nest and open again is wasted cycle time and one more
  chance for the fixture to fail. The test asserts the *count* of release
  rises over the job, which the fake fixture cannot miss: the module waits for
  `released` to follow every change it makes.
- **`has-material` does drop between the removal and the place**, for the few
  hundred ms they span, although §8 says it "stays true". Net-effect-true would
  have meant lying about the window: the station really is empty in it, and an
  estop landing there has to leave a model that says so — the removed part is in
  a picker, and the next job must not be sent to a fixture for it. The pin is
  the module's own model of the station, not a PLC handshake, and the PLC is not
  making decisions off it while `busy` is high.
- **The free-picker requirement is counted, not asked.** A job into an occupied
  process station needs two free pickers — one to carry its material, one to
  take the occupant out — so `checkPickers` compares `freeCount()` against what
  the job's plan needs. Asking `freePicker()` twice, once per leg, would have
  discovered the second answer at the swap, with a part already in the air.
- **The sequence constraint keys off the swap flag, not off "a picker is
  loaded".** Only swap-removed material has nowhere else to be — its station is
  running its process on the piece that replaced it. Material left in a picker by
  an aborted job (a `WAIT_ABORTED`, a machine-off) carries no such obligation,
  and with two pickers the other one can keep working. The flag is persisted
  (`"swap"` in the held record), because a restart that forgot which record was a
  swap would let the next job strand the part indefinitely.
- **§8's first rule is not gated on `pickers=2`.** A single-picker machine
  reaches it too, and there it is the recovery path the design otherwise lacks: a
  job aborted after its pick leaves the material in the picker, and re-commanding
  the same job now completes it instead of refusing with `NO_FREE_PICKER`. An
  occupied *destination* stays `PROC_HAS_MATERIAL` on such a machine (§7.4) —
  with one picker there is no second one to take the occupant out, and the
  refusal says what the operator has to do about it.
- **A skipped pick has no origin preconditions.** The job never goes near the
  station, so an empty tray or a `has-material` that has since been cleared is
  not its problem — the material it carries is already in a picker.
- **The manual close is judged, not assumed.** A retained station id is restored
  only if the gripper feedback says the picker closed onto something; fully
  closed means it gripped nothing and the operator has the part. A picker that
  never actuated at all decides nothing — the judgement RE-ARMS (warned once)
  until the gripper answers, because the close output is still standing over a
  retained part and abandoning the question would leave a loaded picker the
  engine believes free the moment the gripper does close. The countdown hangs
  off the control loop rather than blocking it, cancels itself whenever the
  record it judges is gone, and estop cancels it along with the records (D14).
- **Retention is a reservation, not a memory** (phase-6 review, 2026-08-12;
  this reverses the first build's "leaves the picker free" rule). A retained
  record is a manual intervention in progress: the part is in the operator's
  hands and the next close decides where it went. Until then the picker counts
  occupied, **every job is refused** (with a message naming the picker and the
  pending close), the swap obligation stays armed, and the record — with its
  station and swap flag — **persists across a restart** (restored retained,
  close output left low, the operator's close still judges). A second open
  press is idempotent; a close that grips nothing clears the record AND
  reopens the picker (phase-7 review — jaws left commanded shut on a free
  picker would make the next job's grip check read "closed" whatever sits
  under the head). The free-the-picker workflow is therefore three presses:
  open, take the part, close on the empty gripper — the verdict opens it
  again itself.
- **The sequence constraint accepts any standing swap obligation.** Normally
  one exists; a place that fails after its swap-out leaves two (both parts
  really are in pickers), and a job from either station is the recovery. A
  skipPick job into its own occupied origin — a self-exchange that would
  ping-pong two parts forever — is refused; putting a part back is valid once
  the station is free. The *non*-skipPick station-to-itself job stays allowed
  on purpose (phase-7 review): it is the **re-seat operation** — pick the part
  out, put it back down — the one way a PLC can re-clamp a part without a
  second station, and a bug looping it cycles one part in place without losing
  anything.
- **skipPick matches the material, not just the station** (phase-7 review).
  Held records carry the process step the picking job declared; a skipPick
  whose latched step mismatches a known step is refused — matching by station
  alone silently delivered a step-0 part as step-3 material and corrupted the
  tray model behind it. A swap's removed occupant has an unknown step (the
  model never tracked what the earlier place put there) and is exempt: the
  obligated carry-away job runs whatever step the PLC declares. The step (and
  its known flag) persists with the record.

---

## 9. Phase 7 — Testing *(implemented)*

- **Unit** (no HAL): direction-mode iterator, grid interpolation, slot
  search/probing, action selection, error mapping, persistence codec;
  `pkg/pnproute` fixtures + latency benchmark (Phase 1).
- **Integration** (D27, decided 2026-08-12 — replaces the first draft's
  `internal/pnptasktest` gomod): per-scenario **Python drivers plus one shared
  simulation cmod** (`pnpsim`), run by the standard runtests harness over the
  sim config in `tests/pnptask/`. pnptask's whole surface is HAL pins, so the
  driver commands jobs over the handshake pins and asserts pin outcomes; the
  sim cmod provides the machine physics on the servo thread — gripper
  close → opened/closed feedback with a settling delay, fixture
  release/released, busy scripting, and "next close grips nothing" miss
  injection, all as pins the driver flips between jobs. A test gomod would
  gate runtests on a `@GOMOD:*@` build flag; cmods compile unconditionally.
  Pin access from the driver: `halcmd` invocations, or — the pattern the
  existing tool tests use (`tests/rsh2gmi.py`, `lib/python/stmak_test.py`) —
  the generated **GMI REST client** against the rest-exported `halcmd` API.
  There are no Python HAL bindings and no userspace comps in stratuMAK: every
  HAL component lives inside stmakd, which is exactly why the simulation half
  is a cmod and the driver never owns pins of its own.
  The exhaustive logic coverage stays in the Go unit tests with the
  scripted motion stack; this level exists to exercise the REAL stack:
  motmod/TP/homemod in RT, real persist_sqlite, real HAL wiring.
  Scenarios: full pick→place cycle, empty-slot probing to tray-empty,
  autohome-on-first-job, busy-gated wait (incl. auto-enable abort),
  error+error-reset, estop mid-move, persistence restore,
  alternating-picker chain (the §8 reference flow), manual-handling
  retention round trip (§8.1).
- **Latency check**: assert plan time < 100 ms in the integration run (D13).

### 9.1 As built (2026-08-12)

`src/hal/components/pnpsim.comp` (the simulated field devices), the rewired
`tests/pnptask/` config with `pnpdrv.sh`/`pnpdrv.py`, and nine scenario
directories under it — `cycle probing homing busy errors estop persist
altpicker manual` — each a standard runtests test (`test.sh` + a Python driver
+ `expected`). One addition to the module itself: the `plan-time` out pin
(§5.2), because D13's budget was the one thing §9 asks for that nothing
published.

The scenario list above maps one-to-one onto those directories with one split:
autohome-on-first-job is asserted in `cycle/`, where the first job homes the
machine on its own, because a machine can only be unhomed once per start;
`homing/` takes the other half — the explicit `home` request of D25 and the
manual mode that needs it.

- **The scenarios are separate runtests directories, all driving one config.**
  Per-directory pass/fail is what makes a failing suite readable, and stmakd
  chdirs to the INI's directory, so a scenario in `tests/pnptask/<name>/`
  running `stmakd ../pnptask.ini` shares the trays, the drawings and the
  persistence db with all the others. Wiping that shared state is therefore
  `pnpdrv.sh`'s job, not runtests' per-testdir `rm -rf db`.
- **`pnpsim` carries one gripper and one fixture per instance**, so the sim's
  two pickers and two process stations are two instances. Not because they
  belong together physically — they do not — but because a comp per device
  would have been two comps for four pins each, and a comp with counts would
  have needed the personality-nibble encoding of `logic` to say "two of one and
  two of the other". Unused halves stay unlinked and cost nothing.
- **The gripper is the only part of the machine that had to be simulated
  rather than wired.** Everything else the sim needs is a HAL primitive or a
  pin the driver writes: `busy` is an unconnected input, the position loop-back
  is a `net`, home switches are `setp`. What no wiring can express is the
  difference between closing onto a part and closing onto nothing — the D9
  probing correction, and the reason a pick-and-place task exists. It is
  scripted with `gripper.miss-count`: N misses, or −1 for all of them, reloaded
  whenever the value changes.
- **Feedback settles a `settle-time` after the command.** Looping the command
  straight back — what the phase-2 config did — would let a machine with
  PICK_SETTLE_TIME at 0 pass tests a real one fails (§7.7's "a zero settle time
  still costs one control cycle" is exactly that bug from the other side).
- **The drivers reach pins over the halcmd REST API, not by forking halcmd.**
  A fork costs tens of milliseconds and these drivers poll in loops;
  `GET /pins?pattern=pnp.task.*` also returns the whole tree as *one* sample,
  which is the only way to assert on several pins that are changing together.
  `stmak_test.wait_until` still supplies the deadline discipline (and
  `STMAK_TEST_TIMEOUT_SCALE`); what could not be reused is
  `stmak_test.getp`/`wait_pin`, which read *signals* via `halcmd gets` — most
  pnptask pins are unlinked, and `wait_ready` cannot ask a GMI status buffer
  pnptask does not have. Readiness here is `machine-is-on` going high.
- **`start-job` is driven low before every job, not just raised.** It is armed
  rather than edge-detected (§7.7), and the module clearing it at the end of
  the previous job is not enough: a driver that raises it again inside the same
  10 ms control cycle is a job the module never sees.
- **A skipped pick is proved by making the gripper unable to pick.** "The
  material was already held, so no pick happened" has no pin to read; setting
  `miss-count = -1` for the job gives it one — a job that approached the tray
  would walk it to TRAY_EMPTY instead of completing. The same trick shows the
  restored held record in `persist/` is real.
- **The persistence scenario runs three servers**: one that does the work, one
  restarted on the state it left, and one started with the state wiped. Without
  the third, every assertion in the second would also pass on a machine that
  simply comes up that way.
- **`plan-time` reports the slowest plan of a job, not the latest.** A job
  plans one route per leg, so a pin carrying only the last one cannot be
  sampled from outside — the interesting number is the worst case, and it is
  reset at the `start-job` edge so it always describes one job. Measured on the
  sim config: ~10 µs per plan, four orders under D13's budget.
- Assertions print one `PASS <label>` line each and the directory's `expected`
  file is the list of them, so a diff names the step that stopped rather than
  a boolean. `done()` refuses to sign off a scenario that asserted nothing.

## 10. Delivery order

Each phase is a separately reviewable PR against `add-pnptask`/`main`:

0. `pkg/hal` params (independent) — **done** (#10)
1. `pkg/pnproute` (independent) — **done**
2. module skeleton + config + pins (loads in sim, no motion) — **done**
3. machine control + config push + autohoming (moves in sim) — **done**
4. +5. stations/trays/persistence **and** the job engine (single picker,
   end-to-end sim green) — **done**; implemented together (decided 2026-08-11): the
   model/engine seam — slot search order, probing corrections (D9), slot
   marking, `has-material` transitions, persistence write points — is where
   the design risk sits, and the model's only meaningful end-to-end
   verification is the engine consuming it; both also share the scripted
   picker-feedback harness. Kept as **two commit series** on one branch
   (model + persistence first, then the engine) so the pre-merge review can
   run in two passes, and the model half stays independently mergeable if it
   stabilizes early. The engine must use the per-picker held-record structure
   of D20 wherever it means "the picker holding the job's material" — no
   hardcoded picker 0 — so phase 6 only adds behavior.
6. alternating picker — **done**
7. integration suite + docs polish — **done**

## 11. Review resolutions and remaining points

Open points O1–O8 of the first draft were resolved in review (2026-08-10):
O1 → split confirmed (D2–D4); O2 → D17 (absolute TRAYDEF coordinates, no
station X/Y); O3 → D17 (tray-id change resets to empty); O4 → D16 (no
external abort); O5 → D15 (wait positions folded into proc stations with a
`busy` pin, replacing free-standing wait stations); O6 → D19; O7 → D18;
O8 → manual picker-1 handling in §8.

R1 and R2 were resolved in the second review round (2026-08-10): R1 —
confirmed; normally the PLC only issues a job once the station is done, the
gating stays for completeness. R2 — rejected as stated: with alternating
pickers only *one* picker must be free for a pick action; superseded by the
generalized per-picker model and the `NO_FREE_PICKER` guard (D20, §8).

No open points remain (§12 carries the post-review TODO list).

---

## 12. Final pre-lab review (2026-08-13)

A whole-branch review (conceptual pass + verified multi-agent implementation
pass) before the first lab-machine run: 13 confirmed findings. Ten are fixed on
the branch; the rest are the TODOs below.

### 12.1 Fixed

- **Motion self-disable watchdog** (`machine.go checkMotionEnabled`): motion
  reporting itself disabled while the module believes the machine on — a
  following error, an amp fault, a hard limit, an external enable drop, all of
  which latch in motmod — now drops `machine-is-on`, latches `MOTION_ERROR`
  and aborts a running job at the next wait cycle. Before, the trip surfaced
  only whenever the next `SetLine` happened to be refused, with the PLC
  interlock pin still high and a running job parked in a timeout-free motion
  wait. Debounced over 3 fresh status reads, the same defense in depth as
  milltask's `monitor.go`. `machine-on` must be cycled afterwards, exactly
  like after an estop.
- **Comm-outage presses are dropped, not deferred** (`machine.go motionLost`):
  the rise latches (machine-on, home, manual picker) and the pending tray
  resets are cleared every cycle the outage lasts — a manual-close pressed at
  a machine that looks dead must not snap the gripper shut minutes later when
  comm recovers. Same rule as the estop teardown; error-reset survives for the
  same reason it survives there.
- **Restored-held records gate the first job** (`settleRestoredHeld`, called
  from `runJob`): the one-shot grip verification of §7.6 counted down in
  `step()`, which a job blocks — a start-job inside the settle window
  validated against records nothing had verified, and a part lost in the
  downtime became a phantom place. A job now waits out any pending
  verification (abortable like every wait) before validation reads the world.
- **`markPlaced` clears the probing state** (`stations.go`): material the
  module itself placed refutes a probed-empty verdict; the latch surviving a
  place left an endless place-then-pick transfer station permanently refusing
  picks with `TRAY_EMPTY` until a manual reset.
- **Tray-to-itself places exclude the just-picked slot** (`freeSlot(exclude)`):
  putting the part back into the slot the same job emptied is a
  successful-looking physical no-op a compacting PLC would loop on forever;
  the place now lands in the next free slot in order. (The proc-to-itself
  re-seat of §8.1 is untouched — there the "no-op" re-clamps the part, which
  is its purpose.)
- **`PICKER_OPEN_FAILED` (23)** replaces the pick-side reuse of
  `PLACE_FAILED` in `waitPickerOpen` (§7.5/§7.7).
- **Targets are checked against the pushed axis limits before dispatch**
  (`motion.go checkTargetInLimits`, `motsetup.Result.AxisMinPos/MaxPos`):
  motion rejects an out-of-range line anyway, but only with a bare command
  status — the fault now names the axis, the value and the limits, and points
  at the picker offsets and z-offset inputs, because those shift a validated
  taught position into machine coordinates the load-time validation never sees
  (command = target − offset; see the TODO below).
- **DXF loading rejects malformed geometry values** (`pkg/pnproute`): the
  LWPOLYLINE/VERTEX coordinates, the closed flag, the vertex count, bulge,
  extrusion, circle radius and the ellipse axis/ratio/sweep all error on a
  malformed value instead of silently defaulting (a corrupted vertex loaded a
  differently-shaped zone with no error — routes were planned through the
  missing half). "Absent" stays legal where the format allows it; "present but
  unparseable" never is.
- **Route metrics off the `Plan` hot path** (`pkg/pnproute`): `Curv`,
  `MinRadius` and `MinClear` were computed on every plan — including the
  O(zones × segments × samples) clearance scan — and read by nothing but
  tests. They now fill on demand via `Planner.Metrics(route)`.
- **`manual/` scenario de-raced** (`tests/pnptask/manual/test.py`): the
  gripped-nothing check observed a ~10–25 ms close-high window through a
  polling REST client; it now waits on the module's own verdict marker
  (`wait_manual_judged`) plus the stable post-conditions, which subsume the
  transient.

### 12.2 Migration note

`internal/motsetup` converts `COMP_FILE` leadscrew-compensation triplets
machine-units → mm before `SetJointComp` (phase 3, fixing a pre-existing
milltask bug). Correct for the mm-internal stack — but on a deployed **inch**
machine whose COMP_FILE values were tuned against the old raw push, the same
file's trims change by 25.4× after the upgrade. Any release notes for the
milltask side must carry this.

### 12.3 TODO — deferred, needs a design decision or a broader refactor

- **Per-picker reachability validation.** All geometric validation (§5.1) runs
  in the shared taught frame; what is commanded is target − picker offset with
  the live z-offset added, so each picker's reachable envelope is the soft
  limits shifted by its offset, and a station near the envelope edge can be
  reachable for one picker and not the other — surfacing as an intermittent
  mid-job `MOTION_ERROR` depending on which picker happened to be free. The
  pre-dispatch check (§12.1) makes the failure diagnosable; validation at
  job-validate time (every leg endpoint minus every candidate picker's
  offset, plus Z_PICK + z-offset and MOVE_HEIGHT against the Z limits) would
  refuse before moving. Until then the lab rule is: keep every taught
  position, minus either picker's offset, inside the soft limits — i.e. draw
  the DXF outer limit inside the *intersection* of both pickers' envelopes.
- **Operator resync for proc `has-material`.** Trays have `set-full` /
  `set-empty`; process stations have nothing, although §6.4 promises a
  manual-intervention resync. "Model occupied / fixture empty" self-corrects
  (`PROC_NO_MATERIAL` clears the flag); the inverse — an operator hand-loads
  a fixture the model thinks free — sends the next place-to-proc down onto
  the occupant. Needs a decision: a pair of edge pins per proc station
  (mirroring the tray resets, honored in both modes), or a documented
  operating rule that hand-loaded fixtures must be hand-unloaded.
- **One owner for machine-unit handling.** `parseLinearUnits` exists three
  times (task and ngcpreview silently default to mm, pnptask errors),
  machine-units→mm conversion four times, and the lenient INI accessors are
  copied between `motsetup/ini.go` and `task/config.go`. motsetup — created
  precisely to end unit drift — should export one copy all four packages use;
  divergence here is a silent 25.4× disagreement between motion limits and
  station geometry.
- **Minor, noted:** a `home` press during a job is latched (gated only on
  estop) and runs the homing sequence the moment the job completes — a PLC
  glitch on that pin becomes a surprise homing run. If that ever bites,
  gate the latch on "idle" at press time like the manual-picker edges are
  gated on manual mode.
