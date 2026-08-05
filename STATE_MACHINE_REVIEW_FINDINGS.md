# State Machines & Abort/Estop Paths — Review Findings (Tier 1, hotspot #5)

**Scope:** `PRODUCTION_READINESS.md` Tier-1 hotspot #5 — *"wherever a state machine or
abort/estop path lives, that section gets human eyes regardless of module tier."* This is
the cross-cutting hotspot. It covers the abort/estop/lifecycle surface **not** already
reviewed under the milltask (Phase 0), launcher (#4), HAL (#1), or realtime (#6) reviews:

1. **C RT motion controller** — `src/cnc/motion` (enable/disable/estop/abort + homing FSM,
   incl. the stmak-specific CiA402 drive-internal homing module).
2. **C iocontrol** — `src/cnc/iotask` (estop loop + tool-change handshake), rewritten from
   the 2.9 NML process into two in-process GMI cmods (`ioControl.c` v1, `ioControl_v2.c` v2).
3. **Go-side lifecycle / connection state machines** — classicladder, halscope, ADS
   (server + module + bridge), mqttbridge, apiserver, ngcpreview, persist_sqlite.

**Method:** three independent read-only AI mapping passes (Go-side / motion-C / iotask-C),
each classifying every hunk as **verbatim-2.9 (parity, low-risk)** vs **stmak-specific (needs
eyes)** and flagging risk signals. The synthesizer (this doc) then read the load-bearing code
directly and adversarially checked each top finding. Date: 2026-07-20.

**Verdict tags:** `CONFIRMED` = verified against the code here + survives a refutation
attempt · `PLAUSIBLE` = real code smell, but severity/impact hinges on a runtime fact or a
hardware/protocol decision the human owns. **This is a candidate list — no code changed yet.**

---

## STATUS (2026-07-20) — confirmed fixes APPLIED (awaiting rebuild + runtests)

All **CONFIRMED** findings are fixed on branch `production-readyness`; C cmods
(`io.so`/`iov2.so`/`homemod_cia402.so`) rebuild, `go build ./...` + vet + gofmt clean.

- **T1/T2/T3/T4/T5 — iotask abort wedge → FIXED (faithful port, both files).** Root cause
  confirmed against the 2.9 source: 2.9's `ioControl.cc`/`ioControl_v2.cc` are free-running
  loops that service the tool-change wait, the `emc-abort`→`emc-abort-ack` handshake and the
  fault latch as non-blocking per-cycle state; the stratuMAK port turned each into a blocking cgo
  busy-wait on the sequencer goroutine (and did so unevenly between v1/v2). Restored the 2.9
  semantics: `gmi_get_status` now runs `poll_inputs()` (the async half of `read_inputs()`) on
  the monitor status poll — reaping the abort-ack and latching the toolchanger fault;
  `gmi_io_abort` is non-blocking (2.9 `EMC_TOOL_ABORT`); the tool_load/prepare/start_change
  loops bail when their request line is cleared; a `pthread_mutex_t io_mtx` serialises the
  now-genuinely-shared state (released across every busy-wait `usleep`, estop read kept
  lock-free). v1's `gmi_tool_prepare` wedge (success test that went false-forever on abort)
  fixed the same way. Abort/estop latency = monitor poll period (user-accepted).
- **C1 — CiA402 estop homing → FIXED.** `gmi_home_do_cancel` now forces `opmode_cmd=CSP`,
  `home_cmd=0` synchronously — the drive-motion equivalent of the `free_tp` kill classic
  homing gets from the disable edge (user confirmed classic already enters safe state).
- **CL1 — classicladder free-without-join → FIXED.** modbus master + slave now `sync.WaitGroup`
  their goroutines and join in `stop()` before `module.Stop()` frees `rt`; the slave tracks
  live conns and closes them on stop so parked reads unblock. Joins run outside `mu`.
- **CL3 — non-atomic `rt.state` read → FIXED** (atomic load, matches the accessor pattern).
- **HS1 — halscope goroutine storm/UAF → FIXED.** One coalescing saver goroutine (buffered
  signal) replaces the N detached `saveStateBg`; joined in `Stop`/`Destroy` before
  `halscope_free`. Always persists the latest state; no stale-overwrites-fresh, no UAF.
- **MQ1 → FIXED** (subscribe token errors surfaced; `pubCount` backed by `atomic.Uint32`).
- **ADS1 → FIXED** (client AMS address guarded by `nm.mu`).
- **API1 → FIXED** (watch-push errors now logged, streak-suppressed; dead comment gone).
- **DOC1 → FIXED** (stale "NML + main loop thread" iotask headers corrected).

**Still OPEN (PLAUSIBLE — for human adjudication / follow-up, not auto-fixed):**
CL2 (classicladder live-edit vs RT thread — needs a read of the C `classicladder_refresh`
publication protocol), NGC1 (ngcpreview interp concurrency guard — hinges on rs274ngc static
state), ADS2 (adsmodule Stop/Destroy vs ADS 2 s bounded shutdown), ADS3 (accept busy-spin
backoff), T6 (conflated return codes), E1 (estop poll latency — accepted; safety-boundary doc).

**Test debt — CLOSED:** `tests/abort/toolchange-wedge` (the first test to exercise iov2 —
LIB:linuxcnc.hal hardcodes `load io`, so it substitutes a v2 stack). Stages a stuck change
(tool-prepare looped, tool-changed undriven), aborts (Phase 1) and estops (Phase 2), and
asserts recovery + a fresh MDI each time. Verified: wedges/fails on the pre-fix build, passes
6/6 on the fixed build.

---

**Headline.** The 2.9-inherited state logic (motion enable/disable/fault funnel, tool-table
pocket logic, coolant/lube, ESTOP-chain semantics) is faithfully ported and low-risk. Every
material issue is in **stmak-specific structural changes**: (a) iocontrol's transport moved
from a free-running NML loop to **synchronous blocking cgo handshakes on the sequencer
goroutine** — which introduced a *critical, unrecoverable abort wedge* (T1/T2); (b) the
homing FSM was **relocated + re-gated on `motion_state==FREE`**, which lets an estop strand a
CiA402 drive commanded in HOMING opmode (C1); (c) several Go modules **free C/HAL memory
without joining the goroutines still touching it** (classicladder, halscope).

---

## CRITICAL

### T1 — v2 tool-change loop has no abort-detect branch; an aborted/estop'd M6 wedges the sequencer goroutine forever in cgo
`src/cnc/iotask/ioControl_v2.c:857-901` (`gmi_tool_load`) · cf. the correct v1 at
`src/cnc/iotask/ioControl.c:668-676` · abort source `ioControl_v2.c:624-646` (`gmi_io_abort`)
**CONFIRMED**

`gmi_tool_load` raises `tool_change=1` and busy-waits in `while (!m->done)`. The loop exits
**only** on `*(d->tool_changed)` (success), `*(d->toolchanger_fault)` (v2 fault), or process
shutdown. It **never checks `tool_change`** — the exact branch v1 has:

```c
/* ioControl.c:668-676 — present in v1, MISSING in v2 */
if (!*(d->tool_change)) {          // gmi_io_abort cleared tool_change
    ...pocketPrepped = -1; clear prep pins...
    return -1;
}
```

`gmi_io_abort` (`ioControl_v2.c:634`) *does* clear `tool_change=0`, but v2 ignores it. So if
the toolchanger hangs (never asserts `tool_changed`) and the operator aborts/estops — the one
case abort exists for — `gmi_tool_load` spins until `m->done` (process exit). Because it runs
**on the sequencer goroutine inside cgo** (`sequencer.go:903-910` → `ToolLoad`), the whole
sequencer is stuck in C and cannot reach its `select { case <-seqAbort }`.

The same gap exists in v2 `gmi_tool_prepare` (`ioControl_v2.c:775-797`) and
`gmi_tool_start_change` (`ioControl_v2.c:811-819`). *(v1's `gmi_tool_prepare`,
`ioControl.c:594-601`, is also missing an abort branch — its `tool_prepare && tool_prepared`
success test just goes false-forever once abort clears `tool_prepare`, so a **prepare** wedge
affects both files; only v1's `gmi_tool_load` is safe.)*

*Failure scenario:* M6 to a tool the changer can't deliver (jam, offline PLC). Operator hits
abort or estop. Nothing happens — the sequencer goroutine is frozen in `ToolLoad`.

### T2 — the T1 wedge is unrecoverable: every restart/estop-reset path joins the stuck goroutine
`sequencer.go:95-97` (`restartSequencer` `<-oldDone`) · `sequencer.go:200` (`seqDone` closed
only on `sequencerLoop` return) · joiners: `commands.go` estop / estop-reset / off→on
(`restartSequencer`), `StopSequencer` (`sequencer.go:114-134`)
**CONFIRMED**

`sequencerLoop` closes `seqDone` only when it returns (`sequencer.go:200`). Every recovery
path — `restartSequencer` (used by estop, estop-reset, state off→on), `StopSequencer` — does
`<-oldDone` to wait for the old goroutine to exit before spawning a new one
(`sequencer.go:96`). If that goroutine is wedged in cgo `ToolLoad` (T1), `seqDone` never
closes and **the recovery blocks forever too**. `signalAbort` closing `seqAbort` cannot help:
the goroutine is in C and never reaches the `select`. Only killing the process recovers —
i.e. an operator abort during a stuck tool change **bricks the controller**.

### T3 — v2 `gmi_io_abort` itself blocks in `wait_for_abort_ack`; called from every teardown/estop path
`src/cnc/iotask/ioControl_v2.c:638-641` + `609-622` (`wait_for_abort_ack`) · callers
`commands.go:351` (stopSignals), `commands.go:549` (estop-reset), `sequencer.go:781`
(seqFaultExit), `commands.go:1398` (MDI abort), `commands.go:2125` (task abort)
**CONFIRMED**

For `proto > V1`, `gmi_io_abort` sets `emc_abort=1` then busy-waits `while (!m->done)` for
`emc_abort_ack` (`ioControl_v2.c:613-621`), **on the caller's goroutine**. It is invoked from
every abort/teardown path. If `emc-abort-ack` is unwired, or the toolchanger does not ack when
idle, **every estop/abort blocks until process shutdown**. v1's `gmi_io_abort`
(`ioControl.c:457-470`) returns immediately — this is a v2-only regression vs 2.9's
non-blocking NML abort. T3 is coupled with T1: fixing tool_load to exit on abort is
insufficient if the abort *initiator* still blocks on an ack that never comes.

> **Adjudication needed (T1+T3 together):** what is the intended v2 toolchanger abort
> protocol? Is `emc-abort-ack` mandatory (a conforming v2 changer must always ack, even
> idle), or best-effort (needs a bounded timeout → proceed)? The fix shape depends on this:
> a pure "watch `tool_change`/`emc_abort` and bail" fix (mirroring v1) vs. a bounded-wait +
> forced-idle fallback in `wait_for_abort_ack`. **Every fix here needs a regression test
> (abort racing a hung tool change) — none exists today.**

---

## HIGH

### C1 — estop/disable during CiA402 drive-internal homing leaves the drive commanded in HOMING opmode
`src/cnc/motion/homemod_cia402.c:353-360` (`gmi_home_do_cancel`) · reset only in the
`DRV_HOME_ERROR` tick branch `homemod_cia402.c:242-248` · tick gated FREE-only at
`src/cnc/motion/control.c:441-442` · `write_out_pins` doesn't touch opmode
`homemod_cia402.c:332-338`
**CONFIRMED (mechanism) / PLAUSIBLE (hazard depends on drive STO behavior)**

CiA402 homing commands the drive's own opmode (`opmode_cmd = CIA402_OP_HOMING`, `home_cmd=1`)
so the **drive moves autonomously**, outside motmod's `free_tp`. On estop: `motion.enable`
drops → `check_for_faults` clears `enabling` (`control.c:988`) → `set_operating_mode` disable
edge calls `do_cancel` per joint (`control.c:1084`). But CiA402 `do_cancel` only sets
`drv_state = DRV_HOME_ERROR` — it does **not** write `opmode_cmd`/`home_cmd`. The branch that
resets them to CSP/0 (`homemod_cia402.c:243-244`) runs only when `drive_home_tick` is ticked,
and that tick is reached only through `do_homing_sequence`, gated on `motion_state==FREE`
(`control.c:441`). The same disable edge sets `motion_state=DISABLED` (`control.c:1228`), so
the gate is already shut. For the entire disabled/estop window the drive holds
`opmode_cmd=HOMING` (possibly `home_cmd=1`) — a subsystem that can keep moving autonomously
while motmod believes it is disabled. Self-heals only on re-enable (FREE resumes).

*Whether this is dangerous* depends on the physical estop chain: if estop asserts STO / drops
the drive to Fault or Switch-On-Disabled, the commanded opmode is inert. If estop is
software-only (no STO on this axis), it is a live hazard. Either way the recommended fix is
cheap defense-in-depth: **`do_cancel` (or the disable edge) should synchronously force
`opmode_cmd=CSP`, `home_cmd=0`**, not defer to a tick that won't run. This is entirely
stmak-specific (no 2.9 analog; CiA402 is this fork's primary EtherCAT homing path).

> **Adjudication needed:** is STO/drive-fault guaranteed on estop for CiA402 axes (safety
> boundary doc)? If yes → hardening/parity fix. If no → this is load-bearing and urgent.

### CL1 — classicladder `Stop()` frees the RT struct without joining the modbus goroutines still touching it
`src/stmak/internal/classicladder/module.go:223-228` (`Stop` → `classicladder_rt_free(m.rt)`)
· `modbus.go:156-174` (`modbusMaster.stop` — cancel only, no join) · loop writes `m.rt` at
`modbus.go:273,288` (`C.write_var_ext`) · slave `modbus_slave.go:68-91` (no join, blocked
reads not interruptible)
**CONFIRMED**

`Stop()` calls `modbus.stop()` / `modbusSlave.stop()` then `C.classicladder_rt_free(m.rt)`.
`modbusMaster.stop()` (`modbus.go:156-174`) calls `m.cancel()` + `running=false` but **has no
WaitGroup / join** — the `loop` goroutine can be mid-`executeRequest` calling
`C.write_var_ext(m.rt, …)` when `rt` is freed → use-after-free. The modbus **slave** is worse:
`stop()` closes the listener but doesn't track/join `handleConn` goroutines, and those
connections have **no read deadline** (`modbus_slave.go` `readFull`), so a client parked
between frames leaks the goroutine + fd forever and keeps touching `m.rt` after free. (Compare
`internal/ads/server.go`, which deliberately sets a 100 ms read deadline for exactly this.)

*Fix direction:* WaitGroup per goroutine set, joined in `stop()` before `Stop()` frees `rt`;
read deadlines on slave conns so blocked reads observe cancellation. **Fix needs a
`-race` teardown-under-load test.**

### CL2 — classicladder live ladder edits mutate RT program memory under a Go mutex the RT thread cannot honor
`api.go:58-104` (`SetProgram`/`SetRung`/`SetSection` → `applyX` + `bumpGeneration`) ·
`module.go:324` (`atomic.AddUint32(&m.rt.generation,…)`) · RT reader = exported
`classicladder_refresh` (`module.go:157`)
**PLAUSIBLE (hinges on the C `classicladder_refresh` implementation)**

Go mutates the C ladder program in place under `m.mu`, then bumps `rt.generation`. The HAL RT
thread running `classicladder_refresh` cannot take `m.mu`. Unless the C `refresh` implements a
seqlock / double-buffer keyed on `generation`, a scan can execute a half-written program.
**Needs a read of the C RT refresh path** to confirm the publication protocol — flagged, not
adjudicated here.

### HS1 — halscope spawns unbounded detached persistence goroutines with no dedup, ordering, or join-before-free
`module.go:417,461,476,516,562` (five `go m.saveStateBg()` sites) · body `module.go:790`,
`saveState:796` · teardown `Destroy()` `module.go:254` (`halscope_free(m.s)`, takes no `m.mu`)
**CONFIRMED (goroutine model) / PLAUSIBLE (UAF window vs Destroy)**

Every config setter fires a fresh detached `go m.saveStateBg()`. Consequences: (a) N rapid
edits → N concurrent goroutines racing `persist.SetEntry`; completion order is scheduling, not
recency, so an **older snapshot can overwrite a newer one**; (b) no WaitGroup/cancel, and
`Destroy()` calls `halscope_free(m.s)` without taking `m.mu` — a lingering `saveStateBg`
reading `m.s` under `m.mu` can touch freed C memory. *Fix direction:* single serialized
"dirty → save" goroutine (coalescing) owned by the module, joined/cancelled in `Destroy()`
under the lock. (The `WatchSamples` refcount-borrow path, `module.go:288-335`, is by contrast
carefully written — cite as the good pattern.)

---

## MEDIUM

### CL3 — non-atomic `m.rt.state` read in the modbus poll loop (clean mechanical fix)
`modbus.go:199` (`state := int(m.rt.state)`) vs the atomic accessors everywhere else
(`module.go:308-313`, `atomic.LoadInt32`). **CONFIRMED.** A data race (RT thread + `setState`
both write `rt.state` atomically; this reads it raw) and a consistency defect. Trivial fix:
use the same `atomic.LoadInt32((*int32)(unsafe.Pointer(&m.rt.state)))`.

### T4 — iocontrol has no read loop; toolchanger fault-latch / clear-fault only advance inside a handshake window
`ioControl_v2.c:777-788,844-876`. **CONFIRMED (structural) / PLAUSIBLE (impact).** The 2.9
NML process read HAL every cycle; the stratuMAK cmod reads only inside the prepare/change
busy-loops. A `toolchanger_fault` (or a `clear_fault`) asserted while idle is not observed
until the next change enters its loop. Behavioral divergence from 2.9 — verify against real
toolchanger timing.

### E1 — external estop is sampled only when someone calls `GetStatus` (poll latency)
`ioControl_v2.c:985` (`s.estop = emc_enable_in==0`) · poller `monitor.go:120-129,174`.
**CONFIRMED (mechanism) / PLAUSIBLE (whether latency matters).** No free-running iocontrol
loop anymore; external-estop detection latency == the monitor poll period. Confirm the poll
rate meets the estop-response requirement (and note: the *real* estop guarantee is the
external hardware chain de-energizing power, not this software path — belongs in the safety
boundary doc).

### T5 — unsynchronized concurrent access to `hal_data` / `emcioStatus` across three goroutines
`ioControl_v2.c:624-646` (abort, command goroutine), `857-901` (tool_load, sequencer
goroutine), `977-1002` (`gmi_get_status`, status goroutine). **CONFIRMED.** No mutex;
`s.heartbeat = m->emcioStatus.heartbeat++` (`:989`) is a non-atomic RMW concurrent with other
access. Mostly benign on single HAL bits, but unsynchronized — wants a lock or a documented
rationale.

### NGC1 — ngcpreview runs the rs274ngc interpreter with no concurrency guard
`module.go:885` (`GenPreview`), `module.go:1247` (`EvalExpression`). **PLAUSIBLE.** Each call
does `interp_shim_new → … → interp_shim_destroy` with proper per-call cleanup, but is invoked
from concurrent apiserver HTTP handler goroutines with no serialization. If the interpreter /
canon retains any static/global state (historically it does), concurrent preview/eval requests
race in C. *Fix direction:* per-module mutex (preview is not latency-critical). Error handling
across the cgo boundary here is otherwise good.

### ADS1 — ADS notification client-address read/written outside the mutex
`internal/ads/notification.go:255-258` (`setClientAddr` writes `cNetID/cPort` from the reader
goroutine) vs `:251-252` (`clientNetID/clientPort` read by `sendLoop`). **CONFIRMED.** `nm.mu`
guards `subs`/`stopped` only; the AMS target address is a genuine data race. Fix: guard the
address under `nm.mu` (or an atomic snapshot).

### ADS2 — adsmodule `Destroy()` can exit the HAL component while ADS conn goroutines still touch its pins
`internal/adsmodule/module.go:58-69` (`Stop`→`server.Stop`, `Destroy`→`comp.Exit`) ·
bounded `ads/server.go:30,104` (`shutdownTimeout=2s`, returns even with live goroutines).
**PLAUSIBLE.** `server.Stop()` may return after its 2 s cap with connection goroutines still
running; `Destroy()` then frees the HAL component while a lingering goroutine calls
`symbols.ReadData/WriteData` → `adsbridge` pin accessors → potential HAL-pin UAF. Tighten the
shutdown ordering (join, or a liveness gate on the accessors).

### MQ1 — mqttbridge drops publish/subscribe token errors and races a shared liveness counter
`internal/mqttbridge/bridge.go:344,413` (ignored `Subscribe`/`Publish` token errors) ·
`bridge.go:416` (`b.pubCount.Set(b.pubCount.Get()+1)` — non-atomic RMW across per-topic
`publishLoop` goroutines). **CONFIRMED.** Publish/subscribe failures are silently lost; the
shared `pubCount` HAL pin has torn/lost increments. Fix: check tokens (log), make the counter
atomic (or per-topic). The paho reconnect/`onConnect`-resubscribe design itself is sound.

---

## LOW / mechanical

- **DOC1 — stale "NML + main loop thread" header comments.** `ioControl.c:11-12`,
  `ioControl_v2.c:11-12` describe a thread/NML model that no longer exists (in-process GMI
  cmod, all work in synchronous callbacks). Misleads maintainers. **CONFIRMED** — doc fix.
- **T6 — conflated return codes / silent status.** `gmi_tool_prepare` returns −1 for both
  "tool not found" and "shutdown" (`ioControl_v2.c:731-734` vs `798`); `UNEXPECTED_MSG`
  (`:83`) and `load_tool` failure paths (`:540,548,562,588`) print to stderr with no status
  propagation. **CONFIRMED** — low impact.
- **API1 — apiserver push loops silently drop `watch()` errors** despite a "Log but don't
  kill" comment with no log call (`ws_handler.go:489-492`); `InsecureSkipVerify:true` on both
  WS upgraders flagged in-code "tighten in production" (`ws_handler.go:210`,
  `stream_handler.go:17`). **CONFIRMED** — the apiserver push/stream lifecycle is otherwise
  the cleanest in the tree (ctx-cancellation-driven, supersede-before-start, WaitGroup on
  streams) and is worth citing as the reference pattern.
- **ADS3 — accept-loop busy-spin on persistent errors** (`ads/server.go:114-121`, `continue`
  with no backoff → hot loop under EMFILE). **PLAUSIBLE** — add a small backoff.
- **B4/B6 (motion) — informational.** ABORT scrubs the new stratuMAK sequence-FSM state only in
  `command.c:601-603`; the user→RT `send_command` path (`motctl_handlers.c:57-87`) is now
  serialized by `send_mtx` (commit `104f633164`) so intra-process command drops are closed,
  but ABORT/DISABLE still block up to `comm_timeout` and return −1 on RT comm-loss with no
  prioritized fast-abort. The real estop guarantee is the `motion.enable` HAL watchdog chain,
  not this path — confirm it's wired (safety boundary doc).

---

## Cleared / low-concern (verified, no action)

- **Motion enable/disable/fault funnel** (`control.c` `check_for_faults`,
  `set_operating_mode`) and **ABORT propagation** (`command.c:574-615`) are **verbatim 2.9** —
  proven, low-risk. ABORT not stopping the spindle is **by design / 2.9 parity** (spindle stop
  is a separate task-issued `SPINDLE_OFF` + the estop chain) — flag in the safety doc, not a bug.
- **Coolant / lube** (`ioControl_v2.c:674-720`) — trivial pin writes, 2.9 semantics, no wedge.
- **Random toolchanger pocket logic** — 2.9 parity, additionally hardened for the SQLite
  tooltable (spindle-row-vanish guards) — defensively written.
- **persist_sqlite** — all ops take `m.mu`; the `h.db==nil` check-then-act is actually safe
  (handle fetched under the lock). Global mutex is a contention point (amplifies HS1), not a
  correctness bug.
- **apiserver WS/stream, stress_gc, haljson/halcmd watch, cpupool, emccalib, tooltable,
  pyvcpmodule, config/inirest/inifile/halparse/modcompile/adsconfig** — synchronous,
  mutex-guarded, or cancellation-driven; no state-machine/abort hazard found.
- **motion homing-sequence FSM rewrite** (`do_homing_sequence`, `control.c:133-328`) — a
  stratuMAK reimplementation of proven 2.9 `homing.c` sequencing; not obviously wrong, but warrants
  a line-by-line diff against 2.9 (the edge-triggered completion + `homing_active` force-clear
  at `control.c:310-327` is stmak-original reasoning). Tracked as **C2** (review, not a
  confirmed defect).

---

## Recommended order of attack

1. **T1 + T2 + T3 (iotask v2 abort wedge)** — CRITICAL, unrecoverable, production-relevant.
   Fix as one coordinated change once the toolchanger abort-ack protocol intent is settled;
   add the missing regression test (abort racing a hung tool change).
2. **C1 (CiA402 estop homing)** — resolve the STO/safety-boundary question; apply the
   synchronous `do_cancel` opmode reset as defense-in-depth regardless.
3. **CL1 + HS1 (free-without-join UAF)** — teardown redesign with WaitGroups + `-race`
   teardown-under-load tests. CL2 pending the C `refresh` read.
4. **Mechanical batch** — CL3 (atomic read), DOC1 (stale comments), MQ1/ADS1 (races +
   dropped errors), API1 (drop the dead "log" comment or log). Low-risk, land together.
