// Editing a sequential (SFC) chart — 2.9's edit_sequential.c.
//
// The chart is edited on a copy and written whole: a step is named by its slot
// everywhere a transition refers to it, so placing one element can rewrite the
// links of several others, and there is no smaller unit that leaves the chart
// consistent. Apply sends the copy with set_sequential, Cancel throws it away.
// That is 2.9's shape, and unlike rung editing there is nothing structural
// happening behind it — no insert or delete lands on the controller
// separately — so the copy cannot be clobbered while it is open.
//
// 2.9's own comment on this file is that it is "the part of the editor that
// will not change even if we use another gui" — it is geometry and slot
// bookkeeping, and it is ported here as such.
import { reactive } from 'vue';
import {
  ClassicladderClient,
  MAX_STEPS,
  MAX_TRANSITIONS,
  MAX_SEQ_COMMENTS,
  MAX_SWITCHS,
  SEQ_PAGE_WIDTH,
  SEQ_PAGE_HEIGHT,
  VAR_MEM_BIT,
  type Sequential,
  type Step,
  type Transition,
  type SeqComment,
} from '../generated/classicladder_client';
import { ladderStore, parseVar, registerEditCancelHook } from './ladder';

// The tool palette, numbered as sequential.h numbers it. The first three are
// also the element types an author can click on, which is why the two share a
// numbering: a search reports what it found in the same terms.
export const ELE_SEQ_STEP = 1;
export const ELE_SEQ_TRANSITION = 2;
export const ELE_SEQ_COMMENT = 3;
export const EDIT_SEQ_INIT_STEP = 20;
export const EDIT_SEQ_STEP_AND_TRANS = 21;
export const EDIT_SEQ_START_MANY_STEPS = 22;
export const EDIT_SEQ_END_MANY_STEPS = 23;
export const EDIT_SEQ_START_MANY_TRANS = 24;
export const EDIT_SEQ_END_MANY_TRANS = 25;
export const EDIT_SEQ_LINK = 26;
// The two tools that place nothing. Negative, so they cannot collide with an
// element type.
export const EDIT_SEQ_POINTER = -1;
export const EDIT_SEQ_ERASER = -2;

export interface SeqSelection {
  type: number;    // ELE_SEQ_*
  offset: number;  // slot in the chart's array
}

interface PendingClick extends SeqSelection {
  top: boolean;    // the click was in the upper half of the cell
}

export interface SfcStoreState {
  // The chart being edited, or null when the view is only a view. Editing a
  // chart edits every page of it, because a link may cross pages even though
  // nothing here draws one that does.
  draft: Sequential | null;
  page: number;
  tool: number;
  selected: SeqSelection | null;
  // The first click of a two-click gesture — link, and the four branch tools.
  pending: PendingClick | null;
  error: string;
  busy: boolean;
}

const client = new ClassicladderClient(window.location.origin);

const state = reactive<SfcStoreState>({
  draft: null,
  page: 0,
  tool: EDIT_SEQ_POINTER,
  selected: null,
  pending: null,
  error: '',
  busy: false,
});

// --- The edit session ---

function beginEdit(page: number): boolean {
  const chart = ladderStore.state.sequential;
  if (!chart) return false;
  // A JSON round-trip rather than structuredClone, which refuses a Vue
  // reactive proxy outright — see the rung draft.
  state.draft = JSON.parse(JSON.stringify(chart)) as Sequential;
  state.page = page;
  state.tool = EDIT_SEQ_POINTER;
  state.selected = null;
  state.pending = null;
  state.error = '';
  return true;
}

function cancelEdit() {
  state.draft = null;
  state.selected = null;
  state.pending = null;
  state.tool = EDIT_SEQ_POINTER;
  state.error = '';
}

// The structural and project operations of the ladder store — load_project,
// section add and delete, rung insert and delete — replace the program this
// draft was copied from. The editor survives tab switches, so a draft kept
// across a Load would reopen over the freshly loaded chart and Apply would
// overwrite it with the old one.
registerEditCancelHook(cancelEdit);

