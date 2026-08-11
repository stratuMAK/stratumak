# What is missing

An honest register of what stratuMAK does not do. Two different kinds of
"missing" live here and they should not be confused:

- **Part A — parity with LinuxCNC 2.9.** What you lose, or must do differently,
  bringing a classic machine across. Some of it is gone on purpose and is not
  coming back; some is simply not ported yet.
- **Part B — stratuMAK's own unbuilt design.** Capabilities the architecture is
  aimed at and has been shaped for, but which do not exist in the tree.

Neither part is a promise of a date. Part A is closed-ended and mostly settled;
Part B is direction.

Related: [ARCHITECTURE.md](ARCHITECTURE.md) §13 summarises the same seams from
the code's point of view. [MILLTASK_COMMAND_PARITY.md](MILLTASK_COMMAND_PARITY.md)
is the closed command-by-command audit of the task layer.
`tests/DISPOSITION.md` is the authoritative evidence for Part A — every removal
below was confirmed against the exact mechanism it needed before the classic
test was deleted.

---

# Part A — parity with LinuxCNC 2.9

## A1. Removed on purpose — will not come back

These are architectural rulings, not backlog. Each has a replacement or an
explicit reason.

### Embedded Python in the interpreter

The whole Python extension surface of `rs274ngc` is gone: `py=`/`python=` remap
bodies (rejected at parse), Python O-word subs, the `canterp` "canonical
interpreter" plugin (`rs274 -p`), Python-registered predefined named
parameters, interpreter-bound `self`/`this` state across calls, generator
handlers that `yield INTERP_EXECUTE_FINISH` repeatedly, and direct canon motion
emission from a handler (`emccanon.STRAIGHT_FEED`).

**Replacement:** the C `interp_ext` API (`register_oword`,
`register_remap_prolog`/`_epilog`) plus NGC bodies for remaps, and the
`mcode_handler` GMI for M-codes. `stdglue.py` was ported to `stdglue.c`.

**What this costs you:** a config built on Python remaps needs rewriting. The
multi-yield coroutine pattern has no equivalent — `interp_ext` handlers are
stateless C callbacks with a single `EXECUTE_FINISH`.

### User M-codes as shell scripts (M100–M199)

Classic forks an external `M1xx` script per call, found via
`[RS274NGC]USER_M_PATH`. stratuMAK does not: spawning a process per M-code is
slow and a security surface. Handlers must be compiled cmod/gomod modules
registering through the `mcode_handler` GMI.

**What this costs you:** a stock config shipping M1xx scripts will fault with
"no handler for M1xx". `USER_M_PATH` is read by nothing — leftover lines in INI
files are dead keys. Migrating means porting the script to a module.

The result convention carries over: a handler returning 32–63 lands in `#5399`
for the next block to read, the way the classic script's exit status did. 0 is
plain success, −2 is "aborted", anything else faults the program.

### Python task and I/O plugins

`EMC_EXEC_PLUGIN_CALL` / `EMC_IO_PLUGIN_CALL` have no equivalent. There is no
Python plugin infrastructure in the server at all.

### Tcl for HAL, and the Tk UI stack

No `tclsh`/`wish` HAL extension commands, no `LINUXCNC_EMCSH`, no `TCLLIBPATH`
convention. The UI direction is REST/WebSocket web apps.

### NML, in its entirety

No NML channels, no NML transports, no `tcp.nml`. The remote-shell interface
(`linuxcncrsh`) survives in capability but as REST — there is one transport
now, so the classic "same test over NML-TCP" distinction has nothing left to
express.

### The realtime/userspace split

One cmod does both realtime and userspace work, so everything built on the
split is gone: `loadusr`, `rtapi_spawnv`, userspace components as separate
programs, `option singleton`, `option rtapi_app no` with a custom
`rtapi_app_main`, personality arrays with `--personalities` cycling, and
userspace `count=`/`names=` instancing.

**Instancing replacement:** explicit instance names only —
`load stepgen <stepgen.0,stepgen.1>`. There is no default-channel-count concept
(`num_chan=0`), and a scalar module parameter applies identically to every named
instance.

### Smaller removals

| Gone | Note |
|---|---|
| Custom kernel-safe `rtapi_vsnprintf` | uspace-only now; formatting is libc's. `rtapi_print`/`rtapi_print_msg` remain |
| Key-based `rtapi_shmem_*` for cmods | `rtapi_shmem_new`/`getptr` are exported; `rtapi_shmem_delete` is not — a cmod calling it fails to load |
| `overrun` retry in the test runner | A flakiness-masking re-run, not behaviour. Tests run once |

## A2. Not ported yet

Intended to exist; nobody has done it.

### GladeVCP and QtVCP

**The largest single gap.** Both widget sets are unported, and the first
blocker is not the API: `gladevcp/__init__` → `hal_glib` → `_hal`, a C module
nothing in the tree builds any more, so the GTK stack is unimportable before
the porting question is even reached.

