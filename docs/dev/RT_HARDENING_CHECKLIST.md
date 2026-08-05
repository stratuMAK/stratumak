# stratuMAK — Real-Time Hardening Notes & Review Checklist

**Scope:** hard-RT correctness for a *single-process, mixed Go/C* machine controller — Go runtime for non-RT (REST, interpreter, TP, config, MQTT, persistence), native C `pthread`s under `SCHED_FIFO` for the hard-RT path (servo loop, HAL cyclic components, EtherCAT).

---

## 0. Verification status — audited 2026-07-15; quick wins applied on `rt-validate`

Code audit of the checklist in §4. Legend: **[x]** implemented (evidence cited), **[~]** partial / implicit / deployment-dependent, **[ ]** not implemented.

**Solid (implemented and verifiable in code):**
- RT thread creation, pinning, `SCHED_FIFO`, stack sizing/prefault/mlock, `/dev/cpu_dma_latency` — all in `src/stmak/internal/hallib/uspace_rtapi_lib.c`.
- Memory-locking strategy is the Go-safe one recommended in §1.4: `mlockall(MCL_CURRENT)` (no `MCL_FUTURE`), per-region `mlock` + write-prefault (`rtapi_lock_mem`), dlopen'd cmod segments locked, `mallopt(M_TRIM_THRESHOLD/M_MMAP_MAX)` to stop glibc returning pages.
- Shared memory is C-allocated (`rtapi_shmem_new` → `rtapi_calloc`, `src/rtapi/uspace_common.h:52`); the RT cycle dispatches only C function pointers (`hal_lib.c` `thread_task`) — no Go in the cycle by construction.
- halscope RT↔Go ring: C-allocated, C11 explicit acquire/release atomics used identically from both sides (Go calls inline C atomic helpers).
- Isolated-CPU pool auto-assignment from `/sys/devices/system/cpu/isolated` (`halcmd/cpupool.go`).

**Quick wins fixed on branch `rt-validate` (2026-07-15):**
- RT threads now block `SIGURG` + `SIGPROF` at the top of `task_wrapper()` (`uspace_rtapi_lib.c`).
- `hal_stream_readable()/writable()/depth()` now use acquire loads on `in`/`out` (ARM memory-ordering hole closed, `hal_lib.c`).
- `halscope_alloc()` now uses `rtapi_calloc`/`rtapi_free` — capture buffers prefaulted + mlocked.
- Stale "SysV shmem / SHM_LOCK" strategy comment corrected (`uspace_rtapi_lib.c`).
- cgo + modcompile now honor the configured C/C++ compiler (`STMAK_CGOENV`, baked `config.CCompiler/CxxCompiler`): a `CC=clang` configure now clang-compiles the cgo RT translation units too — previously they silently stayed gcc even in the `rip-and-test-clang` CI job. Verified: clang-built `stmakd`, whole tree compiles under clang.
- The full tree is now **warning-free under clang 19** (was 264 unique sites: 218 format-security in the hostmot2 stratuMAK HAL port, missing `override` in `rs274ngc_interp.hh`/`interp_g7x.cc`, missing virtual dtors in g7x [real UB], unused hm2_modbus helpers, C++ VLAs in `interp_convert.cc`, duplicate-const in gmicompile codegen). gcc stays at zero warnings. This clears the way for a `-Werror` clang gate. Caveat: Go's build cache replays stale cgo compile warnings on cache hits (it does not hash externally-included C headers) — force a recompile before trusting warning output.

**Forbidden-call enforcement landed (2026-07-15, `rt-validate`):**
- `RTAPI_NONBLOCKING`/`STMAK_NONBLOCKING` macro infra (rtapi.h, `stmak_rt_check.h`), inert on gcc and clang < 20.
- HAL funct pointer *types* are nonblocking (`hal_funct_ptr_t` in hal.h, `stmak_hal_funct_t` in stmak_hal.h) — the dispatch indirection is closed.
- Annotated + verified: rtapi time/delay/pll/port primitives, the stratuMAK log ring producer, `@rt_safe` GMI callback types (gmicompile emits the annotation), all modcompile-generated comp functs, halscope sampler. Trust boundaries are explicit `*_TRUSTED_BEGIN/END` blocks with justification (TLS lookups, `clock_gettime` vDSO, bounded `rtapi_delay`, log-ring `vsnprintf`) — grep for `NONBLOCKING_TRUSTED` to audit.
- `make rt-effects-check` verifies 126 RT TUs (core RTAPI/HAL + halscope + every generated .comp) with `-Werror=function-effects`; wired into the `rip-and-test-clang` CI job. Debian 13 ships only clang 19 (broken analysis), so the check uses a pinned LLVM 22.1.8 release binary, sha256-verified, downloaded once by `scripts/rt-clang.sh` — diagnostic only, gcc stays the production compiler.
- Real findings already fixed by the analysis: `anglejog` had 7 function-local statics (shared across instances — a genuine multi-instance bug), `eoffset_per_angle` did `fprintf(stderr, ...)` in the RT path.