// applyEdit writes the chart. A refusal keeps the session open with the reason
// shown: the operator's work is in the draft, and throwing it away because the
// controller said no would be the worst possible answer to a mistake.
async function applyEdit(): Promise<boolean> {
  if (!state.draft) return false;
  state.busy = true;
  state.error = '';
  try {
    await client.setSequential(state.draft);
    cancelEdit();
    await ladderStore.fetchProgram();
    return true;
  } catch (e: unknown) {
    state.error = e instanceof Error ? e.message : String(e);
    return false;
  } finally {
    state.busy = false;
  }
}

function setTool(tool: number) {
  // A gesture half-made with one tool means nothing to the next one.
  if (tool !== state.tool) state.pending = null;
  state.tool = tool;
}

// --- Searching the page ---
//
// Everything the editor does starts by asking what is at a cell. 2.9 scans the
// whole array and keeps the last match; that only matters for a chart that has
// two elements in one cell, which this editor does not make and the controller
// refuses, but the rule is kept so both agree on such a chart.

function searchStep(page: number, x: number, y: number): number {
  const steps = state.draft?.steps ?? [];
  let found = -1;
  for (let i = 0; i < steps.length; i++) {
    const s = steps[i];
    if (s.used && s.page === page && s.posiX === x && s.posiY === y) found = i;
  }
  return found;
}

function searchTransition(page: number, x: number, y: number): number {
  const transitions = state.draft?.transitions ?? [];
  let found = -1;
  for (let i = 0; i < transitions.length; i++) {
    const t = transitions[i];
    if (t.used && t.page === page && t.posiX === x && t.posiY === y) found = i;
  }
  return found;
}

// A comment is four cells wide, so it answers for all four.
function searchComment(page: number, x: number, y: number): number {
  const comments = state.draft?.comments ?? [];
  let found = -1;
  for (let i = 0; i < comments.length; i++) {
    const c = comments[i];
    if (c.used && c.page === page && c.posiY === y && c.posiX <= x && x < c.posiX + 4) found = i;
  }
  return found;
}

// searchIt reports what is at a cell. Steps sit on odd rows and transitions on
// even ones, so which row was clicked already says which of the two to look
// for; a comment can be anywhere, so it is only looked for when neither
// answered.
export function searchIt(page: number, x: number, y: number): SeqSelection | null {
  if (y % 2 !== 0) {
    const off = searchStep(page, x, y);
    if (off !== -1) return { type: ELE_SEQ_STEP, offset: off };
  } else {
    const off = searchTransition(page, x, y);
    if (off !== -1) return { type: ELE_SEQ_TRANSITION, offset: off };
  }
  const off = searchComment(page, x, y);
  if (off !== -1) return { type: ELE_SEQ_COMMENT, offset: off };
  return null;
}

function findFree<T extends { used: boolean }>(arr: T[] | undefined, limit: number): number {
  if (!arr) return -1;
  for (let i = 0; i < arr.length && i < limit; i++) {
    if (!arr[i].used) return i;
  }
  return -1;
}

// freeStepNumber gives a new step `preferred` when nothing else holds it, and
// otherwise the lowest number that is free.
//
// 2.9 hands out the step above's number plus one without looking, and its
// fallback scan only advances while it keeps meeting the number it is on — so
// with numbers out of slot order both paths can return one that is taken. That
// is harmless there and refused here: two steps sharing a number share their
// %X bit, so the controller rejects the whole chart. Drawing a second column
// and then continuing the first was enough to produce one.
function freeStepNumber(steps: Step[], exceptOffset: number, preferred: number): number {
  const taken = new Set<number>();
  steps.forEach((s, i) => {
    if (s.used && i !== exceptOffset) taken.add(s.stepNumber);
  });
  if (preferred >= 1 && preferred < MAX_STEPS && !taken.has(preferred)) return preferred;
  for (let n = 1; n < MAX_STEPS; n++) {
    if (!taken.has(n)) return n;
  }
  return -1;
}

// --- Creating and destroying ---

