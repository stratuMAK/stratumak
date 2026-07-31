// The chart editor's algebra — 2.9's edit_sequential.c, and what it builds.
//
// These drive the store the way an author drives the toolbar: pick a tool,
// click a cell. What matters is the four slot arrays per transition that come
// out of it, because that is the whole of what the engine runs — and getting
// one of them wrong produces a chart that scans happily and does something
// nobody drew.
//
// The last suite here authors a chart with every branch shape in it and writes
// it in the controller's own sequential.csv form, then checks it against the
// project the Go differential test runs through both engines. That is the loop
// the whole thing turns on: this file says which clicks produce which chart,
// and that test says the chart means the same to 2.9 as it does here.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { installFetchStub, emptySequential, type Stub } from './fixtures';

let sfc: typeof import('./sfc');
let ladder: typeof import('./ladder');
let stub: Stub;

beforeEach(async () => {
  stub = installFetchStub(undefined, emptySequential);
  vi.resetModules();
  sfc = await import('./sfc');
  ladder = await import('./ladder');
  await ladder.ladderStore.fetchProgram();
  stub.requests.length = 0;
  sfc.sfcStore.beginEdit(0);
});

afterEach(() => {
  sfc.sfcStore.cancelEdit();
  vi.unstubAllGlobals();
});

// click drives one gesture. `top` says the click landed in the upper half of
// the cell, which only the link tool reads.
function click(x: number, y: number, top = false) {
  sfc.sfcStore.clickCell(x, y, top);
}

function draft() {
  const d = sfc.sfcStore.state.draft;
  if (!d) throw new Error('no draft');
  return d;
}

function stepAt(x: number, y: number) {
  const i = draft().steps.findIndex(s => s.used && s.posiX === x && s.posiY === y);
  return { index: i, step: i >= 0 ? draft().steps[i] : null };
}

function transAt(x: number, y: number) {
  const i = draft().transitions.findIndex(t => t.used && t.posiX === x && t.posiY === y);
  return { index: i, trans: i >= 0 ? draft().transitions[i] : null };
}

// slots strips the -1 padding, which is what every assertion here cares about.
const slots = (a: number[]) => a.filter(v => v !== -1);

describe('placing steps and transitions', () => {
  it('numbers a new step from the one above it', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 1);
    click(1, 3);
    click(1, 5);
    expect(stepAt(1, 1).step?.stepNumber).toBe(1);
    expect(stepAt(1, 3).step?.stepNumber).toBe(2);
    expect(stepAt(1, 5).step?.stepNumber).toBe(3);
  });

  it('starts numbering at 1, not at 0', () => {
    // %X0 works here — this engine publishes only the steps that are placed,
    // where 2.9 republishes all 128 and lets the unused ones, all numbered 0,
    // overwrite it. A chart that used step 0 would still behave differently
    // under 2.9, so a number handed out by default never is 0.
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(4, 7);
    expect(stepAt(4, 7).step?.stepNumber).toBe(1);
  });

  it('joins a new step to the transitions above and below it', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 2);
    click(1, 4);
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 3);

    const { index } = stepAt(1, 3);
    expect(slots(transAt(1, 2).trans!.stepsToActivate)).toEqual([index]);
    expect(slots(transAt(1, 4).trans!.stepsToDeactivate)).toEqual([index]);
  });

  it('gives a new transition the next condition bit after the one above', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 2);
    click(1, 4);
    // %B0 by default, then one on from whatever sits two rows up.
    expect(transAt(1, 2).trans).toMatchObject({ varTypeCondi: 0, varNumCondi: 0 });
    expect(transAt(1, 4).trans).toMatchObject({ varTypeCondi: 0, varNumCondi: 1 });
  });

  it('refuses a step on an even row and a transition on an odd one', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 2);
    expect(sfc.sfcStore.state.error).toMatch(/even row/);
    expect(stepAt(1, 2).index).toBe(-1);

    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 3);
    expect(sfc.sfcStore.state.error).toMatch(/odd row/);
    expect(transAt(1, 3).index).toBe(-1);
  });

  it('places a step and the transition below it in one click', () => {
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_STEP_AND_TRANS);
    click(2, 3);
    expect(stepAt(2, 3).index).toBeGreaterThanOrEqual(0);
    expect(transAt(2, 4).index).toBeGreaterThanOrEqual(0);
    // The transition was joined to the step above it as it was created.
    expect(slots(transAt(2, 4).trans!.stepsToDeactivate)).toEqual([stepAt(2, 3).index]);
  });

  it('does not drop a second transition on a cell that already has one', () => {
    // 2.9 reuses the search from the step's cell for the transition's, so the
    // step-and-transition tool will happily stack two transitions in one cell —
    // a chart the controller refuses.
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(2, 4);
    const before = draft().transitions.filter(t => t.used).length;

    sfc.sfcStore.setTool(sfc.EDIT_SEQ_STEP_AND_TRANS);
    click(2, 3);
    expect(draft().transitions.filter(t => t.used).length).toBe(before);
  });

  it('removes what it placed when the same tool clicks it again', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 1);
    click(1, 1);
    expect(stepAt(1, 1).index).toBe(-1);
  });
});

