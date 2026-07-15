# GOMC — Real-Time Hardening Notes & Review Checklist

**Scope:** hard-RT correctness for a *single-process, mixed Go/C* machine controller — Go runtime for non-RT (REST, interpreter, TP, config, MQTT, persistence), native C `pthread`s under `SCHED_FIFO` for the hard-RT path (servo loop, HAL cyclic components, EtherCAT).

---

## 0. Verification status — audited 2026-07-15; quick wins applied on `rt-validate`

Code audit of the checklist in §4. Legend: **[x]** implemented (evidence cited), **[~]** partial / implicit / deployment-dependent, **[ ]** not implemented.

**Solid (implemented and verifiable in code):**
- RT thread creation, pinning, `SCHED_FIFO`, stack sizing/prefault/mlock, `/dev/cpu_dma_latency` — all in `src/gomc/internal/hallib/uspace_rtapi_lib.c`.
- Memory-locking strategy is the Go-safe one recommended in §1.4: `mlockall(MCL_CURRENT)` (no `MCL_FUTURE`), per-region `mlock` + write-prefault (`rtapi_lock_mem`), dlopen'd cmod segments locked, `mallopt(M_TRIM_THRESHOLD/M_MMAP_MAX)` to stop glibc returning pages.
- Shared memory is C-allocated (`rtapi_shmem_new` → `rtapi_calloc`, `src/rtapi/uspace_common.h:52`); the RT cycle dispatches only C function pointers (`hal_lib.c` `thread_task`) — no Go in the cycle by construction.
- halscope RT↔Go ring: C-allocated, C11 explicit acquire/release atomics used identically from both sides (Go calls inline C atomic helpers).
- Isolated-CPU pool auto-assignment from `/sys/devices/system/cpu/isolated` (`halcmd/cpupool.go`).

**Quick wins fixed on branch `rt-validate` (2026-07-15):**
- RT threads now block `SIGURG` + `SIGPROF` at the top of `task_wrapper()` (`uspace_rtapi_lib.c`).
- `hal_stream_readable()/writable()/depth()` now use acquire loads on `in`/`out` (ARM memory-ordering hole closed, `hal_lib.c`).
- `halscope_alloc()` now uses `rtapi_calloc`/`rtapi_free` — capture buffers prefaulted + mlocked.
- Stale "SysV shmem / SHM_LOCK" strategy comment corrected (`uspace_rtapi_lib.c`).
- cgo + modcompile now honor the configured C/C++ compiler (`GOMC_CGOENV`, baked `config.CCompiler/CxxCompiler`): a `CC=clang` configure now clang-compiles the cgo RT translation units too — previously they silently stayed gcc even in the `rip-and-test-clang` CI job. Verified: clang-built `gomc-server`, whole tree compiles under clang (warnings only).

**Biggest open gaps (priority order):**
1. **No forbidden-call enforcement at all** (§2): no `[[clang::nonblocking]]`, no `-Wfunction-effects`, no RTSan anywhere in the tree. A Clang build+test CI job already exists (`.github/workflows/ci.yml` `rip-and-test-clang`) that could host both.
2. **No deadline-miss detector → E-stop** (§1.5): `unexpected_realtime_delay()` logs once per session and takes no action.
3. **No RT priority headroom**: HAL threads are created at `sched_get_priority_max()-1` (= 98) descending (`hal_lib.c` reserves only the single top slot).
4. **No torture CI / jitter histogram** (§3); the nightly Go race-detector suite (`nightly-gomc.yml`) exists but is a different concern, and the strict cgo pointer checker is not wired into any test target.

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
  — Boot config is a deployment concern, but the runtime *detects* isolated cores via `/sys/devices/system/cpu/isolated` and builds the auto-assignment pool from them (`src/gomc/internal/halcmd/cpupool.go:159`, `InitCPUPool`); it warns when a thread gets no isolated core in RT mode. `nohz_full`/`rcu_nocbs` are neither checked nor documented anywhere in the tree.
