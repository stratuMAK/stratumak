# pnptask: the RT/non-RT boundary

**Status:** built (designed 2026-11-07; steps 1–7 machine-tested and merged
by 2026-11-09 -- stratumak #59–#62, CoatV2conf #34, #37, #39 -- step 8 the
documentation and config cleanup after them). The sections
below are the design as decided; each step in §9 says how it was built and
where it differs, and §12 lists what is still open. §13, the leg feed, is
designed and not built.
**Related:** [`GOMOD_RT_DESIGN.md`](GOMOD_RT_DESIGN.md) (RT functions in Go
modules, first customer `deadzone-check`), [`PNPTASK_DESIGN.md`](PNPTASK_DESIGN.md)
(§5.2 pins, §5.2.1 timing contract, D11/D12/D16/D19/D25/D26/D33/D34/D45/D53),
stmak-concepts `docs/architecture/HAL_SPECIFICATION.md` ("Transfer Plan RT Cycle
Model").

---

## 1. Why

pnptask was designed as a non-RT module whose interface is its HAL pins. Its
control loop runs at `pollInterval` (10 ms), reads its input pins one by one in
`sample()`, and writes its output pins one by one wherever the code happens to
be. The RT part came later and by accident: the dead-zone check had to run at
servo rate (`GOMOD_RT_DESIGN.md` §6).

A pin is one word of shared memory. A group of pins written by a Go goroutine
is not written at one moment, and an RT component reading the group is not
reading at one moment either. Every place where both sides need a *consistent
group* or a *defined moment* has grown its own workaround:

**On the RT side (the coating machine config):**

| Where | Workaround | Why it exists |
|---|---|---|
| `coat_pnp_main` FUNCTION top | busy, error, error-id sampled once, in that order | a TRAY_EMPTY read as flag-new/id-old was a fault (2026-11-04, CoatV2conf #25) |
| `coat_pnp_main` `expected` | id 0 with the flag high accepted in RESET_ERROR | a clear (flag, then id) cannot be read consistently in *any* order |
| `coat_pnp_main` `portal_saw` | wait for `cycle-count` +2 before trusting a pin | nobody knows when pnptask sampled an input |
| `coat_pnp_main` | `job_accept_time`, `req_low_cycles` | the same, as timeouts |
| `coat_auto_handler` | `portal_err_hold`: the reset is held until the error drops | a pulse shorter than 10 ms is lost (§5.2.1) |
| `coat_pnp_jobreq` | a whole component to bridge `start-job` | `start-job` is an IO pin two writers share |
| `threads.hal` | `pnp.jobreq` addf'd before `pnp.main` | the only reason the job's ids reach pnptask a cycle before `start-job` |

**On the Go side (pnptask):**

| Where | Workaround |
|---|---|
| `sample()` | start-job read before its parameters, deadzone-select last (`machine.go`) |
| `acquireScene` | raise scene-busy, look at scene-hold again one tick later (D45) |
| lift watch (D53) | the fixture clamp fires up to a control cycle after the segment change |
| `awaitDescent` (D52) | two poll intervals added to the braking margin |
| motion status | motstat snapshot with a mutex; up to `statusStaleTicks` failed reads tolerated; `dispatchSettleTicks` because status right after a dispatch is older than the dispatch |
| §5.2.1 | a page of rules telling producers to hold every command until acknowledged, and to leave `start-job` low for 50 ms |

The same class of bug exists outside pnptask: milltask's halui writes
`machine.is-on` before `program.is-idle`, and `coat_coating_main` reads them in
the order that can tear (a false "Programm abgebrochen" on a machine off
mid-program). That is out of scope here (§10), but the generic part of this
design is meant to serve it too.

Reader-side read orders are correct on x86 (Go's `Pin.Set` takes a mutex, which
fences) but they are a contract nobody tests, every reader has to know it, and
it cannot cover a pair of pins whose transitions go both ways. The fix belongs
at the boundary, not in every reader.

## 2. Goals and non-goals

**Goals**

1. Every HAL pin of pnptask is read and written **in the servo thread, at one
   point**, by the RT part. Go never touches a pin.
2. Go sees **one consistent servo cycle** per wake (inputs, events, motion
   status), and publishes **one consistent frame** per commit.
3. Edges, latches and handshakes are decided **at servo rate**: no lost pulses,
   no hold rules, no guessed settle times on either side.
4. The pin contract is redesigned for the best fit. The coating machine is the
   only user; its config changes in lockstep.
5. The mechanism (buffers, pin copy) is **generic** in `pkg/hal`/`pkg/cmodule`,
   so halui and other Go modules can use it, and it maps onto the transfer
   plans later.

**Non-goals**

- No Go in the servo thread (`GOMOD_RT_DESIGN.md` §2 stays true).
- Planning, path shaping, motion queueing, the world model, persistence and
  logging stay in Go.
- No locks between RT and Go.

## 3. The generic layer (`pkg/cmodule` + `pkg/hal`)

### 3.1 SPSC triple buffer

One writer, one reader, wait-free on both sides, the latest complete frame
wins. This is motmod's status buffer (`motion_struct.h`) made generic,
**including its dirty flag**: without it, a reader polling faster than the
writer publishes is handed its own previous slot back and sees time go
backwards (observed in motmod as motion ids going backwards).

```c
/* pkg/cmodule/stmak_tribuf.h */
typedef struct {
    atomic_int middle;      /* slot index | STMAK_TRIBUF_DIRTY */
    int        write_idx;   /* writer-private */
    int        read_idx;    /* reader-private */
    uint32_t   slot_size;   /* bytes, multiple of 8 */
    /* 3 slots follow, 8-byte aligned */
} stmak_tribuf_t;

void *stmak_tribuf_write_slot(stmak_tribuf_t *b) STMAK_NONBLOCKING;
void  stmak_tribuf_publish(stmak_tribuf_t *b)    STMAK_NONBLOCKING; /* acq_rel exchange, sets DIRTY */
const void *stmak_tribuf_read(stmak_tribuf_t *b, int *fresh) STMAK_NONBLOCKING; /* swaps only when DIRTY */
```

The writer always writes a **whole frame** into its slot (the slot holds a
frame from two publishes ago). Both directions use one: RT→Go and Go→RT.

Go side: `hal.TriBuf[T]` over an `RTCalloc` block — `Publish(*T)` and
`Read(*T) (fresh bool)`, each one cgo call that copies the whole frame between
Go memory and the slot and does the exchange. No Go pointer is ever stored in C
memory, and the frame layout is defined once, in a C header, and seen by Go
through its cgo type. `NewTriBuf` refuses a frame type that is not plain data
(a pointer, slice, string, map, interface, channel or func anywhere in it).
Go callers on one side are serialized by the handle, so the buffer sees one
logical writer and one logical reader.

Why not the others:

- **Seqlock:** the RT reader would have to skip the copy when it tears. That is
  safe but not deterministic, and a Go writer preempted mid-write makes every
  RT cycle in that window skip.
- **Spinlock or mutex:** a Go thread preempted while holding it stalls the servo
  thread. Not acceptable.
- **Queue:** not needed. Events travel as counters inside the frames (§4.3), so
  latest-wins frames lose nothing.

### 3.2 Pin map

A table the RT function walks to copy pins into a frame and back, built by Go
at init from pin slots (`Pin.RTDataPtr`) and field offsets of the frame struct:

```c
/* pkg/cmodule/stmak_pinmap.h */
typedef struct {
    void   **slot;     /* HAL data-pointer slot: dereferenced every cycle, net repoints it */
    uint32_t ofs;      /* byte offset in the frame */
    uint8_t  type;     /* bit, u32, s32, float */
} stmak_pinmap_entry_t;

void stmak_pinmap_in (const stmak_pinmap_t *m, void *frame) STMAK_NONBLOCKING; /* pins -> frame */
void stmak_pinmap_out(const stmak_pinmap_t *m, const void *frame) STMAK_NONBLOCKING; /* frame -> pins */
```

Go: `m, _ := hal.NewPinMap(hal.In, unsafe.Sizeof(frame), n)`, then
`hal.BindBit(m, pin, unsafe.Pointer(&frame), &frame.estop)` (and `BindFloat`,
`BindS32`, `BindU32`). The Bind function pairs the pin type with the field
type, and the map checks range, alignment and duplicates. An Out map takes only
Out pins: it writes them every cycle, which on an IO pin would overwrite the
other writer.
The pin keeps its `hal.Pin` object for naming, introspection and the REST API,
but pnptask stops calling `Get`/`Set` on it.

This is deliberately the shape of a transfer-plan entry (offset, type,
direction). When the transfer plans land, the pin map is what they replace;
the frames and the RT logic stay.

### 3.3 Liveness

Each frame carries the producer's counter: the RT frame its servo cycle number,
the Go frame its commit number and the servo cycle of the RT frame it was
computed from. Each side can tell whether the other is running (§7).

### 3.4 Waking Go

The RT side cannot wake a goroutine: a futex or eventfd write is a syscall,
and the servo thread makes none. So the function keeps a **doorbell**
(`stmak_doorbell.h`, a block of its own, because reading the triple buffer
consumes a frame): a `cycle` counter it bumps every run, and a **change
counter** it bumps, after publishing, when something its Go side acts on
changed. The Go frame carries a **`wake_at`** servo cycle at which the
function rings as well (a deadline).

`hal.Doorbell.Wait` polls the counters at a fixed short interval (1 ms) with
one atomic load each and returns when the change counter moved, when the
caller's context ends, or with `ErrRTStalled` when `cycle` has stood still too
long (the RT function is not running, §7.1). What counts as a change is the RT
function's business; the doorbell only counts. The poll is the one periodic thing left on the Go side, kept in
`pkg/hal` so modules do not each grow their own.

## 4. The pnptask RT function

### 4.1 One function, one place in the thread

`deadzone-check` becomes `pnp.task.cycle`, in the same place: after
`motion-controller`, before the first reader of pnptask's outputs. Per servo
cycle, in this order:

1. **Pins in:** `stmak_pinmap_in` → the RT-private input struct.
2. **Go frame:** `stmak_tribuf_read` → the latest commands and outputs from Go.
3. **Motion:** one `mot` accessor call → this cycle's motion snapshot (§4.5).
4. **Logic:** edges and counters, gating, job/park/error/scene state, lift
   watch (§4.3, §4.4), the stall check (§7.2).
5. **Dead zones and teach pins:** as today, from the snapshot's position; the
   picker positions (`pos-x/-y/-mu`) from the same position plus the offset
   params.
6. **Change:** compare what Go acts on with the previous cycle, and check
   `wake_at`; increment the change counter if either fired (§3.4, §6.1).
7. **Pins out:** compose the output struct (Go's values, overridden where RT
   owns the pin) → `stmak_pinmap_out`. All outputs change here, together.
8. **RT frame:** inputs, event counters, RT state, motion snapshot, change
   counter →
   `stmak_tribuf_publish` for Go.

Readers after `pnp.task.cycle` in the thread see this cycle's outputs.
Writers after it (pnp.main and the rest) are seen next cycle. Both are now a
property of one documented addf line, not of luck.

### 4.2 Who owns what

The rule: **one owner per pin and per piece of state**.

| Owner | What |
|---|---|
| **RT** | input sampling; edge detection and press-time gating; the job and park handshake (`busy`, accept/refuse); the error latch (`error`, `error-id`, `error-station`, `picker.N.missing`); the scene lock (`scene-busy`); the lift-watch override of `release`; `deadzone.N.free`; the teach pins `picker.N.pos-x/-y/-mu`; `cycle`, `sync`; the change counter and stall check |
| **Go** | everything computed from the model or from motion: `machine-is-on`, `homed`, `parked`, `picker.N.close`, `proc.N.release` (outside the override), `holds`/`origin-id`, tray and station outputs, `plan-time`; and the decisions: when a job ends and how, what to raise, when to ask for the scene lock |

Go's requests to RT are fields in the Go frame: levels where a level is the
truth (scene-lock request), sequence numbers where an event must not be lost
or applied twice (raise an error, finish a job, arm a lift watch).

### 4.3 Edges as counters

Each edge input gets a `u32` counter in the RT frame, incremented by RT on a
**rising edge that passes its gate**. Go keeps the last value it consumed and
acts on the difference. Nothing is cleared, nothing is lost between two Go
wakes, and a pulse of one servo cycle counts.

Gating stays at press time (today's comment in `sample()` stays true), now
with servo-cycle state:

| Edge | Counted only when |
|---|---|
| `machine-on` | not estop (the boot edge stays wanted, see §7.3) |
| `home`, `park` | not estop, not busy (RT owns busy) |
| `error-reset` | an error is latched (RT owns the latch) — and RT clears it in the same cycle (§4.4) |
| `picker.N.manual-open/close` | not estop, not auto |
| `tray.N.set-full/set-empty`, `proc.N.set-has-material/set-empty` | always; set-full also latches the tray's `step` of that cycle with the counter; both in one cycle is ignored, as today |

Station resets keep their semantics: applied by Go between jobs. What changes
is that the PLC can see when (§5.2).

*Steps 4 and 5 as built:* every gate is applied in the cycle function at the
press: estop, auto, and since step 5 busy (home) and the latch (error-reset,
which clears it in the same cycle and is counted only then). `start-job` and
`park` are not counted: the function accepts them (§4.4). A station reset is one counter with the latest value (set-full:
the tray's step of that servo cycle; set-empty: -1), plus a conflict counter
for both in one cycle, which Go logs.

### 4.4 Handshakes in RT

**Job.** `start-job` becomes a plain `in` pin. On its rising edge, not busy
and with no error latched, RT accepts in the same servo cycle: `busy` rises,
origin/dest/step/stay-near/deadzone-select are latched **from that same input
sample**, and the job goes to Go with a job sequence number. An edge while an
error is latched is ignored, with no `refused` output: the PLC sees `error`
high in the same frame and must not request then. The one race, an error
latched in the very cycle of the request, leaves the requester with `error`
high and no `busy`, which is a refusal it already handles (pnp.main: REQUEST,
"abgelehnt"). Everything else stays a Go decision made inside the job, as
today, including `AUTO_DISABLED` and `MACHINE_OFF`, which RT could see but
deliberately does not judge: one place for each rule, and a refusal without
`busy` would need a `refused` output after all. Unknown ids, `UNREACHABLE` and
planning likewise. Such a job ends at once with its error, through the same
end-of-job commit, at Go's first wake after `busy` rose. Go runs the job and,
at the end,
sets `job_done_seq` together with the outcome (raise or none) and the final
model outputs in **one** commit. RT drops `busy` in the cycle it sees that
frame, with the error pins and every model output from the same frame.

So: no IO pin, no arming rule, no 50 ms low time, no read order. The PLC
raises `start-job` with its parameters in one run, sees `busy` one servo cycle
later (or the error), and sees the outcome and the model together when `busy`
falls. `coat_pnp_jobreq`'s bridge half and pnp.main's `portal_saw`/accept
timeout go away.

**Park** (D34) is the same handshake on the `park` edge, ending with `parked`.

**Error latch.** Go raises with `(raise_seq, id, station, missing mask)`. RT
latches first-error-wins (the rule moves from `raise()` to RT) and publishes
flag, id, station and missing **in one cycle**. On a counted `error-reset`
edge RT clears all of them in one cycle, and Go learns of it from the counter
(and forgets its side of the fault). A raise whose sequence number comes after
the clear latches again. No reader order, no id-0 case, no reset hold.

**Scene lock** (D45). Go holds a lock-request level while it wants to start a
leg into the movable region. RT grants in a cycle where the request is high
and `scene-hold` is low: `scene-busy` rises in that same cycle, and the grant
goes back to Go. Hold and request in the same cycle: hold wins. Go dispatches
only after it sees the grant and drops the request when the leg has drained;
RT then drops `scene-busy`. The "look again one tick later" phase disappears,
because arbitration is atomic in the cycle that cam.sphere reads.

**Lift watch** (D53). Go arms `(station, segment id)` with a sequence number.
RT forces that station's `release` low in the first cycle whose motion
snapshot shows a segment beyond the id, and reports it as fired. Go then
writes `release` low itself and disarms. The clamp follows the head within a
servo cycle instead of a control cycle.

### 4.5 Motion snapshot

motmod's RT status already holds everything pnptask reads from motstat:
`carte_pos_fb` (+ ok), the commanded Cartesian position, `id`, `depth`,
`distance_to_go`, `current_vel`, the motion-level inpos, `motion_state`,
enabled, per-joint `homed`/`homing`, and `commandNumEcho`/`commandStatus`.
Only accessors are missing (today there are `status_get_carte_pos_fb` and
`status_get_current_vel`).

Add one `@rt_safe` accessor that fills a struct with exactly these fields, and
bump the `mot` API version (the same route as `status_get_carte_pos_fb`, which
went 1 → 2). The RT function reads it once per cycle, after
`motion-controller`, so it is this cycle's state and consistent with the pins
sampled in the same cycle.

Go then drops `motstat` (`GetStatus`, `GetPosFb`, `GetPosCmd`) completely:

- `statusStaleTicks`, `commErrors` stay as the tolerance for a failed read,
  but a read now fails only before `mot` is resolved, or for a frame from
  before the last motion command (below). The `motionStatus` interface stays
  too: production reads the frame (`rtStatus`), the unit tests keep scripting
  their fake motion through it.
- `dispatchSettleTicks`: 5 guessed cycles become 1 exact one. motctl's
  `send_command` returns only once motmod echoed the command, so a frame of a
  later servo cycle than the one current at that return includes it. The
  cycle function ticks a doorbell after every publish; `committedMotion`
  notes the count after every command, and `rtStatus` refuses older frames.
  (`commandNumEcho` is in the snapshot, but pnptask does not know its own
  command numbers — motctl assigns them — so the cycle count is the handle.)
- `awaitDescent`'s margin shrinks to what a Go wake really costs (§6.1).

Commands still go through `motctl` (non-RT, as today).

## 5. The new pin contract

Names stay where the meaning stays. Changes:

### 5.1 Changed or removed

| Pin | Today | New |
|---|---|---|
| `start-job` | bit **io**, cleared by the module | bit **in**, rising edge; accepted or refused in the same servo cycle |
| `busy` | job executing, set by Go | job accepted and running; rises in the edge's cycle, falls with the outcome in the same cycle |
| `cycle-count` | +1 per Go tick; "+2 proves an input was sampled" | **removed**, replaced by `cycle`/`sync` |
| `origin-id`, `dest-id`, `process-step`, `stay-near` | read before/after `start-job` by rule | latched from the edge's own sample |
| `deadzone-select` | read last, by rule | sampled with everything else; read per leg as today |
| `error-reset` | edge, must be held until `error` drops | edge, one servo cycle is enough |
| every edge input | must be held until acknowledged (§5.2.1) | one servo cycle is enough |

### 5.2 New

| Pin | Type | Dir | Meaning |
|---|---|---|---|
| `cycle` | u32 | out | servo cycles counted by the RT function |
| `sync` | u32 | out | the `cycle` of the input sample that Go's latest published frame was computed from. A producer that changed an input in cycle *k* knows Go has seen it once `sync` − *k* is ≥ 0 (wrap-safe) |
| `tray.N.resets` | u32 | out | station resets applied by Go (they wait for the end of a job); the PLC compares it with the count of resets it asked for |
| `proc.N.resets` | u32 | out | the same for process stations |

`sync` is the exact form of what `cycle-count` + 2 approximated, and it is
generic: any "has pnptask seen this" question is answered by it.

No `jobs` counter: `busy` is high for at least one Go commit, so a reader in
the servo thread cannot miss it, and no UI needs one. Easy to add should a slow
reader ever need it.

### 5.3 What the config loses

- `coat_pnp_main`: the once-per-run sampling of busy/error/id, the id-0 term,
  `portal_saw`, `job_accept_time` (or it stays as a plain watchdog),
  `req_low_cycles` where it was about pnptask.
- `coat_pnp_jobreq`: the `start-job` bridge. The valve pulses stay in the
  config (bistable valves, the coil held in automatic and the panel's force
  buttons are machine wiring, `SAFETY_BOUNDARY.md` §1; they only follow the
  `close` level, which now changes at one defined point). The component is
  renamed `coat_pnp_valves`.
- `coat_auto_handler`: `portal_err_hold`.
- `threads.hal`: `deadzone-check` → `cycle`, same position; the jobreq-before-
  main ordering stops mattering.
- `tests/pnpmain/sim.py` and `tests/jobreq`: the new contract.

## 6. The Go side

- `pins.go` keeps creating the pins, plus binding them into the pin maps.
- `sample()` becomes: read the RT frame; derive edges from counters; take the
  motion snapshot. The read-order comments go.
- Every `c.m.pins.X.Set(...)` writes a field of the Go frame's working copy
  (Go memory): the output fields of `pinSet` are handles (`outBit`, `outU32`,
  `outF64`) that keep their HAL pin for the binding and point into the frame,
  so the call sites did not change. The wait (§6.1) commits before it blocks:
  copy into the write slot, publish. A step's writes become visible together.
- **Outputs are on the pins before a motion command goes out**
  (`committedMotion` wraps the motion stack; `rtIO.applyBeforeMotion`).
  Motion commands reach motmod at once, the outputs at a commit; an output
  written before a command — scene-busy dropped before a descent — must not
  read its old value while the head already moves. The wrapper commits what
  changed and waits until the cycle function has started a run after the last
  commit (two cycle counts, normally under 2 ms; a commit from an earlier tick
  is long past and costs nothing). Committing alone would put pins and motion
  in the same servo cycle, but inside it motion-controller runs before the
  cycle function, so a reader outside the servo thread could still catch the
  head moving with the old pin. (Found by the scenegate runtest in steps 2 and
  3.)
- `publish()` stays as the function that fills the model outputs; it now writes
  the working copy. The teach pins leave it (§6.1).
- `raise()`, `clearError()`, `runJobHandshake`, `runParkRequest`,
  `acquireScene`/`releaseScene`, `watchLift`/`checkLiftWatch` turn into
  requests in the Go frame and reactions to the RT frame.
- `jobRequest()` and its arming logic go: RT decides.

### 6.1 The Go loop: driven by RT frames

With the cyclic work in RT, the 10 ms ticker is no longer the right shape for
the Go side. What stays is the **sequential code**; what changes is **what a
wait waits for**.

**Not a callback or state-machine design.** A job is a sequence — travel, wait
for the sensor, release, wait, lift — and a goroutine with blocking waits says
exactly that. Cutting it into callbacks or an explicit state machine would make
every wait a state and every local variable carried state, a rewrite of proven
code into a worse shape.

**The wait.** Every wait goes through `tick()` today: sleep until the next
10 ms tick, then look. It becomes: *commit, then block until something changed
or a deadline came, then read the frame*.

- **Change counter.** The RT frame carries a `u32` that RT increments whenever
  anything Go acts on changes: an input level, an edge counter, the motion
  snapshot's id, inpos, motion state or command echo, a scene grant, a fired
  lift watch, the latch. Position and velocity alone do not count — they move
  every cycle while the head travels and would wake Go at servo rate; a wait
  that needs them (`awaitDescent`) sets a deadline instead.
- **Deadlines in servo time.** The Go frame carries `wake_at`, a servo cycle
  number; RT bumps the change counter in that cycle. Every time Go measures —
  settle times, release and sensor timeouts, dwells, `awaitDescent`'s
  re-check — becomes a deadline in servo cycles, computed from the `cycle` of
  the frame it is based on. Go's time is servo time: deterministic, and the
  same time the config's rigs already run on (rig.clock, no sleeps).
- **The waiter.** The servo thread makes no syscalls, so RT cannot wake Go
  directly. A small waiter in `pkg/hal` (§3.4) looks at the change counter
  every millisecond — one atomic load — and wakes the goroutine only when it
  moved. That poll is the only periodic thing left on the Go side, and pnptask
  does not see it.
- **Commit after every wake.** Go commits once per wake, after acting on it, so
  outputs change when Go decides something, and `sync` follows every input
  change (an input change is a wake, a wake ends in a commit).

**What leaves the Go loop.**

| Today (per tick) | New |
|---|---|
| teach pins `picker.N.pos-x/-y/-mu` | RT, from the motion snapshot plus the offset params, every cycle (§4.2) |
| persistence `flush()` | on a model change |
| motion-disabled watchdog (`motionDisabled` count) | from the snapshot's enabled, as a change |
| jog | reacts to the jog inputs' changes |
| `settleTicks`, `dispatchSettleTicks`, tick counts | deadlines in servo cycles; command echo (§4.5) |

After that Go does nothing when nothing changes.

**What it buys.**

- Each sequence step waits for its event, not for the next tick: about half a
  tick per wait, some ten waits a job.
- The Go logic becomes a function of the frames it receives. Tests can feed
  frames and servo cycles with no wall clock — deterministic, and replayable
  from a recording.
- No idle work.

Route planning still runs inside the loop and blocks it for up to ~50 ms; the
change counter keeps what happens meanwhile, so nothing is lost, it is only
seen after the plan.

## 7. Failure modes

### 7.1 The RT function is not scheduled

No RT frame is ever fresh and no output ever changes. Since step 2, Start
waits up to a second for the first frame and refuses, naming the addf line:
without the function the module sees no input and drives no output, so a
warning (what `deadzone-check` used to get) is not enough. Later, the doorbell
(§3.4) sees `cycle` stand still while running; Go then treats it like today's
comm failure (motion gone, nothing starts).

### 7.2 Go stalls

An idle Go does not commit (§6.1), so the age of `sync` alone says nothing.
The stall is defined by work Go has not taken: the Go frame carries the change
counter of the RT frame it last acted on, and when RT has a change (or a
`wake_at` that came) that Go has not taken for longer than `task-timeout`
(param, `[PNPTASK]TASK_TIMEOUT`, default 2 s), RT raises `TASK_STALLED` in the
error latch, so the PLC's existing error path reacts; no separate pin and no
heartbeat. The default stays clear of what runs inside the Go loop with no wake
in between: route planning (~50 ms for a spline leg, D13's budget 100 ms) and,
the long one, the dispatch of a smoothed leg -- every motion command waits for
motmod's echo, one servo cycle per segment, so a leg of a few hundred arcs holds
the loop for that many milliseconds (up to ~0.8 s on the coating machine,
2026-11-09; the old ticked loop had the same pauses). 500 ms, the first
choice, would have faulted normal legs.

RT does **not** touch motion: motmod finishes what is queued and stands, and
stopping the machine for safety is the estop chain's business.

Go gets one new rule with it: a latched error Go did not raise ends the job in
flight, as estop does. Otherwise a Go that recovers would carry on with a job
the PLC has already given up.

### 7.3 Start, restart, priming

`primeEdges` moves to RT: the first cycle seeds the previous-input copy, so a
one-shot input already high does not count; `machine-on` is deliberately not
primed (its boot edge stays wanted). The latch starts clear, `busy` low.

### 7.4 Unload

Unchanged from `GOMOD_RT_DESIGN.md` §5: the launcher removes the function and
waits a cycle before `Destroy`; the buffers and the pin map are freed in
`Destroy`, never in `Stop`. `Stop` still gets its last values out: the final
Go commit happens in `park()`, before the barrier.

## 8. Relation to the HAL migration

The transfer plans give each RT thread a consistent copy of the store, which
makes RT readers consistent among themselves. They do not make a non-RT
writer consistent: a Go module writing the global store still writes word by
word unless it commits a group. So this design is not made obsolete by the
migration; it is what a Go module needs on top of it.

What changes when the plans land: the pin map (§3.2) is replaced by the
thread's plan, and `pnp.task.cycle` reads and writes its local store instead of
pin slots. The frames, the triple buffers and the RT logic stay as they are.

## 9. Steps

Each step ends with the stratumak runtests (`tests/pnptask/*`, `pkg/hal`
tests) green, and the steps that change the contract also with the CoatV2conf
rigs and the machine.

1. **Generic layer.** `stmak_tribuf.h`, `stmak_pinmap.h`, `stmak_doorbell.h`,
   `hal.TriBuf`, `hal.PinMap`, `hal.Doorbell`; the headers verified by
   `make rt-effects-check` through `internal/hallib/rtbridgetest`, a cyclic
   function that uses all three. Tests: a two-goroutine torture of the triple
   buffer (never torn, never backwards, fresh exactly when new), the binding
   guards, the function run by hand cycle by cycle, and on a real HAL thread
   (wake on an input change, Go's frame to the pins, a fast reader never
   seeing time go backwards, stalled after the unload barrier). *Built.*
2. **The boundary moves, the contract does not.** `pnp.task.cycle` copies all
   pins through the frames; Go stops calling `Get`/`Set`; edges are still
   derived in Go from the sampled levels. Behaviour is unchanged, but every
   output group is now published at one point, so the tearing is gone. All
   scenarios pass unchanged; the config only renames the addf. *Built:*
   `pnp_rt.{c,h}` and `rtio.go`; commits at tick entry, after `publish()`, at
   the job and park handshakes, at the end of the loop, and before every
   motion command; `start-job` (still IO) cleared by the cycle function in
   the run that applies the commit busy falls in; Start refuses without the
   function; the frames hold at most 16 tray and 16 process stations, 2
   pickers and 9 axes, and the config refuses more. The unit tests run the
   cycle function inside the control loop (`driveRT`).
3. **Motion snapshot.** The `mot` accessor and API bump; Go drops motstat;
   `commandNumEcho` replaces `dispatchSettleTicks`. Scenarios unchanged.
   *Built:* `mot` v3 `status_get_snapshot` (`mot_snapshot_t`, filled like
   motstat's `MotionStatus`); the snapshot in the input frame; `rtStatus`;
   the doorbell's cycle count instead of the echo (see §4.5);
   `dispatchSettleTicks` = 1.
   **Checkpoint after step 3** (decided 2026-11-07): steps 2 and 3 keep the
   pin contract and change timing, so before step 5 changes the contract they
   run on the coating machine (stratumak #59 with CoatV2conf #34, after the
   customer acceptance), and `pnp.task.cycle`'s time is measured there
   (`halcmd show funct`, against the 1 ms servo period; it copies ~200 pins
   plus the dead-zone check). Step 4 is behaviour-neutral and may go ahead.
4. **Edges and latches in RT.** Counters, press-time gating in RT. New
   scenario: every edge input as a one-servo-cycle pulse, counted exactly once.
   *Built:* the edges block of `pnp_rt.h`, Go's `edgesSeen`; the one-cycle
   pulse is covered by unit tests at the function and at the loop level
   (`TestHomePulseBetweenTwoSamples`) rather than a runtest scenario, since
   halcmd from a script cannot place a pulse of one servo cycle.
5. **Handshakes in RT — the contract changes.** Job, park, error latch,
   `cycle`/`sync`, station reset counters. pnptask scenarios and `pnpdrv.py`
   move to the new contract; CoatV2conf (pnp.main, jobreq, auto_handler,
   rigs) changes in the same pair of PRs.
   *Built:* the handshakes block of `pnp_rt.h` (`pnp_rt_job_t`,
   `pnp_rt_latch_t`, `job_done`/`park_done`/`raise_seq` in Go's frame); the
   function composes the output frame with its own pins (`busy`, `error`,
   `error-id`, `error-station`, `picker.N.missing`, `cycle`) and Go cannot
   write them (`rtBit`/`rtU32` have no Set). Go takes requests by number
   (`jobRequest`/`parkRequest`), ends them with `rtIO.endJob`/`endPark` in the
   commit that carries the outcome, raises through `rtIO.raise`. A request
   accepted before the loop starts is run, not primed away. `cycle-count` is
   gone; the unit tests and `pnpdrv.py` wait on `sync`, and for a job on
   `sync` past the request with `busy` low (a refused job's busy is too short
   to poll); the unit tests also count ended handshakes (`handshakes`).
6. **Scene lock and lift watch in RT.** `scenegate` and `zarcs` scenarios
   extended: grant and hold in the same cycle; the clamp within a servo cycle
   of the segment change.
   *Built:* `scene_req`/`scene_granted` and `watch_seq`/`watch_id`/
   `watch_release`/`watch_fired` in the frames; `scene-busy` is the
   function's pin (`rtBit`), and a fired watch holds its stations' `release`
   low in the composed output. The motion snapshot is read before the
   handshakes now, so the watch judges this cycle's segment. Go waits for the
   grant standing only while `scene-hold` is up (`waitScene`): otherwise the
   grant is a servo cycle away and a deferred lift must stay the leg's corner
   arc. A lock released for one leg reaches the pins before the next leg
   requests it, so the other side gets its cycle with `scene-busy` low. Go
   keeps its own check of the watch on the status it read (the same snapshot
   in production; the unit tests' fake motion has only that). As in step 4,
   the one-cycle properties are unit tests at the function level
   (`TestCycleSceneLock`, `TestCycleLiftWatch`) rather than scenario
   extensions: a REST driver cannot place or observe a single servo cycle.
   In a `zarcs` run the watch fired from the function in the frame the loop
   also saw it.
7. **The Go loop driven by RT frames** (§6.1). `hal.Doorbell.Wait`, the change
   counter and `wake_at`; `tick()` becomes commit-wait-read; tick counts
   become deadlines in servo cycles; teach pins move to RT; persistence and
   the watchdog on change; the stall check on untaken changes (§7.2). Its own
   step, after the handshakes, so two changes of timing behaviour never land
   together. Mechanical across the wait loops, but each wait is reviewed for
   its timing assumption. Scenarios unchanged in outcome; the Go unit tests
   gain frame-fed cases with no wall clock.
   *Built:* the cycle function counts servo time (`servo_ns`), compares a
   signature of what the loop acts on with the last cycle's (`pnp_rt_sig_t`:
   the bound inputs, the counters and handshake state, the snapshot without
   positions and velocity) and rings the doorbell on a difference or at
   `wake_at`; it latches `TASK_STALLED` (27) for a ring left untaken for
   `task-timeout`, and computes the teach pins from its snapshot and the
   offsets the loop hands it. The loop: `tick()` is commit (with its
   `wake_at`) -- `Doorbell.Wait` -- read; `control.now` is the frame's servo
   time; every wait for time sets a deadline (`wakeBy`): settle times, dwells,
   timeouts, the homing handover, the mode switch re-send, the self-disable
   debounce, the status age (`statusStale`, `commFailure`), awaitDescent's
   re-check (5 ms; its margin is that plus a doorbell poll instead of two
   10 ms ticks). A cycle function that stops running (no cycles for 1 s)
   counts as motion lost. A latched `TASK_STALLED` ends the job in flight
   (abortCheck). Two deviations: an **idle wake of 100 ms** -- the params
   (offsets, settle times, `task-timeout`) are not in the frame, and it is a
   net under a change source that might be missing -- so an idle loop wakes
   10 times a second, not never; and in the **unit tests** the loop runs the
   function itself and wakes every cycle (`wakeEveryCycle`), because their
   motion is a Go fake the function does not see. The frame-driven wake is
   tested at the function level (`TestCycleChangeCounter`,
   `TestCycleStallCheck`), with the function on its own goroutine
   (`TestLoopSleepsUntilAChange`), and by the runtests against real motmod.
8. **Cleanup.** `PNPTASK_DESIGN.md` §5.2/§5.2.1 rewritten (the timing contract
   mostly disappears), the D-entries touched by this get their superseding
   note, CoatV2conf #25's TODOs and code go.
   *Built:* §5.2.1 is the contract as it stands (gates, applied-when, the job
   handshake, `sync`); D11/D12/D13/D16/D21/D25/D26 carry their note; the
   as-built passages that described the 10 ms loop as current say what it is
   now. CoatV2conf: pnp.main reads pnptask's busy, error and error id straight
   off the pins (#25's once-per-run copies go); its other TODO (main.params'
   limits) is not pnptask's and stays.

Step 2 alone already removes the bug class; steps 3–6 remove the workarounds;
step 7 removes the ticker.

## 10. Out of scope

- **milltask/halui** (`coating.halui.*`): the same tearing (is-on vs is-idle in
  `coat_coating_main`). A separate project that can reuse §3 unchanged.
- **haljson** (`main.params`): a product upload writes many pins; the readers
  only use them at defined moments today. Worth a look once §3 exists.
- **pyvcp**: single buttons, no groups.

## 11. Decisions (2026-11-07)

1. **Go stall:** `TASK_STALLED` in the error latch after `task-timeout`
   (default 2 s since step 7, 2026-11-09: a smoothed leg's dispatch holds the
   loop up to ~0.8 s, §7.2); RT leaves motion alone; a latched error Go did not raise
   ends the job in flight (§7.2).
2. **Refusal:** an edge while an error is latched is ignored, no `refused`
   output. `AUTO_DISABLED` and `MACHINE_OFF` stay in Go's validation only
   (§4.4).
3. **No `jobs` counter** (§5.2).
4. **Valve pulses stay in the config**, `coat_pnp_jobreq` becomes
   `coat_pnp_valves` (§5.3).
5. **Manual picker presses** stay Go's: Go turns them into `close` at its next
   wake, so `close` keeps one writer.

6. **The Go loop is driven by RT frames** (§6.1): sequential code with
   blocking waits stays; a wait blocks on the change counter or a `wake_at`
   deadline in servo cycles instead of a 10 ms tick; the teach pins move to
   RT; a stall is a change Go has not taken (§7.2).

## 12. Found on the way

Things a step turned up that belong to another step, or to nobody yet. Each
step ticks off what it settles.

| # | Found in | What | State |
|---|---|---|---|
| 1 | step 1 | `stmak_hal.h` declares `stmak_hal_bit_t` as `volatile unsigned` (4 bytes), `hal.h` has `hal_bit_t` as `volatile bool` (1 byte), although the comment says they must match. Harmless on little-endian (every pin cell is an 8-byte `hal_data_u`), wrong by the letter. The bridge accesses bits as one byte. The real fix changes the typedef, which reaches ~140 files (the in-tree drivers incl. ethercat and hostmot2, every generated .comp, the CoatV2conf components); a comment-only workaround was rejected. | open: the full fix, later, in its own PR |
| 2 | step 1 | The `STMAK_PKG_FILES` list (the `pkg/hal` copy for external Go modules) does not carry the bridge files. Nothing outside the tree needs them yet. | open, when a module outside the tree wants the bridge |
| 3 | step 2 | `TriBuf.Publish`/`Read` passed a Go pointer to C; cgo checks the whole allocation behind an `unsafe.Pointer`, so a frame inside a struct holding pointers panicked. Go now copies into and out of the C slot itself. | fixed in step 2, regression test `TestTriBufFrameInsideAStruct` |
| 4 | step 2 | Outputs could lag motion commands (motctl is immediate, outputs go at a commit). | fixed in step 2 (commit before each command), made strict in step 3 (wait until applied): `committedMotion`, test `TestOutputsBeforeMotion` |
| 5 | step 2 | The `z-offset` inputs (trays and process stations) and `tray-id` were read live outside `sample()` (approach heights, reach check, world start, persistence restore). They now come from the frame like every other input. | done in step 2 |
| 6 | step 2 | Pulses shorter than a Go tick are still lost: the cycle function samples every servo cycle, but Go still looks at the latest frame only, every 10 ms. §5.2.1's hold rules stay valid until edges move to RT. | done in step 4; §5.2.1 rewritten for the edge inputs |
| 7 | step 2 | CoatV2conf `threads.hal` names `pnp.task.deadzone-check`; the stratumak build of step 2 refuses to start without `pnp.task.cycle`. The two have to be deployed together. | config change with step 2 |
| 8 | step 2 | `start-job` is still an IO pin, cleared by the cycle function on request. | done in step 5: an in pin, accepted by the function |
| 9 | step 3 | The test fake of `mot` (`dztest`) had no `status_get_snapshot`; a NULL callback would have crashed the cycle function. A version-3 provider always has it, but the cycle function checks for NULL anyway, and the fake can now serve a snapshot. | done in step 3 |
| 10 | step 3 | `mot` v3 shifts nothing for tpmod/homemod (rebuilt with the generated header), but any out-of-tree consumer of `mot` v2 is refused at lookup until rebuilt. | note for the release |
| 11 | step 3 | The scenegate runtest read `scene-busy` and `in-position` with two REST calls, so its "leg under way" condition could combine two moments; the hold then landed before the leg took the lock and the job waited forever. It reads both from one listing now. | done in step 3 |
| 12 | step 7 | Dispatching a leg holds the control loop one servo cycle per segment: motctl's `send_command` waits for motmod's echo, and motmod takes one command per cycle. A long smoothed leg is up to ~0.8 s (coating machine, 2026-11-09). It costs no cycle time -- motion starts with the first segment and the feed stays ahead (the arcs run for several ms each) -- and nothing time-critical waits for the loop any more (handshakes, latch, scene lock, lift watch are the cycle function's). What it costs: the loop's own reactions (an estop's teardown, a sensor in a wait) come only after the leg is out, and `task-timeout` defaults to 2 s instead of 500 ms. | designed: the leg feed, §13 |
| 13 | step 7 | The idle wake (100 ms) exists because the params (picker offsets, settle times, `task-timeout`) are not in the frame. Putting them there would let an idle loop sleep for good. | open, cosmetic |
| 14 | step 7 | Two races of the frame-driven loop, found on the machine and in CI: the status age counted the time the loop was busy dispatching (homed dropped for a wake after a long leg), and a mode switch re-sent on a refused read never saw the switch. | fixed in step 7 before merge (`TestStatusNotStaleAfterABusyLoop`; setMotionMode re-sends only after a fresh status) |
| 15 | review | The stall check counted a loop as stalled until it had caught up (`change == taken`): a loop that takes every ring while the next is already out -- an input that changes every cycle -- got `TASK_STALLED` after `task-timeout`. Stalled is `taken` standing still behind a ring. | fixed (`TestCycleStallCheckLoopOneRingBehind`) |
| 16 | review | `TASK_STALLED` latched once per stall: after an error-reset with the loop still stalled the latch stayed clear, and a start-job or park was accepted for a loop that was not running. While the stall lasts it latches again the cycle after a reset. | fixed (`TestCycleStallComesBackAfterAReset`) |
| 17 | review | A job ended by `TASK_STALLED` left the leg it had queued running, while its error path dropped the scene request, the lift watch and busy. The abort check stops motion first (Abort, the machine stays on) and forgets where the head stayed low. It applies to a job or park only -- enabling, homing and a jog go on under this latch as under any other -- and an error-reset counted during the job ends it too, for a latch reset just before the loop looked. | fixed (`TestTaskStalledEndsTheJob`) |
| 18 | review | A dwell of 0 could hang the job: the cycle function rings once per `wake_at` value, and a dwell that followed a wait ended exactly on its deadline asked for that same servo time again, displacing the idle wake as well. `tick()` sends no `wake_at` that is not after `now` (one already due rings in the next cycle), and a dwell has `settleMargin` like every settle time, so the frame that ends it was sampled after its command reached the pins. | fixed (`TestDeadlineAlreadyDueRings`, `TestDwellZeroWaitsForTheFeedback`) |
| 19 | review | Deadlines that came up to the idle wake late: the self-disable debounce was armed once and lost to a wake for anything else; the grip checks after an estop with `ESTOP_KEEPS_GRIP` and after a manual close were set past the point in `step()` that arms them; the loop took the doorbell's change count of the moment it started instead of its priming frame's, so a change in between -- a job accepted -- waited. Each is armed where it is set, the debounce at every wake while pending, and the loop starts from the frame's count. | fixed (`TestSelfDisableDebounceStaysArmed`, `TestDeadlinesArmedWhereTheyAreSet`, `TestChangeBeforeTheLoopStartsEndsItsFirstWait`) |
| 20 | review | The idle wake hides a missing wake source as a delay of up to 100 ms, and the unit tests wake every cycle. A detector: the frame says how many rings a change caused (`sig_changes`), `tick()` knows whether it asked for a deadline; an output changed or a motion command sent on a wake that was the idle wake alone is counted (`idleWork`) and logged as a warning, and every pnptask runtest fails on that warning (`pnpdrv.done()`). The frame's bookkeeping and what `publish` copies from the params (item 13) do not count. It found two on its first run: the loop's first step waited for the idle wake (machine-on's boot edge up to 100 ms late; it is due at once now), and what an action recorded in the model reached the pins a wake after the command it followed from (`tick()` publishes before its commit now). | done (`TestIdleWorkDetector`) |
| 21 | review | `rtStatus` compared every frame's cycle with the cycle of the last motion command; the count wraps, so from 2^31 cycles after the last command (24.9 days at 1 kHz) every frame read as older than it, for as long again: homed dropped, `MOTION_ERROR` repeated, and no command came to end it. The comparison runs only until a newer frame has been seen (`dispatchPending`). | fixed (`TestRTStatusLongAfterTheLastCommand`) |
| 22 | review | `park()` made the loop's last commit and returned; on a runtime unload the function is removed right after `Stop`, so that frame (machine-is-on low, release low, the stall check off) might never reach the pins. It waits until the function has applied it, bounded as before a motion command (`waitApplied`). | fixed (`TestModuleStopDisablesMotion`) |
| 23 | review | The loop dropped a fault itself when its last frame showed the latch set; an error-reset can have cleared it in a cycle the loop has not read, and the fault was lost although the function would have latched it. A fault is sent unless a raise is still on its way; the function keeps the first, as it always did. | fixed (`TestRaiseAfterAResetNotYetRead`) |

## 13. The leg feed (designed 2026-11-10, not built)

**Status:** design for sign-off. It settles §12 item 12.

### 13.1 Why

Every motion command goes through motctl's `send_command`: one command slot,
taken by `motion-command-handler` once per servo cycle, and the sender polls
the status for the echo. A leg of a few hundred segments therefore holds the
control loop for that many milliseconds (up to ~0.8 s on the coating machine),
which is why `task-timeout` had to be 2 s, and why the loop's own reactions
(a sensor in a wait, an estop's teardown) come only after the leg is out.
The feed also depends on Go being scheduled in time for every single segment.

motmod takes one segment per servo cycle, and the cycle function runs in that
same thread. So the boundary moves once more: **Go hands over the planned
segments as data, the cycle function hands them to motmod, one per cycle.**

Considered and not chosen:

- **A feeder goroutine in pnptask.** No motmod change, but a second goroutine
  owning motctl and the abort coordination that comes with it, in the code
  this design just made sequential; and the feed still depends on Go's
  scheduling.
- **A batch command in motmod** (a whole leg appended in one cycle, or a
  queue drained inside motmod). Appending is where the planner does its
  per-segment work (blend arc, lookahead optimization, `tp.c`); a few hundred
  of those do not fit one servo cycle, so the intake has to be spread over
  cycles anyway. What that should look like belongs to a review of motmod's
  command intake and planner split, which is its own project. The leg feed
  needs only a small, additive `mot` call and can be handed to such a design
  later: the records stay, the consumer changes.

### 13.2 What changes, in one picture

```
Go (control loop)                    servo thread, per cycle
-----------------                    -----------------------
plan the leg                         motion-command-handler   (motctl: modes, abort, jog)
write records into the feed ring     motion-controller        (runs the planner)
commit: feed_end = n                 pnp.task.cycle:
  (with the outputs of that step)      apply Go's frame to the pins
go on at once                          one record below feed_end -> mot.traj_add_*
                                       publish: fed, feed_err
```

A record added in cycle *k* is in the planner's queue for `motion-controller`
in cycle *k+1*. That is the timing a command taken by the command handler in
*k+1* has today, so motmod sees its segments no later than it does now, and
exactly one per cycle instead of at most one.

### 13.3 The feed ring

One `RTCalloc` block of `PNP_RT_FEED_CAP` records (1024), written by Go, read
by the cycle function. It needs no atomics of its own: the **frames carry the
indices**, and the triple buffer's publish orders the records before them.

```c
/* pnp_rt.h */
typedef struct {
    uint8_t  kind;          /* PNP_FEED_LINE, PNP_FEED_CIRCLE, PNP_FEED_TERM_COND */
    uint8_t  pad_[3];
    int32_t  id;            /* the segment id (moveID); 0 for a term cond */
    int32_t  motion_type, turn, term_cond;
    double   tolerance;
    double   vel, ini_maxvel, acc, feed_mm_per_min;
    mot_pose_t      pos;
    mot_cartesian_t center, normal;
} pnp_rt_feed_rec_t;
```

Counters are `u32`, counting records since the module started (they wrap; all
comparisons are differences):

| Field | In | Meaning |
|---|---|---|
| `feed_end` | Go frame | records written; the function may feed those below it |
| `feed_begin` | Go frame | records below it are void: a cancel moves it to `feed_end` |
| `fed` | RT frame | records taken out of the ring (fed, or skipped by a cancel) |
| `feed_err_seq`, `feed_err_id`, `feed_err_status` | RT frame | counts a refused record, with its segment id and motmod's status |

Go writes record *i* into slot *i* mod cap only while `feed_end − fed` < cap
(`fed` from the frame it read last, so it errs on the safe side); a leg that
does not fit is refused with `MOTION_ERROR` before anything of it is written.
The planner's own queue (2000) is not the limit: the function feeds one record
per cycle and the head runs them off.

### 13.4 The cycle function

After the scene lock and lift watch, before the outputs are composed:

1. `fed` below `feed_begin`: skip to `feed_begin` (a cancel).
2. Estop up: skip to `feed_end` (nothing of a leg survives an estop; Go's
   teardown sends the abort as today).
3. A refused record not yet acknowledged (below): do nothing.
4. Otherwise, with `fed` below `feed_end` and `mot` resolved: take **one**
   record, call the `mot` function for its kind, count `fed`. A status other
   than OK counts `feed_err_seq` with the record's id and the status, and the
   feed holds until Go has moved `feed_begin` past the record (its cancel).

The change signature takes "the feed is through" (`fed` equal to `feed_end`)
and `feed_err_seq`, not `fed` itself: the loop is woken when the last record
is out and when one is refused, not for each record.

**Outputs before motion, exactly.** The function feeds only records below the
`feed_end` of the frame it applied in this run, and the outputs of that frame
go to the pins in the same run; `motion-controller` runs the segment in the
next cycle. So an output written before a leg is on the pins a cycle before
the head moves, by construction, and `applyBeforeMotion`'s two-cycle wait is
not needed for fed motion. It stays for the commands that still go through
motctl.

### 13.5 motmod: three calls in the `mot` API (version 4)

```
@rt_safe "true"
func traj_set_term_cond(cond: i32, tolerance: f64) -> i32
@rt_safe "true"
func traj_add_line(seg: LineSeg in) -> i32
@rt_safe "true"
func traj_add_circle(seg: CircleSeg in) -> i32
```

`LineSeg` and `CircleSeg` carry what motctl's `SetLine` and `SetCircle` carry
(a `Cartesian` type joins `Pose` in the IDL). They return an
`EMCMOT_COMMAND_*` status. To be called **only from a function
in motmod's own servo thread** (the same rule as `status_get_snapshot`); they
take no lock and touch neither the command slot nor `commandNumEcho` /
`commandStatus`, which stay motctl's echo protocol.

In `command.c` the bodies of `EMCMOT_SET_LINE`, `EMCMOT_SET_CIRCLE` and
`EMCMOT_SET_TERM_COND` move into static functions taking the instance and the
parameters and returning the status; the `switch` calls them and stores the
result in `commandStatus`, the `mot` calls return it. **Both paths run the same
code**: the mode and enable check, `inRange`, `limits_ok`, the at-speed
bookkeeping, `set_id`, the planner's add, the abort and error flag on a
refusal, `rehomeAll`, and the operator messages (the log ring is RT-safe; the
handler logs from the servo thread today).

To check while building, not assumed here:

- `status->head`/`tail`: the handler brackets its changes to the status
  struct with them. Find out what still reads the bracket and give the direct
  calls the same.
- motctl's command trace (`motcmd_trace`) sees only slot commands. Decide
  whether fed segments should appear in it (a debugging aid, no function).
- A v3 consumer of `mot` is refused at lookup until rebuilt (tpmod, homemod
  are rebuilt with the tree) -- as with v3, a note for the release.

### 13.6 The Go side

- `line`, `lineCapped`, `arc`, `zArc` and the term-cond changes of
  `dispatchLeg` **write records** instead of calling motctl. Everything they
  do before -- the finite and limit checks, `moveID`, `cmdPos`, `noteQueued`,
  `motionDispatched` -- stays as it is. `motionControl` loses `SetLine`,
  `SetCircle` and `SetTermCond`; there is one path for a segment, not two.
- **Motctl keeps** enable/disable, the mode switches, homing, jog and abort.
  `committedMotion` cancels the feed (`feed_begin = feed_end`) in the commit
  it already makes before each of them and waits for it to be applied, so the
  function never feeds behind an abort or a mode switch.
- **The gate stays Go's decision.** A leg that needs the scene lock is
  written into the ring but `feed_end` is committed only once the grant is in
  the frame, as dispatching waits for it today. (Granting and starting in one
  cycle would now be possible; it is not part of this step.)
- **A descent decided on the way** (`queueDescent` from `awaitDescent`) is
  more records and a later `feed_end`: the ring is a stream, not one leg.
- **Drained** is "everything written is fed, and motion is in position with
  an empty queue": `waitDrained` and the other waits that mean "the motion I
  asked for is over" add `fed == feed_end` to what they test. The status
  frame is not refused while a feed is under way (that would age `homed`
  out); only these conditions look at the feed.
- **A refused record** (`feed_err_seq` moved) is the error `SetLine` returns
  today, one wake later: `abortCheck` turns it into `MOTION_ERROR` naming the
  segment and the status, cancels the feed, and the job ends through the
  usual path (motmod has already aborted its queue).
- **The lift watch** needs no change: it compares the running segment's id,
  and an id not fed yet cannot be running.
- `task-timeout` defaults to **0.5 s** again (§7.2, §11 decision 1): nothing
  holds the loop for longer than a route plan any more.

### 13.7 What does not change

The planned path (the records carry exactly what the motctl calls carried),
the pin contract, the config, the handshakes, and the order of what a job
does. The intake differs in one way: exactly one record per servo cycle from
the cycle the feed starts, where today Go's round trip sometimes takes two.
The planner therefore knows the leg no later than it does now; job times
should be equal or marginally shorter, and the velocity profile at the start
of a leg may differ within that.

### 13.8 Tests

- **Function level** (`rtio_test.go`): one record per cycle; nothing fed
  beyond `feed_end`; a cancel skips; estop skips; a refused record holds the
  feed and counts the error, a cancel releases it; the ring wraps; `fed`
  rings at the end of a feed and not per record.
- **Loop level:** the unit tests' motion is a Go fake the function does not
  see. The fake `mot` provider (`dztest`) gets the three calls and records
  what it is fed; the loop's test hook (`driveRT`) hands those to the Go fake
  after each run, so the scripted-motion tests keep working and now go
  through the feed. New cases: an abort between two records never lets a
  record follow it; a refused segment ends the job with `MOTION_ERROR`; the
  loop takes a change (an input) while a long leg is still being fed.
- **Runtests** (`tests/pnptask/*`, real motmod): all scenarios unchanged in
  outcome; the `idleWork` count stays 0. One scenario records job time before
  and after.
- **motmod:** the milltask runtests pass unchanged (the slot path runs the
  same functions).
- **Against the machine's recordings** (CoatV2conf `siglog/`, 2026-11-09):
  the same job on the sim, compared on segment ids over time and commanded
  positions.

### 13.9 On the machine

One run, first at slow speed, together with the review fixes:

1. `pnp.task.cycle`'s time (`halcmd show funct`): it was 8.5–9 µs (max ~41),
   and the planner's add now runs in it instead of in the command handler.
   This is the one thing the sim cannot show.
2. A production run at slow speed, recorded, compared with the slow recording
   of 2026-11-09 (`siglog-20261109-182913`): paths, segment ids, job times.
3. E-stop and Stopp during a leg; a sphere passage; a deferred descent.
4. Normal speed: job times against 2750 ms (3→4 median), no `TASK_STALLED`
   with `task-timeout` 0.5 s.

Falling back is redeploying the previous stmakd; there is no switch.

### 13.10 Steps

1. motmod: the three functions factored out of the command handler, the `mot`
   v4 calls, the fake provider. Milltask and pnptask runtests unchanged.
2. The ring and the feed in the cycle function, the frame fields, the
   function-level tests.
3. The Go side: records instead of motctl calls, the cancel before motctl
   commands, the drain conditions, the error path, `task-timeout` 0.5 s; the
   loop-level tests.
4. Docs: §4.5, §6, §7.2, §11, §12 of this file as built; `PNPTASK_DESIGN.md`
   where it describes dispatch.