describe('step numbers stay unique', () => {
  // Two steps sharing a number share their %X bit, and the controller refuses
  // the whole chart for it. 2.9 hands out the step above's number plus one
  // without checking, so drawing a second column and then continuing the first
  // produced two steps numbered 2 — a chart the editor could draw and then not
  // apply.
  it('does not reuse a number the column beside it already took', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 1);   // 1
    click(5, 1);   // a second column
    click(1, 3);   // continues the first column: 2.9 would say 2 again

    const numbers = draft().steps.filter(s => s.used).map(s => s.stepNumber);
    expect(new Set(numbers).size, `numbers were ${numbers.join(', ')}`).toBe(numbers.length);
  });

  it('still hands down the number above when that one is free', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 1);
    click(1, 3);
    click(1, 5);
    expect(draft().steps.filter(s => s.used).map(s => s.stepNumber)).toEqual([1, 2, 3]);
  });

  it('reuses a number freed by a delete rather than counting past it', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 1);
    click(1, 3);
    click(1, 5);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_ERASER);
    click(1, 3);                       // frees number 2
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(5, 1);                       // no step above it
    expect(stepAt(5, 1).step!.stepNumber).toBe(2);
  });
});

describe('deleting', () => {
  it('takes a deleted step out of the transitions that referred to it', () => {
    // 2.9 leaves them pointing at a slot that is no longer on the chart, which
    // its engine still evaluates — deactivating a step nobody can see. The
    // controller here refuses such a chart, so deleting a step in the middle of
    // a chain has to leave one that can still be applied.
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_STEP_AND_TRANS);
    click(1, 1);
    click(1, 3);
    const middle = stepAt(1, 3).index;

    sfc.sfcStore.setTool(sfc.EDIT_SEQ_ERASER);
    click(1, 3);

    expect(stepAt(1, 3).index).toBe(-1);
    for (const t of draft().transitions) {
      expect(t.stepsToActivate).not.toContain(middle);
      expect(t.stepsToDeactivate).not.toContain(middle);
    }
  });

  it('closes the gap rather than leaving a hole in a link array', () => {
    // The engine reads a link array up to its first empty slot, so a hole in
    // the middle hides every link after it.
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 3);
    click(3, 3);
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 2);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_START_MANY_STEPS);
    click(1, 3);
    click(3, 3);

    const right = stepAt(3, 3).index;
    const left = stepAt(1, 3).index;
    expect(slots(transAt(1, 2).trans!.stepsToActivate)).toEqual([left, right]);

    sfc.sfcStore.setTool(sfc.EDIT_SEQ_ERASER);
    click(1, 3);
    // The surviving branch is still the first thing the engine reads.
    expect(transAt(1, 2).trans!.stepsToActivate[0]).toBe(right);
  });
});

