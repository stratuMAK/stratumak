import { describe, it, expect } from 'vitest';
import type { ToolEntry } from '../generated/tools_client';
import { parseNumField, toForm, validateForm, type ToolForm } from './toolform';

const entry: ToolEntry = {
  toolno: 5, pocketno: 2,
  x_offset: 1, y_offset: 2, z_offset: 3.5,
  a_offset: 0, b_offset: 0, c_offset: 0,
  u_offset: 0, v_offset: 0, w_offset: 0,
  diameter: 6.35, frontangle: 0, backangle: 0,
  orientation: 0, comment: 'test tool',
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
