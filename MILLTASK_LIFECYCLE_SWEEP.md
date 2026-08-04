# milltask — Tool-Change / Lifecycle Porting Sweep

**Status:** complete (2026-07-15) · **Scope:** every interp call site and
`_setup`-affecting path in 2.9's task (emctask.cc, emctaskmain.cc, ioControl.cc)
checked against the Go sequencer · **Method:** systematic inventory of the 2.9
reference (`~/source/linuxcnc-2.9`) diffed against `src/stmak/internal/task`,
then class-by-class fixes verified by the previously-xfailed runtests.

This closes the "lifecycle edges were systematically under-ported" review
follow-up: rather than fixing xfail-by-xfail, the whole 2.9 surface was
enumerated once. 17 tests un-xfailed.

---

## 1. The 2.9 consistency model (reference summary)

2.9 keeps the interpreter's world model consistent through exactly three
mechanisms; every lifecycle bug we found was a missing instance of one of them:

1. **`Interp::synch()`** — pulls the world into `_setup`: positions,
   `current_pocket = GET_EXTERNAL_TOOL_SLOT()`, `selected_pocket`,
   feed/flood/mist/plane/units/overrides, then `load_tool_table()` →
   `set_tool_parameters()` (#5400–#5413). Called immediately on mode/state
   transitions, deferred (queued `taskPlanSynchCmd`) at interpreter-lifecycle
   points, and inline via `read_inputs()` when `toolchange_flag` is set.
2. **`Interp::restore_from_tag(traj.tag)`** (`emcTaskStateRestore`, AUTO only)
   — on abort, rolls modal state (G64 P/Q vs G61, coordinate system, tool
   offset, F, S, plane, …) back from readahead to the segment that was
   actually executing. Driven by the per-segment `StateTag` motion carries.
3. **Startup ordering** — `emcIoInit` (tool table into io) → `emcIoUpdate` →
   `emcTaskPlanInit` (interp init: `restore_parameters` → g5x push → `synch()`
   → `load_tool_table` → `init_tool_parameters`) → `RS274NGC_STARTUP_CODE`.
   The tool table is always visible before startup code runs.

## 2. Findings and fixes (all landed on this branch)

| # | 2.9 site | stmak gap | Fix |
|---|---|---|---|
| F1 | emctaskmain initMain ordering | `pkgTTClient` (canon tool getters' tooltable client) was published in `registerTools()` — *after* `initInterpreter()` and `runStartupCode()`; startup `G43 H1` failed "tool 1 not found" | Publish `pkgTTClient` right after the tooltable API lookup in `module.go Start()` |
| F2 ⚠ **superseded 2026-07-23** | `set_tool_parameters()` via `GET_EXTERNAL_TOOL_TABLE(0)` | The key-0 spindle snapshot loses its toolno (tooltable `PutTool` clobbers `entry.Toolno` with the key) → `tool_table[0].toolno` always 0 → #5400/#<_current_tool> stuck at 0 after M6/M61 | ~~`GetExternalToolTable(0)` resolves the spindle via `io.GetToolInSpindle()` + the tool's live table entry~~ — this was a workaround for a store keyed by TOOL NUMBER, in which the spindle record was not representable. The store is keyed by **slot** now (2.9's tooldata model, `920bfb085e`): slot 0 *is* the spindle, its toolno is real data, and the getter reads it directly. The `Canon`'s last-known-good spindle snapshot went with it. |
| F3 ⚠ **superseded 2026-07-23** | `CHANGE_TOOL_NUMBER(pocket)` (M61) | interp passed `current_pocket` but stmak iocontrol keys the spindle by TOOL NUMBER → M61 loaded the wrong/no tool | ~~interp passes the tool number~~ — same cause as F2. With the slot-keyed store the interp passes `settings->current_pocket` (the **slot**) again, exactly as 2.9 does; `stdglue.c` settool_epilog resolves it with a new `find_tool_index` interp-ctx callback rather than reusing `#<pocket>` (2.9's Python stdglue passed the carousel pocket there — a 2.9 bug, not reproduced). |
| F4 | ioControl EMC_TOOL_PREPARE idx-0 branch | stmak completed T0 prepare without the HAL handshake (classic non-random still pulses tool-prepare), and random treated T0 as the spindle instead of an ordinary table entry | `gmi_tool_prepare` restructured in ioControl.c + ioControl_v2.c |
| F5 ⚠ **superseded 2026-07-23** | ioControl random `load_tool` swap | Swap used `get_tool(0)` as "the spindle tool"; the spindle tool is the entry at pocket 0 tracked by `toolInSpindle` | ~~Swap re-keyed on `toolInSpindle`~~ — with the slot-keyed store `load_tool(idx)` is 2.9's verbatim: swap slots 0 and idx, and swap their `pocketno` with them. The `toolInSpindle` keying (and the "entry vanished from the table" guard it needed) is gone. |
| F6 | ioControl init (`random_toolchanger` branch) | `toolInSpindle` hardcoded 0 at startup; classic restores the pocket-0 tool (or -1 unknown) | `iocontrol_start` restores it from the spindle slot (both io modules). **2026-07-23:** was a `list_tools` scan for a `pocketno == 0` row; now a direct `get_tool(0)`, which is what 2.9 does (`tooldata_get(&tdata, 0)`). |
| F7 | `GET_EXTERNAL_TOOL_SLOT` / `GET_EXTERNAL_SELECTED_TOOL_SLOT` | Returned raw toolno; classic semantics are pocket-index (-1 = empty non-random spindle / idle) — broke `#<_current_pocket>`, `#<_selected_pocket>`, `stat.pocket_prepped` | Resolve via the tool's live entry pocket (`toolPocketFor`), random/non-random empty-spindle conventions preserved |
| F8 | `tooldata` idx vs pocket in stat | Classic `stat.pocket_prepped` reported a tooldata array index (file order), which has no stmak equivalent | stmak reports the pocket number (documented intentional divergence; reload-tool asserts updated) |
| F9 | .tbl import | `T0 Pn` lines (random empty-pocket marker) rejected as "no tool number" | Parser tracks a seen-T flag (`import_tbl.go`) |
| F10 | `find_tool_index/pocket(0)` | Unconditional "T0 → idx 0" shortcut baked in non-random semantics; random must look T0 up (and error when absent — G43 H0 semantics) | Shortcut now non-random-only; `GetToolByNumber` gained missing-tool detection (zero-entry ≠ found) + T0 presence via ListTools |
| F11 | emccanon `CHANGE_TOOL` move | `[EMCIO]TOOL_CHANGE_POSITION` was not implemented at all | Task config parses it (3/6/9 coords, machine-units→mm) and `Canon.ChangeTool` emits the EMC_MOTION_TYPE_TOOLCHANGE traverse |
| F12 | `emcTaskStateRestore` → `restore_from_tag` | Nothing rolled modal state back on abort: G64 P/Q from readahead survived; canon g5x shadow desynced from the executed CS (stat gcode G55 / g5x index G56) | Canon captures the interp's packed `state_tag_t` per executed block (`UpdateTag`), segments carry it in `motionMap`; `abortLocked` (AUTO) runs `interp_restore_from_tag` after the sequencer restart — the restore's canon emissions also reconcile SET_TERM_COND and SET_G5X_OFFSET |
| F13 | emcstat | `tool_from_pocket` missing from stat/gmi (io tracked it) | Added through emcstat IDL → stat.go → gmi python; random `tool_from_pocket` now captured pre-swap in `gmi_tool_load` |

Infra fixed en route: three ported drivers had no shebang since their port
commit (bash executed the Python — tlo/m61/reload-tool had never actually run);
`scripts/runtests` now wipes each test's `db/` persistence before running
(interp params / tool table / G10 rotations leaked between runs — the classic
fresh-`.var` assumption); postgui.hal remnants removed from shim-based drivers;
tool-info drivers had silently lost their entire init/T1/M6 sections in
porting (restored verbatim from classic).

## 3. Un-xfailed by this sweep (all green)

rs274ngc-startup, tlo, t0/{nonrandom,random-with-t0,random-without-t0},
tool-info/{non-random,random-no-startup-tool,random-with-startup-tool},
toolchanger/m61, toolchanger/reload-tool/{non-random,random},
toolchanger/toolno-pocket-differ/{nonrandom,random},
io-startup/random/{no-tool-in-P0,tool-in-P0}, mdi-queue/oword-queue-buster,
statbuffer-g5x-abort.

## 4. Deliberate divergences from 2.9 (documented, not bugs)

- **stat.pocket_prepped** reports the pocket number, not the tooldata array
  index (F8) — stmak has no file-order table.
- **State-tag timing:** stmak tags a segment with the modal state *after* its
  source line executed (2.9 tags with the state before the line). Restoring a
  segment on the same line as a modal change restores the post-line state —
  arguably more correct; only differs for single-line mode-change+move.
- **Spindle offsets** — ⚠ **reversed 2026-07-23.** This used to read "`tool_table[0]`
  reads the loaded tool's current table entry, not a copy frozen at load time",
  which was a consequence of the toolno-keyed store having no spindle row.
  Slot 0 is 2.9's spindle **copy** again, written by `load_tool` at change time.
  Both edit paths keep it in step: G10 re-emits `set_tool_table_entry(0, …)` for
  the loaded tool (`interp_convert.cc:4129`), and the tools API mirrors an edit
  of the loaded tool into slot 0 (`toolsImpl.syncSpindleSlot`). On a random
  changer there is nothing to mirror — slot 0 is the tool's only row.

## 5. Still open (tracked in PRODUCTION_READINESS.md)

- **G64 blending extents** (abort/g64's remaining xfail): stmak's canon lacks
  2.9's naive CAM detector (chord-deviation segment merging for G64 Qx) and
  the G64/G64 P tolerance corner-rounding extents diverge. Canon-emission
  feature gap — belongs with the M67/canon-queue-contract workstream.
- **Startup-code motion at estop** faults exec_state (cross-cutting list).
- **Operator-message loss** in the emcerror WS watch (destructive Flush +
  byte-identical suppression in `pushLoop`) — drivers mitigate with pacing
  and retries; needs a queue-semantics watch (per-connection cursor).
- Fault-path restore parity: `restore_from_tag` currently runs on the user
  abort path (`abortLocked`) only, matching the failing tests; 2.9 also
  restores on some fault tails. Extend if a test demands it.