describe('branches', () => {
  // A transition with several steps below it starts parallel branches; one with
  // several steps above it waits for all of them.
  function twoStepsAndATransition(transitionRow: number) {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 3);
    click(3, 3);
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, transitionRow);
  }

  it('makes one transition activate every step across the span', () => {
    twoStepsAndATransition(2);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_START_MANY_STEPS);
    click(1, 3);
    click(3, 3);
    expect(slots(transAt(1, 2).trans!.stepsToActivate))
      .toEqual([stepAt(1, 3).index, stepAt(3, 3).index]);
  });

  it('makes one transition wait for every step across the span', () => {
    twoStepsAndATransition(4);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_END_MANY_STEPS);
    click(1, 3);
    click(3, 3);
    expect(slots(transAt(1, 4).trans!.stepsToDeactivate))
      .toEqual([stepAt(1, 3).index, stepAt(3, 3).index]);
  });

  it('picks up a step that sits between the two that were clicked', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 3);
    click(2, 3);
    click(3, 3);
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 2);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_START_MANY_STEPS);
    click(1, 3);
    click(3, 3);
    expect(slots(transAt(1, 2).trans!.stepsToActivate)).toHaveLength(3);
  });

  it('links alternative transitions to each other and to a shared step', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 5);
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 6);   // takes the step at (1,5)
    click(3, 6);   // takes nothing yet

    sfc.sfcStore.setTool(sfc.EDIT_SEQ_START_MANY_TRANS);
    click(1, 6);
    click(3, 6);

    const left = transAt(1, 6), right = transAt(3, 6);
    expect(slots(left.trans!.transLinkedForStart)).toEqual([right.index]);
    expect(slots(right.trans!.transLinkedForStart)).toEqual([left.index]);
    // The branch has one step on the far side of it, shared by both.
    expect(right.trans!.stepsToDeactivate[0]).toBe(stepAt(1, 5).index);
  });

  it('refuses a span that holds fewer than two transitions', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 6);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_START_MANY_TRANS);
    click(1, 6);
    click(1, 6);
    expect(sfc.sfcStore.state.error).toMatch(/fewer than two/);
  });

  it('refuses two elements that are not on the same row', () => {
    // 2.9 compares the second element's row against the first element's own,
    // so this refusal is unreachable there and the gesture silently builds a
    // branch across two rows.
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 3);
    click(3, 5);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_START_MANY_STEPS);
    click(1, 3);
    click(3, 5);
    expect(sfc.sfcStore.state.error).toMatch(/not on the same line/);
  });

  it('refuses steps for the transition-joining tools', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 3);
    click(3, 3);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_START_MANY_TRANS);
    click(1, 3);
    click(3, 3);
    expect(sfc.sfcStore.state.error).toMatch(/first and the last transition/);
  });
});

describe('the link tool', () => {
  it('points a transition at a step somewhere else on the page', () => {
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_STEP_AND_TRANS);
    click(1, 1);
    click(1, 3);

    // The bottom half of the last transition, then the step to return to.
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_LINK);
    click(1, 4, false);
    click(1, 1);
    expect(transAt(1, 4).trans!.stepsToActivate[0]).toBe(stepAt(1, 1).index);
  });

  it('reads the upper half of a transition as its top end', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(3, 1);
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 4);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_LINK);
    click(1, 4, true);
    click(3, 1);
    expect(transAt(1, 4).trans!.stepsToDeactivate[0]).toBe(stepAt(3, 1).index);
  });

  it('does not arm on a click that hits nothing', () => {
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_LINK);
    click(7, 7);
    expect(sfc.sfcStore.state.pending).toBeNull();
  });
});

describe('properties', () => {
  it('takes a condition written the way the chart shows it', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 2);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_POINTER);
    click(1, 2);
    expect(sfc.sfcStore.setTransitionCondition('%I3')).toBe(true);
    expect(transAt(1, 2).trans).toMatchObject({ varTypeCondi: 50, varNumCondi: 3 });
  });

  it('takes a symbol for a condition', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 2);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_POINTER);
    click(1, 2);
    // %Q0 is named estop-ok in the fixture program.
    expect(sfc.sfcStore.setTransitionCondition('estop-ok')).toBe(true);
    expect(transAt(1, 2).trans).toMatchObject({ varTypeCondi: 60, varNumCondi: 0 });
  });

  it('refuses a condition this PLC does not have', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
    click(1, 2);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_POINTER);
    click(1, 2);
    expect(sfc.sfcStore.setTransitionCondition('%I40')).toBe(false);
    expect(transAt(1, 2).trans).toMatchObject({ varTypeCondi: 0, varNumCondi: 0 });
  });

  it('refuses a step number outside the %X range', () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 1);
    sfc.sfcStore.setTool(sfc.EDIT_SEQ_POINTER);
    click(1, 1);
    expect(sfc.sfcStore.setStepNumber(999)).toBe(false);
    expect(stepAt(1, 1).step?.stepNumber).toBe(1);
  });
});

