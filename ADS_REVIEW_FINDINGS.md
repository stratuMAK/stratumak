# ADS cluster — Review Findings (Tier 2)

**Modules:** `internal/ads` (notification/protocol/server/symbols, ~1178 non-test LOC),
`internal/adsbridge` (~500), `internal/adsconfig` (config/layout/serverconf/xmlgen, ~1473),
`internal/adsmodule` (~163). Phase 2 (field I/O) per `PRODUCTION_READINESS.md`.

The ADS server implements the Beckhoff **ADS/AMS over TCP** protocol so a TwinCAT HMI can
read/write HAL pins. It is **net-new gomc code** (no LinuxCNC 2.9 parity oracle), so this
review is correctness / concurrency / robustness / protocol-safety, not parity. **There is no
end-to-end runtests case** — coverage is unit-test-only (symbols 46, config 43, layout 14,
xmlgen 17), and none of the existing tests exercise malformed / adversarial packets.

**Threat model (the reason severities are high):** the listener binds **`0.0.0.0:48898` by
default** (`serverconf.go:52`) and the ADS protocol carries **no authentication**. Every
command handler is therefore reachable by any host that can route to the controller, with no
credential. A crash of `gomc-server` is a crash of the **motion controller** (uncontrolled
machine stop). See A9.

**Method (Tier-2 adversarial):** one primary read-through plus two *independent* AI passes
with distinct lenses — (D) remote DoS / crash / OOM, (C) concurrency / lifecycle / UAF — each
instructed to *refute* every hypothesis. Verdicts below are the reconciled result. Verified
against the in-tree code and the existing `STATE_MACHINE_REVIEW_FINDINGS.md` ADS items
(ADS1 fixed; ADS2/ADS3 open) so this doc extends rather than duplicates them.

**Verdict tags:** `CONFIRMED` = holds against the code and survives refutation ·
`PLAUSIBLE` = real but severity/scope hinges on a design decision the human owns ·
`REFUTED` = hypothesis investigated and does not hold.

**STATUS:** see the per-finding `[state]` tags and the *Applied* section at the bottom.

---

## HIGH — remote, unauthenticated controller crash (single packet, deterministic)

### A1 — SumWrite `uint32` overflow defeats the bounds check → slice panic → process death
`symbols.go:542` (guard) + `:546` (slice). **CONFIRMED.** `[FIX APPLIED]`

In `IdxGrpSumWrite`, both the guard `if dataOffset+ln > uint32(len(writeData))` and the slice
`writeData[dataOffset : dataOffset+ln]` compute `dataOffset+ln` in **wrapping uint32**. With
`numWrites=1` (→ `dataOffset=12`) and a sub-request `Length = 0xFFFFFFFF`, `12 + 0xFFFFFFFF`
wraps to `11`; the guard sees `11 > len(writeData)` → false (not caught), then the slice
`writeData[12:11]` has low > high → **runtime panic**. With no `recover()` (A4) the panic
takes down the whole process.

*Trigger:* one `CmdReadWrite`, `indexGroup=0xF081`, `indexOffset=1`, a single 12-byte
sub-header with `Length=0xFFFFFFFF` (~28-byte packet). Deterministic, 100% reliable, the
cheapest crash of the set.

### A2 — Unbounded allocation from the client-controlled sub-request count → OOM crash
`symbols.go:491` `make([]readResult, numReads)`, `:531` `make([]uint32, numWrites)`.
**CONFIRMED.** `[FIX APPLIED]`

`numReads`/`numWrites` are `:= indexOffset`, a raw client `uint32` from the AMS header, used
directly to size a slice **before** any validation against `writeData`. `maxAMSPacketSize`
(64 KiB) bounds the *payload byte length* — not this header field. A ~16–28-byte packet with
`indexOffset=0xFFFFFFFF` forces `make([]readResult, 4.29e9)` (≈137 GB) or
`make([]uint32, 4.29e9)` (≈17 GB) → `runtime: out of memory` fatal throw (unrecoverable), or
OOM-killer / swap-thrash on an overcommit box. Even a modest `indexOffset=0x08000000`
(≈4.3 GB) kills a typical 2–8 GB controller.

