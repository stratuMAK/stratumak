# Webapp review findings — Phase 6, 2026-07-23

Adversarial review of the 5 non-deferred webapps (`src/webapp/`, excl.
classicladder): **tooledit, emccalib, halshow, halscope, latency**. Tier 2 on
write/command paths, Tier 3 display. Five independent AI passes (one per app),
each diffed against the generated TS clients, the `.gmi` IDLs and the Go
providers as ground truth; every HIGH re-verified by hand before fixing.
Companion docs: `NETWORK_MODULES_REVIEW_FINDINGS.md` (server half),
`PHASE5_REVIEW_FINDINGS.md` (emccalib/tooltable providers).

Statuses: **FIXED** (code landed this pass), **DEFERRED** (recorded, needs
design/wire change or a ruling), **RULED** (kept as-is with rationale),
**NOTED** (informational).

## Cross-cutting results

- **200-with-failure taxonomy settled per app.** tooledit + emccalib providers
  can NOT return 200 with a failure body (whole chain verified: provider error
  → `writeDispatchError` → non-2xx → client throw), so ignored result bodies
  there are latent, not active. halshow (`CmdResult{success:false}` on 200)
  and halscope (negative errno rc on 200) DO — and halshow checked all of
  them already; halscope checked almost none (S-1, FIXED).
- **Three provider-side (Go/C) bugs found via the client review**, all FIXED
  with mutation-verified tests: emccalib C-3 fan-out, halscope S-3 trigger
  union, S-5 channel-edit gate, S-11 seqlock (see below).
- **Write-path tests** added per the cross-cutting ruling: vitest + happy-dom
  request-level tests (stubbed `fetch`/WebSocket, asserting emitted JSON) in
  tooledit, emccalib, halshow, halscope.

## tooledit (T-*)

- **T-1 CRITICAL FIXED** — `ToolEditDialog.onInput` coerced with
  `Number(input.value)`: a number input's `.value` is `''` when empty AND in
  every badInput intermediate (`-`, `1e`, decimal comma), and `Number('')===0`
  with `isNaN(0)` false — so the form silently held 0 (Save writes 0 while the
  screen shows `-`/`5,2`), and on non-zero previous values the reactive
  re-render clobbered typing mid-keystroke (`-` → `0`, continue `5.2` →
  `05.2` → **+5.2 saved instead of −5.2**). Fix: the dialog holds raw strings;
  strict shared parse (full-string regex, single decimal comma normalized,
  finite check) happens only in `validate()` at Save; per-field errors.
- **T-2 HIGH FIXED** — toolno was editable on an existing tool: PUT to the new
  number upserts/overwrites that tool and leaves the old one behind (operator
  believes they renamed; next `T5 M6 G43` uses the stale length). Classic
  tooledit renames in place and refuses duplicates. Fix: toolno readonly when
  editing; Add mode refuses a toolno that already exists in the table.
- **T-3 HIGH FIXED (minimum)** — dialog edited an arbitrarily stale row copy
  (table fetched once at mount) and PUT all 16 fields back, silently reverting
  concurrent touch-off (`G10 L1` path shares the store). Fix: dialog re-GETs
  the entry on open. Optimistic concurrency (surfacing the persist `updated`
  stamp through the tools API) DEFERRED — wire change.
- **T-4 MEDIUM FIXED** — int fields accepted floats (server 400s them but via
  T-5 the input was lost); `Number.isInteger` checks added to `validate()`.
- **T-5 MEDIUM FIXED** — dialog closed before the save round-trip; any
  failure discarded all 16 fields. Save is now awaited; dialog stays open
  with the error inline; closes only on success.
- **T-6 MEDIUM FIXED** — comment ≤255 rule mirrored into `validate()`.
- **T-7 MEDIUM FIXED** — post-save reload failure left a silently stale table;
  now flagged (stale banner until a reload succeeds).
- **T-8 MEDIUM FIXED** — table sorted numerically by toolno (backend returns
  TEXT-key order: 1, 10, 11, 2 …).
- **T-9 MEDIUM PARTIAL** — no unit labels; values are internal mm by the
  mm-everywhere convention, so offset/diameter headers now say (mm).
  Machine-units display for inch configs DEFERRED (needs units metadata).
- **T-10 LOW FIXED** — "No tools loaded. Click Add Tool" empty-state no longer
  shown when the load actually failed.
- **T-11 LOW FIXED** — table no longer unmounts on every reload (overlay
  instead of `v-if/v-else`).
- **T-12 LOW NOTED** — Add Tool defaults pocketno 0 (spindle sentinel);
  unreachable by pocket lookups on random-TC configs. Left as-is: pocket
  semantics belong to the deferred config-migration/random-TC pass.
