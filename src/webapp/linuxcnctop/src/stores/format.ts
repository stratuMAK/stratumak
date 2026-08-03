// Flattening and value formatting for the emcstat StatFull snapshot.
//
// This is the transliteration of the `maps` dict in the Tk linuxcnctop
// (src/emc/usr_intf/axis/scripts/linuxcnctop.py): a per-field formatter table
// plus a generic fallback. Two things differ from the Tk original, both
// because StatFull is a typed nested struct rather than the flat 2.9-named
// attribute bag the Python gmi.Stat shim exposes:
//
//   - enum fields render through the generated TS enums, not a hand-written
//     int -> name table that has to be kept in sync by hand;
//   - the per-joint / per-spindle / per-axis records are shown. The Tk version
//     suppressed them ('axis': None, 'joint': None) because its one flat text
//     widget had nowhere to put them; here they are ordinary nested rows and
//     the server only sends the ones that exist.

import {
  TaskMode,
  TaskState,
  InterpState,
  ExecState,
  TrajMode,
  KinematicsType,
  type Position,
  type StatFull,
} from '../generated/emcstat_client';

/** One rendered line of the status display. */
export interface StatusRow {
  /** Dotted path into StatFull, e.g. "task.exec_state" or "joints.0.homed". */
  key: string;
  /** Formatted value; never empty (an empty render becomes "-", as in Tk). */
  value: string;
}

/** Axis letters in axis_mask bit order (bit 0 = X ... bit 8 = W). */
const AXIS_LETTERS = ['x', 'y', 'z', 'a', 'b', 'c', 'u', 'v', 'w'] as const;

/** Context the formatters need beyond the value itself. */
export interface FormatContext {
  axisMask: number;
  joints: number;
}

function fixed(n: number): string {
  return Number.isFinite(n) ? n.toFixed(4) : String(n);
}

/** Render an enum value through the generated enum's reverse mapping, falling
 *  back to the raw number for a value the IDL does not name. */
function enumName(e: object, v: number): string {
  const name = (e as Record<number, string>)[v];
  return typeof name === 'string' ? name : String(v);
}

/** Positions render only the axes the machine actually has (2.9 behaviour:
 *  show_position() filters on stat.axis_mask). */
function showPosition(p: Position, ctx: FormatContext): string {
  const out: string[] = [];
  for (let i = 0; i < AXIS_LETTERS.length; i++) {
    if (ctx.axisMask & (1 << i)) {
      out.push(fixed(p[AXIS_LETTERS[i]]));
    }
  }
  return out.join(' ');
}

/** Per-joint arrays are declared [MAX_JOINTS] on the wire; only the configured
 *  joints carry meaning. Numbers follow formatGeneric's rule — integers render
 *  integrally (limit is i32; "0.0000" for a limit switch state is noise),
 *  fractional values get the fixed 4 decimals. */
function showJointArray(a: unknown[], ctx: FormatContext): string {
  return a.slice(0, ctx.joints)
    .map(v => typeof v === 'number' ? (Number.isInteger(v) ? String(v) : fixed(v)) : String(v))
    .join(' ');
}

/** active_gcodes[0] / active_mcodes[0] are the interpreter's sequence number,
 *  not a code (interp_write.cc write_g_codes/write_m_codes); -1 means "no code
 *  active in this modal group". Both are skipped, exactly as 2.9 does. */
function showGcodes(a: number[]): string {
  return a.slice(1).filter(i => i !== -1).map(i => 'G' + (i / 10)).join(' ');
}

function showMcodes(a: number[]): string {
  return a.slice(1).filter(i => i !== -1).map(i => 'M' + i).join(' ');
}

function showFloats(a: number[]): string {
  return a.map(fixed).join(' ');
}

function showBools(a: unknown[]): string {
  return a.map(v => String(Boolean(v))).join(' ');
}

/** motion.motion_type — the canon motion kind of the executing segment. */
const MOTION_TYPE_NAMES: Record<number, string> = {
  0: 'none', 1: 'traverse', 2: 'feed', 3: 'arc', 4: 'toolchange', 5: 'probing',
};

/** program_units — canon length units (emcstat.gmi: 1=inch, 2=mm, 3=cm). */
const PROGRAM_UNITS_NAMES: Record<number, string> = {
  1: 'inch', 2: 'mm', 3: 'cm',
};

/** state / rcs_status — the RCS command status enum. */
const RCS_NAMES: Record<number, string> = {
  1: 'rcs_done', 2: 'rcs_exec', 3: 'rcs_error',
};

/** input_timeout — M66 wait state (emcstat.gmi). */
const INPUT_TIMEOUT_NAMES: Record<number, string> = {
  0: 'none', 1: 'timed out', 2: 'waiting',
};

type Formatter = (v: never, ctx: FormatContext) => string;

/** Per-path formatters, keyed by the flattened dotted path. Anything not
 *  listed here falls through to formatGeneric(). */
