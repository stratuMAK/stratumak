# pnptask: the RT/non-RT boundary

**Status:** design, for discussion (2026-11-07). Nothing here is built.
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
2. Go sees **one consistent servo cycle** per tick (inputs, events, motion
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

Go side: `hal.TriBuf` over an `RTCalloc` block — `WriteSlot()`, `Publish()`,
`Read() (p unsafe.Pointer, fresh bool)`. The exchange goes through the C
helpers (one cgo call per tick), the frame content is copied in and out of Go
memory with one call each, so no Go pointer is ever stored in C memory and the
frame layout is defined once, in a C header, and seen by Go through cgo types.

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

Go: `m := hal.NewPinMap(); m.Bind(pin, unsafe.Offsetof(C.pnp_rt_in_t{}.estop))`.
The pin keeps its `hal.Pin` object for naming, introspection and the REST API,
but pnptask stops calling `Get`/`Set` on it.

This is deliberately the shape of a transfer-plan entry (offset, type,
direction). When the transfer plans land, the pin map is what they replace;
the frames and the RT logic stay.

### 3.3 Liveness

Each frame carries the producer's counter: the RT frame its servo cycle number,
the Go frame its commit number and the servo cycle of the RT frame it was
computed from. Each side can tell whether the other is running (§7).

## 4. The pnptask RT function

### 4.1 One function, one place in the thread

`deadzone-check` becomes `pnp.task.cycle`, in the same place: after
`motion-controller`, before the first reader of pnptask's outputs. Per servo
cycle, in this order:

1. **Pins in:** `stmak_pinmap_in` → the RT-private input struct.
2. **Go frame:** `stmak_tribuf_read` → the latest commands and outputs from Go.
3. **Motion:** one `mot` accessor call → this cycle's motion snapshot (§4.5).
4. **Logic:** edges and counters, gating, job/park/error/scene state, lift
   watch (§4.3, §4.4).
5. **Dead zones:** as today, from the snapshot's position.
6. **Pins out:** compose the output struct (Go's values, overridden where RT
   owns the pin) → `stmak_pinmap_out`. All outputs change here, together.
7. **RT frame:** inputs, event counters, RT state, motion snapshot →
   `stmak_tribuf_publish` for Go.

Readers after `pnp.task.cycle` in the thread see this cycle's outputs.
Writers after it (pnp.main and the rest) are seen next cycle. Both are now a
property of one documented addf line, not of luck.

### 4.2 Who owns what

The rule: **one owner per pin and per piece of state**.

| Owner | What |
|---|---|
| **RT** | input sampling; edge detection and press-time gating; the job and park handshake (`busy`, accept/refuse); the error latch (`error`, `error-id`, `error-station`, `picker.N.missing`); the scene lock (`scene-busy`); the lift-watch override of `release`; `deadzone.N.free`; `cycle`, `sync` |
| **Go** | everything computed from the model or from motion: `machine-is-on`, `homed`, `parked`, `picker.N.close`, `proc.N.release` (outside the override), `holds`/`origin-id`, tray and station outputs, teach pins, `plan-time`; and the decisions: when a job ends and how, what to raise, when to ask for the scene lock |

Go's requests to RT are fields in the Go frame: levels where a level is the
truth (scene-lock request), sequence numbers where an event must not be lost
or applied twice (raise an error, finish a job, arm a lift watch).

### 4.3 Edges as counters

Each edge input gets a `u32` counter in the RT frame, incremented by RT on a
**rising edge that passes its gate**. Go keeps the last value it consumed and
acts on the difference. Nothing is cleared, nothing is lost between two Go
ticks, and a pulse of one servo cycle counts.

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

### 4.4 Handshakes in RT

**Job.** `start-job` becomes a plain `in` pin. On its rising edge, not busy
and with no error latched, RT accepts in the same servo cycle: `busy` rises,
origin/dest/step/stay-near/deadzone-select are latched **from that same input
sample**, and the job goes to Go with a job sequence number. An edge while an
error is latched is not taken (today: start-job cleared, error left as it is),
see §11 for how to report that. Everything RT cannot know stays a Go decision
made inside the job, as today: `AUTO_DISABLED`, `MACHINE_OFF`, unknown ids,
`UNREACHABLE`, planning — the job then ends at once with that error, through the
same end-of-job commit. (RT could refuse `AUTO_DISABLED` and `MACHINE_OFF`
itself, since it sees both; whether that is worth a second place for the rule
is §11.) Go runs the job and, at the end,
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

- `statusStaleTicks`, `commErrors`: gone; "status is stale" becomes "the RT
  frame is not fresh" (§7).
- `dispatchSettleTicks`: replaced by `commandNumEcho` — Go knows exactly when
  motmod has taken its last command.
- `awaitDescent`'s margin shrinks to what the Go tick really costs.

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

Open: whether `jobs` (u32, finished jobs) is worth adding next to `busy`.
`busy` is high for at least one Go tick, so a reader in the servo thread cannot
miss it; a counter would only help a slow reader (UI).

### 5.3 What the config loses

- `coat_pnp_main`: the once-per-run sampling of busy/error/id, the id-0 term,
  `portal_saw`, `job_accept_time` (or it stays as a plain watchdog),
  `req_low_cycles` where it was about pnptask.
- `coat_pnp_jobreq`: the `start-job` bridge (the valve pulses stay, or move to
  a component named for them).
- `coat_auto_handler`: `portal_err_hold`.
- `threads.hal`: `deadzone-check` → `cycle`, same position; the jobreq-before-
  main ordering stops mattering.
- `tests/pnpmain/sim.py` and `tests/jobreq`: the new contract.

## 6. The Go side

- `pins.go` keeps creating the pins, plus binding them into the pin maps.
- `sample()` becomes: read the RT frame; derive edges from counters; take the
  motion snapshot. The read-order comments go.
- Every `c.m.pins.X.Set(...)` writes a field of the Go frame's working copy
  (Go memory). `tick()` commits once per tick: copy into the write slot,
  publish. A step's writes become visible together, at the next commit.
- `publish()` stays as the function that fills the model outputs; it now writes
  the working copy.
- `raise()`, `clearError()`, `runJobHandshake`, `runParkRequest`,
  `acquireScene`/`releaseScene`, `watchLift`/`checkLiftWatch` turn into
  requests in the Go frame and reactions to the RT frame.
- `jobRequest()` and its arming logic go: RT decides.

The Go loop keeps its 10 ms ticker. Waking Go from RT would need a syscall from
the servo thread; the counters make a missed tick harmless.

## 7. Failure modes

### 7.1 The RT function is not scheduled

No RT frame is ever fresh and no output ever changes. Go treats "no fresh RT
frame for N ticks" like today's comm failure (motion gone, nothing starts), and
at `Start` warns with the addf line, as `warnIfDeadzoneUnscheduled` does now.

### 7.2 Go stalls

RT sees `sync` stop moving. Proposal: after a configurable age (param, e.g.
`task-timeout`, default 200 ms) RT raises a `TASK_STALLED` error in the latch,
so the PLC's existing error path reacts. RT does **not** touch motion: a
running leg is motmod's and the estop chain's business. Open question, §11.

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

1. **Generic layer.** `stmak_tribuf.h`, `stmak_pinmap.h`, `hal.TriBuf`,
   `hal.PinMap`; covered by `make rt-effects-check`. Tests: a concurrency test
   of the triple buffer (monotonic, never torn, never backwards, both
   directions), and a pin map on a real HAL thread like
   `TestExportFunctOnThread`.
2. **The boundary moves, the contract does not.** `pnp.task.cycle` copies all
   pins through the frames; Go stops calling `Get`/`Set`; edges are still
   derived in Go from the sampled levels. Behaviour is unchanged, but every
   output group is now published at one point, so the tearing is gone. All
   scenarios pass unchanged; the config only renames the addf.
3. **Motion snapshot.** The `mot` accessor and API bump; Go drops motstat;
   `commandNumEcho` replaces `dispatchSettleTicks`. Scenarios unchanged.
4. **Edges and latches in RT.** Counters, press-time gating in RT. New
   scenario: every edge input as a one-servo-cycle pulse, counted exactly once.
5. **Handshakes in RT — the contract changes.** Job, park, error latch,
   `cycle`/`sync`, station reset counters. pnptask scenarios and `pnpdrv.py`
   move to the new contract; CoatV2conf (pnp.main, jobreq, auto_handler,
   rigs) changes in the same pair of PRs.
6. **Scene lock and lift watch in RT.** `scenegate` and `zarcs` scenarios
   extended: grant and hold in the same cycle; the clamp within a servo cycle
   of the segment change.
7. **Cleanup.** `PNPTASK_DESIGN.md` §5.2/§5.2.1 rewritten (the timing contract
   mostly disappears), the D-entries touched by this get their superseding
   note, CoatV2conf #25's TODOs and code go.

Step 2 alone already removes the bug class; steps 3–6 remove the workarounds.

## 10. Out of scope

- **milltask/halui** (`coating.halui.*`): the same tearing (is-on vs is-idle in
  `coat_coating_main`). A separate project that can reuse §3 unchanged.
- **haljson** (`main.params`): a product upload writes many pins; the readers
  only use them at defined moments today. Worth a look once §3 exists.
- **pyvcp**: single buttons, no groups.

## 11. Open questions

1. **Go stall** (§7.2): raise `TASK_STALLED` from RT, and with which default
   timeout? Or only publish a `task-ok` pin and leave the reaction to the PLC?
2. **Refusal** (§4.4): an edge while an error is latched — ignore it (the PLC
   sees `error` high anyway and must not request then), or a `refused` output?
   And does RT refuse `AUTO_DISABLED`/`MACHINE_OFF` itself, or does that stay
   only in Go's validation?
3. **`jobs` counter** (§5.2): needed?
4. **Valve pulses**: keep them in a renamed `coat_pnp_jobreq` in the config, or
   is a pulse mode for bistable valves generic enough to move into pnptask's
   RT function (`picker.N.close` as a level *and* `open-coil`/`close-coil`
   pulses)?
5. **Manual picker presses**: today Go turns them into `close` at its next tick
   (≤ 10 ms). Fine for a button; noted only so nobody moves it to RT for
   symmetry's sake, which would give `close` two writers.
