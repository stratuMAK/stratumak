# internal/ngcpreview — Tier-2 review findings

Module: `src/gomc/internal/ngcpreview` (server-side G-code preview; replaces the
classic in-process `gcode` C extension) plus its hand-written Python adapter
`lib/python/gcode.py`. Classic oracle: 2.9 `gcodemodule.cc`. Reviewed 2026-07-23
(one adversarial pass, every HIGH re-verified by hand before fixing). Prior
coverage not re-reported: NGC1 (interp static-state concurrency,
`STATE_MACHINE_REVIEW_FINDINGS.md`) — now mitigated by the N-4 serialization.

Architecture verified sound: fresh in-process `Interp` per request, recording
canon; tool table from the tooltable service, params read-only from persist
`ngc_vars` (both same sources as the task interp); AXIS sends the machine's
modal state as initcodes after `task_plan_synch`. Wire unit space is **inches**
(AXIS internal units), byte-matching classic incl. arc centers and feed rates.

## CONFIRMED — fixed

- **N-1 HIGH gen_preview bypassed path containment entirely.** `filename` went
  straight to the interpreter's `fopen`; `resolveProgramPath` + the allow-list
  guarded only `get_file` (the comment claimed both). Any REST client could
  make the interp open any server-readable path (or block forever on a FIFO).
  **Fix:** `GenPreview` resolves + contains first, same allow-list. Mutation
  test: escapee refused *with reason*, identical file inside PROGRAM_PREFIX
  previews (per the containment-vacuity rule).
- **N-2 HIGH preview silently truncated at the first M6 / G38.x / M66 / user-M.**
  The read/execute loop treated `INTERP_EXECUTE_FINISH` (2) — success-with-flag
  for every toolchange/probe/input/user-M block — as a stop, and the error
  branch (`rc > ENDFILE(3)`) never fired, so the preview ended there with NO
  error: every real job with a tool change previewed wrong-but-clean. Classic
  defined `RESULT_OK` as OK-or-EXECUTE_FINISH. **Fix:** EXECUTE_FINISH
  continues (the next read clears the flag via queue-empty, which this canon
  provides); the strict unitcode check aligned too. Test drives T0 M6 + G38.2
  through and asserts both later moves exist.
- **N-3 HIGH preview interpreter ran with no INI configuration.** The rs274ngc
  preview shim had no INI-accessor API, so `Interp::init` read none of REMAP /
  SUBROUTINE_PATH / PROGRAM_PREFIX / RANDOM_TOOLCHANGER / WRAPPED_ROTARY /
  arc tolerances — a remapped M6 or an o-call from SUBROUTINE_PATH failed in
  the preview while the machine executes it fine. **Fix:**
  `interp_shim_set_ini_accessor` added to `emc/rs274ngc/interp_shim.{h,cc}`
  (mirrors `_setup.ini_accessor`), Go bridge `ini_accessor.go` (own //export
  names — cgo exports are binary-global), wired before init from the module's
  namespace INI. Mutation test: SUBROUTINE_PATH o-call previews with the INI,
  fails without.