### A3 — Unbounded `make([]byte, length)` on process-image read → OOM crash
`symbols.go:569` (`readProcessImageRange`). **CONFIRMED.** `[FIX APPLIED]`

`length` is a client `uint32`, unbounded, reachable three ways:
- direct `CmdRead` with `indexGroup=IdxGrpProcessImageRW (0x4040)` (`server.go:283`);
- a SumRead sub-request `ln` field (`symbols.go:501`, `st.ReadData(ig, io, ln)` with
  `ig=0x4040`) — and multiple sub-requests accumulate;
- **`AddNotification` with a huge `length`** (`notification.go:140`) makes the *background*
  `sendLoop` re-allocate ≈4 GB **every 10 ms** (cycle floored at `server.go:384`) — a
  repeating OOM hammer from a single subscribe.

`length ≈ 0xFFFFFFFF` → ≈4 GB allocation → controller death. (The by-handle read path
`readSymbol` only truncates real data, so it is safe.)

### A4 — No panic recovery in any server goroutine (amplifies A1 to lethal)
`server.go:110` (`acceptLoop`), `:129` (`handleConn`), `notification.go:108` (`sendLoop`).
**CONFIRMED.** `[FIX APPLIED]`

Grep-confirmed: zero `recover()` in the package. A Go panic in any goroutine kills the whole
process, so the A1 slice panic — or any future indexing slip on the untrusted wire — escalates
from "drop one connection" to "kill the motion controller." A `recover()` per connection/
subscription goroutine is the defense-in-depth backstop that makes a malformed packet a dropped
connection instead of a machine stop. (Note: A2/A3 are *fatal throws*, not panics — `recover`
does not catch them; the bounds fixes A2/A3 are the real remedy there.)

---

## MEDIUM

### A5 — Shutdown use-after-free: `Destroy()` frees HAL pins while conn goroutines may still read them
`module.go:58-68` (`Stop`→`server.Stop`, then `Destroy`→`comp.Exit`) · `server.go:102-106`
(2 s cap, returns even with live goroutines) · `server.go:198-208` (5 s stage-2 read ignores
`s.quit`) · `server.go:92-96` vs `:133-135` (accept/register window). **CONFIRMED** (narrow
trigger). *Deepens the existing PLAUSIBLE `ADS2`.* `[PARTIAL FIX APPLIED; contract decision OPEN]`

`Server.Stop()` is best-effort: on the 2 s cap it logs and returns. `Destroy()` then
unconditionally `comp.Exit()`s → `hal_exit()` frees the component's HAL shared memory, which
every pin accessor dereferences via cgo with **no liveness guard** (`pkg/hal/pin.go` — this is
the same lifecycle exposure as pkg/hal H1). Two concrete ways a goroutine outlives the cap:
1. **Accept/register race.** `Stop()`'s close-loop and `handleConn`'s self-registration both
   take `connsMu`. If the close-loop wins before a freshly-accepted `handleConn` reaches
   `:133`, that conn is **never force-closed** and relies only on read timeouts to exit.
2. **Stage-2 read outlives the cap.** Once a TCP header has arrived, `handleConn` blocks in a
   5 s `amsDataReadTimeout` read that does **not** re-check `s.quit`. A read completing between
   t=2 s and t=5 s dispatches `ReadData`→accessor→`pin.Get/Set` on **freed** shmem → SIGSEGV.

Independent of both, a 2 s cap is an unsound free-barrier on an RT box: any STW GC / scheduler
starvation > 2 s during shutdown realises the same UAF.

*Applied now (make goroutines promptly cancellable — narrows the window):* stage-2 read honors
`s.quit` on timeout; accept registers the conn under `connsMu` **before** spawning the handler
so `Stop()` can never miss it; write deadlines (A6) so a stuck write can't outlive the cap.
*OPEN for the human:* the shutdown **contract** — either `Stop()` must truly join
(`wg.Wait()` with no silent cap) before `Destroy()` frees pins, or the accessors need a
component-liveness gate. Decide alongside pkg/hal H1.