// createStep places a step, numbers it, and joins it to the transitions
// immediately above and below. Both conveniences are 2.9's: an author drawing a
// chain downwards gets a numbered, connected chart without touching either.
function createStep(page: number, x: number, y: number, init: boolean): number {
  const draft = state.draft;
  if (!draft) return -1;
  const offset = findFree(draft.steps, MAX_STEPS);
  if (offset === -1) return -1;

  const step = draft.steps[offset];

  // A step directly above hands down its number plus one; otherwise start at 1.
  // Either way the number has to be one nothing else holds — see
  // freeStepNumber.
  //
  // Numbering starts at 1, not at 0 as 2.9 does. %X0 works here — this engine
  // publishes only the steps that are placed, where 2.9 republishes all 128 and
  // lets the unused ones, all numbered 0, overwrite it — but a chart that uses
  // step 0 behaves differently under 2.9, and a number handed out by default is
  // not the place to introduce that.
  const topStep = searchStep(page, x, y - 2);
  const preferred = topStep !== -1 ? draft.steps[topStep].stepNumber + 1 : 1;
  const number = freeStepNumber(draft.steps, offset, preferred);
  if (number === -1) {
    state.error = 'Every step number is taken; renumber a step before adding another.';
    return -1;
  }

  step.used = true;
  step.page = page;
  step.posiX = x;
  step.posiY = y;
  step.initStep = init;
  step.stepNumber = number;

  const above = searchTransition(page, x, y - 1);
  if (above !== -1) draft.transitions[above].stepsToActivate[0] = offset;
  const below = searchTransition(page, x, y + 1);
  if (below !== -1) draft.transitions[below].stepsToDeactivate[0] = offset;
  return offset;
}

function createTransition(page: number, x: number, y: number): number {
  const draft = state.draft;
  if (!draft) return -1;
  const offset = findFree(draft.transitions, MAX_TRANSITIONS);
  if (offset === -1) return -1;

  const trans = draft.transitions[offset];
  trans.used = true;
  trans.page = page;
  trans.posiX = x;
  trans.posiY = y;
  trans.varTypeCondi = VAR_MEM_BIT;
  trans.varNumCondi = 0;
  trans.stepsToActivate = emptySlots();
  trans.stepsToDeactivate = emptySlots();
  trans.transLinkedForStart = emptySlots();
  trans.transLinkedForEnd = emptySlots();

  // A transition two rows up hands down its condition variable, one offset on.
  // Chart conditions are usually a run of consecutive bits, and the author who
  // wanted otherwise types over it.
  const topTrans = searchTransition(page, x, y - 2);
  if (topTrans !== -1) {
    trans.varTypeCondi = draft.transitions[topTrans].varTypeCondi;
    trans.varNumCondi = draft.transitions[topTrans].varNumCondi + 1;
  }

  if (y > 0) {
    const above = searchStep(page, x, y - 1);
    if (above !== -1) trans.stepsToDeactivate[0] = above;
  }
  if (y < SEQ_PAGE_HEIGHT - 1) {
    const below = searchStep(page, x, y + 1);
    if (below !== -1) trans.stepsToActivate[0] = below;
  }
  return offset;
}

function createComment(page: number, x: number, y: number): number {
  const draft = state.draft;
  if (!draft) return -1;
  const offset = findFree(draft.comments, MAX_SEQ_COMMENTS);
  if (offset === -1) return -1;
  const comment = draft.comments[offset];
  comment.used = true;
  comment.page = page;
  comment.posiX = x;
  comment.posiY = y;
  comment.comment = '';
  return offset;
}

function emptySlots(): number[] {
  return new Array(MAX_SWITCHS).fill(-1);
}

// dropRef removes one slot number from a link array and closes the gap. The
// engine reads such an array up to its first empty slot, so a hole in the
// middle would hide every link after it.
function dropRef(slots: number[], ref: number) {
  const kept = slots.filter(v => v !== -1 && v !== ref);
  for (let i = 0; i < slots.length; i++) {
    slots[i] = i < kept.length ? kept[i] : -1;
  }
}

// destroyStep takes a step off the chart *and* out of every transition that
// referred to it.
//
// 2.9 only removes the step, leaving transitions pointing at a slot that is no
// longer on the chart — which its engine then still evaluates, deactivating a
// step nobody can see. The controller here refuses such a chart, so leaving the
// references behind would make deleting a step in the middle of a chain produce
// something that cannot be applied until the author deletes the transitions too.
function destroyStep(offset: number) {
  const draft = state.draft;
  if (!draft) return;
  draft.steps[offset].used = false;
  for (const t of draft.transitions) {
    if (!t.used) continue;
    dropRef(t.stepsToActivate, offset);
    dropRef(t.stepsToDeactivate, offset);
  }
}