- [~] Process affinity excludes RT cores, set **before** Go runtime init
  — No explicit exclusion exists anywhere (no `sched_setaffinity`, no C constructor, no `taskset` in launcher/scripts). It works *implicitly* when `isolcpus` is used: the process inherits a boot-time mask that excludes isolated cores, so Go runtime threads never land there. Without `isolcpus` there is no separation at all (and the CPU pool is empty anyway). An explicit constructor-time exclusion would make the guarantee independent of boot config.
- [x] RT `pthread`(s) pinned to isolated core(s)
  — `pthread_attr_setaffinity_np` per task when `cpu_number >= 0` (`src/gomc/internal/hallib/uspace_rtapi_lib.c:609`); cpu assigned from the isolated pool or validated against it (`cpupool.go:68` `acquireCPU`).
- [x] `SCHED_FIFO` set on the RT thread only (never the whole process / Go threads)
  — Set per-thread via `pthread_attr_setschedpolicy` + `PTHREAD_EXPLICIT_SCHED` (`uspace_rtapi_lib.c:603-607`); falls back to `SCHED_OTHER` + cooperative lock if `harden_rt()` fails.
- [ ] RT priority leaves headroom below kernel/IRQ threads (e.g. ≤ 80); below the EtherCAT NIC IRQ thread
  — Not met: `hal_create_thread` reserves only the single highest priority and hands out `sched_get_priority_max()-1` (= 98 on Linux) descending (`src/gomc/internal/hallib/hal_lib.c:2021-2050`). No policy caps HAL threads below IRQ-thread priorities (default 50) — deliberate headroom requires threadsirq-priority tuning at deployment, and nothing enforces or documents it.
- [x] `/dev/cpu_dma_latency` opened + `0` written, fd held open
  — `uspace_rtapi_lib.c:458-467`; fd is intentionally never closed (held for process lifetime, `O_CLOEXEC`).