describe('applying', () => {
  it('sends the whole chart and nothing else', async () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 1);
    await sfc.sfcStore.applyEdit();

    const writes = stub.requests.filter(r => r.method !== 'GET');
    expect(writes).toHaveLength(1);
    expect(writes[0].method).toBe('PUT');
    expect(writes[0].url).toMatch(/sequential$/);
    const body = writes[0].body as { sequential: ReturnType<typeof emptySequential> };
    const sent = body.sequential ?? body;
    expect(sent.steps).toHaveLength(128);
    expect(sent.steps.filter(s => s.used)).toHaveLength(1);
    // The session closes on success, so the next Apply cannot resend it.
    expect(sfc.sfcStore.state.draft).toBeNull();
  });

  it('keeps the session open when the controller refuses', async () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 1);
    stub.failNext('sequential', 400, 'transition 3 activates step 9, which is not on the chart');
    expect(await sfc.sfcStore.applyEdit()).toBe(false);
    expect(sfc.sfcStore.state.draft).not.toBeNull();
    expect(sfc.sfcStore.state.error).toMatch(/not on the chart/);
  });

  it('writes nothing on cancel', async () => {
    sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
    click(1, 1);
    sfc.sfcStore.cancelEdit();
    expect(sfc.sfcStore.state.draft).toBeNull();
    expect(stub.requests.filter(r => r.method !== 'GET')).toHaveLength(0);
  });
});

// --- The chart the differential test runs ---

// authorBranchedChart draws, click for click, a chart holding every branch
// shape there is: a parallel divergence and convergence, an alternative
// divergence and convergence, and a return to the first step.
function authorBranchedChart() {
  // The spine: five steps down the left column, each with a transition below.
  sfc.sfcStore.setTool(sfc.EDIT_SEQ_STEP_AND_TRANS);
  for (const y of [1, 3, 5, 7, 9]) click(1, y);

  // The chart starts in the first of them.
  sfc.sfcStore.setTool(sfc.EDIT_SEQ_POINTER);
  click(1, 1);
  sfc.sfcStore.setStepInit(true);

  // The right-hand halves of the two branches.
  sfc.sfcStore.setTool(sfc.ELE_SEQ_STEP);
  click(3, 3);
  click(3, 7);
  sfc.sfcStore.setTool(sfc.ELE_SEQ_TRANSITION);
  click(3, 6);
  click(3, 8);

  // Parallel: one transition starts both branches, the next waits for both.
  sfc.sfcStore.setTool(sfc.EDIT_SEQ_START_MANY_STEPS);
  click(1, 3);
  click(3, 3);
  sfc.sfcStore.setTool(sfc.EDIT_SEQ_END_MANY_STEPS);
  click(1, 3);
  click(3, 3);

  // Alternative: two transitions take the same step, two give to the same one.
  sfc.sfcStore.setTool(sfc.EDIT_SEQ_START_MANY_TRANS);
  click(1, 6);
  click(3, 6);
  sfc.sfcStore.setTool(sfc.EDIT_SEQ_END_MANY_TRANS);
  click(1, 8);
  click(3, 8);

  // The return: the last transition gives back to the first step.
  sfc.sfcStore.setTool(sfc.EDIT_SEQ_LINK);
  click(1, 10, false);
  click(1, 1);

  // The two transitions of the right-hand column were numbered from their own
  // column, so they collide with the spine's. Give every transition a bit of
  // its own, so a script can fire them one at a time.
  sfc.sfcStore.setTool(sfc.EDIT_SEQ_POINTER);
  click(3, 6);
  sfc.sfcStore.setTransitionCondition('%B5');
  click(3, 8);
  sfc.sfcStore.setTransitionCondition('%B6');

  sfc.sfcStore.setTool(sfc.ELE_SEQ_COMMENT);
  click(5, 1);
  sfc.sfcStore.setCommentText('every branch shape there is');
}

