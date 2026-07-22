# AXIS NML→GMI adaptation — Tier-2 review findings

Scope: the diff vs merge-base `f5a72ff602` in `src/emc/usr_intf/axis/`
(`scripts/axis.py` ~1270 changed lines, gutted `extensions/emcmodule.cc`, new
`extensions/glhelpers.cc`, new `scripts/manualtoolchange_ui.py`,
`scripts/linuxcnctop.py`) plus the glcanon/gcode.py surface AXIS drives.
Classic oracle: `~/source/linuxcnc-2.9`. Reviewed 2026-07-23; inherits GP-16/
GP-17/GP-18 from `GMI_PYTHON_REVIEW_FINDINGS.md`. Untouched classic Tk/GL code
out of scope.

## CONFIRMED — fixed

- **A-1 HIGH (= GP-16) the 25.4× inch-machine unit break — full inventory +
  fix.** gomc serves classic `linear_units` (machine-units-per-mm) but mm
  positions; every classic `to/from_internal_*` helper divides by
  `linear_units*25.4`, which degenerates to 1.0 on inch configs. Precisely
  determined: the live backplot was CORRECT on metric machines (logger points
  are server-mm; the classic divisor is 25.4 there) and 25.4× too big on inch
  only — the whole break is inch-only, which is why the metric CI never saw
  it. **Fixes:**
  - Both Stat objects wrapped: `gmi.Stat().machine_units()` (global `s` and
    LivePlotter's) — restores machine units to every classic read site (DRO,
    soft limits, preview initcodes `g53 g0`/G43.1 injection, offsets/origin
    overlays, maxvel slider, glcanon rotation offsets).
  - Shim: `MachineUnitsStat` now converts `tool_table` entries (XYZ/UVW
    offsets + diameter × lin, ABC × ang) — REST entries are mm; the status
    bar and lathe tool cone displayed raw mm.
  - Command boundaries converted to the wire's mm: jog velocity (was machine
    units/s → jogged 25.4× too slow on inch), JOG_INCREMENT distance,
    `c.maxvel`, foam `set_depth` (logger space).
  - Jog mirror state canonicalized to the IDL's mm / mm/s / deg/s on send
    AND read-back (A-11; the IDL comments now say so). The server stores
    these opaquely, so cross-client sync stays coherent.
  - Logger draw space: glcanon's backplot scale is a constant 1/25.4
    (points are server-mm by contract); `lp.last()` and foam U/V convert
    with unit=1, the stat fallback with the machine conversion.
  - Preview segment space verified CLEAN (server emits AXIS internal inches,
    byte-matching classic).
- **A-2 MED (= GP-17) logger rotation offsets never wired.** `set_roffsets`
  had zero callers; on `!`-geometries with A/B/C the live plot ignored
  rotations while the preview applied them. **Fix:** glcanon mirrors its
  glhelpers updates to the logger — respect/rotary mask computed in
  `init_glcanondraw`, g5x+g92 offsets fed per redraw converted to the
  logger's mm space; guarded for `lp is None` (set late by axis).
- **A-3 HIGH (= GP-18) no command wrapper existed — the changelog claim was
  false.** `bin/axis` is a shebang-rewritten byte-copy of axis.py; zero
  HTTPError handling anywhere, `c = gmi.Command()` bare — every refused
  command (409/503) from a Tk callback was an uncaught traceback, including
  the ten bare `wait_complete()` sites. **Fix:** `AxisCommand(_GmiCommand)`
  overrides `_post` to catch HTTPError, extract the server's reason, route it
  to `notifications.add("error", …)` (stderr before the widget exists) and
  return -1 (the classic timeout value). Also fixed here: `LivePlotter.stop()`
  set `running` to **True** (typo — plotter unrestartable) and never called
  `stat.stop()`; the PRODUCTION_READINESS changelog line is corrected.
- **A-4 MED releasing one axis of a multi-axis jog killed the others.**
  `jog_off_actual` cleared `continuous_jog_in_progress` unconditionally →
  the dead-man refresh stopped for the still-held axis and the server killed
  it 2 s later mid-keypress. **Fix:** per-axis `jogging[]` drives the
  refresh; the flag clears only when no axis remains.