**motmod in scope (2026-07-15, follow-up):** all 8 motmod TUs (`emcmotController` + `emcmotCommandHandler` transitively: control/command/axis/simple_tp/cubic/handlers) verify clean — 134 TUs total in `make rt-effects-check`. The tp/home/kins GMI APIs got their RT surface marked `@rt_safe` in the IDL (evidence-driven: exactly the members motmod calls from the cycle), comps implementing `gmi_*` callbacks annotate their impls. **Real finding fixed:** the jerk-filter reconfig path allocated/freed its boxcar buffers in the servo thread (`jerk_filter_recompute_window` via SET-param commands) — now preallocated worst-case at init, reconfig only re-strides. Notable: motmod needed zero trust-exemptions — the only trusted primitive it relies on is `rtapi_mutex_try/give` (single atomic bit ops, verified not exempted).

**lcec in scope (2026-07-15, follow-up):** all 53 EtherCAT driver TUs (core, DC-sync, classes, all 44 device drivers) verify clean — 187 TUs total in `make rt-effects-check`. The `lcec_slave_rw_t` / `lcec_dcsync_callback_t` types are nonblocking-typed (formalizing the doc comment "must not block, sleep, or allocate"), every device's `proc_read`/`proc_write` implementation is annotated and body-verified. The EtherLab master is a **trust boundary**: `ecrt_rt_api.h` re-declares exactly its documented RT interface (send/receive/domain/DC/state + real-value PDO accessors) as nonblocking — any other ecrt call (SDO/EoE/config) from RT code is a diagnosable error. The master implementation itself is a separate audit item (own session). One deliberate exemption: the RT cycle takes `master->mutex` (`lcec_master_rt_lock`, TRUSTED) to serialize against the master's fragment-locked background access per the `ecrt_master_callbacks()` contract — hold times bounded by design; a try-lock+skip redesign for the state-polling sites is a possible refinement.

**EtherLab master no longer a trust boundary (2026-07-20, follow-up):** the master submodule now natively annotates its documented rt_safe subset with `ECRT_RT_ATTR` (`master/include/ecrt.h`, overrideable macro in `ecrt_rt.h`) and ships its own transitive function-effects check of the cyclic *implementation* (`master/script/rt-effects-check.sh`: master send/receive/domain, the userspace device layer, the raw/ccat transports, plus rt_ok/rt_bad/rt_override contract self-tests). Two consequences on the LinuxCNC side: (1) the hand-maintained `ecrt_rt_api.h` re-declaration is **retired** — `lcec.h` instead does `#define ECRT_RT_ATTR STMAK_NONBLOCKING` before `#include "ecrt.h"`, so lcec RT code verifies against the library's own annotations with no parallel list to drift (a non-RT ecrt call from RT context stays a diagnosable error — verified: an RT-annotated `ecrt_master_sdo_download` caller is rejected, the send/receive/domain subset passes). (2) `make rt-effects-check` now runs the submodule's check as **section 8**, so one target verifies the whole EtherCAT RT path — driver *and* master implementation. All 240 TUs green (was 228). The master audit that §43.1/§50 called a separate session is closed by this; the objdump two-level walk (§50) is now a redundant cross-check, not the primary mechanism. Still hardware-gated: exercise on a real EtherCAT rig before production (§52). The one lcec-side deliberate exemption (the RT cycle taking `master->mutex` per the `ecrt_master_callbacks()` contract) is unchanged.

**tpmod + homemod in scope (2026-07-15, follow-up):** trajectory planner (tp/tc/tcq/blendmath/spherical_arc + emcpose + **posemath**, verified not trusted), homing FSM and the CiA-402 homing module all verify clean — 196 TUs total. The `mot` GMI API's RT surface (58 members) is now `@rt_safe`-typed, evidence-driven from what tp/homing actually call in the cycle, and motmod's implementations of it are annotated and verified. **Real finding fixed:** `tpUpdateRigidTapState` kept the previous-cycle spindle position in a function-local `static` — shared across TP instances; moved into `PmRigidTap` (per-move state). Note: the check deliberately omits the production rule's `-fno-builtin-sin/-cos` so libm calls stay inferrable builtins.

