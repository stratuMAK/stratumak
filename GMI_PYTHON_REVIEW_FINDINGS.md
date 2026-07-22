# gmi Python shim — Tier-2 review findings

Module: `src/gmi/python/` (source of truth; built into `lib/python/gmi/` by
`src/gmi/codegen/Submakefile`). 2047 LOC hand-written; the generated
`ini_client.py` / `manualtoolchange_client.py` / `ngcpreview.py` are Tier 3 under the
closed gmicompile review. Consumers: AXIS, `lib/python/linuxcnc_util.py`, `gcode.py`,
`glcanon.py`, `gomc_test.py`, ~30 runtests drivers.

Method: three independent AI passes (classic-2.9 parity vs `emcmodule.cc`;
wire-contract ground truth vs IDL/generated dispatch/apiserver; concurrency &
robustness incl. consumer interleavings), findings adjudicated against the source by
the coordinating reviewer; every HIGH re-verified by hand before acceptance.
Classic reference: `~/source/linuxcnc-2.9/src/emc/usr_intf/axis/extensions/emcmodule.cc`.

Verified-clean ground (no findings): every stat wire key the shim reads exists on the
wire (field-by-field vs `emcstat_cgo.go` json tags, no `omitempty`); all 28 command
routes + body field names match the generated dispatch; all constant *values* in
`constants.py` match 2.9 headers; error-kind constants and `(kind, text)` poll
contract; positionlogger geometry/colinear/color/ring logic line-by-line equal to the
classic C (incl. the `-B` rotation sign); `wait_complete` default timeout and bare-int
success return.

## CONFIRMED — fixed in this pass (shim, plus one server-side cap)

- **GP-1 HIGH `tools.py` PUT silently zeroes the tool.** The shim sent tool fields as
  flat JSON keys; the generated dispatch (`tools_bridge.go` `put_tool`) unmarshals
  `{toolno, entry:{…}}` — unknown flat keys ignored, `Entry` stays zero, zero entry
  passes validation, HTTP 200. `gladevcp/tooledit_widget.py:save()` would wipe every
  edited tool's geometry with no error. Never caught because `tests/tooledit` GETs the
  raw `/api/v1/tooltable/` store and never calls `tools.py.put()`.
  **Fix:** body is now `{"entry": {...}}` (toolno from the path); caller's dict no
  longer mutated. Covered by the new stub-server test.
- **GP-2 HIGH ErrorChannel/MessageList die permanently on a startup race.** Unlike
  `Stat`/`PositionLogger` (20×0.25 s connect retry + try/except), `error.py`/`messages.py`
  ran a bare single `websockets.connect` in `_run`: server-not-yet-accepting → thread
  death, `_connected` never set, channel permanently dead while `poll()` returns None
  forever (no REST fallback exists for `get_errors`). `linuxcnc_util.LinuxCNC.__init__`
  constructs `ErrorChannel()` *before* waiting for startup; only the convention of
  constructing `Stat()` first masked the window. **Fix:** all four WS clients now share
  one resilient client (`_watch.py`) with uniform connect retry.
- **GP-3 HIGH no reconnect + fatal/silent recv loops across all four WS clients.** A
  server restart, 20 s ping timeout, or a single malformed frame ended the recv loop
  for the session — `error.py`'s `except Exception: pass` did so with zero trace: the
  channel whose job is reporting failures failed unreportably. Stat survived via
  `poll()`; ErrorChannel lost every subsequent operator error; MessageList froze and
  silently dropped acks (call futures discarded unobserved, incl. `_ws=None` window);
  PositionLogger froze the backplot; a raising `on_update` callback also killed the
  loop. **Fix:** `_watch.py` reconnects with backoff and resubscribes, tolerates
  per-message errors (logs, continues), guards consumer callbacks, logs failed calls,
  and `stop()` cancels the task / closes the socket / closes the loop (GP-10).