This is specified, not open-ended. [VCP_MIGRATION.md](VCP_MIGRATION.md) defines
one unified panel module for both — qtvcp's `Status` subclasses gladevcp's
`GStat`, so they are architecturally the same thing — with the widget-class →
`WidgetType` mapping and per-toolkit pin specs worked out widget by widget
against the real sources, plus the list of widgets that create pins but have no
`WidgetType` yet. `pyvcp` is already migrated to that model and is the working
precedent.

### Other classic GUIs

gmoccapy, Touchy, panelui and the tracking-test harness are not ported. AXIS
**is** ported and is the working full machine UI. ngcgui, pyngcgui and the
gremlin/qt5_graphics backplot widgets are already off the removed API.
linuxcnctop became a web app.

### Example configurations

Some shipped configs still assume the classic model and have not been migrated.

### TWOPASS HAL loading

No two-pass HAL file loading.

### `HAL_PORT` over the client boundary

`HAL_PORT` is fully implemented in `hal_lib`, but no client can reach it:
`haljson.parseHalType` knows only bit/float/s32/u32, and there is no REST
read/write for port buffers. Buffer/stream semantics over REST are nontrivial;
deliberately deferred.

## A3. Present, but behaves differently

The things most likely to bite when porting a client or a config. None of these
is a bug.

### Continuous jogs are dead-man'd

A REST/GMI continuous jog not refreshed within **2 seconds** is killed
(`internal/task` `jogTimeout`), as runaway protection against a disconnected
client. Classic NML jogs ran until `JOG_STOP` with no such contract.
HAL-pin-driven jogs (`JogFromHAL`) are exempt.

**Every ported client must re-issue a continuous jog inside the interval.**

### The client boundary is millimetres

Positions and velocities cross the API in mm. A classic inch-config client
passing machine-units/s will jog 25.4× too slow.

### HAL and halcmd details

| | stratuMAK | Classic |
|---|---|---|
| HAL lock levels | `all` / `tune` / `none` | 4-level LOAD/CONFIG/PARAMS/RUN, plus `status` rendering |
| `getp` output | verbose line (`s32 OUT name = val`) | bare value — parsers need `awk '{print $NF}'` |
| `getp` on RW params | does not resolve them; use `show param` | resolves |
| One-shot `list`/`show` | render nothing to stdout under `-f`; use a resident server + `halcmd` | print |

### modcompile vs halcompile

No `--personalities` flag, and components ignore `personality=` at load — only
the `.time` pin is created rather than the configured pins. The generated
`New()` also flattens any init error to `-1`, so a specific `EXTRA_SETUP` errno
is lost; the load correctly fails, but the diagnostic code is generic.

### Streaming timing

The live streamer/sampler does not preserve one-line-per-thread-cycle
multiplicity: values are correct but repeated-row counts differ from classic for
held or debounced signals. File-backed `filestream` is deterministic and is what
the tests use.

### Standalone `rs274`

Emits extra `ON_RESET()` canon calls versus the classic dump — benign, but it
breaks byte-exact comparison against classic goldens.

### Not divergences — do not "fix" these

- **`G64 P<tol>` persists across programs.** This is exactly what 2.9 does; M2/M30
  deliberately excludes motion control mode from the reset list.
- **Runtime NML setters for joint backlash/ferror/limits.** Set via INI + HAL
  pins instead. GUIs effectively never sent these live.
- **`JOINT_ENABLE`/`DISABLE`.** Vestigial in upstream too — 2.9's handler is
  also a no-op; amp-enable follows machine-enable + joint-active in the servo
  loop.

## A4. Known small gaps

Individually minor, listed so nobody rediscovers them.

| Gap | Effect |
|---|---|
| `conv_float_u32` component missing | Not built at all |
| `logic` ignores `personality=` | Configured and/or/in-NN pins are not created |
| `mux_generic` single-instance only | Rejects the classic comma-separated multi-instance config |
| `gmi.Stat` has no motion queue depth | The controller has it (`motstat get_queue_depth`); clients gating on read-ahead fill cannot |
| `gmi.Stat` field gaps | No `cycle_time`, `max_acceleration`, `max_velocity`, `program_units`, `queued_mdi_commands`, `tool_from_pocket`; joint position is `joint_actual_position` |
| `libgmi.so` declares no deps | Uses curl and cJSON but lists only libc as `NEEDED`; consumers must add `-lcurl -lcjson` |
| Mode switch drops queued MDIs silently | 2.9 prints "dropping %d queued MDI commands" |
| Startup-code motion at E-stop | Faults exec_state; 2.9 parks the move in the interp list |
| hostmot2 sim / hm2 test comp | Path not validated on stratuMAK |

---

# Part B — designed, not built

Direction the architecture has been shaped for. Each entry states what exists
today, so the gap is the difference — not the whole thing.

