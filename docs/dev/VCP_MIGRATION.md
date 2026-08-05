# VCP Frontend Migration Guide

This document describes the architecture for migrating ALL LinuxCNC UI
code (pyvcp, gladevcp, qtvcp, and full GUIs) to the stratuMAK infrastructure.
It covers both the widget-centric panel protocol (implemented for pyvcp)
and the broader migration strategy for the complete feature set.

## Migration Strategy

The goal is to eliminate direct `linuxcnc` Python module usage and move
all UI communication through the stratuMAK WebSocket/REST infrastructure.
Two patterns handle different complexity levels:

### Pattern A: Panel Module (server-side companion)

For `.ui`/`.xml`-based panels — a stratuMAK module that:
- Parses the panel file (pyvcp XML, Glade `.ui`, Qt `.ui`)
- Creates a HAL component with required pins
- Serves the file + widget definitions to the frontend client
- Handles widget events (server-authoritative state)
- Uses GMI-generated code for all protocol types and routing

**Already implemented**: `pyvcpmodule` for pyvcp panels.
**To implement**: unified `vcpmodule` for gladevcp/qtvcp `.ui` panels.

### Pattern B: Widget Descriptor (code-based panels)

For panels where widgets were created in Python code rather than a .ui
file, write a widget descriptor XML that declares the same widgets with
their constraints. The vcpmodule loads this just like a .ui file:

```
# .ui-based panel (layout from Glade/Qt Designer)
load vcpmodule <mypanel> ui=panel.ui

# Widget descriptor (same protocol, simpler format — like pyvcp XML)
load vcpmodule <mypanel> xml=widgets.xml
```

Both produce the same widget-centric protocol (WidgetDef, WidgetState,
EventType). The frontend doesn't know or care which source format was
used.

### Pattern C: haljson (non-widget HAL pins)

For custom HAL pins that are NOT widgets (e.g., a protocol bridge,
computed values, integration glue):
- haljson XML defines arbitrary HAL pin structures
- Frontend subscribes to haljson watch for pin-level JSON (delta-encoded)
- No widget semantics — raw pin values only

haljson is NOT a replacement for vcpmodule. Use it only when you need
HAL pins that don't map to any widget type (no events, no constraints,
no widget-centric protocol).

This replaces the gladevcp/qtvcp "handler" pattern where user Python
code added extra non-widget HAL pins to a panel's component.

### LinuxCNC-Integrated Widgets (client-side, no module needed)

Widgets like DRO, overrides, toolpath preview, G-code editor, MDI
history, etc. do NOT need a server-side panel module. They consume
existing stratuMAK APIs directly:

| Widget            | Reads from          | Sends to                    |
|-------------------|---------------------|-----------------------------|
| DRO               | emcstat (positions) | —                           |
| Override sliders  | emcstat (overrides) | emccmd (feed/spindle/rapid_override) |
| Offset table      | emcstat (offsets)   | emccmd (MDI G10 commands)   |
| Tool table        | tooltable API       | tooltable + emccmd.load_tool_table |
| 3D preview        | ngcpreview REST     | —                           |
| MDI history       | client-local        | emccmd.mdi()                |
| G-code editor     | ngcpreview.get_file | emccmd.program_open()       |
| Status labels     | emcstat             | —                           |
| Error log         | emcerror            | —                           |

### Command Validation (milltask handles it)

Milltask provides automatic mode switching and precondition checks:
- `ensureMode()` silently switches to MDI/AUTO/MANUAL as needed
- Commands are rejected with errors if preconditions fail (not homed,
  estop, interpreter busy)
- Jogging works from any mode when interpreter is idle

Frontends derive button sensitivity from `emcstat` fields client-side
(task_state, interp_state, homed flags). No server-side action
enablement layer is needed — milltask validates at execution time and
the error stream delivers rejection messages.

### Widget State Persistence