- **T-13 LOW FIXED** — delete now reloads from the server instead of patching
  local state.
- **Verified clean:** no 200-with-failure on this API (chain above; `ReloadTools`
  rc-discard chased into task/tools.go — `executed()` only exists on the MDI
  path); Reload is non-destructive (legacy .tbl import only on empty store);
  no unhandled rejections; wire field names match; `fmtNum` display rounding
  immaterial.

## emccalib (C-*)

- **C-1 CRITICAL FIXED** — `parseFloat` let `Infinity`/`1e999` through
  (`isNaN(Infinity)` false); `JSON.stringify({value: Infinity})` →
  `{"value":null}`; Go `json.Unmarshal` treats null as no-op → **`setp
  pid.N.Pgain 0`** on a running servo loop, no error anywhere. Fix: strict
  `parseTunableValue` (full-string regex + `Number.isFinite`).
- **C-2 HIGH FIXED** — `parseFloat` truncation: `"0,5"`→0 (German decimal
  comma!), `"1.5abc"`→1.5, `"0x1A"`→0. Same fix; a single decimal comma with
  no dot is accepted and normalized to `.`.
- **C-3 HIGH FIXED (client+server)** — tandem/gantry configs feed two pins
  from one `[SECTION]KEY` (two `setp` lines); calibreg records both, but the
  provider index kept only the LAST registration, so `set_pin`/`revert` wrote
  only one pin — **mismatched tandem PID gains** while the panel reported
  success. Server: index is now `map[string][]int`; SetPin/Revert fan out over
  all matches (partial failure reported via errors.Join); HAL-backed
  fan-out tests, mutation-verified. Client: `v-for` keys are unique per
  hal_pin (duplicate keys corrupted row reuse).
- **C-4 MEDIUM FIXED** — the three ignored `Promise<boolean>` results are now
  checked (`false` on 200 is legal per the IDL even though today's provider
  never produces it — cheap insurance against the contract fragility).
- **C-5 MEDIUM FIXED (both halves)** — `isModified` exact-compared the
  `%.7g`-roundtripped live value against the full-precision INI value: every
  >7-significant-digit INI entry showed "modified" at startup, and SaveIni
  rewrote such entries with truncated values. Client: relative-epsilon compare;
  server: same epsilon in the SaveIni "changed?" test. `%.7g` itself is 2.9
  halcmd parity and stays.
- **C-6 MEDIUM FIXED** — stale "INI file saved successfully" banner survived
  subsequent Tests; now cleared on test/revert.
- **C-7 LOW FIXED** — post-await `edits.delete` only if the operator hasn't
  retyped meanwhile.
- **C-8 LOW FIXED** — generation counter drops out-of-order `getTunables`
  responses.
- **C-9 LOW FIXED** — empty edit box clears the edit (Test disabled) instead
  of remaining a live edit forever.
- **Verified clean:** 200+false impossible today (chain proven); Save-All
  semantics match classic (live HAL values; pending edits survive, correctly);
  refresh cannot clobber typed text (edit map separate; live value is
  placeholder only); Revert client half of E-1 correct. RULED (release-note):
  gomc Revert goes to the INI value, classic Cancel restored the pre-test HAL
  value — after sequential Tests there is no path back to an intermediate.

## halshow (H-*)

- **H-1 HIGH FIXED** — Set-value dialog read `selectedItem` at SUBMIT time
  while the overlay covered only the detail pane: selecting another tree node
  mid-dialog retargeted the write → **value written to the wrong HAL pin**
  with an "OK" confirmation. Fix: name/kind captured at dialog open, used
  exclusively at submit.
- **H-2 HIGH FIXED** — watch WS never reconnected (same class as the Python
  GP-2/3): server restart froze the Watch tab at its last values, bright
  green, forever, while REST kept working. Fix: reconnect loop with backoff +
  resubscribe; watch values marked stale (dimmed, '—') on close.
- **H-3 MEDIUM FIXED** — one `connected` flag conflated REST and WS health
  (WS-refused → green "Connected" with dead watch; WS-dropped → "Connecting…"
  while REST worked). Split into REST/watch states, surfaced in WatchPanel.
- **H-4 MEDIUM FIXED** — transport-level failures of set/unlink/refresh were
  unhandled rejections: no error text, dialog stayed open, operator could
  believe the value applied. All four wrapped and routed into the existing
  error slots.
- **H-5 MEDIUM FIXED** — deleted/unloaded watched items kept showing their
  last value as live (meta re-resolve cleared metaMap but not valueMap; server
  re-reports '-' only on first subscribe). Fix: valueMap cleared in the meta
  branch (safe: the same message inverts every live shadow); `kind:"unknown"`
  rows render dead.
