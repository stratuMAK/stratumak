# pkg/hal — Review Findings (Tier 1)

**Module:** `src/gomc/pkg/hal` (~900 non-test LOC) — the Go↔C HAL binding layer every
non-RT HAL interaction crosses. **Tier 1** per `PRODUCTION_READINESS.md` (binding layer;
focus: pin/signal lifecycle, type conversions, thread interaction, error propagation).

**Method (pre-scan for human adjudication):** three *independent* AI review passes with
distinct lenses — (A) concurrency/lifecycle/RT-boundary, (B) cgo boundary & memory model,
(C) functional correctness / API-contract / 2.9-parity / checklist — cross-checked against
the in-tree C (`src/hal/hal.h`, `hal_priv.h`, `internal/hallib/hal_lib.c`) and the 2.9
reference (`~/source/linuxcnc-2.9`). Each finding carries a self-refutation attempt; the
synthesizer read `cgo.go`/`pin.go`/`component.go` directly and adjudicated. Date: 2026-07-19.

**Verdict tags:** `CONFIRMED` = verified against the code and survives refutation ·
`PLAUSIBLE` = real but severity/impact hinges on a runtime fact or a design decision the
human owns. **This is a candidate list — no code was changed.**

**STATUS (2026-07-19):** the clean mechanical fixes **M4, M9, L1, L5 are APPLIED**
(commit `4c023b0de5`; verified: both build tags, vet, lint 0, full suite green). **L4**
deliberately left as-is (unreachable sentinel — churn for no benefit). Everything else
below remains **open for adjudication**: the Tier-1 design calls **H1, H2, H3, M1, M2, M3**,
and the efficiency item **M5** (belongs with the M1 Pin rework), plus L2/L3.

**Headline:** the cgo pointer discipline is careful and largely correct (double-pointer
re-deref, no Go pointer retained by C, paired `CString`/`free`, nil-checked `hal_malloc`,
guarded empty slices — see *Cleared* below). The concentration of real issues is in the
**Pin/Component synchronization & lifecycle model** — several findings share one root and
one fix.

---

## HIGH

### H1 — Use-after-free / lifecycle race: `Pin.Get/Set` vs `Component.Exit` (disjoint mutexes)
`pin.go:126-242` (all `**ptrPtr` derefs) · `component.go:104-125` (`Exit`→`halExit`)
**CONFIRMED (race) / PLAUSIBLE (crash-vs-corruption depends on HAL arena lifetime)**

`Pin.ptr` points into HAL shared memory owned by the component; `Exit()` calls `hal_exit()`
which returns the component's pins to the HAL allocator. `Get/Set` guard with `Pin.mu`;
`Exit` guards with `Component.mu` — **disjoint locks**, so nothing serializes a concurrent
pin access against teardown.

*Failure scenario:* during shutdown, goroutine A holds a `*Pin` and calls `Set()` while
goroutine B calls `comp.Exit()`. A's `**ptrPtr` write lands in a pin slot HAL has just freed
(→ logical corruption of a recycled slot), or — if this is the last component and the HAL
data segment is released — touches unmapped memory (→ SIGSEGV). gomc's whole premise is
goroutine concurrency, so the window is materially wider than in a classic single-threaded C
comp, and nothing in the type surface warns the caller.

*Adjudication:* the race is real and confirmed. The exact consequence needs one verification:
does `gomc-server` keep the HAL data segment mapped for process lifetime (→ corruption, not
segfault) or release it on last `hal_exit` (→ segfault)? **Human to verify + decide the
contract:** either enforce "all pin access ceases before Exit()" at call sites, or add a
component-liveness flag Pin checks under a barrier.

### H2 — `Set()` on an input pin writes the linked signal; the doc claims "no effect" (false)
`pin.go:200-242`, doc at `pin.go:195-197`
**CONFIRMED**