Use the existing `persist_sqlite` module with a `ui_` namespace prefix:
- Namespace: `ui_{panel_name}` (e.g. `ui_mypanel`)
- Key: widget ID, Value: serialized state
- `set_entries` for transactional bulk save on panel close
- `get_entries` for restore on panel open

The `ui_` prefix avoids collisions with other persist consumers
(`hal_retain`, `tooltable`, etc.).

## Existing stratuMAK APIs (available for all frontends)

| API           | Purpose                                        |
|---------------|------------------------------------------------|
| emcstat       | Machine state push (50ms delta) — positions, joints, modes, overrides |
| emccmd        | All machine commands — MDI, jog, estop, home, run/pause, overrides |
| emcerror      | Operator error/message stream (200ms push)     |
| ngcpreview    | G-code toolpath geometry (REST, used by axis)  |
| tooltable     | Tool table CRUD                                |
| haljson       | Generic HAL-to-JSON bridge (XML-configured)    |
| halrest       | Low-level HAL pin REST access                  |
| inirest       | INI file REST access                           |
| persist       | Key/value persistence (SQLite per namespace)   |

## GMI as Single Source of Truth

All protocol types, enums, REST endpoints, and WebSocket functions MUST
be defined in `.gmi` IDL files. Generated code handles:
- Type serialization (JSON marshaling)
- REST route registration
- WebSocket dispatch
- C callback API (for in-process consumers)

Only business logic (XML parsing, HAL pin creation, event→pin mapping)
is hand-written Go code.

## GladeVCP / QtVCP Panel Module Plan

