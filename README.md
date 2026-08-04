# STMAK — Golang Machine Controller

> **Working title.** STMAK is an effort to push the high-value but aged concepts of
> EMC/LinuxCNC to the forefront of modern industrial automation — without carrying
> the burden of decades-old compatibility constraints.

## Motivation

LinuxCNC has proven for over 30 years that open-source CNC control is not only
viable but often superior to proprietary alternatives. Its core ideas — a
hardware abstraction layer (HAL), real-time motion control, pluggable kinematics,
and an interpreted G-code engine — remain sound and relevant.

What has aged is the surrounding infrastructure: NML message passing from the
1990s, Tcl/Tk user interfaces, a monolithic build system tied to kernel modules,
and an architecture that resists incremental modernization. Every attempt to
improve one layer pulls in the weight of all others.

STMAK takes a different path: **preserve the proven control concepts, rewrite the
plumbing.**

## Technical Approach

### Single-Process, Mixed Go/C Architecture

STMAK runs as a single process with a unified address space:

- **Go runtime** handles non-real-time tasks: REST API, G-code interpretation,
  trajectory planning, configuration, MQTT, persistence
- **C pthreads with SCHED_FIFO** handle hard real-time: servo loops, HAL cyclic
  components, EtherCAT communication
- **Direct memory sharing** between Go and C — no IPC serialization,
  sub-microsecond latency between domains, lock-free ring buffers for
  streaming data

This eliminates the complexity of LinuxCNC's multi-process NML architecture while
maintaining strict RT guarantees. If the servo loop crashes, the entire process
dies and an external watchdog triggers E-stop — clean, predictable failure
instead of ambiguous partial failures.

### Why Go — a Garbage-Collected Language — for Machine Control?

The short answer: **Go never runs in the real-time path.**

- **All RT code is C.** The servo loop, HAL cyclic components and fieldbus
  cycle run in plain C pthreads (SCHED_FIFO, locked memory) that are never
  attached to the Go runtime. The garbage collector cannot pause them — its
  stop-the-world only reaches Go-managed threads. This is not an assumption;
  it is verified by measurement (see below).
- **Go replaces Python, not C.** In classic LinuxCNC the non-RT layer is a mix
  of Python, C++ and Tcl — and Python is just as garbage-collected and
  non-deterministic as Go, without anyone objecting. Go fills exactly that
  role, but with compile-time type checking, real concurrency, and refactoring
  safety that an interpreted language cannot offer.
- **A powerful standard ecosystem.** HTTP/WebSocket servers, TLS, JSON, MQTT,
  databases — the entire modern API surface of STMAK is standard-library-grade
  Go, with no dependency sprawl.
- **Single static binary.** No interpreter, no virtualenv, no version drift on
  the deployment machine.
- **Easy to learn.** A deliberately small language keeps the entry barrier low
  for machine integrators and new contributors alike.

### Measured Real-Time Jitter

Measured with the built-in `latency-test` under **full adversarial load**,
including a dedicated Go garbage-collector stress generator running inside the
control process — the exact scenario the single-process architecture is
criticized for:

| | |
|---|---|
| Hardware | Beckhoff C6030 industrial PC |
| Kernel | Debian 13, `6.12.95+deb13-rt-amd64` (PREEMPT_RT) |
| Boot parameters | `isolcpus=2,3 nohz_full=2,3 rcu_nocbs=2,3 irqaffinity=0,1 intel_idle.max_cstate=1 processor.max_cstate=1 cpufreq.default_governor=performance nmi_watchdog=0 nosoftlockup consoleblank=0` |
| RT thread | 1 ms servo thread, SCHED_FIFO on isolated CPU |
| Duration | 8,000,000 cycles ≈ 2 h 13 min |
| **Max jitter** | **34.95 µs** |
| Min / max latency | −31.36 µs / +34.95 µs |
| Mean \|latency\| | 1.24 µs |
| Std deviation | 1.78 µs |

The stress load ran every `latency-test --stress` vector simultaneously:
Go GC pressure (in-process), memory bandwidth (`stress-ng --stream`),
last-level-cache thrashing (`--cache`), TLB-shootdown IPIs
(`--tlb-shootdown`), fork/exec churn (`--exec`), ALU load (`--cpu`),
disk I/O (`--hdd`), network softirqs (`--sock`) and GPU load (glxgears).
Stressors are confined to the housekeeping CPUs — exactly as real non-RT
load is in production — and a watchdog verifies that nothing violates the
CPU isolation during the run.

A typical cycle deviates ~1.2 µs from its nominal period; the worst cycle in
over two hours of hostile load deviated 35 µs — 3.5% of the 1 ms period.
The garbage collector was part of the attack, not a victim of special
treatment: the single-image concept holds under measurement, not just in
theory.

### Measured on a Real EtherCAT Machine