- **N-4 HIGH unbounded segment growth / no deadline / unchecked realloc /
  no serialization.** An `o100 while [1]` with motion grew the C segment buffer
  without limit **inside the controller process** (classic ran this loop in
  the GUI process); realloc failure was a NULL-deref crash; concurrent
  gen_preview requests each ran an independent unbounded interp. **Fix:**
  hard segment cap (1M default, `ctx.seg_limit` + `truncated` flag, "preview
  truncated" error), wall-clock deadline (default 60 s, `[DISPLAY]
  PREVIEW_TIMEOUT` overrides — the classic INI key, revived server-side),
  realloc-failure-safe cap growers (old buffer preserved, arc recorder no
  longer touches `segments[seg_count-1]` after a skipped add), and
  `interpMu` serializing GenPreview/EvalExpression (also mitigates NGC1).
  Client half: the generated Python REST clients now set a socket timeout
  (gmicompile `--client-python` emits `timeout=self.timeout`, default 90 s,
  constructor-overridable) — a stalled server surfaces as an error instead of
  freezing AXIS's Tk thread forever.
- **N-5 HIGH any dwell (G4, canned cycles) crashed the replay → empty preview.**
  `gcode.py`'s `_SequenceState` carried only `sequence_number`, but
  `GLCanon.dwell` decodes `state.plane` (and comment/user_defined_function
  iterate `state.gcodes`/`mcodes`) → AttributeError out of `gcode.parse`,
  swallowed by AXIS as a generic notification, empty screen for any drilling
  program. **Fix:** `_SequenceState` carries plane (wire 1/2/3 → G-code
  170/190/180) + empty gcodes/mcodes tuples; dwells replay with their wire
  plane.
- **N-6 HIGH partial geometry discarded on interp error.** The server returns
  partial segments + error, but `gcode.py` returned before replaying — an
  error at line 900 showed an empty screen instead of 899 lines + the error
  dialog (classic behavior). **Fix:** replay first, return the error code
  after. Server-side test asserts partial segments + correct line.
- **N-7 MED replay ordering: dwells/tool-changes replayed after all segments.**
  Dwell markers drew at the program's FINAL position; change_tool's
  first_move rapid-suppression applied after the fact. **Fix (client half):**
  the three wire lists are interleaved by line number during replay (exact
  for monotonic programs, each list keeping its own execution order).
  **CLOSED 2026-07-29 — ordered event stream on the wire.** The line-number
  interleave only held while line numbers increased: an O-word loop revisits
  a line and a call into another file restarts numbering, so a dwell inside
  a loop still replayed against the wrong move. The recorder now stamps every
  segment, dwell and tool change with `seq` from one counter
  (`preview_ctx_t.next_seq`), and `gcode.parse` merges the three lists by it
  instead of walking line numbers — the emission order reproduced exactly,
  with no assumption about line numbers at all.
  Note recorded while doing it: `Dwell.line_no` and `ToolChange.line_no` are
  approximate — the canon interface passes no line number to DWELL or
  CHANGE_TOOL (as in classic emccanon), so they inherit the last motion's.
  Their `file_idx` and `seq` are exact. Documented in the IDL; fixing the
  line numbers means a canon API change and a departure from classic.
  Tests: `internal/ngcpreview/eventorder_test.go`,
  `tests/axis-subfile-highlight` (replay-honours-emission-order).
  **Still deferred (wire change):** mid-program G5x/G92/rotation changes — the
  server records only the LAST offset, so a G54→G55 multi-fixture program
  draws both fixtures at the final offset. Needs offset-change events in the
  segment stream; same deferral class as the pyvcp watch seq marker.
- **N-9 MED error line was `maxLine+1`** (max over executed lines — wrong for
  O-word flow that jumps backward). **Fix:** the sequence number of the
  just-read line is tracked and reported (read errors +1); initcode failures
  now include the interp error text and the failing line.
- **N-11 LOW EvalExpression g-code injection** (`expr = "1] G0 X10"` →
  `#1=[1] G0 X10`). **Fix:** bracket-balance validation (a `]` closing the
  template bracket, newlines, `;` rejected). Tested.
- **N-12 LOW GetFile robustness:** 64 MiB size cap before read; CRLF
  normalized (classic read in Python text mode). Non-UTF8 → U+FFFD via JSON
  marshaling is accepted (display-only).
- **N-14 LOW stale/wrong comments:** shim header's wrong ENDFILE constant,
  "no-ops for preview" on recorders — corrected.

## Deferred / recorded

- **N-8 MED AXIS preview timeout / ESC-cancel dead code.** `set_timeout` has
  no caller, `check_abort`/`cancel_open` are unreachable since the C parse
  loop left. Partially compensated: server deadline (N-4) honors
  `[DISPLAY]PREVIEW_TIMEOUT` and the client socket timeout unfreezes AXIS.
  Restoring interactive cancel + progress needs a streaming/chunked preview
  protocol — file with the UI milestone. The dead axis.py code should go when
  that lands.
- **N-10 LOW custom interpreter (`[TASK]INTERPRETER`) silently dropped** —
  now warns on stderr when configured; server capability does not exist.
- **N-13 LOW duplicated persist param-restore C code** (module.go preamble vs
  `internal/task/interp_param_io_persist.c`) — known copy-divergence class;
  extraction deferred (the preview copy additionally grew a NULL-persist
  guard for testability).
- **UNVERIFIED (agent):** whether tooltable ListTools ordering guarantees the
  "index 0 = spindle" claim in stat.py's tool_table; whether ngcpreview folds
  TLO into emitted positions (fork `gcode.parse` never calls
  `canon.tool_offset`, extents-with-tool == extents-without — display-only).

## Tests added

`internal/ngcpreview/genpreview_test.go` (real interpreter, librs274 at
runtime): continues-past-toolchange/probe (N-2), refuses-uncontained +
contained-succeeds (N-1, one-mutation), partial-segments+line-on-error
(N-6/N-9), wire-units-inches G20 vs G21 (incl. arc centers), bounded on
endless programs (segment cap + time limit, N-4), INI-accessor
SUBROUTINE_PATH mutation pair (N-3), eval injection rejection + value (N-11).
`cgen/client_py_test.go` extended for the generated timeout.