### A6 — No write deadline on `conn.Write` → per-connection goroutine stall
`protocol.go:233`, `notification.go:242`. **CONFIRMED.** `[FIX APPLIED]`

A client that stops reading fills the TCP send window and blocks the writing goroutine
indefinitely. It does **not** wedge the whole server (no lock is held across `Write` —
`sendNotifications` runs after `nm.mu.Unlock()`), so impact is a stalled per-connection
goroutine, but combined with A5 a stuck write can outlive the 2 s shutdown cap. A bounded write
deadline makes every write promptly cancellable.

### A7 — No connection or subscription cap → resource exhaustion
`server.go:124` (`acceptLoop`, unlimited conns), `notification.go:78` (`add`, unlimited subs).
**PLAUSIBLE.** `[OPEN — design decision]`

A remote peer can open unlimited connections (2 goroutines + buffers each) and register
unlimited subscriptions (map growth + per-sub 10 ms HAL polling). Grep-confirmed: no
semaphore/limit anywhere. The natural fix is a small connection cap and a per-connection
subscription cap — but the right numbers depend on how many HMIs a deployment expects, so this
is a design call, not a mechanical fix.

### A8 — Arrays declared `[0..N]` are silently mis-laid-out (lower bound 0 reads as "not an array")
`layout.go:150,173`, `bridge.go:282`, `xmlgen.go:217,358,399,420`. **CONFIRMED.** `[FIX APPLIED]`

Array-ness was detected via `node.ArrayStart > 0`, but `parseContainerNode` (`config.go`)
accepts any `start <= end` including `start=0`. A config using `aFoo[0..3]` (a perfectly legal
TwinCAT bound) parses with `ArrayStart=0`, so every `> 0` guard treated it as a **scalar/struct**
and laid out **one** element instead of four — silent process-image corruption with no error.
Fixed by adding an explicit `Node.IsArray bool` (set in `parseContainerNode`) and switching every
guard to it. Regression test `TestComputeLayoutZeroLowerBoundArray` (adsconfig). The bracket-
string parsing paths (`symbols.parentPrefixes`, `bridge` `arrayGroups`) already handled
lower-bound-0 correctly, so only the `ArrayStart>0` guards were affected.

### A9 — Default bind `0.0.0.0` + no authentication = any host on the network can drive HAL outputs
`serverconf.go:52`. **PLAUSIBLE / by-protocol-design.** `[OPEN — safety-boundary doc]`

ADS has no auth by design, and the default binds all interfaces. Any host that reaches
:48898 can write `out`/`inout` pins, i.e. command machine outputs, and (with A1–A4) crash the
controller. This belongs in the **Safety boundary document** cross-cutting item: state the
network trust assumption explicitly, and consider defaulting `$bind` to a loopback/named
interface so exposure is opt-in. Not a code bug per se — a deployment/contract decision.

---

## LOW / mechanical

- **A10 — `acceptLoop` busy-spin on persistent accept errors** (`server.go:118-121`): a
  non-timeout, non-quit `Accept` error (e.g. EMFILE / fd exhaustion) `continue`s in a tight
  loop with no backoff → pegged core + log flood. *Same as the known `ADS3`.* **CONFIRMED.**
  `[FIX APPLIED]` (small capped backoff.)
- **A11 — `Stop()` not idempotent** (`server.go:88`): `close(s.quit)` is unguarded; a second
  `Stop()` panics `close of closed channel`. Safe only while the launcher guarantees a single
  Stop — but the launcher has both a shutdown path (`stopGoModules`) and a runtime-unload path
  (`unloadGoModule`→`mod.Stop`), and its module-map is itself racy (launcher L-3). **CONFIRMED.**
  `[FIX APPLIED]` (`sync.Once`.)
- **A12 — Construction-error HAL component leak** (`module.go:118-142`): if `newADSModule`
  fails after `hal.NewComponent` (e.g. `NewBridge`/`ParseAMSNetID` error) it returns without
  `comp.Exit()`, leaking a HAL component slot per failed load. **CONFIRMED.** `[FIX APPLIED]`