The bench test above shows the jitter ceiling under synthetic attack on strong
hardware. The complementary measurement is the full stack — Go process,
RT servo thread, and a realistic EtherCAT bus — soaking on **entry-level**
industrial hardware:

| | |
|---|---|
| Hardware | WAGO 752-940x (Intel Atom E3845, 4 cores @ 1.91 GHz, 8 GB RAM) |
| Fieldbus | EtherCAT, 23 slaves in OP (couplers, digital/analog I/O, DC motor stages, NC axis controllers) |
| Clocking | Distributed clocks, master synced to the slave reference clock (`refClockSyncCycles="-1"`) |
| NIC driver | XDP-native `r8169_xdp` ([legacy-xdp](https://github.com/sittner/legacy-xdp)) |
| RT thread | 1 ms cycle, SCHED_FIFO |
| Load | full `latency-test --stress-only` vector set |
| Duration | > 15 h (54,500 jitter samples; 54.6 M bus cycles at 1 kHz) |
| **Max jitter** | **16.94 µs** |
| Mean \|latency\| | 0.80 µs |
| Bus health | **0 lost frames** in 54.6 M, **0 PLL resets** |

An Atom-class CPU driving a 23-slave bus at 1 kHz under full stress load:
the worst cycle in over fifteen hours deviated 1.7% of the period, and the
fieldbus delivered every single frame. Real-time behavior is not a property
of expensive hardware here — it is a property of the architecture.

### Component Model (cmod + gomod)

Two component types serve different needs:

**cmod** — C shared libraries (`.so`), loaded at runtime. Can combine RT and
non-RT functions in a single component:

| Execution | Use Case | Scheduling |
|-----------|----------|-----------|
| **Cyclic** | PID, filtering, interpolation | Fixed period, SCHED_FIFO |
| **Triggered** | Homing, tool change, probing | Start signal, busy/result |
| **Threaded** | Device I/O, protocol handlers | Own thread, non-RT |

**gomod** — Go modules for complex non-RT tasks (MQTT bridge, REST endpoints,
database persistence, protocol gateways). Full access to Go's ecosystem,
goroutines, and standard library.

Both types share the same HAL signal namespace and are managed by the stmak
server process.

### EtherCAT Fieldbus

Native EtherCAT support via IGH EtherLab Master with:
- YAML-based device configuration
- Automatic PDO/SDO mapping to HAL signals
- CiA 402 drive profile state machine
- FSoE (Fail Safe over EtherCAT) support

Currently requires the `uspace` branch from
https://github.com/sittner/ethercat.

### Web-Based Tooling

Operator and engineering interfaces are moving to browser-based implementations
(Vue.js + TypeScript), served directly by the Go process. No X11 dependency, no
Tcl/Tk, no Python GUI stack required. The REST/WebSocket/GMI architecture gives
full freedom of choice for UI technology — the GMI compiler can generate client
bindings for Python, TypeScript, and Go.

**Already migrated:**
- HAL signal scope (oscilloscope-style waveform viewer)
- HAL signal browser
- Tool table editor
- ClassicLadder logic viewer
- Machine calibration

**Machine UI:**
- Modified Axis UI (communicating via REST/WebSocket) — available now
- Other existing LinuxCNC UIs will be migrated
- New purpose-built web UIs to follow

### Generic Machine Interface (GMI)

A typed, versioned API layer between the control engine and user-facing tools.
Defined via IDL, with generated client bindings for Go, Python, and TypeScript.
Replaces LinuxCNC's untyped NML stat/command channels, moving from asynchronous
message passing to synchronous function calls with clear request/response
semantics.

## What This Is Not

- **Not a fork that tries to stay compatible.** INI files, NML tools, and the
  old Python/Tcl interfaces are deliberately not carried forward.
- **Not a competing project.** STMAK builds directly on LinuxCNC's proven
  motion control, trajectory planning, and G-code interpretation. We acknowledge
  and respect the decades of engineering in that codebase.
- **Not starting from scratch.** The rs274ngc interpreter, trajectory planner,
  kinematics modules, and core motion controller are inherited and evolved.

## Target Audience

- **LinuxCNC contributors** frustrated by architectural constraints that prevent
  meaningful modernization
- **Machine builders** looking for an open, flexible alternative to proprietary
  PLCs without vendor lock-in
- **Automation engineers** who need modern APIs, web interfaces, and EtherCAT
  support without the overhead of enterprise licensing

## Building

```bash
git clone -b main https://github.com/stratuMAK/stratumak.git stratumak
cd stratumak
git submodule update --init

# Install build dependencies (Debian/Ubuntu)
cd debian && ./configure && cd ..
sudo apt-get build-dep .

# Build
cd src
./autogen.sh
./configure
make -j$(nproc)
```

The build process is essentially the same as classic LinuxCNC (autoconf + make).
The EtherCAT master is built automatically from a git submodule.
A future goal is migration to CMake.

## Running the Simulator

**Terminal 1 — start the server:**

```bash
cd stratumak
./bin/stmakd configs/sim/axis/axis_mm.ini
```

**Terminal 2 — start a UI client (as many as you like):**

```bash
cd stratumak
. scripts/rip-environment
axis
```

See [README_LINUXCNC.md](README_LINUXCNC.md) for full build options and
additional configuration.

## Multi-Instance Demo

A single stmakd process can host multiple independent CNC instances.
Multiple UI clients can connect to the same or different instances
simultaneously — state updates are synchronized in real time.

**Terminal 1 — start the server with multi-instance config:**

```bash
cd stratumak
./bin/stmakd configs/sim/axis/multiinst/multiinst.ini
```

**Terminal 2 — Axis client connected to mill1:**

```bash
cd stratumak
. scripts/rip-environment
GMC_INSTANCE=mill1 axis
```

**Terminal 3 — second Axis client, also connected to mill1:**

```bash
cd stratumak
. scripts/rip-environment
GMC_INSTANCE=mill1 axis
```

**Terminal 4 — Axis client connected to mill2 (independent instance):**

```bash
cd stratumak
. scripts/rip-environment
GMC_INSTANCE=mill2 axis
```

**Things to try:**

1. On client 1 (mill1): E-stop off → Machine on → Home all axes.
   Observe client 2 (also mill1) mirrors the state changes in real time.
2. On client 1: File → Open → `nc_files/3dtest.ngc`.
   Observe the backplot preview appears on client 2 as well.
3. On client 1: Run the program.
   Observe client 2 shows the live execution progress.
4. On client 3 (mill2): Independently home, load `nc_files/axis.ngc`, and run.
   Mill2 operates completely independently from mill1.

## License

This project contains code under multiple open source licenses:

- **GPL-2.0** — inherited LinuxCNC code (motion, HAL, interpreter, drivers, etc.)
  and new `src/stmak` components by Sascha Ittner. See [COPYING](COPYING).
- **LGPL-2.0** — HAL library (`hal_lib.c`, `stmak_hal.h`), originally by
  John Kasunich. See [COPYING.more](COPYING.more).
- **LGPL-2.1** — RTAPI interface (`stmak_rtapi.h`) originally by John Kasunich
  and Paul Corner; GMI library headers (`src/gmi/lib/`) by Sascha Ittner.
- **LGPL-2.1+** — Classic Ladder RT engine, originally by Marc Le Douarain.

See individual source file headers for per-file license and copyright details.

## Status

**Lab-prototype stage.** STMAK has completed the automated half of a structured
production-readiness program and is moving onto supervised prototype machines.
Not yet suitable for unattended production use.

What stands behind that:

- **Systematic review.** Every subsystem went through a phased, per-module
  review: independent adversarial AI passes cross-checked against the LinuxCNC
  2.9 sources, each finding adjudicated and fixed with mutation-verified
  regression tests. The full record — per-module matrix, findings documents,
  design rulings — lives in [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md).
- **Tests.** The classic LinuxCNC runtest suite is fully ported and green
  (240+ tests, nothing skipped or expected-to-fail) and runs on every pull
  request, plus nightly race-detector builds. Unit coverage was raised module
  by module against a live in-process HAL; the web UIs carry their own
  type-check/lint/test gates in CI.
- **Fault paths.** Abort, E-stop and error semantics are verified against 2.9
  as written-spec tests — not just the happy paths.
- **Real-time.** RT correctness is tracked in
  [RT_HARDENING_CHECKLIST.md](RT_HARDENING_CHECKLIST.md): compiler-enforced
  non-blocking guarantees across the RT paths (including the EtherCAT master),
  and the adversarial-load jitter measurement documented above.

Known limitations — read before deploying:

- **No API authentication yet.** The REST/WebSocket control surface trusts its
  network. Keep it on loopback or an isolated machine network. The plan is an
  external authentication gateway plus fine-grained in-process authorization —
  designed, not yet built.
- **Safety.** STMAK is not a safety component. Operator protection must be
  implemented in certified external hardware, independent of this software —
  see [SAFETY_BOUNDARY.md](SAFETY_BOUNDARY.md).
- **Pending.** The review program's final human sign-off pass runs alongside
  lab deployment; a 15 h on-machine EtherCAT soak is documented above, with a
  multi-day certification soak still to come.
- **Deferred subsystems.** ClassicLadder is mid-rework; GladeVCP/QtVCP UIs are
  not ported; some shipped example configurations still await migration to the
  stmak model.

The scope of this architectural migration — touching hundreds of files across
real-time control, build system, HAL drivers, and UI — would not have been
feasible for a small team without massive AI-assisted development (GitHub
Copilot, Claude). This enabled rapid prototyping, large-scale refactoring, and
the adversarial review program at a scale that would otherwise require years
of manual effort.