function destroyTransition(offset: number) {
  const draft = state.draft;
  if (!draft) return;
  draft.transitions[offset].used = false;
  for (const t of draft.transitions) {
    if (!t.used) continue;
    dropRef(t.transLinkedForStart, offset);
    dropRef(t.transLinkedForEnd, offset);
  }
}

function destroyComment(offset: number) {
  if (state.draft) state.draft.comments[offset].used = false;
}

function destroyIt(sel: SeqSelection) {
  switch (sel.type) {
    case ELE_SEQ_STEP: destroyStep(sel.offset); break;
    case ELE_SEQ_TRANSITION: destroyTransition(sel.offset); break;
    case ELE_SEQ_COMMENT: destroyComment(sel.offset); break;
  }
}

// --- Linking ---

// doLinkTransitionAndStep points one end of a transition at a step: its top
// takes the step, its bottom gives to it. This is what makes a chart return to
// its beginning — the link the drawing shows as a chevron and a step number,
// because a line to a step several rows away would run across everything
// between.
function doLinkTransitionAndStep(transOffset: number, topOfTransition: boolean, stepOffset: number) {
  const trans = state.draft?.transitions[transOffset];
  if (!trans) return;
  if (topOfTransition) trans.stepsToDeactivate[0] = stepOffset;
  else trans.stepsToActivate[0] = stepOffset;
}

interface SpanSearch {
  transition: number;   // the transition one of the two clicks landed on, or -1
  stepsRow: number;     // the row the clicked steps share, or -1
  transitionsRow: number;
  leftX: number;
  rightX: number;
}

// commonSearchForSpan works out what two clicks selected: the row they are on
// and the columns they span. Everything between those columns on that row joins
// the branch — that is how one gesture rewires several elements at once.
//
// 2.9's version has two checks that cannot fire: it compares the second
// element's row against the *first* element's row, so the "not on the same
// line" refusals are unreachable, and it is called with "for many steps" set
// even from the transitions gesture, so the wrong-element-type refusal is too.
// Both are meant, and both are made to work here: the chart an author gets from
// a well-formed gesture is the same either way, and the one from a malformed
// gesture is a branch spanning two rows that nobody drew.
function commonSearchForSpan(
  forManySteps: boolean, ele1: SeqSelection, ele2: SeqSelection,
): SpanSearch | string {
  const draft = state.draft;
  if (!draft) return 'No chart is being edited.';

  if (!forManySteps && (ele1.type === ELE_SEQ_STEP || ele2.type === ELE_SEQ_STEP)) {
    return 'Select the first and the last transition to be joined.';
  }
  if (forManySteps && ele1.type !== ele2.type) {
    return 'Select two elements of the same kind — two steps, or two transitions.';
  }

  let transition = -1;
  let stepsRow = -1;
  let transitionsRow = -1;
  const columnOf = (sel: SeqSelection): number | string => {
    if (sel.type === ELE_SEQ_STEP) {
      const s = draft.steps[sel.offset];
      if (stepsRow !== -1 && stepsRow !== s.posiY) {
        return 'The first and last steps selected are not on the same line.';
      }
      stepsRow = s.posiY;
      return s.posiX;
    }
    if (sel.type === ELE_SEQ_TRANSITION) {
      const t = draft.transitions[sel.offset];
      if (transitionsRow !== -1 && transitionsRow !== t.posiY) {
        return 'The first and last transitions selected are not on the same line.';
      }
      transitionsRow = t.posiY;
      transition = sel.offset;
      return t.posiX;
    }
    return 'That is not a step or a transition.';
  };

  const x1 = columnOf(ele1);
  if (typeof x1 === 'string') return x1;
  const x2 = columnOf(ele2);
  if (typeof x2 === 'string') return x2;

  return {
    transition, stepsRow, transitionsRow,
    leftX: Math.min(x1, x2), rightX: Math.max(x1, x2),
  };
}

