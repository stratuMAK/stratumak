import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';

// The naming table normally arrives from GET /var_classes. Stub fetch so the
// store loads a known one, then check that programs are labelled the way
// ClassicLadder writes them — this is the logic that used to live in the ladder
// view with a hand-maintained copy of the prefixes, where an old-style timer was
// labelled %TM (the IEC prefix) and the IEC timer %TI (not a prefix at all).

const varClasses = [
  { prefix: 'B', suffix: '', varType: 0, count: 20, writable: true },
  { prefix: 'T', suffix: '.D', varType: 10, count: 5, writable: false },
  { prefix: 'T', suffix: '.R', varType: 11, count: 5, writable: false },
  { prefix: 'TM', suffix: '.Q', varType: 15, count: 10, writable: false },
  { prefix: 'M', suffix: '.R', varType: 20, count: 5, writable: false },
  { prefix: 'C', suffix: '.D', varType: 25, count: 10, writable: false },
  { prefix: 'C', suffix: '.E', varType: 26, count: 10, writable: false },
  { prefix: 'C', suffix: '.F', varType: 27, count: 10, writable: false },
  { prefix: 'X', suffix: '', varType: 30, count: 128, writable: false },
  { prefix: 'I', suffix: '', varType: 50, count: 10, writable: false },
  { prefix: 'Q', suffix: '', varType: 60, count: 10, writable: true },
  { prefix: 'W', suffix: '', varType: 200, count: 100, writable: true },
  { prefix: 'X', suffix: '.V', varType: 220, count: 128, writable: false },
  { prefix: 'IW', suffix: '', varType: 270, count: 10, writable: false },
];

const program = {
  sizes: {},
  rungs: [],
  sections: [{ used: true }],
  symbols: [],
  arithmExprs: [
    { expr: '@200/0@>5' },
    { expr: '@200/1@:=@270/2@+1' },
    { expr: '' },
    { expr: '@999/0@:=1' },
  ],
  timersIec: [],
  timers: [],
  monostables: [],
  counters: [],
};

const el = (type: number, varType = 0, varNum = 0) =>
  ({ type, connectedWithTop: 0, varType, varNum });

let store: typeof import('./ladder');

beforeEach(async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const body = String(url).includes('var_classes') ? varClasses : program;
    return {
      ok: true,
      status: 200,
      headers: { get: () => 'application/json' },
      json: async () => body,
      text: async () => JSON.stringify(body),
    } as unknown as Response;
  }));
  vi.resetModules();
  store = await import('./ladder');
  await store.ladderStore.fetchProgram();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('variable naming from the served table', () => {
  it('fetches the table alongside the program', () => {
    expect(store.ladderStore.state.error).toBe('');
    expect(store.ladderStore.state.varClasses.length).toBe(varClasses.length);
  });

  it('writes variables with their suffix', () => {
    expect(store.formatVar(50, 3)).toBe('%I3');
    expect(store.formatVar(60, 0)).toBe('%Q0');
    expect(store.formatVar(0, 7)).toBe('%B7');
    // The suffix is what tells three counter outputs apart; without it all
    // three read "%C0".
    expect(store.formatVar(25, 0)).toBe('%C0.D');
    expect(store.formatVar(26, 0)).toBe('%C0.E');
    expect(store.formatVar(27, 0)).toBe('%C0.F');
    expect(store.formatVar(220, 7)).toBe('%X7.V');
    expect(store.formatVar(30, 7)).toBe('%X7');
  });

  it('distinguishes the two timer kinds', () => {
    // An old-style timer is %T; an IEC timer is %TM. There is no %TI.
    expect(store.formatVar(10, 0)).toBe('%T0.D');
    expect(store.formatVar(11, 0)).toBe('%T0.R');
    expect(store.formatVar(15, 0)).toBe('%TM0.Q');
  });

  it('reports an unknown type rather than inventing a name', () => {
    expect(store.formatVar(9999, 0)).toBe('%?');
  });
});

describe('element labels', () => {
  it('names a block by its kind and number', () => {
    expect(store.elementLabel(el(10, 0, 2))).toBe('%T2'); // timer block
    expect(store.elementLabel(el(11, 0, 1))).toBe('%M1'); // monostable
    expect(store.elementLabel(el(12, 0, 3))).toBe('%C3'); // counter
    expect(store.elementLabel(el(13, 0, 4))).toBe('%TM4'); // IEC timer
  });

  it('names a contact and a coil by their variable', () => {
    expect(store.elementLabel(el(1, 50, 2))).toBe('%I2');
    expect(store.elementLabel(el(50, 60, 1))).toBe('%Q1');
  });

  it('shows the expression a compare or operate holds', () => {
    // Not "%B0" for expression 0, which is what running an expression index
    // through the variable table produces.
    expect(store.elementLabel(el(20, 0, 0))).toBe('%W0>5');
    expect(store.elementLabel(el(60, 0, 1))).toBe('%W1:=%IW2+1');
    expect(store.elementLabel(el(20, 0, 2))).toBe('');
  });

  it('names a jump target and a sub-routine call', () => {
    expect(store.elementLabel(el(54, 0, 7))).toBe('>7');
    expect(store.elementLabel(el(55, 0, 3))).toBe('SR3');
  });
});

describe('expression rendering', () => {
  it('converts every reference and leaves the rest alone', () => {
    expect(store.exprToNames('@200/0@>5')).toBe('%W0>5');
    expect(store.exprToNames('MINI(@200/0@,@200/1@)')).toBe('MINI(%W0,%W1)');
    expect(store.exprToNames('no refs here')).toBe('no refs here');
  });

  it('keeps an index on the variable it applies to', () => {
    expect(store.exprToNames('@200/0[200/1]@:=7')).toBe('%W0[%W1]:=7');
  });

  it('keeps a reference it cannot resolve rather than dropping it', () => {
    // An expression must never silently lose a term.
    const out = store.exprToNames('@999/0@:=@200/0@');
    expect(out).toContain('@999/0@');
    expect(out).toContain('%W0');
  });

  it('survives a malformed reference', () => {
    expect(store.exprToNames('@200/0')).toBe('@200/0');
    expect(store.exprToNames('@@')).toBe('@@');
  });
});