- **H-6 MEDIUM FIXED** — buildTree hid items whose name is a dotted prefix of
  another (`x.y` + `x.y.z`: one of them invisible depending on arrival
  order) — an introspection tool hiding an existing HAL object. Collision now
  marks the interior node as leaf+kind and leaf-with-children renders a self
  row.
- **H-7 LOW FIXED** — console `getp` on a parameter now falls back to
  getParam (halcmd parity).
- **H-8 LOW FIXED** — add-to-watch during WS CONNECTING threw synchronously
  (`send` on CONNECTING); readyState guard + updateWatch re-run on open.
- **H-9 LOW DEFERRED** — pin/param/signal namespace collision resolves by
  fixed pin-first precedence on the set path; storing {name, kind} tuples in
  the watch list is a localStorage-format change; server watch resolve uses
  the same order, so display and set at least agree today.
- **Verified clean:** every CmdResult.success checked (the hunt found zero
  200-swallows); bit encoding TRUE/FALSE matches server; canSet whitelist
  matches server dir/link semantics; no XSS; single-subscription watch model
  leak-free; 64-bit fields displayed via bigint→string correctly; server-side
  re-subscribe epoch race already fixed in ws_handler.

## halscope (S-*)

- **S-1 HIGH FIXED** — command rcs ignored on every path except addChannel
  (provider reports refusals as 200 + negative errno: -EBUSY from
  Configure/Arm, thread-attach failures), and `run()`/`arm()` ended with
  `state.error=''`, actively erasing inner failures → UI showed config the RT
  never accepted, scope sat Idle with no error. Fix: every rc checked
  (shared `checkRc` with errno→text), failures surfaced, config resynced from
  `getStatus()` on refusal; outer wrappers no longer blank errors.
- **S-2 HIGH FIXED** — time base for the displayed capture was computed from
  UI-editable config (selected thread period × Mult input, silent 1 ms
  fallback) while window/sec-div/CSV used current server status — neither
  being the parameters of the capture on screen (40× time-axis error
  scenarios during thread switch / mult edit in the DONE window). Fix: period,
  mult, recLen, preTrig are snapshotted at frame-decode time and drive the
  time base, display window, indicator and CSV export.
- **S-3 HIGH FIXED (provider)** — trigger level for S32/U32 trigger channels
  was stored typed in the union but ALWAYS read back as `d_real` → status +
  persisted state returned the int bytes reinterpreted as half a double;
  clients resynced the garbage into the Lvl box and wrote it back as the real
  trigger level (also corrupted across restarts via saveState). Fix: typed
  read helper switching on the trigger channel's HAL type; regression test
  s32/u32/float, mutation-verified.
- **S-4 HIGH FIXED** — trace labels and trace data could belong to different
  pins (rebuild gated on series COUNT and on state==DONE): pinB's waveform
  under pinA's legend label, or stale pinA data relabeled pinB. Fix: pin names
  snapshotted into the sample set at decode; series labels render from the
  snapshot; samples cleared on channel-set edits; unconditional rebuild on
  channel-list change.
- **S-5 HIGH FIXED (provider+client)** — SetChannel/ClearChannel had no
  capture-state gate (RT re-reads the slot every sample): mid-capture edits
  spliced two pins into one trace, FLOAT→S32 swaps made RT read 8 bytes
  through a 4-byte pin. Provider: -EBUSY unless IDLE/DONE/RESET (RESET never
  touches channels; verified in RT source) with regression test; client:
  add/remove disabled while capturing.
- **S-6 MEDIUM FIXED** — enabled channels without captured data were drawn as
  a fabricated flat-zero trace; now rendered as a gap (nulls), not data.
- **S-7 MEDIUM FIXED (client half)** — no liveness detection: dead connection
  (pulled cable/power) showed the frozen capture as live for minutes. Client
  staleness watchdog (periodic getStatus; UI marked stale/greyed on timeout).
  Server keepalive frames DEFERRED (shared apiserver WS change).
- **S-8 MEDIUM FIXED** — Single didn't clear continuous mode and a failed
  `run()` left `continuous=1` armed server-side → the next Single free-ran.
  `arm()` now clears continuous first; `run()` rolls back on arm failure.
- **S-9 MEDIUM FIXED** — removing the trigger-source channel left a dangling
  trigger (capture waits forever, no diagnosis): trigger now reset/pushed on
  removing its channel; trigger re-sent after channel type changes.