// doManySteps builds a parallel branch: one transition activating (start) or
// deactivating (end) every step across the span. The whole array is rebuilt
// from the span rather than added to, so a second gesture over a shorter span
// narrows the branch instead of leaving the elements it dropped behind.
function doManySteps(page: number, flagStart: boolean, ele1: SeqSelection, ele2: SeqSelection): string {
  const draft = state.draft;
  if (!draft) return 'No chart is being edited.';
  const span = commonSearchForSpan(true, ele1, ele2);
  if (typeof span === 'string') return span;

  let transition = span.transition;
  // Neither click landed on the transition, so look for one in the row the
  // branch runs from: above the steps for a start, below them for an end.
  if (transition === -1 && span.stepsRow !== -1) {
    const row = span.stepsRow + (flagStart ? -1 : 1);
    for (let x = span.leftX; x <= span.rightX; x++) {
      const found = searchTransition(page, x, row);
      if (found !== -1) transition = found;
    }
  }
  if (transition === -1 || span.stepsRow === -1) {
    return flagStart
      ? 'Select the first and last step of the branch; a transition has to sit on the row above them.'
      : 'Select the first and last step of the branch; a transition has to sit on the row below them.';
  }

  const trans = draft.transitions[transition];
  const slots = flagStart ? trans.stepsToActivate : trans.stepsToDeactivate;
  const found: number[] = [];
  for (let x = span.leftX; x <= span.rightX && found.length < MAX_SWITCHS; x++) {
    const step = searchStep(page, x, span.stepsRow);
    if (step !== -1) found.push(step);
  }
  for (let i = 0; i < slots.length; i++) {
    slots[i] = i < found.length ? found[i] : -1;
  }
  return '';
}

// doManyTransitions builds an alternative branch: several transitions on one
// row, of which whichever becomes true first is taken. They are linked to each
// other rather than to a shared element, so every one of them names all the
// others, and they all take from (start) or give to (end) the same step.
function doManyTransitions(page: number, flagStart: boolean, ele1: SeqSelection, ele2: SeqSelection): string {
  const draft = state.draft;
  if (!draft) return 'No chart is being edited.';
  const span = commonSearchForSpan(false, ele1, ele2);
  if (typeof span === 'string') return span;
  if (span.transitionsRow === -1) return 'Select the first and last transition to be joined.';

  const linked: number[] = [];
  for (let x = span.leftX; x <= span.rightX && linked.length < MAX_SWITCHS; x++) {
    const found = searchTransition(page, x, span.transitionsRow);
    if (found !== -1) linked.push(found);
  }
  if (linked.length < 2) {
    return 'That span holds fewer than two transitions to link.';
  }

  for (const self of linked) {
    const trans = draft.transitions[self];
    const slots = flagStart ? trans.transLinkedForStart : trans.transLinkedForEnd;
    const others = linked.filter(n => n !== self);
    for (let i = 0; i < slots.length; i++) {
      slots[i] = i < others.length ? others[i] : -1;
    }
    // The branch has one element on the far side of it, shared by all of them:
    // the step the others already take from, or the one they already give to.
    // Whichever of the linked transitions is drawn to it lends it to the rest.
    let shared = -1;
    for (const other of others) {
      const ref = flagStart
        ? draft.transitions[other].stepsToDeactivate[0]
        : draft.transitions[other].stepsToActivate[0];
      if (ref !== -1) shared = ref;
    }
    if (shared !== -1) {
      if (flagStart) trans.stepsToDeactivate[0] = shared;
      else trans.stepsToActivate[0] = shared;
    }
  }
  return '';
}

// --- The click ---

// clickCell is the whole editing gesture: which tool is held decides what a
// click on a cell means. `top` says the click landed in the upper half, which
// is how the link tool learns which end of a transition was meant.
function clickCell(x: number, y: number, top: boolean) {
  if (!state.draft) return;
  if (x < 0 || x >= SEQ_PAGE_WIDTH || y < 0 || y >= SEQ_PAGE_HEIGHT) return;
  state.error = '';

  const page = state.page;
  const found = searchIt(page, x, y);

  switch (state.tool) {
    case EDIT_SEQ_POINTER:
      state.selected = found;
      return;

    case EDIT_SEQ_ERASER:
      if (found) {
        destroyIt(found);
        state.selected = null;
      }
      return;

    case ELE_SEQ_STEP:
    case EDIT_SEQ_INIT_STEP:
    case ELE_SEQ_TRANSITION:
    case EDIT_SEQ_STEP_AND_TRANS:
      placeStepOrTransition(page, x, y, found);
      return;

    case ELE_SEQ_COMMENT:
      placeComment(page, x, y);
      return;

    case EDIT_SEQ_LINK:
    case EDIT_SEQ_START_MANY_STEPS:
    case EDIT_SEQ_END_MANY_STEPS:
    case EDIT_SEQ_START_MANY_TRANS:
    case EDIT_SEQ_END_MANY_TRANS:
      twoClickGesture(page, found, top);
      return;
  }
}