// toSequentialCsv writes the chart the way the controller saves it
// (fileformat.go emitSequential), so the result can be compared against a
// project file rather than against a copy of these expectations.
function toSequentialCsv(chart: ReturnType<typeof emptySequential>): string {
  const lines = ['#VER=1.0'];
  chart.steps.forEach((s, i) => {
    if (!s.used) return;
    lines.push(`S${i},${s.initStep ? 1 : 0},${s.stepNumber},${s.page},${s.posiX},${s.posiY}`);
  });
  chart.transitions.forEach((t, i) => {
    if (!t.used) return;
    const all = [
      ...t.stepsToActivate, ...t.stepsToDeactivate,
      ...t.transLinkedForStart, ...t.transLinkedForEnd,
    ];
    lines.push(`T${i},${all.join(',')},${t.page},${t.posiX},${t.posiY}`);
  });
  chart.transitions.forEach((t, i) => {
    if (!t.used) return;
    lines.push(`C${i},0,${t.varTypeCondi}/${t.varNumCondi}`);
  });
  chart.comments.forEach((c, i) => {
    if (!c.used) return;
    lines.push(`N${i},${c.page},${c.posiX},${c.posiY},${c.comment}`);
  });
  return lines.join('\n') + '\n';
}

// sequentialCsvOf pulls the chart out of a .clp project, which is a set of
// files concatenated with markers between them.
function sequentialCsvOf(clp: string): string {
  const start = clp.indexOf('_FILE-sequential.csv\n');
  const end = clp.indexOf('_/FILE-sequential.csv');
  if (start < 0 || end < 0) throw new Error('no sequential.csv in the project');
  return clp.slice(start + '_FILE-sequential.csv\n'.length, end);
}

// Relative to the app directory, which is where vitest is run from. Not to this
// file: happy-dom serves the suite from an http URL, so import.meta.url is not
// a path.
const branchedProject = resolve(
  process.cwd(), '../../gomc/internal/classicladder/testdata/branched_sfc.clp');

describe('the chart the differential test runs', () => {
  it('is what these clicks produce', () => {
    authorBranchedChart();
    const project = readFileSync(branchedProject, 'utf8');
    expect(toSequentialCsv(draft())).toBe(sequentialCsvOf(project));
  });

  it('holds every branch shape', () => {
    authorBranchedChart();
    const spine = [1, 3, 5, 7, 9].map(y => stepAt(1, y).index);
    const parallel = stepAt(3, 3).index;

    // Parallel divergence and convergence.
    expect(slots(transAt(1, 2).trans!.stepsToActivate)).toEqual([spine[1], parallel]);
    expect(slots(transAt(1, 4).trans!.stepsToDeactivate)).toEqual([spine[1], parallel]);
    // Alternative divergence: both transitions take the step above the left one.
    expect(transAt(3, 6).trans!.stepsToDeactivate[0]).toBe(spine[2]);
    expect(slots(transAt(1, 6).trans!.transLinkedForStart)).toEqual([transAt(3, 6).index]);
    // Alternative convergence: both give to the step below the left one.
    expect(transAt(3, 8).trans!.stepsToActivate[0]).toBe(spine[4]);
    expect(slots(transAt(1, 8).trans!.transLinkedForEnd)).toEqual([transAt(3, 8).index]);
    // The return.
    expect(transAt(1, 10).trans!.stepsToActivate[0]).toBe(spine[0]);
    expect(draft().steps[spine[0]].initStep).toBe(true);

    // Every transition waits on a bit of its own, so a script can fire one at
    // a time — which is what makes the differential test able to walk the
    // chart rather than watch it settle.
    const conditions = draft().transitions.filter(t => t.used)
      .map(t => `${t.varTypeCondi}/${t.varNumCondi}`);
    expect(new Set(conditions).size).toBe(conditions.length);
  });
});