- **S-10 MEDIUM FIXED (minimum)** — loading a capture CSV while connected let
  the next watch push clobber half the file state (live pin names over file
  traces): loading now enters an explicit file-view mode that pauses watch
  application until the operator returns to live.
- **S-11 MEDIUM FIXED (provider)** — WatchSamples read `done_len`/
  `done_ring_start` AFTER the done_buf recheck: RT completing another capture
  in that window linearized buffer db with the new capture's length/ring
  (circularly shifted / truncated trace). Fix: seqlock order — len/ring read
  before the borrow, acquire fence, then done_buf AND done_gen verified;
  refcount protects the copy.
- **S-12 LOW FIXED** — sample-frame decode validates header vs byteLength;
  mismatched frames dropped with a surfaced error instead of NaN pixels or a
  silent throw in onmessage.
- **S-13 LOW FIXED** — channel visibility toggle now takes effect immediately
  (watched), not on the next unrelated rebuild.
- **S-14 LOW FIXED** — fullReset snapshots the channel list before its await
  loop (live reactive array could be replaced mid-loop) and checks clear rcs.
- **S-15 LOW FIXED** — dead `calcDisplayWindow` start/end sample fields
  (wrong index space, missing +preTrig) removed.
- **S-16 LOW FIXED** — CSV load caps channels at MAX_CHANNELS (>16-column file
  broke rendering via undefined channelUI).
- **S-17 LOW RULED** — external config changes (second client) are invisible
  and overwritten by this UI's next apply; accepted single-operator tradeoff
  for now, revisit with the multi-client story.
- **Verified clean:** 64-bit periods bigint-correct end-to-end (no
  bigint×number mixing); delta-free full-snapshot watch_state so revivers
  always see their fields; trigger t=0 alignment verified against the RT
  state machine; channel→column mapping and ring linearization correct;
  reconnect/resubscribe works (the gap was liveness, S-7); vertical/cursor
  math self-consistent; 1-2-5 scale port faithful.

## latency (LT-*)

- **LT-1 HIGH FIXED** — frozen data could display under the green "live"
  badge: no fetch timeout, no staleness watchdog, no ordering guard —
  exactly during a `--stress` soak (the tool's intended workload) a starved
  server left hanging GETs piling behind the browser connection cap while the
  UI showed the last numbers as live = **false-good certification evidence**.
  Fix: per-poller timeout race + single-in-flight guard + sequence numbers
  (late responses discarded) + staleness watchdog that flips the badge to
  stale when status stops arriving.
- **LT-2 MEDIUM FIXED** — one-shot startup: page loaded before gomc-server →
  permanently dead app with a misleading "no latency instances found". Now
  retries enumeration on an interval and distinguishes registry-unreachable
  from genuinely-empty.
- **LT-3 MEDIUM FIXED** — the timeline plot dropped `minNs`: negative
  excursions (early wakes) were invisible on the max line. The plot now shows
  the signed min series alongside max.
- **LT-4 MEDIUM PARTIAL** — under/overflow counters are the provider's
  windowed autoscale triggers (zeroed on every coarsen) but were presented as
  cumulative totals — "over 37" silently became "over 0" after a rescale.
  UI now labels them "since rescale". Cumulative out-of-range totals need a
  small provider addition — DEFERRED to the latency-test branch.
- **LT-5 LOW RULED** — post-reset repaint of pre-reset data for ≤1 poll cycle
  (provider clears at the next RT cycle); self-heals in 200–500 ms.
- **LT-6 LOW FIXED** — `rangeSec` included in the history staleness fence.
- **LT-7 LOW FIXED** — instance list re-enumerated periodically (runtime
  load/unload is production per the standing ruling).
- **LT-8 LOW FIXED** — histogram tab shows the same "waiting for data…"
  overlay as the plot instead of a silently blank chart.
- **Verified clean:** every ns/µs/ms constant enumerated and correct; bigint
  handling exact everywhere (no mixing, values < 2^53 where converted);
  histogram bin-edge math matches the provider contract incl. negative-bias
  correction; no client-side accumulators to mis-seed; update loops cannot be
  killed by exceptions; pure-REST design recovers from server restart by
  itself (the gap was the hang case, LT-1).

## Cross-cutting deferrals out of this pass

- **Optimistic concurrency for tooledit** (persist `updated` through the tools
  API) — wire change (T-3 full fix).
- **apiserver WS keepalive/ping** — shared transport change benefiting
  halshow/halscope/AXIS watches alike (S-7 server half).
- **Cumulative histogram out-of-range counters** — provider addition on the
  latency-test branch (LT-4).
- **Machine-units display in tooledit** for inch configs (T-9).
- **halshow watch list as {name, kind} tuples** (H-9, localStorage migration).