// placeStepOrTransition covers the four placing tools. Clicking a cell that
// already holds what the tool places removes it, which is how 2.9 lets one tool
// both draw and undo without swapping to the eraser.
function placeStepOrTransition(page: number, x: number, y: number, found: SeqSelection | null) {
  const tool = state.tool;
  const wantsStep = tool === ELE_SEQ_STEP || tool === EDIT_SEQ_INIT_STEP || tool === EDIT_SEQ_STEP_AND_TRANS;
  const wantsTransition = tool === ELE_SEQ_TRANSITION || tool === EDIT_SEQ_STEP_AND_TRANS;
  let removed = false;

  if (wantsStep) {
    if (y % 2 === 0) {
      state.error = 'A step cannot go on an even row — those rows are for transitions.';
      return;
    }
    if (found?.type === ELE_SEQ_STEP) {
      destroyStep(found.offset);
      state.selected = null;
      removed = true;
    } else if (found) {
      state.error = 'There is already an element here.';
      return;
    } else {
      // The step-and-transition tool places two pieces, and both need room
      // before either is placed: a step created first and refused second would
      // be a half-succeeded gesture.
      if (tool === EDIT_SEQ_STEP_AND_TRANS && y + 1 >= SEQ_PAGE_HEIGHT) {
        state.error = 'There is no room below for the transition.';
        return;
      }
      const offset = createStep(page, x, y, tool === EDIT_SEQ_INIT_STEP);
      if (offset === -1) {
        if (!state.error) state.error = 'The chart has no free step left.';
        return;
      }
      state.selected = { type: ELE_SEQ_STEP, offset };
    }
  }

  if (!wantsTransition) return;

  // The step-and-transition tool draws the transition one row below the step,
  // and what is at *that* cell is looked up afresh. 2.9 reuses the search from
  // the step's cell, so it will drop a second transition on top of one that is
  // already there — a chart the controller refuses.
  const ty = tool === EDIT_SEQ_STEP_AND_TRANS ? y + 1 : y;
  if (ty % 2 !== 0) {
    state.error = 'A transition cannot go on an odd row — those rows are for steps.';
    return;
  }
  if (ty >= SEQ_PAGE_HEIGHT) {
    // Reachable only by removing a bottom-row step, which never had a
    // transition below it to remove — placing checked the room up front.
    if (removed) return;
    state.error = 'There is no room below for the transition.';
    return;
  }
  const here = tool === EDIT_SEQ_STEP_AND_TRANS ? searchIt(page, x, ty) : found;

  if (here?.type === ELE_SEQ_TRANSITION) {
    // One click of the step-and-transition tool draws both, so one click
    // removes both — but only when it was a removal. Finding a transition
    // already below a step being *placed* means the author drew the chain from
    // the bottom up, and the new step has just been joined to it; taking it
    // away would undo the click that is being made.
    if (tool === EDIT_SEQ_STEP_AND_TRANS && !removed) return;
    destroyTransition(here.offset);
    state.selected = null;
    return;
  }
  if (here) {
    state.error = 'There is already an element here.';
    return;
  }
  if (removed) return;

  const offset = createTransition(page, x, ty);
  if (offset === -1) {
    state.error = 'The chart has no free transition left.';
    return;
  }
  state.selected = { type: ELE_SEQ_TRANSITION, offset };
}

function placeComment(page: number, x: number, y: number) {
  if (x > SEQ_PAGE_WIDTH - 4) {
    state.error = 'A comment is four cells wide and there is not enough room to the right.';
    return;
  }
  for (let cx = x; cx < x + 4; cx++) {
    if (searchIt(page, cx, y)) {
      state.error = 'A comment needs four free cells and one of them is taken.';
      return;
    }
  }
  const offset = createComment(page, x, y);
  if (offset === -1) {
    state.error = 'The chart has no free comment left.';
    return;
  }
  state.selected = { type: ELE_SEQ_COMMENT, offset };
}