### Signals & Go isolation
- [x] RT thread blocks `SIGURG` + `SIGPROF` (and other Go runtime signals) via `pthread_sigmask`
  — Fixed on `rt-validate`: `task_wrapper()` blocks `SIGURG` + `SIGPROF` via `pthread_sigmask(SIG_BLOCK, ...)` before any RT work (`uspace_rtapi_lib.c`). Synchronous fault signals (SIGSEGV etc.) remain deliverable. (Background: Go's async preemption sends *thread-directed* `SIGURG` only at threads executing Go code, so raw C threads were never preemption targets — the mask closes the *process-directed* delivery path.)
- [x] RT thread is raw `pthread_create`, not `LockOSThread`
  — `task_start()` → `pthread_create` (`uspace_rtapi_lib.c:620`). The only `LockOSThread` in the tree pins the *main* goroutine for Boost.Python thread-state (`cmd/gomc-server/main.go:50`) — non-RT, unrelated.
- [x] RT cycle is 100% cgo-free and Go-pointer-free (no callback into Go)
  — True by construction: `thread_task()` walks the funct list and calls C function pointers only (`hal_lib.c:2982-2984`). Go-side modules that need an RT funct (halscope, classicladder) export a *C* function via a cgo wrapper (`halscope/module.go` `go_hal_export_funct`), never a Go trampoline. All shared structs are C-allocated. Caveat: nothing *enforces* this for future cmods — that is exactly the §2 tooling gap.

### Memory
- [x] Shared ring buffers are C-allocated, never on the Go heap
  — `rtapi_shmem_new()` allocates via `rtapi_calloc` (C heap, prefaulted + mlocked) (`src/rtapi/uspace_common.h:52-89`); halscope instance + triple buffers likewise via `rtapi_calloc` (`halscope/halscope_rt.c`, fixed on `rt-validate`).
- [x] RT regions (stacks + buffers) locked and **write-touched once** at init (prefaulted)
  — `rtapi_lock_mem()` write-prefaults every page then `mlock`s (`uspace_rtapi_lib.c:196-225`); used by `rtapi_malloc/calloc/realloc`, thread stacks, dlopen'd cmod PT_LOAD segments (RELRO-aware, `dl_mlock_callback`), and — since `rt-validate` — the halscope instance + capture buffers. `mlockall(MCL_CURRENT)` one-shot + `mallopt(M_TRIM_THRESHOLD=-1, M_MMAP_MAX=0)` in `configure_memory()`.
- [x] RT `pthread` stacks pre-grown to worst-case depth
  — Minimum 1 MB enforced (`task_new`, `uspace_rtapi_lib.c:720`), and the whole stack (minus guard page) is write-prefaulted + mlocked at thread start (`task_wrapper`, `uspace_rtapi_lib.c:637-656`).
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
  — Not wired up: no `GODEBUG=cgocheck2`/`GOEXPERIMENT=cgocheck2` in `gomc-test*` targets (`src/gomc/Submakefile:286-293`) or CI. (The nightly race-detector run in `.github/workflows/nightly-gomc.yml` is valuable but checks a different property.)

### Forbidden-call enforcement
- [ ] RT entry points annotated `[[clang::nonblocking]]` — no occurrence in the tree.
- [ ] HAL cyclic dispatch function-pointer *type* is `nonblocking` — `hal_funct_t`/funct pointers are plain types (`hal_lib.c:2984` dispatch).
- [ ] Clang CI job: `-Wfunction-effects -Werror` on all RT translation units — not present; note a Clang build+test job already exists (`.github/workflows/ci.yml` `rip-and-test-clang`, post-merge on `gomc`) that could carry the flag. Since `rt-validate` the job's `CC=clang` also reaches the cgo-compiled RT units (hallib, halscope, shims) — before, cgo silently fell back to gcc there. The job is gated to post-merge pushes on `gomc`, so PR branches never trigger it.
- [ ] Clang CI job: `-fsanitize=realtime` against the sim — not present.
- [ ] Every `__rtsan_disable` / `NONBLOCKING_UNSAFE` exemption is reviewed & justified — N/A until RTSan is introduced (no occurrences).

### Failure semantics
- [ ] Deadline-miss detector in the RT cycle → E-stop on overrun
  — Not implemented. `unexpected_realtime_delay()` logs **once per session** and takes no action (`uspace_rtapi_lib.c:563-575`). HAL tracks per-funct/per-thread `runtime`/`maxtime` but nothing acts on them. The Go-side comm watchdog (motion-status reads failing for 1 s → machine off, `src/gomc/internal/task/monitor.go:15-22`) catches a fully hung RT thread from the non-RT side, but it is not in-cycle, not overrun-based, and lives in the same process.
- [~] Watchdog is genuinely external (HW or separate process/line)
  — The mechanism exists at the fieldbus level: EtherCAT slave SM watchdogs are configurable per slave (`ecrt_slave_config_watchdog`, `src/hal/drivers/ethercat/main.c:221-223`; `lcecConfTypeWatchdog` config), so drives fault autonomously if cyclic traffic stops — genuinely external to the host. But it is opt-in per configuration; nothing asserts a watchdog is configured, and non-EtherCAT setups have no external watchdog at all.
- [~] SIL-rated stop rides FSoE, independent of the Go/host E-stop path
  — The in-tree EtherCAT driver supports FSoE logic/safety slaves (`is_fsoe_logic`, `priv.h:70`; EL1904/EL2904/logic-device staged preinit, `main.c:500-511`), so the architecture supports it. Whether the SIL stop actually rides FSoE is a per-machine deployment property — not verifiable in-repo.

### Verification
- [ ] 24–72 h jitter histogram under realistic + adversarial load, published
  — No cyclictest-equivalent / latency-histogram instrumentation in gomc, no published results in the repo.
- [ ] Torture CI (RTSan + forced GC + alloc pressure + net saturation) asserts no overrun
  — Not present. Closest existing guard: nightly full Go test suite under the race detector plus runtests against a race-built `gomc-server` (`.github/workflows/nightly-gomc.yml`) — a concurrency regression guard, not an RT-latency one.