- **GP-4 HIGH queued jog can land after a synchronous abort.** `jog`/`jog_stop` went
  through a private unbounded single-worker queue while `abort()`/`state()`/`mode()`
  POST synchronously — under a server stall the queue backs up (10 s timeout per item)
  and a recovered server replays stale `JOG_CONTINUOUS` items *after* an abort the
  operator saw complete (bounded only by the 2 s server jog watchdog). Classic NML
  serialized all commands on one channel. **Fix:** jog/jog_stop are synchronous again
  (classic ordering restored); the async queue is deleted. Classic parity also means a
  stalled server blocks the caller — same as NML's serial-echo wait.
- **GP-5 HIGH `stop_logger` never sent; server `pending` unbounded.** `PositionLogger.stop()`
  only stopped the local loop; the server sampler keeps running and `poslog.go` appends
  to `pending` forever once no subscriber drains it (~100 Hz of points while the
  machine moves → unbounded controller memory; next session receives the stale backlog
  as its opening backplot — classic `Logger_start` began empty). **Fix (both sides):**
  shim `stop()` sends `stop_logger` before closing and `start()` resets the local
  buffer + sends `clear_logger` on the first connect only (a mid-session reconnect
  keeps the server backlog for a gap-free plot); server `poslog.go` caps `pending`
  (drop-oldest, 2× ring) as intrinsic robustness against vanished clients. Remaining
  caveat (noted in the `stop()` docstring): the sampler is shared server state — one
  client's `stop_logger`/`clear_logger` affects every client of the instance;
  refcounting is a server-side follow-up if multi-GUI plotting becomes a real
  deployment shape.