const FORMATTERS: Record<string, Formatter> = {
  'task.mode': (v: TaskMode) => enumName(TaskMode, v),
  'task.state': (v: TaskState) => enumName(TaskState, v),
  'task.interp_state': (v: InterpState) => enumName(InterpState, v),
  'task.exec_state': (v: ExecState) => enumName(ExecState, v),
  'task.program_units': (v: number) => PROGRAM_UNITS_NAMES[v] ?? String(v),
  'task.input_timeout': (v: number) => INPUT_TIMEOUT_NAMES[v] ?? String(v),
  'motion.mode': (v: TrajMode) => enumName(TrajMode, v),
  'motion.motion_type': (v: number) => MOTION_TYPE_NAMES[v] ?? String(v),
  'motion.dtg': showPosition,
  'kinematics_type': (v: KinematicsType) => enumName(KinematicsType, v),
  'position': showPosition,
  'actual_position': showPosition,
  'probed_position': showPosition,
  'g5x_offset': showPosition,
  'g92_offset': showPosition,
  'tool_offset': showPosition,
  'joint_position': showJointArray,
  'joint_actual_position': showJointArray,
  'homed': showJointArray,
  'limit': showJointArray,
  'active_gcodes': showGcodes,
  'active_mcodes': showMcodes,
  'active_settings': showFloats,
  'ain': showFloats,
  'aout': showFloats,
  'din': showBools,
  'dout': showBools,
  'state': (v: number) => RCS_NAMES[v] ?? String(v),
  'rcs_status': (v: number) => RCS_NAMES[v] ?? String(v),
  // The EMC_DEBUG_* bitmask. Rendered as the hex an INI [EMC]DEBUG= would
  // carry. Note this value is inert on gomc — nothing reads the flags; the
  // live verbosity knob is the halcmd log level (halshow -> Cmd tab).
  'debug': (v: number) => '0x' + (v >>> 0).toString(16).padStart(8, '0'),
};

// Note: there is no suppression list here. The Tk version needed one
// ('poll': None, 'tool_table': None, 'stop': None, ...) to hide the gmi.Stat
// object's methods and helpers from dir(); a JSON snapshot carries only data.

/** Record arrays that arrive at their full declared width regardless of the
 *  machine's configuration — joints[] is always MAX_JOINTS long and axis[]
 *  always MAX_AXIS, exactly as classic NML sent them, with joints_count and
 *  axis_mask as the validity selectors. Without this a 3-axis mill renders 368
 *  rows of joints 3..15 that do not exist, burying the ones that do. This is
 *  the same rule showPosition and showJointArray already apply to the flat
 *  arrays; spindle[] needs none — it arrives correctly sized. */
const CONTAINER_FILTERS: Record<string, (v: unknown[], ctx: FormatContext) => [number, unknown][]> = {
  joints: (v, ctx) => v.slice(0, ctx.joints).map((e, i) => [i, e]),
  // axis[] is indexed by axis letter position (0=X .. 8=W), the same order
  // axis_mask's bits are in, so the index is kept rather than renumbered: on an
  // XZ lathe the rows are axis.0 and axis.2, matching the position columns.
  axis: (v, ctx) => v.map((e, i): [number, unknown] => [i, e])
    .filter(([i]) => ctx.axisMask & (1 << i)),
};

function formatGeneric(v: unknown): string {
  if (v === null || v === undefined) return '';
  if (typeof v === 'number') return Number.isInteger(v) ? String(v) : fixed(v);
  if (typeof v === 'bigint') return String(v);
  if (typeof v === 'boolean') return String(v);
  if (typeof v === 'string') return v;
  if (Array.isArray(v)) return v.map(formatGeneric).join(' ');
  return JSON.stringify(v);
}

/** True for a value that should be recursed into rather than rendered as one
 *  row: a plain object, or an array of objects (joints/spindle/axis). */
function isContainer(v: unknown): boolean {
  if (Array.isArray(v)) {
    return v.length > 0 && typeof v[0] === 'object' && v[0] !== null;
  }
  return typeof v === 'object' && v !== null;
}

/**
 * Flatten a StatFull snapshot into display rows, in IDL declaration order
 * (which groups task.*, then motion.*, then the top-level fields) rather than
 * the alphabetical order Python's dir() forced on the Tk version.
 */
export function flattenStat(stat: StatFull): StatusRow[] {
  const ctx: FormatContext = {
    axisMask: stat.axis_mask ?? 0,
    joints: stat.joints_count ?? 0,
  };
  const rows: StatusRow[] = [];

  const walk = (obj: Record<string, unknown>, prefix: string) => {
    for (const [k, v] of Object.entries(obj)) {
      const path = prefix ? prefix + '.' + k : k;
      const fmt = FORMATTERS[path];
      if (fmt) {
        rows.push({ key: path, value: (fmt as (v: unknown, c: FormatContext) => string)(v, ctx) || '-' });
        continue;
      }
      const filter = CONTAINER_FILTERS[path];
      if (filter && Array.isArray(v)) {
        const kept = filter(v, ctx);
        if (kept.length === 0) {
          rows.push({ key: path, value: '-' });
          continue;
        }
        for (const [i, entry] of kept) {
          walk(entry as Record<string, unknown>, `${path}.${i}`);
        }
        continue;
      }
      if (isContainer(v)) {
        walk(v as Record<string, unknown>, path);
        continue;
      }
      rows.push({ key: path, value: formatGeneric(v) || '-' });
    }
  };

  walk(stat as unknown as Record<string, unknown>, '');
  return rows;
}

/** The rows as a plain tab-separated text block (the "Copy All" payload, and
 *  the same shape linuxcnctop-dump prints). */
export function rowsToText(rows: StatusRow[]): string {
  return rows.map(r => `${r.key}\t${r.value}`).join('\n');
}