// twoClickGesture drives the five tools that join two elements. The first click
// on an element remembers it; the second does the work. Clicking empty space is
// not a click at all, so a miss does not silently arm or fire the gesture.
function twoClickGesture(page: number, found: SeqSelection | null, top: boolean) {
  if (!found) return;
  const first = state.pending;
  if (!first) {
    state.pending = { ...found, top };
    state.selected = found;
    return;
  }
  state.pending = null;

  switch (state.tool) {
    case EDIT_SEQ_LINK:
      if (first.type === ELE_SEQ_TRANSITION && found.type === ELE_SEQ_STEP) {
        doLinkTransitionAndStep(first.offset, first.top, found.offset);
      } else if (first.type === ELE_SEQ_STEP && found.type === ELE_SEQ_TRANSITION) {
        doLinkTransitionAndStep(found.offset, top, first.offset);
      } else {
        state.error = 'A link joins a transition to a step — click one of each.';
      }
      break;
    case EDIT_SEQ_START_MANY_STEPS:
      state.error = doManySteps(page, true, first, found);
      break;
    case EDIT_SEQ_END_MANY_STEPS:
      state.error = doManySteps(page, false, first, found);
      break;
    case EDIT_SEQ_START_MANY_TRANS:
      state.error = doManyTransitions(page, true, first, found);
      break;
    case EDIT_SEQ_END_MANY_TRANS:
      state.error = doManyTransitions(page, false, first, found);
      break;
  }
  state.selected = found;
}

// --- Properties of the selected element ---

export function selectedStep(): Step | null {
  const sel = state.selected;
  if (!state.draft || sel?.type !== ELE_SEQ_STEP) return null;
  return state.draft.steps[sel.offset] ?? null;
}

export function selectedTransition(): Transition | null {
  const sel = state.selected;
  if (!state.draft || sel?.type !== ELE_SEQ_TRANSITION) return null;
  return state.draft.transitions[sel.offset] ?? null;
}

export function selectedComment(): SeqComment | null {
  const sel = state.selected;
  if (!state.draft || sel?.type !== ELE_SEQ_COMMENT) return null;
  return state.draft.comments[sel.offset] ?? null;
}

function setStepNumber(n: number): boolean {
  const step = selectedStep();
  if (!step) return false;
  if (!Number.isInteger(n) || n < 0 || n >= MAX_STEPS) {
    state.error = `A step number is the n in "%Xn" and runs from 0 to ${MAX_STEPS - 1}.`;
    return false;
  }
  step.stepNumber = n;
  state.error = '';
  return true;
}

function setStepInit(init: boolean) {
  const step = selectedStep();
  if (step) step.initStep = init;
}

// setTransitionCondition takes what the operator typed and reads it with the
// same rules the display writes with, symbols included — so what a transition
// is labelled with is what can be typed back into it.
function setTransitionCondition(text: string): boolean {
  const trans = selectedTransition();
  if (!trans) return false;
  const ref = parseVar(text);
  if (!ref) {
    state.error = `"${text}" is not a variable this PLC has.`;
    return false;
  }
  trans.varTypeCondi = ref.varType;
  trans.varNumCondi = ref.offset;
  state.error = '';
  return true;
}

function setCommentText(text: string) {
  const comment = selectedComment();
  if (comment) comment.comment = text;
}

export const sfcStore = {
  state,
  beginEdit,
  cancelEdit,
  applyEdit,
  setTool,
  clickCell,
  setStepNumber,
  setStepInit,
  setTransitionCondition,
  setCommentText,
};

// Exported for the tests, which drive the algebra directly rather than through
// a rendered chart.
export const sfcInternals = {
  searchStep,
  searchTransition,
  searchComment,
  createStep,
  createTransition,
  destroyStep,
  destroyTransition,
  doManySteps,
  doManyTransitions,
  doLinkTransitionAndStep,
};

export { MAX_SWITCHS, SEQ_PAGE_WIDTH, SEQ_PAGE_HEIGHT };