- **A13 — Process-image RMW under `RLock` can lose an update** (`symbols.go:608-647`):
  `writeProcessImageRange` read-modify-writes pins while holding only `st.mu.RLock()`; two
  concurrent overlapping process-image writes can interleave and lose one. Pin-level `p.mu`
  keeps it memory-safe, so this is a consistency wart, not a crash. **CONFIRMED.**
  `[OPEN — low priority]`
- **A14 — `handles` map grows unbounded** (`symbols.go:343-355`): `CreateHandle` allocates
  monotonically and only `ReleaseHandle` reclaims; a client that never releases (buggy or
  hostile) grows the map without limit. Minor, slow DoS. **CONFIRMED.** `[OPEN — low priority]`

---

## Refuted / cleared (investigated, no action)

- **notifyManager data races — REFUTED.** `cNetID`/`cPort` are written and read under `nm.mu`
  (ADS1 already fixed); `stopped`/`subs`/`nextHdl` are lock-guarded; there is a single
  `sendLoop` per manager so `sub.lastData` has one writer; the `data` slice handed to
  `sendNotifications` is a fresh `ReadData` allocation, not aliased to `lastData`. No race.
- **SymbolTable lock correctness — REFUTED.** Every map touch (`byName`/`byOffset`/`handles`/
  `symbolOrder`/`nextHandle`/`nextOffset`) is under `st.mu`; `findSymbolWithFallback` is only
  ever called with the lock held. The suspected re-entrant-RLock deadlock in `IdxGrpSumRead`
  **does not exist**: `ReadWriteData` does not hold `st.mu` in that case, so the inner
  `ReadData` calls take their own locks (N separate acquisitions, not nested). No lock-order
  inversion (`sendLoop` takes `nm.mu`→`st.mu`; nothing takes them the other way).
- **`readProcessImageRange`/`writeProcessImageRange` copy math — REFUTED as a panic vector.**
  The overlap condition guarantees `srcStart < symSize` and `copyLen` is clamped; accessors
  return exactly `Size()` bytes, so the copies are in-bounds. (The only over-alloc there is
  A3's `make`, not an index panic.)

---

## Applied (this pass)

All fixes below are mechanical, self-contained, and verified: `go build`/`go vet` clean,
`gofmt` clean, pinned golangci-lint **0 issues**, `go test -race ./internal/ads/...` green.
Regression tests for the crash/OOM vectors live in `internal/ads/dos_test.go`.

- **A1** — `symbols.go` SumWrite sub-request bounds check now uses `uint64` arithmetic so a
  huge `Length` cannot wrap and produce a panicking slice.
- **A2** — SumRead/SumWrite reject an `indexOffset` (sub-request count) larger than
  `len(writeData)/12` before allocating, killing the multi-GB `make`.
- **A3** — `readProcessImageRange` rejects a `length` larger than the whole process image
  before `make([]byte, length)` (also caps the notification `sendLoop` path).
- **A4** — `recover()` added to `handleConn` and the notification `sendLoop` so a wire-path
  panic drops the connection instead of the process.
- **A5 (partial)** — accept registers the conn under `connsMu` *before* spawning the handler
  (closes the accept/register race); the stage-2 read exits on `s.quit` instead of dispatching
  on stale data during shutdown. The full free-barrier **contract** (true join vs. accessor
  liveness gate) remains **open** — decide with pkg/hal H1.
- **A6** — write deadlines on the response (`protocol.go`) and notification (`notification.go`)
  writes.
- **A10** — capped backoff in `acceptLoop` on persistent (non-timeout) Accept errors.
- **A11** — `Server.Stop()` is now idempotent (`sync.Once`).
- **A12** — `newADSModule` releases the HAL component on any post-`NewComponent` failure.

- **A8** — `Node.IsArray` flag replaces the broken `ArrayStart > 0` array detector across
  `config.go`/`layout.go`/`bridge.go`/`xmlgen.go`; regression test added (separate commit).

**Still open** (documented above): A5 contract, A7 (connection/subscription caps — needs the
expected-HMI-count decision), A9 (0.0.0.0/no-auth — safety-boundary doc), A13/A14 (low-priority
consistency/leak).
