// Form model and strict numeric parsing for ToolEditDialog, kept out of the
// component so the parse/validate rules are testable without mounting it.
import type { ToolEntry } from '../generated/tools_client';

// The concurrency stamp is not editable: it is carried through the form
// verbatim as a bigint and never appears in the field list.
export type EditableKey = Exclude<keyof ToolEntry, 'updated'>;

export interface Field {
  key: EditableKey;
  label: string;
  type: 'int' | 'float' | 'text';
  readonly?: boolean;
}

export const fields: Field[] = [
  { key: 'toolno', label: 'Tool Number', type: 'int', readonly: true },
  { key: 'pocketno', label: 'Pocket', type: 'int' },
  { key: 'x_offset', label: 'X Offset', type: 'float' },
  { key: 'y_offset', label: 'Y Offset', type: 'float' },
  { key: 'z_offset', label: 'Z Offset', type: 'float' },
  { key: 'a_offset', label: 'A Offset', type: 'float' },
  { key: 'b_offset', label: 'B Offset', type: 'float' },
  { key: 'c_offset', label: 'C Offset', type: 'float' },
  { key: 'u_offset', label: 'U Offset', type: 'float' },
  { key: 'v_offset', label: 'V Offset', type: 'float' },
  { key: 'w_offset', label: 'W Offset', type: 'float' },
  { key: 'diameter', label: 'Diameter', type: 'float' },
  { key: 'frontangle', label: 'Front Angle', type: 'float' },
  { key: 'backangle', label: 'Back Angle', type: 'float' },
  { key: 'orientation', label: 'Orientation', type: 'int' },
  { key: 'comment', label: 'Comment', type: 'text' },
];

// Units handling (finding T-9). The tool table is stored in mm everywhere; the
// operator edits/reads in machine-linear units. `linearScale` is machine-linear-
// units-per-mm (1.0 metric, ~0.03937 inch). LINEAR fields scale by it; ANGULAR
// fields are always degrees and never scale; counts/text never scale.
export const LINEAR_KEYS: ReadonlySet<EditableKey> = new Set<EditableKey>([
  'x_offset', 'y_offset', 'z_offset', 'u_offset', 'v_offset', 'w_offset', 'diameter',
]);

export const ANGULAR_KEYS: ReadonlySet<EditableKey> = new Set<EditableKey>([
  'a_offset', 'b_offset', 'c_offset', 'frontangle', 'backangle',
]);

// "mm" on a metric machine, "in" otherwise.
export function unitLabel(metric: boolean): string {
  return metric ? 'mm' : 'in';
}

// Suffix used in a column/field label: linear -> unit, angular -> deg, else none.
export function fieldUnit(key: EditableKey, metric: boolean): string | null {
  if (LINEAR_KEYS.has(key)) return unitLabel(metric);
  if (ANGULAR_KEYS.has(key)) return 'deg';
  return null;
}

// Render a stored-mm value as a DISPLAY string in operator units. Rounded to 6
// decimal places (trailing zeros stripped) so an inch conversion shows "0.25"
// rather than "0.2500000000000001"; 6 decimals preserves sub-micron precision
// on a mm machine. This is display only — the exact mm value is preserved on
// save via the original-entry no-op path in validateForm.
export function mmToDisplay(mm: number, linearScale: number): string {
  const scaled = mm * linearScale;
  return String(Number(scaled.toFixed(6)));
}

// All editable fields are held as raw strings while editing; numbers are only
// parsed on save so partial input ("-", "1e") is never coerced behind the
// user's back. `updated` rides along unchanged (bigint) for the optimistic
// concurrency check on PUT.
export type ToolForm = Record<EditableKey, string> & { updated: bigint };