## B1. Go path planner (trajectory planner, phase 2)

**Today:** the inherited C trajectory planner runs in RT unchanged, but is
cleanly isolated — `tp.gmi` wraps it, `tpmod.so` is a standalone cmod with zero
undefined HAL/RTAPI symbols, `motmod` consumes it through function pointers with
no TP headers included, and all global state is gone (`motmod_inst_t` passed
explicitly). That isolation was phase 1 and it is done.

**The gap:** move the expensive, non-cyclic work out of RT into Go — segment
addition, blend arc calculation, velocity optimisation, S-curve solving,
geometry setup. What stays in RT is position interpolation, jogging (simple and
coordinated), trajectory playback, and kinematics.

**Why it is worth doing:** `src/cnc/tp/` is currently ~7,260 lines of C running
in the servo cycle (`tp.c` 3,897, `blendmath.c` 1,882, `tc.c` 925, `tcq.c` 354,
`spherical_arc.c` 204). The design target leaves a low-hundreds-of-lines RT
executor plus jogging, and moves the rest to a language with tests, types and
tooling — shrinking the part that must be reviewed as hard-RT to something
reviewable. The typed-API isolation means the Go planner can be developed and
swapped in incrementally without touching `motmod` or the servo loop.

## B2. Unified configuration, and native EtherCAT config

**Today:** three formats, all inherited. INI for the machine, Go-templated HAL
files for module loading and wiring, and expat-parsed XML in the LinuxCNC-EtherCAT
`lcec` format for the fieldbus. This is a deliberate migration-era choice — it
kept the inherited EtherCAT device library (44 device drivers) working
unchanged — not an end state.

**The gap, two halves:**

- A structured, version-control-friendly configuration format replacing
  INI + HAL + XML, with a defined layering of system defaults → module config →
  machine overrides.
- Native EtherCAT configuration generated from vendor **ESI** files: a device
  library browser, an interactive and batch generator producing network and
  per-slave configuration plus HAL signal mapping, and validation. Today an
  integrator hand-writes the `lcec` XML.

## B3. IEC 61131-3 Structured Text cross-compiler

**Today:** the ClassicLadder compiler is built and working — a Go compiler for
Ladder Diagram and Sequential Function Charts producing an RT-safe C evaluator,
with arithmetic expression evaluation, Modbus RTU/TCP, a Vue editor and a full
GMI surface. It is differentially verified against a headless build of the 2.9
engine.

**The gap:** the same pipeline for Structured Text — POUs (`FUNCTION`,
`FUNCTION_BLOCK`, `PROGRAM`), the IEC type set, and compilation to both C (for
the RT path) and Go, with symbol tables for debugging.

**Why the order makes sense:** ClassicLadder proves the architecture that ST
needs — Go-side compilation producing RT-safe C, executed in the servo cycle.
ST is the same shape with a harder front end.

## B4. Transfer plans and online change

**Today:** the RT cycle is a linked list of function pointers walked by
`thread_task()`, and the `New()`/`Destroy()` lifecycle already supports loading
and unloading a module at runtime.

**The gap:** replacing a component **while the machine runs**, the way CODESYS
does. That needs an offset-based uniform HAL store, precomputed transfer plans
describing the cycle's data movement, an atomic plan swap protocol confirmed
against the cycle counter, and state transfer matching old to new symbols by
name and type. Stable symbol-table export from components is the prerequisite,
and it also unlocks remote debugging.

## B5. API authentication and authorization

**Today: none.** The REST/WebSocket control surface trusts its network
completely and must be kept on loopback or an isolated machine network. The ADS
server is likewise unauthenticated and machine-internal.

This is **deferred-but-required**, not won't-fix. The shape is decided:

- Robustness is intrinsic and independent of binding — the crash/DoS surface is
  hardened regardless, because the endpoints will be exposed eventually.
- The authentication *mechanism* (authN, TLS, coarse allow/deny) belongs in an
  **external** reverse proxy or API gateway, not inside stratuMAK.
- Per-command **authorization** needs a thin in-process seam at
  `handleAPIRequest`/`handleCall` — a gateway cannot make those decisions blind
  to command semantics.
- The same-origin WebSocket restriction already in place is complementary and
  stays either way.

A newer surface sharpens this: the server now executes a user-configured
`[FILTER]` program to convert source files to G-code, server-side, so every
client can open them.

---

## Keeping this file honest

- A capability is only listed as removed after the **exact mechanism** it needed
  was confirmed absent — not on a blanket "Python is gone". That discipline is
  recorded in `tests/DISPOSITION.md`, and the per-test reasoning survives there.
- If something moves from missing to present, delete the entry rather than
  annotating it. The closed-audit file next door shows how a register reads once
  every line is struck through.
- Part B entries state today's baseline first. A gap described without its
  starting point invites re-solving what is already built.