**hostmot2 in scope (2026-07-16):** the Mesa FPGA driver core (27 TUs), the eth/pci/7i43/7i90 transports and hm2_modbus all verify clean — **228 TUs total** in `make rt-effects-check`. The internal `llio` vtable (read/write/queue ops) is nonblocking-typed, ~250 driver functions annotated. Trust boundaries: `eth_socket_send/recv` in hm2_eth (raw LBP16 UDP from the servo thread is that transport's documented architecture; latency is a NIC/IRQ property audited by latency tests) plus errno/strerror helpers on its error paths, and `hm2_hz_to_mhz` (bounded snprintf, log formatting only). **Real findings fixed:** cross-board function-local statics — the resolver write FSM (`state/cmd_val/data_val/timer`), the sserial Fanuc 2-part encoder reassembly state (shared across *channels and boards*), abs_encoder per-channel error arrays, the dpll settle counter, and hm2_eth's 1 KB write scratch packet — all moved into per-board/per-channel structs. One-shot log guards hoisted to file scope (process-wide by design).

**hostmot2 follow-up (2026-07-16, pre-merge review):** the check now globs *every* `.c` in the driver directory, closing a coverage gap — the **spi/rpspi/spix transports** (and hm2_test/setsserial) were built by the Submakefile but not checked. All verify clean after annotation. New trust boundaries: `spi_ioc_message`/`spidev_ioc_message` (the spidev SPI_IOC_MESSAGE exchange is those transports' documented RT path, same argument as hm2_eth's UDP), the shared `hm2_rt_errno/clear_errno/strerror` shims (hoisted into hostmot2-lowlevel.h, replacing hm2_eth's local copies), and `buffer_check_room` in hm2_spix (**warm-up allocations** — the queue buffers grow with `rtapi->calloc/realloc` from the servo thread until steady state; a setup-time preallocation is open, below). **Real finding fixed:** `hm2_uart_send`'s warn-once guard let every second failing call fall past validation into an `instance[-1]` OOB read and a garbage `llio->write` from the servo thread (pre-existing upstream; error returns are now unconditional, only the log is one-shot — `hm2_uart_read`'s bitrate guard had the same shape). The public serial API (`hostmot2-serial.h`) now types the BSPI transfer callbacks `hm2_bspi_xfer_fn_t` (nonblocking), closing the registration seam for external C components; the GMI shim (`hm2_serial_provider.c`) still transports the callback as an opaque ptr with a justified trust cast — typing the callback in the hm2_serial GMI IDL (`@rt_safe` callback type) is open, below.

**Biggest open gaps (priority order):**
1. **Effects-check scope**: remaining hand-written cmod TUs (small stragglers, e.g. classicladder RT part). ~~The **EtherLab master library** needs its own audit session~~ — DONE 2026-07-20: the master now self-annotates and is verified transitively by section 8 of `rt-effects-check.sh` (see the follow-up above). RTSan (`-fsanitize=realtime`, needs a runtime harness against the sim) also still open. The motmod/tpmod/homemod/lcec TU lists in `rt-effects-check.sh` are still hand-copied from the Submakefiles (hostmot2 now globs) — a drift assertion against the Submakefile source variables would make list rot fail loudly.
1a. **hm2_spix queue preallocation**: `buffer_check_room` grows the queued-transfer buffers with `rtapi->calloc/realloc` from the servo thread (trusted as warm-up allocations, see above); replace with a setup-time preallocation sized from the tram queue.
1b. **GMI bspi callback typing** — *now unblocked (2026-07-21)*: the hm2_serial GMI API transports the BSPI transfer callback as an opaque `ptr` (`bspi_set_read_function`/`bspi_set_write_function` in `hm2_serial.gmi`), erasing the nonblocking type at the module seam (trust cast in `hm2_serial_provider.c`); give it a typed `@rt_safe` callback in the IDL so module-side registration is checked end-to-end. **The enabling gmicompile capability landed as G-L1** (`505e87d19f`): `@rt_safe` on a `@callback` now stamps the `_cb` typedef `STMAK_API_NONBLOCKING`. Remaining work is driver-side + verification (declare the callback type in the IDL, drop the opaque-ptr trust cast in the provider, confirm under clang `-Wfunction-effects` in the `rt-validate` worktree) — a task for this RT session, no longer blocked on the emitter.
2. **No deadline-miss detector → E-stop** (§1.5): `unexpected_realtime_delay()` logs once per session and takes no action.
3. **No RT priority headroom**: HAL threads are created at `sched_get_priority_max()-1` (= 98) descending (`hal_lib.c` reserves only the single top slot).
4. **No torture CI / jitter histogram** (§3); the nightly Go race-detector suite (`nightly-stmak.yml`) exists but is a different concern, and the strict cgo pointer checker is not wired into any test target.
5. **gcc-native complement (planned)**: objdump-based *function-level reachability* check on the built cmods — per-function call graph from `objdump -d`, BFS only from the RT roots (non-RT code in the same .so is never visited), forbidden = malloc/lock/blocking PLT symbols, witness path in the report. Runs on the production gcc binaries with distro tools only; prototype exists. It would also cross-check the **EtherLab master** on the production gcc binaries: a two-level walk `lcec.so → libethercat.so` rooted at the master's rt_safe subset — now a redundant complement to the master's own clang function-effects audit (section 8), no longer the primary mechanism. Known blind spot (indirect calls inside a library) is covered by RTSan at runtime.
6. **`.text.rt` section marker (planned)**: the `nonblocking` attribute leaves no trace in the ELF, so binaries can't self-describe their verified RT set. A companion decl-attribute macro (`__attribute__((section(".text.rt")))`) alongside the annotations would make RT roots machine-readable for the objdump check and enable an escape criterion ("call from `.text.rt` into unmarked `.text` that is not whitelisted").
7. **Hardware validation debt**: sim runtests are green at baseline (2026-07-16), but the hm2/lcec state-relocation fixes (resolver write FSM, sserial Fanuc encoder reassembly, abs_encoder error arrays, dpll counter, hm2_eth write scratch) only get genuinely exercised on a Mesa/EtherCAT rig — smoke-test before trusting them in production.

Item-by-item detail in §4 below.

**Core premise:** the RT threads are raw C `pthread`s that the Go scheduler does not manage. Everything below exists to keep that separation true — at runtime, across every contributed `cmod`, and forever. The failure modes are almost all *tail* problems: invisible in normal operation, visible only in a long-run jitter histogram or under a sanitizer.

---

## 1. The five hardening areas

### 1.1 Keep the Go scheduler off the RT cores
The Go runtime scheduler does **not** honour `isolcpus`. It spreads Ps, the sysmon thread, and GC workers across whatever is in the *process* affinity mask.

- Boot the RT core(s) with `isolcpus`, `nohz_full`, `rcu_nocbs`.
- Set the **process** affinity mask to *exclude* the RT cores **before the Go runtime spins up** (C constructor via `sched_setaffinity`, or `taskset`/cgroup at launch). If you do this from Go, sysmon/GC threads have already been placed on the RT cores.
- Pin the RT `pthread`(s) to the isolated core(s) with `pthread_setaffinity_np`.
- Open `/dev/cpu_dma_latency` and write `0` to disable deep C-states; keep the fd open for the process lifetime.

### 1.2 Mask signals on the RT thread (not optional)
Go uses `SIGURG` (async preemption) and `SIGPROF` (profiling). Process-directed signals are delivered to *any* thread that hasn't blocked them — including your servo thread.

- Immediately after RT thread start: `pthread_sigmask` to block at least `SIGURG` and `SIGPROF`.
- The RT thread must be a raw `pthread_create` thread, **never** a Go-locked OS thread (`runtime.LockOSThread`), and must never call back into Go during a cycle (any cgo→Go trampoline can block on GC coordination).

### 1.3 The cgo boundary / ring buffers — the most likely subtle bug
- Shared ring buffers must live in **C-allocated** memory (`mmap`/`malloc`); Go accesses them via `unsafe.Pointer`. If the ring lives on the **Go heap** and C reads/retains it, that violates cgo pointer rules and can corrupt/crash *under GC* — nondeterministically.
- Memory model must agree on both sides: Go `sync/atomic` vs C11 `__atomic`, acquire/release on head/tail, single-producer/single-consumer.
- 64-bit indices without torn reads (relevant on 32-bit/ARM targets).
- Cache-line pad head vs tail to avoid false sharing (pure jitter hygiene, shows up in the tail).

### 1.4 Memory locking & prefaulting
- `mlockall(MCL_FUTURE)` + Go is double-edged: it locks Go's growing heap too, and can hit `RLIMIT_MEMLOCK`. Prefer explicit locking of RT regions (RT thread stacks + shared buffers) and **write-touch every page once at init** to prefault, rather than relying on `MCL_FUTURE` for the Go side.
- RT `pthread` stacks must be pre-grown/pre-faulted — Go does not manage them, so this is your responsibility.

### 1.5 Failure semantics — the dangerous case is the silent overrun, not the crash
- "Servo loop crashes → process dies → external watchdog → E-stop" only covers the *clean* crash.
- Add a **deadline-miss detector** inside the RT cycle (measured vs expected cycle time → trip E-stop on overrun). This catches the hang: page fault, unexpected mutex, a cgo call that stalls on GC.
- Confirm the "external watchdog" is genuinely external (hardware watchdog / separate process or hardware line), not another software thread in the same dying process.
- The SIL-rated safety function must ride **FSoE** and must **not** depend on the Go/host E-stop path — the point of FSoE is that it doesn't trust the host.

---

## 2. Tooling to enforce "nothing forbidden in the RT path"

### 2.1 Static (compile time): Clang Function Effect Analysis
- Mark RT entry points `[[clang::nonblocking]]`, build with `-Wfunction-effects` (ideally `-Werror`).
- The compiler verifies the transitive call graph and flags: allocation/deallocation, calling any non-`nonblocking` function, exceptions, and static-local variables.
- Works in **C** (`-x c -std=c23 -Wfunction-effects`), not just C++.
- **Key for HAL:** the attribute applies to function *types*, so type the cyclic HAL dispatch **function pointer** as `nonblocking` — then assigning a non-`nonblocking` `cmod` cyclic function to it is a diagnostic. This is what closes the function-pointer indirection that defeats naive call-graph tools.

### 2.2 Runtime: RealtimeSanitizer (RTSan)
- Same `[[clang::nonblocking]]` attribute; build with `-fsanitize=realtime`.
- Errors at runtime on `malloc`/`free`/`pthread_mutex_lock`/syscalls/etc. inside a nonblocking context.
- In LLVM since v20; C and C++; Linux (x86 + aarch64); no Windows.
- C interface available: `__rtsan_disable()`/`__rtsan_enable()`, standalone `rtsan_standalone.h` with `__RTSAN_NOTIFY_BLOCKING_CALL()`.
- Catches what static misses: blocking calls deep in the stack, in precompiled libs, or through unresolved indirection — but only on paths actually executed, so it is only as good as the torture tests.

### 2.3 Practical constraint
Both tools are **Clang-only** (GCC has no equivalent). No need to switch production off GCC — add a Clang CI job that builds the RT translation units (servo loop, HAL cyclic, `cmod`) with `-Wfunction-effects -Werror`, and once more with `-fsanitize=realtime` against the sim.

### 2.4 Complements that run on the GCC production build
- `/proc/<tid>/{minflt,majflt}` freeze assertion after warmup — any increase = a page fault = something touched unlocked memory.
- Kernel `osnoise` / `timerlat` tracers (PREEMPT_RT) — catch kernel/HW noise no userspace tool sees.
- (The classic `LD_PRELOAD` malloc-abort shim is made redundant by RTSan — it's the productised version of the same idea.)

### 2.5 Go side (different tool, different concern)
- Run the test suite under Go's strict cgo pointer checker to validate the ring-buffer boundary. The flag moved between `GODEBUG=cgocheck=2` and `GOEXPERIMENT=cgocheck2` across recent Go releases — check the exact form for your Go version.

---

## 3. Verification — the number that actually decides it
- Build a cyclictest-equivalent into the running system: log worst-case RT cycle latency over **24–72 h under realistic load** (streaming G-code, deliberate GC churn via `runtime.GC()` in a loop, REST hammering, EtherCAT at target rate).
- Publish the histogram. It is simultaneously the technical proof, the regression guard, and the political currency for a "LinuxCNC 3.0" blessing.
- Torture CI: run the RT thread under RTSan + forced GC + allocation pressure + network saturation, assert **no cycle overrun**. This is what stops a future `cmod` from sneaking an allocation into the RT path.

---

## 4. Review checklist

Status audited 2026-07-15 against the working tree (see §0 for summary). `[x]` = implemented, `[~]` = partial/implicit/deployment-dependent, `[ ]` = open.

### CPU isolation & scheduling
- [~] RT core(s) booted with `isolcpus` + `nohz_full` + `rcu_nocbs`
  — Boot config is a deployment concern, but the runtime *detects* isolated cores via `/sys/devices/system/cpu/isolated` and builds the auto-assignment pool from them (`src/stmak/internal/halcmd/cpupool.go:159`, `InitCPUPool`); it warns when a thread gets no isolated core in RT mode. `nohz_full`/`rcu_nocbs` are neither checked nor documented anywhere in the tree.
- [~] Process affinity excludes RT cores, set **before** Go runtime init
  — No explicit exclusion exists anywhere (no `sched_setaffinity`, no C constructor, no `taskset` in launcher/scripts). It works *implicitly* when `isolcpus` is used: the process inherits a boot-time mask that excludes isolated cores, so Go runtime threads never land there. Without `isolcpus` there is no separation at all (and the CPU pool is empty anyway). An explicit constructor-time exclusion would make the guarantee independent of boot config.
- [x] RT `pthread`(s) pinned to isolated core(s)
  — `pthread_attr_setaffinity_np` per task when `cpu_number >= 0` (`src/stmak/internal/hallib/uspace_rtapi_lib.c:609`); cpu assigned from the isolated pool or validated against it (`cpupool.go:68` `acquireCPU`).
- [x] `SCHED_FIFO` set on the RT thread only (never the whole process / Go threads)
  — Set per-thread via `pthread_attr_setschedpolicy` + `PTHREAD_EXPLICIT_SCHED` (`uspace_rtapi_lib.c:603-607`); falls back to `SCHED_OTHER` + cooperative lock if `harden_rt()` fails.
  **Note:** `rtapi_initialize_app()` is what performs that fallback — `app_policy` is a
  static `SCHED_FIFO` until it runs, so any embedder that reaches `hal_create_thread`
  without calling it first gets `EPERM` from `pthread_create` in an unprivileged process
  and no threads at all.
- [x] Cooperative task exit releases `thread_lock` on every path (non-RT `do_thread_lock`)
  — **Fixed 2026-07-22.** `task_wrapper()` took `thread_lock` at task start and never
  released it, relying on `task_wait()` to drop it when it saw `task_exit`. `task_wait()`
  does so on its two exit checks, but the flag can be set in the window *after* it
  re-acquires the lock and checks it — the task loop's own condition then ends the task
  with the lock **held**, leaking it locked with its owner gone. The next `newthread`
  blocked forever in `task_wrapper`'s acquire, never saw its own exit flag, and
  `hal_thread_delete`'s `pthread_join` never returned: **a hung controller on `delthread`
  once a thread had already been deleted, on every non-RT-privileged deployment**. Ownership
  is now explicit (`struct rtapi_task.holds_thread_lock`, `rtapi/rtapi_task.h`) and released
  exactly once, on whichever path the task leaves. Regression test:
  `internal/halcmd.TestThreadCreateDeleteCycles` (mutation-verified: hangs 5/5 without the
  fix). Sibling of the earlier `task_wait()` shutdown-deadlock fix — same lock, different
  escape path.
- [ ] RT priority leaves headroom below kernel/IRQ threads (e.g. ≤ 80); below the EtherCAT NIC IRQ thread
  — Not met: `hal_create_thread` reserves only the single highest priority and hands out `sched_get_priority_max()-1` (= 98 on Linux) descending (`src/stmak/internal/hallib/hal_lib.c:2021-2050`). No policy caps HAL threads below IRQ-thread priorities (default 50) — deliberate headroom requires threadsirq-priority tuning at deployment, and nothing enforces or documents it.
- [x] `/dev/cpu_dma_latency` opened + `0` written, fd held open
  — `uspace_rtapi_lib.c:458-467`; fd is intentionally never closed (held for process lifetime, `O_CLOEXEC`).

### Signals & Go isolation
- [x] RT thread blocks `SIGURG` + `SIGPROF` (and other Go runtime signals) via `pthread_sigmask`
  — Fixed on `rt-validate`: `task_wrapper()` blocks `SIGURG` + `SIGPROF` via `pthread_sigmask(SIG_BLOCK, ...)` before any RT work (`uspace_rtapi_lib.c`). Synchronous fault signals (SIGSEGV etc.) remain deliverable. (Background: Go's async preemption sends *thread-directed* `SIGURG` only at threads executing Go code, so raw C threads were never preemption targets — the mask closes the *process-directed* delivery path.)
- [x] RT thread is raw `pthread_create`, not `LockOSThread`
  — `task_start()` → `pthread_create` (`uspace_rtapi_lib.c:620`). The only `LockOSThread` in the tree pins the *main* goroutine for Boost.Python thread-state (`cmd/stmakd/main.go:50`) — non-RT, unrelated.
- [x] RT cycle is 100% cgo-free and Go-pointer-free (no callback into Go)
  — True by construction: `thread_task()` walks the funct list and calls C function pointers only (`hal_lib.c:2982-2984`). Go-side modules that need an RT funct (halscope, classicladder) export a *C* function via a cgo wrapper (`halscope/module.go` `go_hal_export_funct`), never a Go trampoline. All shared structs are C-allocated. Caveat: nothing *enforces* this for future cmods — that is exactly the §2 tooling gap.

### Memory
- [x] Shared ring buffers are C-allocated, never on the Go heap
  — `rtapi_shmem_new()` allocates via `rtapi_calloc` (C heap, prefaulted + mlocked) (`src/rtapi/uspace_common.h:52-89`); halscope instance + triple buffers likewise via `rtapi_calloc` (`halscope/halscope_rt.c`, fixed on `rt-validate`).
- [x] RT regions (stacks + buffers) locked and **write-touched once** at init (prefaulted)
  — `rtapi_lock_mem()` write-prefaults every page then `mlock`s (`uspace_rtapi_lib.c:196-225`); used by `rtapi_malloc/calloc/realloc`, thread stacks, dlopen'd cmod PT_LOAD segments (RELRO-aware, `dl_mlock_callback`), and — since `rt-validate` — the halscope instance + capture buffers. `mlockall(MCL_CURRENT)` one-shot + `mallopt(M_TRIM_THRESHOLD=-1, M_MMAP_MAX=0)` in `configure_memory()`.
- [x] RT `pthread` stacks pre-grown to worst-case depth
  — Minimum 1 MB enforced (`task_new`, `uspace_rtapi_lib.c:751`), and the whole stack (minus guard page) is write-prefaulted + mlocked at thread start (`task_wrapper`, `uspace_rtapi_lib.c:656-668`).
- [ ] `minflt`/`majflt` frozen after warmup (asserted)
  — Not implemented; no `/proc/<tid>/stat` fault-counter check anywhere.

### cgo / ring-buffer boundary
- [x] Pointer direction respects cgo rules (C owns the shared memory)
  — All shared state is C-allocated; Go reaches into it via `unsafe.Pointer` (e.g. `halscope/module.go:291+`). No C code retains Go pointers.
- [x] Matching acquire/release semantics on both sides (Go `sync/atomic` ↔ C11 atomics)
  — **halscope:** both sides use C11 explicit atomics (Go through inline helpers `halscope_atomic_*`, `halscope_rt.h:159-179`; acquire loads / release stores on `state`, `done_buf`, `done_gen`, `readers`).
  — **hal_stream (sampler/streamer/filestream fifo):** `in`/`out` are release-stored and acquire-loaded everywhere; the emptiness/fullness predicates `hal_stream_readable()/writable()/depth()` were converted from plain `volatile` loads to acquire loads on `rt-validate` (previously a stale-data window on weakly-ordered targets like ARM; x86 was unaffected).
- [x] 64-bit indices safe against torn reads on target arch
  — N/A by design: all fifo indices are 32-bit (`volatile unsigned int in/out`, `hal_priv.h:454-455`; `atomic_int`/`atomic_uint` in halscope).
- [ ] head/tail cache-line padded (no false sharing)
  — Not done: `struct hal_stream_shm` has `in`/`out` adjacent in the same cache line (`hal_priv.h:452-462`); `halscope_t` likewise unpadded. Pure jitter hygiene, not correctness.
- [ ] Test suite passes under Go's strict cgo checker
  — Not wired up: no `GODEBUG=cgocheck2`/`GOEXPERIMENT=cgocheck2` in `stmak-test*` targets (`src/stmak/Submakefile:286-293`) or CI. (The nightly race-detector run in `.github/workflows/nightly-stmak.yml` is valuable but checks a different property.)

### Forbidden-call enforcement
- [~] RT entry points annotated `[[clang::nonblocking]]`
  — Done on `rt-validate` for the core + generated scope: rtapi primitives (`rtapi.h` `RTAPI_NONBLOCKING`), stratuMAK rtapi/log producer APIs (`stmak_rt_check.h`, `stmak_rtapi.h`, `stmak_log.h`), `@rt_safe` GMI callback types (gmicompile-emitted), every modcompile-generated comp funct, halscope sampler. Open: motmod, hostmot2, lcec, hand-written cmods. GNU spelling `__attribute__((nonblocking))`, gated to clang ≥ 20 (clang 19's analysis is broken: false conversion warnings, no body verification — verified empirically).
- [x] HAL cyclic dispatch function-pointer *type* is `nonblocking`
  — `hal_funct_ptr_t` (hal.h) used by `hal_export_funct`, `hal_funct_t`/`hal_funct_entry_t` and the `thread_task` dispatch; `stmak_hal_funct_t` (stmak_hal.h) in the cmod vtable. On gcc the annotation is empty — types and ABI unchanged.
- [x] Clang CI job: `-Wfunction-effects -Werror` on RT translation units
  — `make rt-effects-check` → `scripts/rt-effects-check.sh`: 126 TUs (core RTAPI/HAL, halscope, all generated comps) with `-Werror=function-effects`; in the `rip-and-test-clang` CI job (post-merge on `stmak`) with the pinned toolchain cached. Uses LLVM 22.1.8 official release binaries (sha256-pinned per arch, X64+ARM64) via `scripts/rt-clang.sh` since no distro clang ≥ 20 exists on Debian 13. Scope grows with item 1.
- [ ] Clang CI job: `-fsanitize=realtime` against the sim — not present; the pinned toolchain now makes this possible (needs an RTSan-instrumented sim build + runtime harness).
- [x] Every `__rtsan_disable` / `NONBLOCKING_UNSAFE` exemption is reviewed & justified
  — Exemption mechanism is `RTAPI_NONBLOCKING_TRUSTED_BEGIN/END` (+ STMAK variant); each of the current uses carries a justification: task-self/PLL (TLS lookup), `rtapi_get_time`/`stmak_log_now_ns` (`clock_gettime` vDSO), `rtapi_delay` (clamped ≤ 10 µs), `stmak_log_emit` (lock-free ring, fixed-buffer `vsnprintf`, drop-on-full). Audit with `grep -rn NONBLOCKING_TRUSTED src/`.

Design note — `nonallocating`: deliberately **not** used. Verified on LLVM 22.1.8: `nonblocking` verification already diagnoses allocation (strict superset), and a `nonblocking` function may not call a merely-`nonallocating` one — so mixing the weaker attribute into the RT call graph would break chains without adding coverage. Reserve `nonallocating` for a future "may block briefly, never allocate" tier (setup paths at RT-quiescence) if one gets defined; it's a 3-line macro addition then.

### Failure semantics
- [ ] Deadline-miss detector in the RT cycle → E-stop on overrun
  — Not implemented. `unexpected_realtime_delay()` logs **once per session** and takes no action (`uspace_rtapi_lib.c:563-575`). HAL tracks per-funct/per-thread `runtime`/`maxtime` but nothing acts on them. The Go-side comm watchdog (motion-status reads failing for 1 s → machine off, `src/stmak/internal/task/monitor.go:15-22`) catches a fully hung RT thread from the non-RT side, but it is not in-cycle, not overrun-based, and lives in the same process.
- [~] Watchdog is genuinely external (HW or separate process/line)
  — The mechanism exists at the fieldbus level: EtherCAT slave SM watchdogs are configurable per slave (`ecrt_slave_config_watchdog`, `src/hal/drivers/ethercat/main.c:221-223`; `lcecConfTypeWatchdog` config), so drives fault autonomously if cyclic traffic stops — genuinely external to the host. But it is opt-in per configuration; nothing asserts a watchdog is configured, and non-EtherCAT setups have no external watchdog at all.
- [~] SIL-rated stop rides FSoE, independent of the Go/host E-stop path
  — The in-tree EtherCAT driver supports FSoE logic/safety slaves (`is_fsoe_logic`, `priv.h:70`; EL1904/EL2904/logic-device staged preinit, `main.c:500-511`), so the architecture supports it. Whether the SIL stop actually rides FSoE is a per-machine deployment property — not verifiable in-repo.

### Verification
- [ ] 24–72 h jitter histogram under realistic + adversarial load, published
  — No cyclictest-equivalent / latency-histogram instrumentation in stratuMAK, no published results in the repo.
- [ ] Torture CI (RTSan + forced GC + alloc pressure + net saturation) asserts no overrun
  — Not present. Closest existing guard: nightly full Go test suite under the race detector plus runtests against a race-built `stmakd` (`.github/workflows/nightly-stmak.yml`) — a concurrency regression guard, not an RT-latency one.

