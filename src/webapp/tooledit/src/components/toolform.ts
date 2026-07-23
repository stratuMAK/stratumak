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

// All editable fields are held as raw strings while editing; numbers are only
// parsed on save so partial input ("-", "1e") is never coerced behind the
// user's back. `updated` rides along unchanged (bigint) for the optimistic
// concurrency check on PUT.
export type ToolForm = Record<EditableKey, string> & { updated: bigint };

export function toForm(t: ToolEntry): ToolForm {
  const f = {} as Record<string, string | bigint>;
  for (const field of fields) {
    const v = t[field.key];
    f[field.key] = typeof v === 'string' ? v : String(v);
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

export function validateForm(form: ToolForm, isNew: boolean, existingToolnos: number[]): ValidateResult {
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