- **A-5 MED jog refresh period scaled with CYCLE_TIME.** 10 update cycles ×
  a legitimate `[DISPLAY]CYCLE_TIME=0.2` = 2.0 s == the server dead-man →
  stuttering/dying jogs. **Fix:** wall-clock refresh every 0.5 s.
- **A-7 MED filtered-program load clobbered `loaded_file`.** `program_open`
  carries the filter's tempfile while `loaded_file` keeps the original; the
  preview_seq resync then treated our own load as another client's and
  rewrote `loaded_file` to the tempfile (breaking reload-refilter) + spurious
  autofit. **Fix:** the path actually sent to `program_open` is tracked and
  excluded from the resync comparison.
- **A-8 MED failed tool-change confirm wedged the change.** Both UIs
  swallowed a `confirm()` failure with the request latch still set — the
  dialog never re-appeared, only abort recovered (classic HAL pin set could
  not fail). **Fix:** on confirm failure the latch resets (dialog re-arms)
  and the failure is surfaced (notification / stderr).
- **A-12 LOW linuxcnctop rendered a garbage `stop` row** (bound method leaked
  through `dir(s)`): `stop`/`machine_units`/`invalidate_tool_table` excluded.
- **A-13 LOW glhelpers `py_vertex9` had classic's inherited double bug**
  (swapped in/out arrays = OOB read; pointers passed to Py_BuildValue "d") —
  fixed rather than deleted (exported helper; no in-tree callers).
- **A-14 LOW leftover startup debug print removed.**
- **A-15 LOW running-line highlight restored classic `motion_id or
  motion_line` fallback** (highlight tracked readahead, not execution).
- **A-16 LOW gutted emcmodule GL stubs documented as non-functional** (module
  docstring) — a classic consumer (gremlin) calling them silently draws
  nothing; trap flagged for the qtvcp/gladevcp milestone.
- **A-17 LOW→fixed server-side: the task message list was unbounded** — a
  chatty error source grew server memory and every UI's notification area
  without limit (classic AXIS capped its area at 10). Capped drop-oldest at
  200 (`internal/task/messages.go`, `TestMessageListBounded`).
- **A-10 MED preview/file REST calls had no timeout on the Tk main thread**
  — fixed at the generator (all `--client-python` clients: socket timeout,
  default 90 s, constructor-overridable); the interactive
  progress/ESC-cancel restoration is deferred with N-8 (needs a streaming
  preview protocol).

## Open — needs human ruling

- **A-6 MED (PLAUSIBLE) ghost jog through a lost KeyRelease.** The narrowed
  `_focusout_handler` (deliberate: in-app focus changes must not kill jogs)
  plus the active 0.5 s refresh can keep a jog alive if a modal grab steals
  the KeyRelease while a key is held — classic's server-side persistence had
  no client actively re-arming the watchdog. Proposed: suppress the refresh
  (or jog_off_all) while a Tk grab is active / the manual tab is not the key
  target. Not fixed — the interaction with deliberate fork behavior needs a
  ruling.
- **A-9 MED duplicate manual-tool-change dialogs.** axis.py auto-activates
  its integrated MTC poller whenever the endpoint answers — which is exactly
  when the comp is loaded — while shipped configs (rolfmill, 3axis-tutorial,
  ethercat/swm-fm45a, stepconf/pncconf emitters) still `loadusr
  manualtoolchange_ui`: two dialogs for one request (they do auto-dismiss on
  the other's confirm). Product decision needed: drop the integrated poller,
  gate it behind an INI key, or make it authoritative and strip `loadusr`
  from AXIS-display configs + generators. Both UIs also poll at 1 s vs
  classic 100 ms (dialog up to 1 s late) — fold into the same ruling.

## Verified clean (by the review, no findings)

Frozen-snapshot Stat: no axis code depends on live-updating attributes (every
decision block polls). `ensure_mode` removal sound (server-side ensureMode).
Notification kind/ack routing. Dead-man refresh reaches all jog origins
(modulo A-4/A-5/A-6). Startup/shutdown order. Spindle/override calls match the
shim signatures (classic ambiguous positional-spindle form unused — GP-21 not
triggered). manualtoolchange request→confirm→deassert handshake incl.
auto-dismiss + SIGTERM. `iocontrol.*` pin renames. glhelpers line-equal to
classic in all live paths (GIL held, no memory-safety issues).
