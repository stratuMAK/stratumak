import { describe, it, expect } from 'vitest';
import type { ToolEntry } from '../generated/tools_client';
import { mmToDisplay, parseNumField, toForm, validateForm, type ToolForm } from './toolform';

const entry: ToolEntry = {
  toolno: 5, pocketno: 2,
  x_offset: 1, y_offset: 2, z_offset: 3.5,
  a_offset: 0, b_offset: 0, c_offset: 0,
  u_offset: 0, v_offset: 0, w_offset: 0,
  diameter: 6.35, frontangle: 0, backangle: 0,
  orientation: 0, comment: 'test tool',
  // 2^53+1: only survives if the stamp never touches Number parsing
  updated: 9007199254740993n,
};

function form(overrides: Partial<ToolForm> = {}): ToolForm {
  return { ...toForm(entry), ...overrides };
}

describe('parseNumField', () => {
  it('parses plain and scientific numbers', () => {
    expect(parseNumField('5')).toBe(5);
    expect(parseNumField('-3.25')).toBe(-3.25);
    expect(parseNumField('+.5')).toBe(0.5);
    expect(parseNumField('1e3')).toBe(1000);
    expect(parseNumField(' 3.5 ')).toBe(3.5);
  });

  it('accepts a single decimal comma when no dot is present', () => {
    expect(parseNumField('5,2')).toBe(5.2);
    expect(parseNumField('5,2,3')).toBeNull();
    expect(parseNumField('5.2,3')).toBeNull();
  });

  it('rejects what Number() would silently coerce', () => {
    expect(parseNumField('')).toBeNull();
    expect(parseNumField('   ')).toBeNull();
    expect(parseNumField('-')).toBeNull();
    expect(parseNumField('0x1A')).toBeNull();
    expect(parseNumField('Infinity')).toBeNull();
  });

  it('rejects trailing garbage and non-finite results', () => {
    expect(parseNumField('1.5abc')).toBeNull();
    expect(parseNumField('1e999')).toBeNull();
  });
});

describe('validateForm', () => {
  it('builds a fully numeric entry from a valid form', () => {
    const { entry: out, errors } = validateForm(form({ diameter: '5,2' }), false, []);
    expect(errors).toEqual([]);
    expect(out).toEqual({ ...entry, diameter: 5.2 });
  });

  it('flags empty numeric fields instead of coercing to 0', () => {
    const { entry: out, errors } = validateForm(form({ x_offset: '' }), false, []);
    expect(out).toBeNull();
    expect(errors).toContain('X Offset must be a number');
  });

  it('enforces integers for pocketno', () => {
    const { entry: out, errors } = validateForm(form({ pocketno: '2.5' }), false, []);
    expect(out).toBeNull();
    expect(errors).toContain('Pocket must be an integer');
  });

  it('refuses a duplicate toolno in Add mode only', () => {
    const dup = validateForm(form(), true, [5, 7]);
    expect(dup.entry).toBeNull();
    expect(dup.errors).toContain('Tool 5 already exists');
    const edit = validateForm(form(), false, [5, 7]);
    expect(edit.errors).toEqual([]);
  });

  it('checks ranges and comment length in code points', () => {
    expect(validateForm(form({ orientation: '10' }), false, []).errors)
      .toContain('Orientation must be 0–9');
    expect(validateForm(form({ toolno: '0' }), false, []).errors)
      .toContain('Tool number must be > 0');
    expect(validateForm(form({ comment: 'x'.repeat(256) }), false, []).errors)
      .toContain('Comment must be at most 255 characters');
    // 200 astral chars = 400 UTF-16 units but only 200 code points: allowed
    expect(validateForm(form({ comment: '\u{1F600}'.repeat(200) }), false, []).errors)
      .toEqual([]);
  });
});