Since gladevcp and qtvcp are architecturally identical (qtvcp's `Status`
subclasses gladevcp's `GStat`), they share ONE unified module.

### Widget Class → WidgetType Mapping

GladeVCP and qtvcp use different class names but map to the same
WidgetType enums and protocol behavior as pyvcp.

They do **not**, however, share a pin pattern — even where they share a
WidgetType — so each toolkit gets its own column. Do not assume a
GladeVCP pin name carries over to the QtVCP widget of the same role, or
vice versa; several roles are also implemented in only one of the two.

| WidgetType    | GladeVCP class      | GladeVCP pins                                | QtVCP class    | QtVCP pins                                   |
|---------------|---------------------|----------------------------------------------|----------------|----------------------------------------------|
| LED           | HAL_LED             | {name}(IN bit)                               | LED            | {name}(IN bit)                               |
| LED           | —                   | —                                            | StateLED       | {name}(**OUT** bit) — status-driven, see note |
| RECTLED       | —                   | —                                            | —              | — (pyvcp only)                               |
| NUMBER        | HAL_Label (type 1)  | {name}(IN float)                             | HALLabel       | {name}(IN float)                             |
| U32           | HAL_Label (type 2)  | {name}(IN u32)                               | —              | — (no u32 variant)                           |
| S32           | HAL_Label (type 0)  | {name}(IN s32)                               | HALLabel       | {name}(IN s32)                               |
| MULTILABEL    | —                   | —                                            | HALLabel       | {name}(IN s32) — multi-label mode            |
| (none)        | —                   | —                                            | HALLabel       | {name}(IN bit) — bit variant, renders true/false text |
| BAR           | HAL_Bar/HBar/VBar   | {name}(IN float)                             | HalBar         | {name}(IN float **or** s32)                  |
| METER         | HAL_Meter           | {name}(IN float)                             | Gauge          | {name}\_value(IN float), {name}\_setpoint(IN float) |
| BUTTON        | HAL_Button          | {name}(OUT bit)                              | PushButton     | {name}(OUT bit), {name}-led(IN bit)          |
| BUTTON        | —                   | —                                            | ActionButton   | {name}-led(IN bit) **only** — no output pin  |
| CHECKBUTTON   | HAL_CheckButton     | {name}(OUT bit), {name}-not(OUT bit)         | CheckBox       | {name}(OUT bit), {name}-not(OUT bit)         |
| CHECKBUTTON   | HAL_ToggleButton    | {name}(OUT bit), {name}-not(OUT bit)         | —              | —                                            |
| RADIOBUTTON   | HAL_RadioButton     | {name}(OUT bit), {name}-not(OUT bit)         | RadioButton    | {name}(OUT bit), {name}-not(OUT bit)         |
| SPINBOX       | HAL_SpinButton      | {name}-f(OUT float), {name}-s(OUT s32)       | DoubleScale    | {name}-f(OUT float), {name}-s(OUT s32)       |
| SCALE         | HAL_HScale/VScale   | {name}(OUT float), {name}-s(OUT s32)         | Slider         | {name}-s(OUT s32), {name}-f(OUT float), {name}-scale(IN float) |
| DIAL          | Hal_Dial            | {name}(OUT s32), {name}-scaled(OUT float), {name}-delta-scaled(OUT float) | Dial | {name}-s(OUT s32), {name}-f(OUT float), {name}-d(OUT float), {name}-scale(IN float) |
| JOGWHEEL      | JogWheel            | {name}(OUT s32), {name}-scaled(OUT float), {name}.scale(IN float) | — | — (no QtVCP jogwheel)             |
| IO_BUTTON     | HALIO_Button        | {name}(IO bit)                               | —              | —                                            |
| IO_SCALE      | HALIO_HScale        | {name}(IO float)                             | —              | —                                            |
| PROGRESSBAR   | HAL_ProgressBar     | {name}(IN float), {name}.scale(IN float)     | —              | — (RoundProgressBar has no HAL pins)         |
| COMBOBOX      | HAL_ComboBox        | {name}-f(OUT float), {name}-s(OUT s32)       | —              | — (HALSelectionBox is a pin browser, see note) |
| LIGHTBUTTON   | HAL_LightButton     | {name}-button(OUT bit, or IO), {name}-button-not(OUT bit, non-IO only), {name}-enable(IN bit), {name}-light(IN bit) | — | —          |
| CONTAINER_SENS| HAL_HBox/HAL_Table  | {name}(IN bit)                               | —              | —                                            |
| CONTAINER_VIS | HAL_HideTable       | {name}(IN bit)                               | —              | —                                            |

Verified against `lib/python/gladevcp/` and `lib/python/qtvcp/widgets/`
`_hal_init()` implementations. Pin naming follows each VCP's existing
convention to maintain backward compatibility with existing HAL files.
The server-side module maps these to the unified WidgetType for protocol
purposes.

Notes for the parser:

- **`{name}` is not always the widget name.** Most QtVCP widgets honour a
  `pin_name` property that overrides it; GladeVCP uses the widget's
  `hal_name`. Read the property before deriving pins.
- **`StateLED` writes its pin.** It subclasses `LED` but creates an OUT
  bit reflecting a STATUS condition. Under the server-authoritative
  model this is a status subscription, not a panel pin — do not model it
  as an LED input.
- **`ActionButton` has no output pin.** It calls emccmd actions directly;
  only the inherited `-led` indicator pin exists. Its migration target is
  a frontend emccmd call, not a pin.
- **`HALSelectionBox` creates no pins.** It enumerates existing HAL
  pins/signals/params via `hal.get_info_pins()` — a HAL introspection
  API, not a COMBOBOX. It is listed here only to stop it being mapped to
  one.
- **GladeVCP's `-scaled`/`-delta-scaled` are conditional.** `_HalJogWheelBase`
  creates each only if the subclass implements `get_scaled_value()` /
  `get_delta_scaled_value()`. `Hal_Dial` has both; `JogWheel` has only
  the first (and adds `.scale`). The pin set is per-class, not per-base.

### Widgets With No WidgetType Yet

These create HAL pins but have no protocol mapping. They are the reason
the table above must not be treated as exhaustive — several use **fixed
pin names** that ignore `{name}` entirely, which breaks any parser that
assumes a name-derived pin.

| Toolkit  | Class            | Pins                                                                        |
|----------|------------------|-----------------------------------------------------------------------------|
| GladeVCP | HAL_Graph        | {name}(IN float)                                                            |
| GladeVCP | SpeedControl     | {name}.value(OUT float), .scale(IN float), .scaled-value(OUT float), .increase(IN bit), .decrease(IN bit) |
| QtVCP    | LCDNumber        | {name}(IN bit\|float\|s32)                                                  |
| QtVCP    | StatusSlider     | {name}(OUT float)                                                           |
| QtVCP    | AxisToolButton   | {name}-joint(OUT bit), {name}-axis(OUT bit)                                 |
| QtVCP    | CamView          | `cam-rotation`(OUT float) — **fixed name**                                  |
| QtVCP    | HALPad           | {name}.right/.left/.top/.bottom/.center(OUT), {name}.light.center(IN bit)    |
| QtVCP    | GeneralHALInput  | {name}(IN bit\|float\|s32)                                                  |
| QtVCP    | GeneralHALOutput | {name}(OUT) + {name}-not(OUT), or {name}-s32 + {name}-float                 |
| QtVCP    | ToolDialog       | `change`(IN bit), `number`(IN s32), `changed`(OUT bit) — **fixed names, created under a switched `manualtoolchange` component prefix** — plus {name}change_button(IN bit) |
| QtVCP    | VersaProbe       | `searchvel`, `probevel`, `probeheight`, `blockheight`, `backoffdist` (OUT float) — **fixed names** |

### New Widget Types (beyond pyvcp)

| Widget           | HAL Pins                              | Events          | Notes                    |
|------------------|---------------------------------------|-----------------|--------------------------|
| PROGRESSBAR      | value(IN, float), scale(IN, float)    | none            | Bar with configurable scale |
| COMBOBOX         | -f(OUT, float), -s(OUT, s32)          | SELECT          | Dropdown selection       |
| LIGHTBUTTON      | button(OUT/IO), button-not(OUT, non-IO only), light(IN), enable(IN) | PRESS, RELEASE  | Button + LED combo       |
| CONTAINER_VIS    | visible(IN, bit)                      | none            | HAL-driven visibility    |
| CONTAINER_SENS   | sensitive(IN, bit)                    | none            | HAL-driven sensitivity   |
| IO_BUTTON        | state(IO, bit)                        | PRESS, RELEASE  | Bidirectional button     |
| IO_SCALE         | value(IO, float)                      | SET, INCREMENT  | Bidirectional slider     |

### Bidirectional IO Pins

For IO-direction widgets (`HALIO_Button`, `HALIO_HScale`): **last writer
wins**. The server tracks whether HAL or client wrote last, always pushes
the current truth. This matches HAL's native behavior for IO pins.

### .ui File Handling

The module parses standard Glade/Qt `.ui` XML to extract:
- HAL widget class names (e.g. `HAL_Button`, `HAL_SpinButton`)
- Widget names (→ HAL pin names)
- Properties (min/max/resolution from adjustment/property elements)
- Layout structure (served to frontend for rendering)

### Handler Replacement

| Old pattern (Python)                    | New pattern (stratuMAK)                     |
|-----------------------------------------|----------------------------------------|
| .ui panel, no handler                   | vcpmodule loads .ui (Pattern A)        |
| .ui panel + handler adding extra pins   | vcpmodule loads .ui + haljson for extra pins |
| Code-built widgets (no .ui)             | Write widget descriptor XML → vcpmodule (Pattern B) |
| Handler with non-widget HAL pins        | haljson XML (Pattern C)                |
| Handler reading linuxcnc status         | Frontend subscribes to emcstat directly |
| Handler sending machine commands        | Frontend calls emccmd directly         |
| Handler with computed logic (pin_a = pin_b * x) | Custom Go module              |

### Migration Tooling (future work)

A migration tool could auto-extract widget descriptor XML and haljson
XML from existing handler Python code:

- `halcomp.newpin(...)` → haljson pin declaration
- `HAL_Dial(name=..., min=..., max=...)` → widget descriptor entry
- Widget instantiation with Gtk.Adjustment → widget + constraints

Behavioral logic (signal handlers, status-reactive code, computed
values) cannot be auto-migrated and requires human judgment. The tool
would output a report of unmigrated logic with migration hints.

For now, migration is manual: inspect the handler, write the descriptor
XML(s), and move behavioral logic to either client-side emcstat/emccmd
usage or a custom Go module.

### Module Loading

```
# .ui-based panel (gladevcp/qtvcp style)
load vcpmodule <mypanel> ui=panel.ui

# Widget descriptor panel (code-based replacement, pyvcp style)
load vcpmodule <mypanel> xml=widgets.xml

# Extra non-widget HAL pins (alongside vcpmodule, for handler logic)
load haljson <mypanel_logic> config=extra_pins.xml

# Custom Go module for computed logic (if needed)
load mylogic <mypanel_calc>
```

### HAL Pin Namespacing

When loaded as `load vcpmodule <gladevcp> ui=panel.ui`, the HAL
component name is the alias (`gladevcp`). Pins are namespaced as
`gladevcp.{widget_name}` (or `gladevcp.{widget_name}-f`, etc.),
matching the existing convention that HAL files already reference
(e.g. `gladevcp.pins.mpg` becomes `gladevcp.mpg` — the `.pins.`
infix was a Python GladePanel artifact that is dropped).

### What Gets Deleted (not ported, not shimmed)

The existing Python widget libraries are **replaced**, not wrapped:

| Deleted / obsolete                     | Replaced by                            |
|-----------------------------------------|----------------------------------------|
| `lib/python/gladevcp/hal_widgets.py`    | vcpmodule server-side pin creation     |
| `lib/python/gladevcp/makepins.py`       | vcpmodule .ui parser (Go)             |
| `lib/python/gladevcp/hal_actions.py`    | Frontend calls emccmd directly         |
| `lib/python/gladevcp/gtk_action.py`     | Frontend calls emccmd directly         |
| `lib/python/gladevcp/core.py` (Info/Action/Status) | emcstat + emccmd + inirest APIs |
| `lib/python/gladevcp/hal_glib.py` (GStat/GPin) | emcstat WebSocket subscription  |
| `lib/python/gladevcp/persistence.py`    | persist_sqlite module (ui_ namespace)  |
| `lib/python/qtvcp/core.py`             | Same — emcstat + emccmd + inirest      |
| `lib/python/qtvcp/qt_action.py`        | Frontend calls emccmd directly         |
| `lib/python/qtvcp/qt_makegui.py`       | vcpmodule .ui parser (Go)             |

**Key principle**: There is NO Python shim layer. The old libraries that
created `GComponent`/`GPin` objects, polled `linuxcnc.stat`, or called
`linuxcnc.command()` are eliminated entirely. Frontends are thin
rendering clients that:

1. Receive widget definitions + state via WebSocket (panel module)
2. Subscribe to `emcstat` for machine state (positions, modes, etc.)
3. Call `emccmd` for machine commands (MDI, jog, etc.)
4. Read `inirest` for configuration constants

**What moves to Go (server-side)**:
- `makepins.py` logic → vcpmodule's .ui XML parser creates HAL pins
- `_hal_init()` per widget → vcpmodule's `extractWidget()` equivalent
- Pin constraints (min/max/resolution) → vcpmodule's WidgetDef
- Event→pin mapping → vcpmodule's `handleEvent()`

**What the frontend client does** (toolkit-specific, thin):
- Render widgets from the .ui layout + WidgetDef metadata
- Apply WidgetState updates to visual elements
- Translate user gestures to EventType and send widget_event
- Subscribe to emcstat for DRO, status labels, override displays
- NO pin creation, NO direct HAL access, NO linuxcnc module import

### Existing `bin/gladevcp` Rewrite

The `bin/gladevcp` script on the stratuMAK branch is already a thin gmi
client (not using `linuxcnc` Python module). It demonstrates the
frontend pattern: connect to stratuMAK WebSocket, subscribe to panel watch,
render GTK widgets from server-pushed state. The vcpmodule plan
formalizes what this script consumes.

### Anti-Pattern: Do Not Build a Shim Layer

Do NOT attempt an intermediate step where existing Python widget classes
(hal_widgets.py, hal_glib.py, hal_actions.py) are kept alive by
shimming their backends to use gmi WebSocket calls. This approach:

- Recreates `GComponent`/`GPin` over WebSocket with polling — contradicts
  the event-based server-authoritative model
- Reimplements `GStat` over gmi — duplicates what `emcstat` subscription
  already provides natively
- Results in editing generated code to fill gaps — violates GMI as
  single source of truth
- Produces a fragile compatibility layer that must track two APIs

The correct path is: implement the server-side vcpmodule (Go), then
write a thin frontend client that consumes the panel protocol directly.
Skip the intermediate "keep old widgets, swap backend" step entirely.

### API Reference

Protocol types and available fields are defined in the `.gmi` IDL files
(`src/gmi/idl/`). These are the single source of truth — do not
hard-code field names or API shapes from documentation alone. Key files:

- `pyvcp.gmi` — widget protocol (WidgetType, WidgetState, EventType)
- `emcstat.gmi` — machine status fields
- `emccmd.gmi` — available commands
- `persist.gmi` — persistence API

## PyVCP Widget Protocol (reference implementation)

The following sections describe the widget-centric protocol as
implemented by `pyvcpmodule`. This same protocol pattern applies to the
planned unified `vcpmodule` for gladevcp/qtvcp panels.

### Architecture

```
┌──────────────┐         WebSocket          ┌──────────────────────┐
│  Frontend    │◄──── watch_state (delta) ───│  stmak panel module   │
│  (Tk/Qt/GTK) │                             │                      │
│              │──── widget_event ──────────►│  HAL component       │
└──────────────┘         REST               │  (pins, constraints) │
       │         ◄── GET /panel/{name} ──────└──────────────────────┘
       │
       ▼
  Render widgets from XML/UI + WidgetDefs
  Translate gestures to EventType
```

**Server-authoritative**: The server owns all HAL pins, constraints
(min/max), quantization (resolution), and derived pins (-i from -f).
Clients never write pin values directly — only send events.

**Delta-encoded watch**: First message is a full snapshot
(`map[string]WidgetState`, keyed by widget ID — declared in the IDL as
`watch_state`'s return type with `@watch_delta true`, so the registration
is generated code). Subsequent messages contain only changed widgets.
Rate: 100ms default.

## Protocol Reference

### REST: Panel Load

```
GET /api/v1/pyvcp/panel/{name}
→ { name, xml, widgets: [WidgetDef...] }
```

**WidgetDef** (static, sent once):
| Field      | Type     | Description                              |
|------------|----------|------------------------------------------|
| id         | string   | Unique widget ID (= HAL pin namespace)   |
| type       | int      | WidgetType enum                          |
| min        | f64      | Lower limit (0 = not applicable)         |
| max        | f64      | Upper limit (0 = not applicable)         |
| resolution | f64      | Quantization step (0 = continuous)       |
| choices    | []string | Radio button choice labels               |
| format     | string?  | Printf format for display (e.g. "2.1f") |
| text       | string?  | Label/title text                         |

### WebSocket: watch_state

Subscribe:
```json
{"action":"subscribe","api":"pyvcp","instance":"<name>","func":"watch_state","rate_ms":100}
```

Update message:
```json
{"type":"update","func":"watch_state","data":{"widget.id":{"value":3.14,"state":false,"index":-1,"disabled":false}}}
```

**WidgetState** (per widget, pushed on change):
| Field    | Type    | Description                                   |
|----------|---------|-----------------------------------------------|
| value    | f64?    | Numeric value (null/omitted = not applicable); for TIMER = server-accrued elapsed seconds |
| state    | bool    | On/off (LED, button, checkbutton, timer run)  |
| index    | int32   | Selected index (radio, multilabel, image); -1 = N/A |
| disabled | bool    | Widget disabled via disable_pin               |
| reset    | bool    | Timer reset pin active (advisory display hint) |

**WidgetDef.min / WidgetDef.max** are `f64?` (nullable): `null` = no limit. A real
limit of `0` is distinct from "no limit", so `0` is *not* used as the sentinel.
`resolution` stays `f64` (0 = continuous, which is unambiguous).

### WebSocket: widget_event

```json
{"action":"call","api":"pyvcp","instance":"<name>","func":"widget_event","id":0,
 "args":{"event":{"widget":"dial.0","event":6,"value":0,"increment":1,"index":-1}}}
```

**EventType enum**:
| Value | Name      | Used by                          | Fields used        |
|-------|-----------|----------------------------------|--------------------|
| 1     | PRESS     | button                           | —                  |
| 2     | RELEASE   | button                           | —                  |
| 3     | TOGGLE    | checkbutton                      | —                  |
| 4     | SELECT    | radiobutton                      | index              |
| 5     | SET       | scale, spinbox, dial             | value              |
| 6     | INCREMENT | scale, spinbox, dial, jogwheel   | increment (+/- N)  |

**Unused fields**: Set `value=0` (not NaN), `increment=0`, `index=-1`.

## Widget Type Map

| WidgetType   | Pins (server-side)                  | Events accepted        | State fields used     |
|--------------|-------------------------------------|------------------------|-----------------------|
| LED          | state(IN), disable?(IN)             | none (display only)    | state, disabled       |
| RECTLED      | state(IN), disable?(IN)             | none                   | state, disabled       |
| NUMBER       | value(IN, float)                    | none                   | value                 |
| U32          | value(IN, u32)                      | none                   | value                 |
| S32          | value(IN, s32)                      | none                   | value                 |
| TIMER        | run(IN), reset(IN)                  | none                   | value(=elapsed s), state(=run), reset |
| BAR          | value(IN, float)                    | none                   | value                 |
| METER        | value(IN, float)                    | none                   | value                 |
| MULTILABEL   | legend.0..5(IN), disable?(IN)       | none                   | index, disabled       |
| IMAGE_BIT    | value(IN, bit)                      | none                   | index (0 or 1)        |
| IMAGE_U32    | value(IN, u32)                      | none                   | index                 |
| BUTTON       | state(OUT), disable?(IN)            | PRESS, RELEASE         | state, disabled       |
| CHECKBUTTON  | state(OUT), changepin(IN)           | TOGGLE                 | state                 |
| RADIOBUTTON  | choice.N(OUT)                       | SELECT                 | index                 |
| SCALE        | -f(OUT), -i(OUT), param?(IN)        | SET, INCREMENT         | value                 |
| SPINBOX      | value(OUT), param?(IN)              | SET, INCREMENT         | value                 |
| DIAL         | value(OUT, float, `.out` suffix when auto-named), param?(IN) | SET, INCREMENT | value |
| JOGWHEEL     | count(OUT, float, `.count` suffix when auto-named), reset?(IN), scale?(IN) | INCREMENT | value |

## Porting a New Frontend

### Step 1: Connect

1. `GET /api/v1/pyvcp/panel/{name}` → get XML + widget definitions.
2. Open WebSocket, subscribe to `watch_state`.
3. Wait for first full snapshot before rendering.

### Step 2: Render widgets

Parse the XML for layout (frames, boxes, tabs, labels). For each widget
element, use the `WidgetDef` (matched by ID) for constraints/config and
the `WidgetState` for initial values.

### Step 3: Handle server state updates

Register per-widget callbacks. On each delta update:
- Merge into local state cache.
- Update only changed widget visuals.
- Use `_from_server` flag to suppress feedback loops when programmatically
  setting widget values.

### Step 4: Send user events

Map UI gestures to `EventType`:
- Button press/release → PRESS/RELEASE
- Checkbox toggle → TOGGLE
- Radio select → SELECT with index
- Slider drag/set → SET with value
- Scroll wheel / arrow keys → INCREMENT with +1/-1
- Spinbox direct entry → SET with value

The server will clamp, quantize, and push back the authoritative value.
**Do not** assume the value you sent will be echoed back unchanged.

### Step 5: Timer (server-authoritative)

Timer elapsed time is accrued **by the server** (in its periodic scan) and pushed
in `value` as elapsed seconds — the client just formats and displays it. This is
what keeps multiple clients (and a client that connects mid-run) in agreement;
the old client-computed model showed zero/garbage to a late joiner.
- server: while `run` pin is high, `value += dt`; while `reset` pin is high, `value = 0`
- client: render `value` (e.g. as HH:MM:SS); `state`/`reset` are advisory only

## HAL Pin Naming Convention

The widget ID is the HAL pin namespace root. Pin names are constructed
by `pinName(widget, role)`:

| Role      | HAL pin name               | Example                    |
|-----------|----------------------------|----------------------------|
| state     | `{id}`                     | `button.0`                 |
| value     | `{id}` or `{id}.out`       | `spinbox.0`, `dial.0.out`  |
| count     | `{id}.count`               | `jogwheel.0.count`         |
| -f        | `{id}-f`                   | `scale.0-f`                |
| -i        | `{id}-i`                   | `scale.0-i`                |
| disable   | `{id}.disable`             | `button.0.disable`         |
| changepin | `{id}.changepin`           | `checkbutton.0.changepin`  |
| param     | `{autoBase}.param_pin`     | `scale.0.param_pin`        |
| choice.N  | `{id}.{choiceName}`        | `radiobutton.0.forward`    |
| legend.N  | `{id}.legendN`             | `multilabel.0.legend0`     |
| reset     | `{id}.reset`               | `jogwheel.0.reset`         |
| run       | `{id}.run`                 | `timer.0.run`              |
| scale     | `{id}.scale`               | `jogwheel.0.scale`         |

When `halpin` is specified in XML, it overrides the auto-generated ID.
The `autoBase` (counter-based name) is used for `param_pin` naming to
maintain backward compatibility with existing HAL files.

Note: When dial/jogwheel use auto-generated names (no explicit `halpin`
in XML), the primary output pin gets a suffix (`.out` / `.count`) for
legacy pyvcp compatibility. With explicit `halpin`, the ID is used directly.

## Design Invariants

1. **Clients never write HAL pins** — only send events (or use haljson
   REST for Pattern B).
2. **Server always pushes truth** — client must accept server state even
   if it differs from what was sent.
3. **Widget ID = stable identity** — same across connections, same across
   frontends. HAL nets reference these.
4. **XML/UI is layout-only for clients** — constraints (min/max/resolution)
   come from WidgetDef, not from re-parsing XML attributes.
5. **Multiple clients sync automatically** — shared server state, no
   client-to-client communication needed.
6. **GMI IDL is the single source of truth** — all protocol types and
   endpoints defined in `.gmi` files, code generated from there.
7. **Last writer wins for IO pins** — matches HAL's native behavior.
8. **Milltask owns command validation** — frontends grey out buttons
   based on emcstat but do not gatekeep commands.