// `linearScale` converts stored mm -> display units for the LINEAR fields; it
// defaults to 1 (metric identity) so callers/tests that ignore units are
// unaffected.
export function toForm(t: ToolEntry, linearScale = 1): ToolForm {
  const f = {} as Record<string, string | bigint>;
  for (const field of fields) {
    const v = t[field.key];
    if (typeof v === 'string') {
      f[field.key] = v;
    } else if (LINEAR_KEYS.has(field.key)) {
      f[field.key] = mmToDisplay(v, linearScale);
    } else {
      f[field.key] = String(v);
    }
  }
  f.updated = t.updated;
  return f as ToolForm;
}

const NUM_RE = /^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/;

// Strict parser: rejects everything Number() silently accepts ('' -> 0,
// '0x1A', whitespace-only) and non-finite results; a single decimal comma is
// accepted when no dot is present.
export function parseNumField(raw: string): number | null {
  let s = raw.trim();
  if (!s.includes('.') && s.indexOf(',') === s.lastIndexOf(',')) {
    s = s.replace(',', '.');
  }
  if (!NUM_RE.test(s)) return null;
  const n = Number(s);
  return Number.isFinite(n) ? n : null;
}

export interface ValidateResult {
  entry: ToolEntry | null;
  errors: string[];
}

// `linearScale` (display-units-per-mm) and `original` (the mm entry the form was
// opened from) drive the display->mm conversion of the LINEAR fields. The
// no-op guarantee: if a linear field's string still equals what toForm produced
// from the original mm value, the original mm value is emitted verbatim — so an
// unedited save is an exact no-op and display rounding can never perturb a value
// (which would spuriously trip the 409 concurrency check). Only fields the
// operator actually changed are re-derived as typed_display / linearScale.
export function validateForm(
  form: ToolForm,
  isNew: boolean,
  existingToolnos: number[],
  linearScale = 1,
  original?: ToolEntry,
): ValidateResult {
  const e: string[] = [];
  const vals: Partial<Record<EditableKey, number | string>> = {};
  for (const field of fields) {
    const raw = form[field.key];
    if (field.type === 'text') {
      vals[field.key] = raw;
      continue;
    }
    const n = parseNumField(raw);
    if (n === null) {
      e.push(`${field.label} must be a number`);
    } else if (field.type === 'int' && !Number.isInteger(n)) {
      e.push(`${field.label} must be an integer`);
    } else if (LINEAR_KEYS.has(field.key)) {
      if (original !== undefined && raw === mmToDisplay(original[field.key] as number, linearScale)) {
        // untouched: emit the original mm exactly (no round-trip drift)
        vals[field.key] = original[field.key] as number;
      } else {
        vals[field.key] = n / linearScale;
      }
    } else {
      vals[field.key] = n;
    }
  }
  const num = (k: EditableKey) => vals[k] as number | undefined;
  const toolno = num('toolno');
  if (toolno !== undefined) {
    if (toolno <= 0) e.push('Tool number must be > 0');
    else if (isNew && existingToolnos.includes(toolno)) e.push(`Tool ${toolno} already exists`);
  }
  const pocketno = num('pocketno');
  if (pocketno !== undefined && (pocketno < 0 || pocketno > 1000)) e.push('Pocket must be 0–1000');
  const orientation = num('orientation');
  if (orientation !== undefined && (orientation < 0 || orientation > 9)) e.push('Orientation must be 0–9');
  const frontangle = num('frontangle');
  if (frontangle !== undefined && (frontangle < -360 || frontangle > 360)) e.push('Front angle must be -360..360');
  const backangle = num('backangle');
  if (backangle !== undefined && (backangle < -360 || backangle > 360)) e.push('Back angle must be -360..360');
  if ([...form.comment].length > 255) e.push('Comment must be at most 255 characters');
  // the concurrency stamp goes back on the entry verbatim — it is a bigint and
  // must never pass through the numeric field parsing above
  return {
    entry: e.length === 0 ? ({ ...vals, updated: form.updated } as unknown as ToolEntry) : null,
    errors: e,
  };
}