describe('machine-units conversion (T-9)', () => {
  const INCH = 1 / 25.4; // display-units-per-mm on an inch machine

  // an entry with representative linear + angular + count values, in mm
  const mmEntry: ToolEntry = {
    toolno: 3, pocketno: 4,
    x_offset: 12.7, y_offset: 0, z_offset: 25.4,
    a_offset: 30, b_offset: 0, c_offset: 0,
    u_offset: 0, v_offset: 0, w_offset: 0,
    diameter: 6.35, frontangle: -45, backangle: 45,
    orientation: 2, comment: 'inch tool',
    updated: 11n,
  };

  it('inch: stored 25.4 mm z_offset displays as 1 in; diameter 6.35 -> 0.25', () => {
    const f = toForm(mmEntry, INCH);
    expect(f.z_offset).toBe('1');
    expect(f.x_offset).toBe('0.5');   // 12.7 mm
    expect(f.diameter).toBe('0.25');  // 6.35 mm
  });

  it('inch: angular and count fields are NOT scaled in the form', () => {
    const f = toForm(mmEntry, INCH);
    expect(f.a_offset).toBe('30');
    expect(f.frontangle).toBe('-45');
    expect(f.backangle).toBe('45');
    expect(f.toolno).toBe('3');
    expect(f.pocketno).toBe('4');
    expect(f.orientation).toBe('2');
  });

  it('inch: typing 2.0 in for z_offset saves 50.8 mm', () => {
    const f = { ...toForm(mmEntry, INCH), z_offset: '2.0' };
    const { entry, errors } = validateForm(f, false, [], INCH, mmEntry);
    expect(errors).toEqual([]);
    expect(entry!.z_offset).toBeCloseTo(50.8, 9);
  });

  it('inch: editing z leaves the other linear fields at their exact original mm', () => {
    const f = { ...toForm(mmEntry, INCH), z_offset: '2.0' };
    const { entry } = validateForm(f, false, [], INCH, mmEntry);
    // untouched linear fields come back byte-exact (original-entry no-op path)
    expect(entry!.x_offset).toBe(12.7);
    expect(entry!.diameter).toBe(6.35);
    // angular/count untouched by the scale
    expect(entry!.a_offset).toBe(30);
    expect(entry!.frontangle).toBe(-45);
    expect(entry!.toolno).toBe(3);
    expect(entry!.pocketno).toBe(4);
  });

  it('mm machine (scale 1) is an identity for both directions', () => {
    const f = toForm(mmEntry, 1);
    expect(f.z_offset).toBe('25.4');
    expect(f.diameter).toBe('6.35');
    const { entry, errors } = validateForm(f, false, [], 1, mmEntry);
    expect(errors).toEqual([]);
    expect(entry).toEqual(mmEntry);
  });

  it('round-trip is an exact no-op: unedited save returns the original mm within 1e-9', () => {
    // a value whose inch display rounds and would NOT survive a naive /scale
    const tricky: ToolEntry = { ...mmEntry, z_offset: 3.5, x_offset: 1.234567, diameter: 7.9 };
    const f = toForm(tricky, INCH);
    const { entry, errors } = validateForm(f, false, [], INCH, tricky);
    expect(errors).toEqual([]);
    for (const k of ['x_offset', 'y_offset', 'z_offset', 'u_offset', 'v_offset', 'w_offset', 'diameter'] as const) {
      expect(Math.abs((entry![k] as number) - (tricky[k] as number))).toBeLessThan(1e-9);
    }
    // and genuinely exact for these
    expect(entry!.z_offset).toBe(3.5);
    expect(entry!.x_offset).toBe(1.234567);
  });

  it('mmToDisplay rounds inch display without a naive-divide corruption path', () => {
    // 3.5 mm in inch = 0.13779527... -> rounded display
    expect(mmToDisplay(3.5, INCH)).toBe('0.137795');
    expect(mmToDisplay(25.4, INCH)).toBe('1');
    expect(mmToDisplay(0, INCH)).toBe('0');
    expect(mmToDisplay(3.5, 1)).toBe('3.5');
  });
});

describe('updated stamp round-trip', () => {
  it('toForm carries the stamp through as a bigint, not a string field', () => {
    const f = toForm(entry);
    expect(typeof f.updated).toBe('bigint');
    expect(f.updated).toBe(9007199254740993n);
  });

  it('validateForm puts the stamp back verbatim, bypassing numeric parsing', () => {
    const { entry: out, errors } = validateForm(form(), false, []);
    expect(errors).toEqual([]);
    expect(typeof out!.updated).toBe('bigint');
    // exact value beyond Number precision: parseNumField/Number() would have
    // rounded 2^53+1 to 2^53
    expect(out!.updated).toBe(9007199254740993n);
  });

  it('preserves a 0n create stamp for Add mode', () => {
    const { entry: out, errors } = validateForm(
      { ...toForm({ ...entry, updated: 0n }), toolno: '9' }, true, [5]);
    expect(errors).toEqual([]);
    expect(out!.updated).toBe(0n);
  });
});
