# Network modules — Review Findings (Tier 2)

**Modules:** `internal/apiserver` (REST + WebSocket surface, ~1950 non-test LOC),
`internal/halrest` (~659), `internal/inirest` (~87), `internal/mqttbridge` (~861),
`internal/halscope` (~1035). Phases 4–6 per `PRODUCTION_READINESS.md`. Reviewed together
because they share one lens: **untrusted-wire allocation/panic → controller death** (the risk
class the ADS review surfaced — see `gomc-review-learnings` #5) plus goroutine/lifecycle safety.

**Exposure:** the REST/WS server binds `127.0.0.1:5080` by default but a deployment can set
`GMC_REST_ADDR` / `[GMC]REST_ADDR` to `0.0.0.0` for a remote HMI. Crucially, the **cross-site
WebSocket hijack (N1) works even on the loopback default** — a browser tab is enough. A crash of
`gomc-server` is an uncontrolled machine stop.

**Method (Tier-2 adversarial):** primary read-through + two independent refutation passes (one on
`apiserver`, one on `halrest`/`inirest`/`mqttbridge`); `halscope` read directly. Cross-checked
against the existing `STATE_MACHINE_REVIEW_FINDINGS.md` items (API1, MQ1, HS1) and launcher L-3.

**Verdict tags:** `CONFIRMED` / `PLAUSIBLE` / `REFUTED` as in the ADS doc.

---

## HIGH

### N1 — Cross-site WebSocket hijacking → unauthenticated remote command injection
`ws_handler.go:210`, `stream_handler.go:17` (both `InsecureSkipVerify: true`). **CONFIRMED.** `[FIX APPLIED]`

Both WS upgraders disabled the Origin check, and a WS `call` action dispatches the **same**
controller commands as REST (`handleCall` → `cmdMeta.Handler` → `fn.Dispatch`; jog/MDI/state —
no safe-command allow-list). Browsers permit cross-origin WS *connections* (same-origin policy
only sets the ignored `Origin` header), so **any page open in the operator's browser** could
`new WebSocket("ws://127.0.0.1:5080/api/v1/watch")` and drive the machine — loopback binding does
not help, and DNS-rebinding defeats it too. This is the previously-flagged API1 ("tighten in
production"), never enforced. **Fix:** removed `InsecureSkipVerify`; empty `OriginPatterns` now
means same-origin only (secure default — the bundled UI is same-origin, so it is unaffected). A
cross-origin HMI opts in via `GMC_REST_ORIGINS` / `[GMC]REST_ORIGINS` (comma-separated;
`Server.SetWSOriginPatterns`; `*` allows any origin). Regression test `TestWatchOriginCheck`.

### N2 — Panic in a spawned push goroutine crashes the whole controller
`ws_handler.go` `pushLoop`/`pushLoopBinary` (spawned at `handleSubscribe`). **CONFIRMED.** `[FIX APPLIED]`

`net/http`'s per-request `recover` covers the readLoop (`handleCall`) and the **synchronous**
stream `ServeConn` — but **not** the `go c.pushLoop(...)` / `go c.pushLoopBinary(...)` goroutines.
Those call `watch()` → generated/cgo `WatchFunc`/`BinaryWatchFunc` (e.g. halscope's binary sample
converter); a nil-deref/index panic there aborts the process. A single `subscribe` reaches it
(and, with N1, a browser page could). **Fix:** `recover()` added to both push loops (drop the
subscription, keep the controller up). *(The stream handler's `ServeConn` is synchronous — the
stale "in a goroutine" doc comment notwithstanding — so it is already stdlib-recovered.)*

### N3 — Unbounded request body → OOM
`server.go:180` (`io.ReadAll(r.Body)`, no cap). **CONFIRMED.** `[FIX APPLIED]`

No `http.MaxBytesReader`, and the `http.Server` set no limits, so a large POST is fully buffered
(then `json.Unmarshal`'d, with slice amplification) → OOM. Loopback-limited by default, but a
trivial fix. **Fix:** `http.MaxBytesReader(w, r.Body, 8 MiB)`. *(Inbound WS frames are already
capped at the coder/websocket 32 KiB default — `SetReadLimit` is not called — so this vector is
REST-only.)*

---

## MEDIUM

### N4 — No `http.Server` timeouts → Slowloris
`server.go:55` (only `Addr`/`Handler` set). **CONFIRMED.** `[FIX APPLIED]`

Slow-header / idle-keepalive connections pin goroutines indefinitely. **Fix:** `ReadHeaderTimeout`
(10 s) + `IdleTimeout` (120 s). `ReadTimeout`/`WriteTimeout` are **deliberately not set** — they
would also kill the long-lived WebSocket watch/stream connections (which are hijacked and no longer
governed by these two).

### N5 — pprof exposed unauthenticated on the API mux
`server.go:49-53`. **CONFIRMED.** `[FIX APPLIED]`

`/debug/pprof/*` was always mounted, no auth: `profile` is a repeatable 30 s CPU-profile DoS, and
`heap`/`cmdline` leak memory layout, argv, and config — directly reachable if bound `0.0.0.0`.
**Fix:** pprof now mounts only when `GMC_REST_PPROF=1` (opt-in), keeping it available for field
debugging without exposing it by default.

### N6 — halrest load/unload REST surface reaches the launcher L-3 data race
`halrest_impl.go:342,352` → launcher `runtimeLoadModule`/`UnloadModule`. **CONFIRMED (reachability).**
`[OPEN — belongs to launcher L-3]`

`halcmdImpl.Load`/`Unload` (HTTP-handler goroutines) call into the launcher, which appends/splices
`l.cModules`/`l.goModules` with **no lock** while shutdown iterates/frees them (and two concurrent
REST calls race each other, since every `net/http` request is its own goroutine) → torn slice /
double-`dlclose` / UAF. halrest itself adds no unsafe allocation — the danger is purely the
unlocked launcher state. **This is exactly the open Tier-1 launcher finding L-3**; the fix (a
locking design that avoids the `gomc_ini_get` `//export` re-entrancy deadlock) belongs there, not
in halrest. Recorded here as confirmation that the REST surface makes L-3 remotely reachable.
**RULING 2026-07-21 (user): runtime REST load/unload IS a supported production path**, so L-3
gets the FULL locking fix (`arenaMu` around the arena append/free + `modMu` serialising the REST
handlers, snapshot-under-lock in the shutdown iterators) — not the shrink. Now the
highest-priority open item. See `gomc-rest-auth-and-loadunload-rulings` (auto-memory).

---

## LOW

- **N7 — mqttbridge: `Publish` token dropped + `publish-count` over-reports** (`bridge.go:426,429`).
  Fire-and-forget publish is a defensible hot-loop tradeoff (waiting on a QoS≥1 token would stall
  `publishLoop`), but the liveness pin advances even when the publish errored / the client is
  disconnected-and-buffering, so a supervisor can be misled. **CONFIRMED — low.** `[OPEN]`
- **N8 — mqttbridge: `publishLoop`/`handleMessage` had no `recover()`** (paho/spawned goroutines,
  outside net/http). Inputs are currently validated (no reachable untrusted-input panic), but it
  was a latent single-point-of-crash. **CONFIRMED — latent.** `[FIX APPLIED]` (`recover()` added
  to both.)
- **N9 — apiserver: no global connection cap.** Standard `net/http` behavior; per-connection
  subscriptions are already bounded (only *registered* watch funcs are subscribable), so the only
  unbounded axis is connection count. Capping risks breaking legitimate multi-client use; left as
  a documented follow-up (N1's same-origin gate is the real control). **PLAUSIBLE — low.** `[OPEN]`

---

## Refuted / cleared (investigated, no action)

- **inirest `make([]…, len(items))` (`inirest.go:25`) — REFUTED.** `items` is the decoded JSON
  array, not a separate client count field; the allocation is proportional to bytes actually sent
  (bounded by N3's body cap). Malformed input returns an error, nil-INI is guarded. Safe pattern.
- **halscope lifecycle — CLEAN.** HS1 (detached-saver storm/UAF) is properly fixed: a single
  coalescing `saverLoop` (buffered `saveReqCh`), joined before `halscope_free`; `saveStateBg` is a
  non-blocking channel send. Its watch funcs inherit N2 (fixed in apiserver).
- **mqttbridge MQ1 — CONFIRMED FIXED** (Subscribe token checked `bridge.go:356`; `pubCount` is an
  `atomic.Uint32` with lossless `Add`). Shutdown/reconnect/nil-INI all sound.
- **apiserver registry maps / webapp path traversal — REFUTED.** Registry maps are init-time-only
  writes then read-only; `push_watch` double-checked locking is sound; `http.ServeMux` +
  `http.Dir` reject `..` traversal in `webapp.go`.
- **halrest/inirest handler panics — REFUTED as a crash vector.** They run under `net/http`, which
  recovers per-request (500 + connection close; process survives).

---

## Applied (this pass)

All fixes mechanical and verified: `go build ./...` clean, `gofmt`/`go vet` clean, pinned
golangci-lint **0 issues**, `go test -race ./internal/apiserver/ ./internal/launcher/...` green.

- **N1** — secure same-origin WS default via `OriginPatterns` (replacing `InsecureSkipVerify`) +
  `Server.SetWSOriginPatterns` + launcher `GMC_REST_ORIGINS`/`[GMC]REST_ORIGINS` opt-in allow-list.
  Regression test `TestWatchOriginCheck`.
- **N2** — `recover()` in `pushLoop`/`pushLoopBinary`.
- **N3** — `http.MaxBytesReader` (8 MiB) on the REST body.
- **N4** — `ReadHeaderTimeout` + `IdleTimeout` (not `ReadTimeout`/`WriteTimeout`, which would break WS).
- **N5** — pprof gated behind `GMC_REST_PPROF=1`.
- **N8** — `recover()` in mqttbridge `publishLoop`/`handleMessage`.
- **Apiserver stream lifecycle** — `streamWg.Add(1)` moved inside `streamMu` (closes a
  shutdown-vs-new-stream window where `Wait()` could return with a cgo call in flight).

**Still open:** N6 (= launcher L-3, now FULL fix per the 2026-07-21 ruling), N7 (mqtt
publish-count semantics), N9 (connection cap — policy).

**Auth ruling (2026-07-21, user).** The REST/WS surface has **no authentication** and that is a
deferred-but-required architecture item, not "won't fix": auth needs **fine-grained permission
control**, is an OPEN design question, and until it lands the surface **binds loopback only**.
Key decisions: (a) **robustness is intrinsic** — the crash/DoS hardening above stands regardless
of binding, because the endpoints will be exposed eventually; (b) the auth *mechanism* (authN,
TLS, coarse allow/deny) is an **external** reverse-proxy / API-gateway (a product, not built into
gomc); (c) **caveat:** fine-grained **authZ** cannot live entirely in a gateway blind to gomc's
command semantics — the expected split is gateway→verified-identity, gomc enforces per-command
permissions at `handleAPIRequest`/`handleCall` (one thin app-side seam, future work); (d) **N1
stays required** even with a gateway — cross-site WS hijacking is a browser-origin attack the
gateway can't see. Belongs in the Safety-boundary / security-model doc.
