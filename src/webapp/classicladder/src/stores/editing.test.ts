// The editing half of the store: reading names an operator typed, keeping a
// rung edit on a copy, and what actually goes over the wire.
//
// This is the part that decides whether a program is right or subtly wrong, and
// it had no coverage — the app could place a contact but not say what it read,
// so there was nothing to test.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFetchStub, makeProgram, type Stub } from './fixtures';

let store: typeof import('./ladder');
let stub: Stub;

beforeEach(async () => {
  stub = installFetchStub();
  vi.resetModules();
  store = await import('./ladder');
  await store.ladderStore.fetchProgram();
  stub.requests.length = 0; // the load is not what these tests are about
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('reading a variable name', () => {
  it('accepts what formatVar writes', () => {
    // Round-tripping is the property that matters: whatever the display shows,
    // the editor must take back.
    for (const [varType, offset] of [[50, 3], [60, 0], [0, 7], [200, 12], [270, 1]] as const) {
      const text = store.formatVar(varType, offset);
      expect(store.parseVar(text), text).toEqual({ varType, offset });
    }
  });

  it('matches the longest written form', () => {
    // "%X0.V" is a step time, not a step activity with stray characters.
    expect(store.parseVar('%X0.V')).toEqual({ varType: 220, offset: 0 });
    expect(store.parseVar('%X0')).toEqual({ varType: 30, offset: 0 });
    // "%IW0" is a word input, not "%I0" with a leftover W.
    expect(store.parseVar('%IW0')).toEqual({ varType: 270, offset: 0 });
    expect(store.parseVar('%I0')).toEqual({ varType: 50, offset: 0 });
    // "%TM0.Q" is an IEC timer, not an old-style one.
    expect(store.parseVar('%TM0.Q')).toEqual({ varType: 15, offset: 0 });
    expect(store.parseVar('%T0.D')).toEqual({ varType: 10, offset: 0 });
  });

  it('refuses an offset the PLC was not configured with', () => {
    // %I is sized 10 here. Accepting %I40 would store a contact that reads a
    // variable the engine bounds-checks away — a rung that silently never fires.
    expect(store.parseVar('%I40')).toBeNull();
    expect(store.parseVar('%I9')).toEqual({ varType: 50, offset: 9 });
  });

  it('refuses nonsense rather than guessing', () => {
    expect(store.parseVar('')).toBeNull();
    expect(store.parseVar('%')).toBeNull();
    expect(store.parseVar('%I')).toBeNull();
    expect(store.parseVar('%ZZ0')).toBeNull();
    expect(store.parseVar('%I0x')).toBeNull();
    expect(store.parseVar('%T0')).toBeNull(); // a timer needs a suffix
  });

  it('accepts a symbol, because that is what someone who named their signals types', () => {
    expect(store.parseVar('estop-ok')).toEqual({ varType: 60, offset: 0 });
    expect(store.parseVar('no-such-symbol')).toBeNull();
  });

  it('reports which regions a coil may drive', () => {
    expect(store.varIsWritable(60)).toBe(true);  // %Q
    expect(store.varIsWritable(50)).toBe(false); // %I is an input
    expect(store.varIsWritable(10)).toBe(false); // %T.D belongs to the timer
  });
});

describe('the rung edit session', () => {
  it('edits a copy, so cancelling changes nothing', () => {
    const state = store.ladderStore.state;
    store.ladderStore.beginEdit(0);
    store.ladderStore.setEditTool(1); // a contact
    store.ladderStore.selectCell(0, 0, 0);

    expect(state.draft!.elements[0].type).toBe(1);
    // The program the controller is running has not moved.
    expect(state.program!.rungs[0].elements[0].type).toBe(0);

    store.ladderStore.cancelEdit();
    expect(state.draft).toBeNull();
    expect(state.program!.rungs[0].elements[0].type).toBe(0);
    expect(stub.requests.filter(r => r.method !== 'GET')).toHaveLength(0);
  });

  it('ignores clicks on a rung that is not the one being edited', () => {
    const state = store.ladderStore.state;
    store.ladderStore.beginEdit(0);
    store.ladderStore.setEditTool(1);
    store.ladderStore.selectCell(1, 0, 0);
    expect(state.draft!.elements[0].type).toBe(0);
    expect(state.selectedCell).toBeNull();
  });

  it('sends the rung on apply', async () => {
    store.ladderStore.beginEdit(0);
    store.ladderStore.setEditTool(1);
    store.ladderStore.selectCell(0, 0, 0);
    const el = store.selectedElement()!;
    el.varType = 50;
    el.varNum = 3;

    await store.ladderStore.applyEdit();

    const put = stub.requests.find(r => r.method === 'PUT' && r.url.includes('/rung/0'));
    expect(put, 'no PUT /rung/0 was sent').toBeTruthy();
    const sent = put!.body as { rung: { elements: { type: number; varType: number; varNum: number }[] } };
    expect(sent.rung.elements[0]).toMatchObject({ type: 1, varType: 50, varNum: 3 });
    expect(store.ladderStore.state.editRung).toBe(-1);
  });

  it('keeps the session open when the controller refuses', async () => {
    // The operator's work is in the draft. Throwing it away because of a typo
    // would be the worst possible response to one.
    stub.failNext('/rung/0', 400, 'rung 0 (2,0): coil drives %I3, which is read-only');
    store.ladderStore.beginEdit(0);
    store.ladderStore.setEditTool(50);
    store.ladderStore.selectCell(0, 0, 2);

    const ok = await store.ladderStore.applyEdit();

    expect(ok).toBe(false);
    expect(store.ladderStore.state.editRung).toBe(0);
    expect(store.ladderStore.state.draft!.elements[2].type).toBe(50);
    expect(store.ladderStore.state.error).toContain('read-only');
  });

  it('places a block with its body cells, and removes it whole', () => {
    const state = store.ladderStore.state;
    store.ladderStore.beginEdit(0);
    store.ladderStore.setEditTool(10); // timer: 2 wide, 2 high
    store.ladderStore.selectCell(0, 0, 2); // click marks the top-left

    const at = (row: number, col: number) => state.draft!.elements[row * 10 + col];
    expect(at(0, 3).type, 'head goes at the right column').toBe(10);
    expect(at(0, 2).type).toBe(99);
    expect(at(1, 2).type).toBe(99);
    expect(at(1, 3).type).toBe(99);

    // Deleting through a body cell must take the head with it, or the rung is
    // left with an orphaned body cell the controller refuses.
    store.ladderStore.setEditTool(0);
    store.ladderStore.selectCell(0, 1, 2);
    for (const [r, c] of [[0, 2], [0, 3], [1, 2], [1, 3]] as const) {
      expect(at(r, c).type, `cell (${r},${c})`).toBe(0);
    }
  });

  it('refuses to place a block that would run off the grid', () => {
    const state = store.ladderStore.state;
    store.ladderStore.beginEdit(0);
    store.ladderStore.setEditTool(12); // counter: 2 wide, 4 high
    store.ladderStore.selectCell(0, 4, 0); // rows 4..7, but there are only 6

    expect(state.draft!.elements[4 * 10].type).toBe(0);
    expect(state.error).toContain('does not fit');
  });
});

describe('expressions in an edit session', () => {
  it('gives a new compare an expression nothing else uses', () => {
    const state = store.ladderStore.state;
    // Expressions 0,1,3 hold text; 2 and 4 are free.
    store.ladderStore.beginEdit(0);
    store.ladderStore.setEditTool(20); // compare
    store.ladderStore.selectCell(0, 0, 0);
    const first = state.draft!.elements[2].varNum;
    expect([2, 4]).toContain(first);

    store.ladderStore.selectCell(0, 2, 0);
    const second = state.draft!.elements[2 * 10 + 2].varNum;
    expect(second, 'two compares must not share one expression').not.toBe(first);
  });

  it('stages the expression and writes it before the rung', async () => {
    store.ladderStore.beginEdit(0);
    store.ladderStore.setEditTool(60); // operate
    store.ladderStore.selectCell(0, 0, 0);
    const exprNum = store.ladderStore.state.draft!.elements[2].varNum;

    store.ladderStore.setDraftExpression(exprNum, '%W3:=%W0+1');
    // Nothing has gone to the controller yet — 2.9 applies expressions with the
    // rung, so Cancel undoes what was typed.
    expect(stub.requests.filter(r => r.method !== 'GET')).toHaveLength(0);
    // ...but the rung shows it, so the operator sees what they typed.
    expect(store.elementLabel(store.ladderStore.state.draft!.elements[2])).toBe('%W3:=%W0+1');

    await store.ladderStore.applyEdit();

    const writes = stub.requests.filter(r => r.method === 'PUT');
    const exprIdx = writes.findIndex(r => r.url.includes(`/expression/${exprNum}/text`));
    const rungIdx = writes.findIndex(r => r.url.includes('/rung/0'));
    expect(exprIdx, 'the expression was not written').toBeGreaterThanOrEqual(0);
    expect(rungIdx, 'the rung was not written').toBeGreaterThanOrEqual(0);
    expect(exprIdx, 'the expression must land before the rung').toBeLessThan(rungIdx);
    expect((writes[exprIdx].body as { text: string }).text).toBe('%W3:=%W0+1');
  });

  it('does not write a staged expression that was cancelled', () => {
    store.ladderStore.beginEdit(0);
    store.ladderStore.setEditTool(20);
    store.ladderStore.selectCell(0, 0, 0);
    store.ladderStore.setDraftExpression(2, '%W0>9');
    store.ladderStore.cancelEdit();

    expect(store.ladderStore.state.draftExprs).toEqual({});
    expect(store.expressionText(2)).toBe('');
  });
});

describe('the rung chain', () => {
  it('walks a section in order and stops at its last rung', () => {
    expect(store.sectionRungs(0).map(r => r.index)).toEqual([0, 1]);
  });

  it('reports no rungs for a sequential section', () => {
    // A chart has no rungs to draw; the ladder view says so rather than walking
    // firstRung, which is meaningless there.
    expect(store.sectionRungs(1)).toEqual([]);
  });

  it('does not loop forever on a chain that points at itself', () => {
    // The controller owns and validates the chain, but a program loaded from a
    // file it did not write should not hang the browser.
    const looped = makeProgram();
    looped.rungs[0].nextRung = 1;
    looped.rungs[1].nextRung = 0;
    looped.sections[0].lastRung = 3;
    vi.unstubAllGlobals();
    installFetchStub(() => looped);
    return store.ladderStore.fetchProgram().then(() => {
      expect(store.sectionRungs(0).map(r => r.index)).toEqual([0, 1]);
    });
  });
});

describe('structural edits go to the controller', () => {
  it('inserts a rung rather than writing the links itself', async () => {
    await store.ladderStore.insertRungAfter(1);
    const post = stub.requests.find(r => r.method === 'POST' && r.url.includes('/rung/insert'));
    expect(post).toBeTruthy();
    expect(post!.body).toEqual({ afterIndex: 1 });
    // And it re-reads: the controller decided which slot to use.
    expect(stub.requests.some(r => r.method === 'GET' && r.url.endsWith('/program'))).toBe(true);
  });

  it('deletes a rung through the controller', async () => {
    await store.ladderStore.deleteRung(1);
    expect(stub.requests.some(r => r.method === 'DELETE' && r.url.includes('/rung/1'))).toBe(true);
  });

  it('drops an open edit session before a structural change', async () => {
    store.ladderStore.beginEdit(0);
    await store.ladderStore.insertRungAfter(0);
    // insert_rung takes effect immediately, so an unsaved draft of a rung whose
    // neighbours just moved is worse than no draft at all.
    expect(store.ladderStore.state.editRung).toBe(-1);
  });
});

describe('block parameters', () => {
  it('shows the preset while editing and the value while running', async () => {
    const live = await import('./live');
    const timer = { type: 10, connectedWithTop: 0, varType: 0, varNum: 1 };
    // Nothing live yet: the preset, from the program.
    expect(store.blockValueText(timer, false)).toBe('9');
    expect(store.blockValueText(timer, true)).toBe('9');

    live.liveStore.state.variables = {
      bools: {
        bits: [], physInputs: [], physOutputs: [], errorBits: [], stepActivity: [],
        timerDone: [], timerRunning: [], monostableRunning: [],
        counterDone: [], counterEmpty: [], counterFull: [], timerIecDone: [],
      },
      words: {
        words: [], physWordInputs: [], physWordOutputs: [], stepTimes: [],
        timerValues: [0, 4], monostableValues: [], counterValues: [], timerIecValues: [],
      },
      floats: { physFloatInputs: [], physFloatOutputs: [] },
    };
    expect(store.blockValueText(timer, false)).toBe('4');
    // While the rung is being edited it is the preset again — an edited rung is
    // not the rung the engine is scanning.
    expect(store.blockValueText(timer, true)).toBe('9');
  });

  it('writes one block, in the units the API counts in', async () => {
    await store.ladderStore.setTimer(1, 12, 2);
    const put = stub.requests.find(r => r.method === 'PUT' && r.url.includes('/timer/1'));
    expect(put).toBeTruthy();
    expect(put!.body).toEqual({ preset: 12, base: 2 });
  });
});