`Set` writes `**ptrPtr` unconditionally — no direction check. For a `net`-linked IN pin,
`*ptrPtr` aims into the connected **signal's** memory (`hal_lib.c` sets `*data_ptr_addr =
&sig->value` on link), so `myInPin.Set(v)` overwrites the shared signal, not a private cell.
The doc comment says *"For input pins, calling Set() has no effect (the value is overwritten
by the connected signal)."*

*Failure scenario:* a developer trusts the doc and calls `Set(0)` defensively on an IN pin
that is `net`-linked to a signal feeding several consumers. Until the RT writer's next cycle
every consumer reads the corrupted 0; for a writer-less signal (initialized via `sets`) the
corruption is permanent. Silent cross-component data corruption.

*Adjudication:* classic HAL also doesn't enforce direction at the memory level (convention-
based) — so *enforcing* is a design choice. But the **doc is affirmatively wrong** regardless.
Minimum fix: correct the doc. **Human to decide:** make `Set` on `direction==In` a no-op /
error, or keep convention + accurate doc.

### H3 — `Pin.String()` recursively read-locks → latent deadlock
`pin.go:275-280` (`String` RLock) → `pin.go:126-128` (`Get` RLock)
**CONFIRMED**

`String()` takes `p.mu.RLock()` then calls `p.Get()`, which takes `p.mu.RLock()` again. Go's
`sync.RWMutex` forbids recursive read-locking: a pending writer blocks the *second* RLock.

*Failure scenario:* goroutine A calls `pin.String()` (outer RLock held). Goroutine B calls
`pin.Set()` and queues in `Lock()`. A proceeds into `Get()`, whose `RLock()` now blocks
(writer pending) — while A still holds the read lock it is trying to re-acquire → permanent
deadlock of both, plus any later access to that pin. `String()` is a logging/diagnostic path
hit under load, and concurrent `Get`/`Set` callers exist (e.g. `adsbridge`), so the window is
realistic. `Component.String()` is **not** affected (it reads fields, not lock-taking
methods). Fixing the locking model (M1) dissolves this.

---

## MED

### M1 — `Pin`'s `RWMutex` is the wrong primitive: false RT-safety + hot-path overhead + non-atomic word access
`pin.go:43,127,201` (mutex) · `pin.go:137-155,211-226` (plain `**ptrPtr` access)
**CONFIRMED (design) / PLAUSIBLE (real-world impact on the amd64 target)**

The mutex serializes only Go-vs-Go access. The scalar word is also read/written by the hard-RT
C thread, which never takes it — so for the one race that matters (Go `Set` vs RT read, or an
`IO` pin written by both) the mutex gives **zero** protection. It is simultaneously (a) pure
overhead on the documented hot path and (b) misleading ("mu protects the pin value" — it does
not, against the only other writer). The underlying `**ptrPtr` access is also non-atomic → a
data race under the Go memory model (race-detector flags it; torn 64-bit float on 32-bit/ARM).

*Refutation that partly holds:* classic HAL uses plain `volatile` word access too, and
`hal.h:286` asserts aligned HAL words are read/written atomically — so on the amd64 target
this is *consistent-if-not-portable*, and the mutex's Lock/Unlock incidentally acts as a
compiler barrier. **So this is not a corruption bug on the target platform** — it's "wrong
tool + overhead + non-portable + false documentation." **Human to decide:** for scalars, drop
the mutex and use `sync/atomic`/C11-atomic single-word load/store (matching `hal_stream`/
`hal_port` index discipline, per `RT_HARDENING_CHECKLIST §1.3`); keep locking only for the
multi-step PORT/string framing path. This fix also dissolves H3 and the `Set`-under-lock half
of L3.

### M2 — The documented "signal-handler goroutine" does not exist; `done` channel is dead; `doc.go` promises auto-SIGTERM that never happens
`component.go:22-27,104-114` · `doc.go:~126`
**CONFIRMED**

Grep of the whole package: no `os/signal`, no `signal.Notify`, no `go func`, and nothing ever
*receives* from `c.done`. Yet the fields/comments reference "the signal handler goroutine" and
`doc.go` states *"HAL components automatically handle SIGTERM and SIGINT… Running() returns
false when a shutdown signal is received."* The real binaries wire signals externally
(`signal.NotifyContext` in `cmd/`), confirming the package-level promise is unmet. Consequence:
`close(c.done)` signals nothing; `Running()` never self-flips on a signal; a developer trusting
`doc.go` ships a component that ignores SIGTERM. **Fix:** either own the goroutine inside the
package (`signal.Notify` in `NewComponent` → `Stop()` → teardown), or delete `done` + the
comments + the `doc.go` paragraph so the contract matches reality.

### M3 — `Exit()` is not idempotent: double-call runs `hal_exit(id)` twice
`component.go:104-125`
**CONFIRMED**

`close(c.done)` is guarded (select/default) but `halExit(c.id)` is not; there is no `exited`
flag and `c.id` is not invalidated. The codebase mixes `defer comp.Exit()` with explicit
`Exit()` in teardown (e.g. `mqttbridge/module.go` calls `Exit()` on two branches), so
double-invocation is a live pattern. Second call → `hal_exit` on a removed id (noisy
`-EINVAL/-ESRCH`), or — if HAL recycled that id for a newly loaded component — tears down the
**wrong** component. **Fix:** add an `exited bool` guard; invalidate `c.id` after success.

### M4 — Component name limit `len(name) > 47` is a wrong magic number (`HAL_NAME_LEN` is 127)
`component.go:36,42`
**CONFIRMED**

Hard-coded `47`, commented "(HAL_NAME_LEN)", but `hal.h:133` defines `HAL_NAME_LEN 127`, and
`pin.go:82` correctly uses the package const `NameLen = 127` (`hal.go:15`). So `47` is a magic
number, factually wrong in its comment, inconsistent within the package, and **over-restrictive**
— it rejects valid 48–127-char component names that `hal_init` accepts. `47` is the *old*
(pre-2.x) HAL_NAME_LEN — a stale port artifact. Refutation ("47 reserves headroom for the
`.pinname` suffix") is rejected: pin creation re-checks `len(fullName) > NameLen` at `pin.go:82`.
**Fix:** replace `47` with `NameLen`; fix/delete the comment. (Clean, low-risk.)

### M5 — `Get/Set` heap-allocate on every non-zero float/string access via `any()` boxing
`pin.go:126-242`
**CONFIRMED**

`C.hal_float_t(any(value).(float64))` / `return any(val).(T)` box the *value* into an interface;
for non-static values `runtime.convT64`/`convTstring` call `mallocgc`. (The zero-value
`switch any(zeroValue)` is alloc-free — Go serves zeros from static storage — so only the real
value conversion allocates.) A Go loop polling a float pin at kHz generates one alloc per Get and
per Set → GC pressure/jitter on what should be a bare shared-memory read/write. **Fix:** resolve
the type once at `NewPin` (store typed getter/setter closures or a type tag), not a runtime
`any()` round-trip per access. (Also removes the per-access type-switch duplication.)

### M6 — String `Set()` calls reader-only `hal_port_clear` from the writer side (breaks SPSC); framing is gomc-private
`pin.go:234-240`; C at `hal_lib.c` (`hal_port_clear` advances the *read* index), contract at `hal.h:916-919`
**CONFIRMED (contract) / PLAUSIBLE (impact only with a non-gomc peer)**

`Set` does `halPortClear` then `halPortWrite`, but `hal_port_clear` is documented "should only be
called by a reader" (it moves the read pointer). So the writer mutates the reader's index,
violating the single-producer/single-consumer invariant the port atomics assume. Within a
closed gomc-only graph (writers only `Set`, readers only `Get`/peek) the read index is written by
one serialized goroutine, so the only visible artifact is an occasional transient empty read —
**but** if a real C HAL component is `net`-linked to the same port signal, two entities write the
read index → corrupt readable/writable accounting. Also note the 4-byte length framing is a gomc
invention no C HAL peer understands. **Human to decide:** use the port as intended (writer writes,
reader commits), or document these string pins as gomc-private and stop clearing from the writer.

### M7 — `NewPin` accepts `HAL_PORT && IO`, which C rejects; doc lists `IO` as universally valid
`pin.go:67-104`; C rejects at `hal_lib.c:707`
**CONFIRMED**

`NewPin[string](comp, "msg", hal.IO)` passes Go validation, reaches `halPinPortNew`, and fails
with the generic "invalid argument" from C rather than a precise "port pins cannot be IO"; the
doc (`pin.go:52-53`, `doc.go`) never warns. No corruption (C surfaces an error), so MED. **Fix:**
special-case `T==string && dir==IO` with a clear error; note the restriction in the doc.

### M8 — `nocgo` stub skips all validation and silently diverges on direction, masking H2/H3/H1 in pure-Go tests
`nocgo.go:23-45`
**CONFIRMED**

The `!cgo` `NewPin` accepts a nil component, skips the empty-name/direction/length checks, and
`Set`/`Get` are a plain in-memory store — so a Set on an "IN" pin round-trips its own value (the
*opposite* of real HAL). A pure-Go test (`CGO_ENABLED=0`) that sets an IN pin and reads it back
passes, giving false confidence and hiding the finding-H2 corruption and invalid-name handling.
**Fix:** mirror the cgo `NewPin` validation in the stub and document that stub Set/Get have no
direction/link semantics (or gate it behind an explicit test tag).

### M9 — Five near-identical `halPin*New` functions (copy-paste; codegen-duplication risk class)
`cgo.go:81-205`
**CONFIRMED**

`halPinBitNew/FloatNew/S32New/U32New/PortNew` differ only in the C type token; the malloc,
nil-check, `hal_pin_*_new` call, and error handling are duplicated ~25 lines each. A fix to the
malloc-failure code or the double-pointer contract must be applied in five places. Classic HAL
funnels all five through a single `hal_pin_new(hal_type_t, …)`. **Fix:** a single C-side
`go_hal_pin_new(hal_type_t, void**, …)` shim (or generate the wrappers) collapses the Go-side
repetition. (Cosmetic-ish, but this is exactly the duplication the review checklist targets.)

---

## LOW

- **L1 — `halError` switch is partly redundant with its own `default` and uses errno magic
  numbers.** `cgo.go:245-296`. Arms like `-13`/`-11`/`-14` restate what `default`'s
  `syscall.Errno(-code).Error()` already yields; codes are raw literals, not `syscall.E*`.
  Numeric mappings are all correct. Keep only the HAL-context arms (`-2`,`-16`,`-36`,`-110`),
  drop the restatements. **CONFIRMED (style/maintainability).**
- **L2 — Two-stage `peek` in string `Get()` can splice a header and body from different
  frames** on a concurrent writer → wrong/empty string, never a crash (overflow + `readable<total`
  guards make it memory-safe). `pin.go:162-186`. Single-shot peek fixes it. **CONFIRMED (benign).**
- **L3 — Locks held across cgo calls** (`Exit` across `hal_exit`; string `Set` across
  `clear`+`write`). Latency hygiene, not a bug; `Running()/Name()/ID()` stall for `hal_exit`'s
  duration at shutdown. Dissolved for scalars by M1. **CONFIRMED (hygiene).**
- **L4 — `Type()`/`Get` return sentinels (`-1`, `*new(T)`) on unreachable `default` arms.**
  `pin.go:187-189,270`. Genuinely unreachable given the `PinValue` constraint; consider
  `panic("unreachable")` to fail loud if the constraint grows. **CONFIRMED (dead defensive code).**
- **L5 — `lookup.go` has no `!cgo` stub**, so `LookupValue` vanishes under `CGO_ENABLED=0`,
  inconsistent with the rest of the API that degrades to stubs. Loud compile error, test-only
  surface. **CONFIRMED (build symmetry).**

---

## Cleared (checked, not findings — recorded so the human knows the coverage)

- **Double-pointer indirection is correct:** scalar Get/Set recompute `(**ptrPtr)` each access,
  so a pin re-linked via `net` (HAL rewriting `*data_ptr_addr`) is always followed;
  `unsafe.Sizeof((*C.hal_x_t)(nil))` correctly sizes one pointer slot.
- **No Go pointer is retained by C:** `CString` results are C-heap and freed; `hal_malloc`
  pointers are C shared memory; only `unsafe.SliceData` of local buffers crosses, to synchronous
  memcpy-and-return C calls — cgocheck-compliant.
- **Empty-slice / zero-count guards** present in `halPortWrite`/`halPortPeek` (no nil `SliceData`
  to C). **`hal_malloc` nil-checked** at every call.
- **`halError` numeric errno→message mappings are all correct** for Linux (EPERM=1 … ETIMEDOUT=110).
- **The zero-value `switch any(zeroValue)` does not allocate** (only the non-zero value conversion
  in M5 does).
- **`NameLen`=127 in `pin.go`/`hal.go` matches `HAL_NAME_LEN`** (the bug is only the `47` in
  `component.go`, M4).
- **`Component.String()` does not recursively lock** (reads fields, not methods — unlike `Pin.String`, H3).
- **String-pin overflow guard** (`length > MaxUint32-4`) and `readable < total` bounds check
  prevent over-allocation from the external length field.

---

## Suggested adjudication / fix ordering

1. **One rework resolves the cluster:** redo `Pin` scalar synchronization (M1) → drop the
   `RWMutex` for scalars, use atomic single-word access. This dissolves **H3** (recursive RLock)
   and the `Set`-under-lock part of **L3**, and forces a decision on the RT memory-model
   (`RT_HARDENING §1.3`). *Tier-1 human design call.*
2. **Lifecycle contract (H1 + M2 + M3):** decide and document component/pin liveness — who owns
   the signal goroutine, whether pin access must cease before `Exit()`, and make `Exit`
   idempotent. *Tier-1 human design call* (also verify the HAL-arena-lifetime fact for H1).
3. **Direction & doc truth (H2, M7, M8):** decide enforce-vs-convention for `Set` on IN pins and
   `PORT&&IO`; fix `doc.go`/pin docs; align the nocgo stub. *Human design call + clear doc fixes.*
4. **Clean mechanical fixes (M4, M9, L1, L5):** `47`→`NameLen`, collapse the five `halPin*New`,
   `syscall.E*` in `halError`, nocgo stub symmetry. **DONE** (commit `4c023b0de5`). L4 left as-is.
5. **Efficiency (M5, L2):** type-resolve pins once at construction; single-shot string peek.

**Every finding above needs a test that would have caught it** (milltask review risk class 4) —
notably: a `-race` test exercising concurrent Get/Set/String and Get/Set-vs-Exit (H1/H3/M1), and
a linked-signal test asserting IN-pin Set behavior (H2).