- **GP-6 MED `tools.py get()` returns a phantom all-zero tool.** The server deliberately
  returns the zero entry for an absent tool (tooltable contract: "callers tell no-such-tool
  from Toolno == 0"); the shim only mapped HTTP 404 → None, which cannot arrive.
  `if tt.get(n) is None` treated a missing tool as existing zero geometry. **Fix:**
  `toolno == 0` → None.
- **GP-7 MED (design) live WS cache vs classic frozen-on-poll snapshots.** Classic stat
  attributes change only on `poll()` (memcpy snapshot); the shim's cache mutated live
  from the WS between polls, so multi-attribute predicates could observe mixed epochs
  (`glcanon.redraw` sums g5x+g92 across six reads; the lathe/jog-axis float-`==` CI flake
  was this class). Since `poll()` became a synchronous fresh GET, the WS stream was a
  *redundant second data path* whose only effect was live-mutating attrs between polls.
  **Fix:** `Stat` is now REST-only — `poll()` swaps in the fetched snapshot wholesale
  (atomic reference replace; never mutated), attributes are frozen between polls
  exactly like classic. Deletes the WS thread, delta merge, and heartbeat-ordering
  machinery (~150 lines), frees one WS slot per client, and removes Stat from the
  reconnect problem entirely. Every in-tree consumer polls (verified: axis LivePlotter
  polls per cycle; all sampled test wait-loops poll inside the loop).
- **GP-8 MED `stat.tool_table` stale after touch-off.** The client cache re-fetched only
  when `tool_in_spindle` changed; classic read live mmap on every access. Tool
  touch-off (G10 L10/L11 + G43) changes the current tool's offsets without a tool
  change → AXIS status bar showed stale geometry until the next M6. **Fix:** cache key
  now includes the `tool_offset` 9-tuple, so an offset change invalidates too.
- **GP-9 MED positionlogger shared-buffer races.** `_add_point`/memmove ring-shift (WS
  thread) vs `call()`/`clear()`/`last()` (GUI thread) on one ctypes array with no lock:
  garbage frames during the shift, `clear()` silently undone by a racing store of the
  old count. Bounded (no crash — indices stay in the fixed array), but wrong. **Fix:**
  a `threading.Lock` around buffer mutation and render/clear/last.
- **GP-10 MED teardown leaks** — no `stop()` cancelled its recv task or closed its
  websocket ("Task was destroyed but it is pending!"); `Stat.stop()` leaked
  `_poll_conn`. **Fix:** in `_watch.py` + `Stat.stop()`.
- **GP-11 MED module namespace/constants gaps.** `gmi.MODE_MANUAL` raised
  AttributeError (constants not re-exported; `gomc_test.install_constants()` exists
  purely to patch this); classic names missing: `MIST_ON/OFF`, `FLOOD_ON/OFF`,
  `BRAKE_ENGAGE/RELEASE`, `LINEAR`/`ANGULAR`, `MOTION_TYPE_TRAVERSE..INDEXROTARY`,
  `EXEC_WAITING_FOR_SYSTEM_CMD` (value 9 existed only under the renamed
  `…_MCODE_HANDLER`). All present values verified correct. **Fix:** names added
  (classic alias kept alongside the gomc name), `from gmi.constants import *`
  re-exported from `gmi/__init__.py`.
- **GP-12 LOW three classic command wrappers missing though endpoints exist:**
  `set_feed_override` → `/feed-override-enable`, `set_feed_hold` → `/feed-hold-enable`,
  `set_spindle_override` → `/spindle-override-enable`. **Fix:** added.
- **GP-13 LOW `IniFile.find` lacked classic's third occurrence-index arg.** **Fix:**
  `find(section, key, num=None)` via the findall path.
- **GP-14 LOW `tools.py` docstring documented the wrong URL space** (`/api/v1/tools/*`;
  real routes are `/api/v1/{instance}/…` under the milltask module instance — the code
  was right, the docstring invited confusing it with the separate raw
  `/api/v1/tooltable/` store). **Fix:** docstring.
- **GP-15 LOW `component_exists`/`pin_has_writer` didn't URL-encode the pattern.**
  **Fix:** `urllib.parse.quote`. (Their return-False-on-any-error remains — documented:
  "server down" is indistinguishable from "not found" by design of these probes.)

## CONFIRMED — deferred to the AXIS-adaptation review row (recorded, not fixed here)

- **GP-16 HIGH AXIS + glcanon are 25.4× wrong on inch machines.** axis.py uses raw
  `gmi.Stat()` (mm-everywhere) while `glcanon.to_internal_units` is byte-identical to
  2.9 (divides by `linear_units*25.4` — correct only for machine-unit positions; gomc
  serves classic `linear_units` = machine-units-per-mm but mm positions, so the divisor
  degenerates to 1.0 on inch configs): preview/backplot/DRO scale off by 25.4 on any
  `LINEAR_UNITS = inch` machine. Invisible on metric machines/CI, which is exactly why
  it survives. Two tests already opt into `machine_units()` — AXIS must too (or convert
  at its boundary). This is the AXIS diff's problem, not the shim's: the shim documents
  mm and provides `MachineUnitsStat`.
- **GP-17 MED `PositionLogger.set_roffsets` has no caller** — the live backplot never
  applies A/B/C rotations or g5x/g92 rotation offsets; classic shared module globals
  with the preview, the fork's `glhelpers.cc` globals feed only the preview. On a
  GEOMETRY with `!`/rotary letters the plot diverges from its own preview. Fix belongs
  where glcanon updates glhelpers.
- **GP-18 MED ten bare `c.wait_complete()` calls in axis.py** can now raise
  `urllib.error.HTTPError` (fork contract: a wait that didn't happen raises) inside Tk
  callbacks. The changelog claims bin/axis routes command refusals into the
  notification area, but no HTTPError wrapper around the global `c` was found in
  axis.py — verify/complete in the AXIS review. Side-note found en route:
  `LivePlotter.stop()` sets `running` to True (axis.py:795), which both prevents
  restart and accidentally prevents Stat-leak cycles.

## Ruled / kept as-is (documented)

- **GP-19 `wait_complete` raises on a wait-that-didn't-happen instead of returning -1,
  and never returns RCS_ERROR** (an accepted-and-faulted command reports via the error
  channel; the failed call itself raised at issue time). Deliberate fork contract per
  the REST fault-status rulings; `gomc_test` wraps it. Classic code comparing
  `== RCS_ERROR` must be ported knowingly.
- **GP-20 `Stat.poll()` keeps the stale cache on fetch failure instead of raising
  `linuxcnc.error`** — deliberate (drivers poll in loops); combined with
  empty-collection defaults before the first successful poll, a classic
  caught-exception path can become an uncaught IndexError (`stat.spindle[0]`) — accepted;
  consumers get classic-length data after the first poll, and AXIS's startup poll loop
  exits cleanly if the server never appears.
- **GP-21 `spindle()` positional semantics** — classic reads arg 1 as *spindle number*
  for OFF/INCREASE/DECREASE/CONSTANT and as speed for FORWARD/REVERSE; the shim has one
  fixed `(direction, speed, spindle, wait)` signature. No heuristic added (silent
  misinterpretation either way); divergence documented in the docstring. Out-of-tree
  multi-spindle callers must use keywords.
- **GP-22 `wait_complete(0)` blocks up to 5 s** (server coerces ≤0 to the 5 s default)
  vs classic's immediate single probe. Matters first for the gladevcp port
  (`hal_actions.py` polls with `wait_complete(0)`); needs a server-side probe semantic
  — file with the gladevcp/qtvcp milestone.
- **GP-23 classic command methods with no server endpoint at all:** `tool_offset`,
  `reset_interpreter`, `traj_mode`, `set_min_limit`/`set_max_limit`,
  `set_adaptive_feed`, `set_digital_output`/`set_analog_output`,
  `error_msg`/`text_msg`/`display_msg`. None called by served in-tree consumers; qtvcp/
  gladevcp/gscreen do call them → IDL additions belong to the widget-centric UI
  migration milestone, not the shim.
- **GP-24 classic stat attrs intentionally absent:** `echo_serial_number` (no NML
  serials — two tests document the removal), `ini_filename`, `misc_error`. `spindles`
  (count) was the one worth adding — **added** (derived from the spindle array).
- **GP-25 `input_timeout` is tri-state on the wire** (2 = M66 still waiting) vs classic
  bool (true only after timeout) — truthiness differs mid-wait; server-side semantic,
  noted for any consumer porting `if s.input_timeout`.
- **GP-26 `start_logger` while running ignores the new interval** (server returns
  early) — a GUI restart cannot change the sampling rate; minor, noted.
- **GP-27 positionlogger minor divergences kept:** fixed 10000-point cap vs classic's
  geometric growth (classic retained up to ~2× more before dropping); motion_type 6
  clamps to color 0 (classic read `colors[6]` out of bounds — UB); WS delivery adds up
  to 200 ms plot lag vs classic 10 ms in-process sampling.
- **GP-28 no `linuxcnc.error` exception equivalent** — the name `gmi.error` is taken by
  the module; failures surface as `urllib.error.HTTPError` or logged degradation.
  Classic `except linuxcnc.error:` handlers can never fire — port them knowingly.
- **GP-29 probe semantics of `component_exists`/`pin_has_writer`** — see GP-15.

## Test coverage added

`tests/gmi-shim/` (runtests, standalone — no gomc-server): a Python stub REST+WS
server drives the shim's real code paths. Covers: GP-1 (PUT body carries the `entry`
envelope; caller dict unmutated), GP-6 (zero entry → None), GP-4 (jog and abort arrive
in issue order), GP-7 (attributes frozen between polls; poll swaps snapshots), GP-2/3
(ErrorChannel connects after delayed server start, survives a server restart,
delivers messages after reconnect), GP-5 (stop() sends stop_logger). Server-side cap
(GP-5) covered by a Go test in `internal/task`.
