import { reactive, watch } from 'vue';
import {
  ClassicladderClient,
  RUNG_WIDTH,
  RUNG_HEIGHT,
  type Program,
  type VarClass,
  type Element,
  type Rung,
  type HalLink,
  type Section,
  type Sequential,
  type ExprSlot,
  LadderState,
  SectionLanguage,
  TimerIECMode,
  VAR_TIMER_DONE,
  VAR_MONOSTABLE_RUNNING,
  VAR_COUNTER_DONE,
  VAR_TIMER_IEC_DONE,
  ELE_TIMER,
  ELE_MONOSTABLE,
  ELE_COUNTER,
  ELE_TIMER_IEC,
  ELE_COMPAR,
  ELE_OUTPUT_OPERATE,
  ELE_OUTPUT_JUMP,
  ELE_OUTPUT_CALL,
  ELE_FREE,
  ELE_UNUSABLE,
} from '../generated/classicladder_client';
import { liveStore } from './live';

const COLS = RUNG_WIDTH;
const ROWS = RUNG_HEIGHT;

export interface SelectedCell {
  rungIdx: number;
  row: number;
  col: number;
}

export interface LadderStoreState {
  program: Program | null;
  activeSection: number;
  // How variables are written, served by the backend. Held here rather than
  // hard-coded in a component: the prefixes and suffixes are part of the
  // program's meaning, not of its presentation, and a copy in the UI drifts —
  // it had an old-style timer labelled with the IEC prefix and the IEC timer
  // labelled with one that does not exist.
  varClasses: VarClass[];
  // The expression table in written form, as the backend renders it — index
  // aligned with program.arithmExprs.
  expressionTexts: string[];
  symbolMap: Map<string, string>;
  // symbol -> written variable name, for operators who type the symbol.
  symbolToVar: Map<string, string>;
  // Which HAL signal carries each physical variable. This is what makes
  // someone else's ladder readable, and only the backend can answer it.
  halLinks: HalLink[];
  // The SFC chart, for sections whose language is SEQUENTIAL. Static structure,
  // so it is fetched with the program; the live step activity arrives with the
  // variables instead.
  sequential: Sequential | null;

  // --- Editing ---
  //
  // One rung at a time, on a copy: the toolbar and the property editor work on
  // `draft`, Apply writes it with set_rung and Cancel throws it away. That is
  // the shape 2.9's edit mode has, and it is the only shape that composes with
  // the structural calls — insert_rung and delete_rung take effect on the
  // controller immediately, so a parallel buffer of unsaved rung edits would
  // be silently clobbered by them.
  editRung: number;   // rung index being edited, -1 when not editing
  draft: Rung | null;
  editTool: number;   // element type to place; ELE_FREE deletes, -1 = none
  selectedCell: SelectedCell | null;
  // Expressions typed during this session, by expression index. 2.9 edits a
  // copy of the expression table and applies it with the rung; block presets,
  // by contrast, it writes straight through. Both are kept here — Cancel has
  // to undo what the operator typed into a compare, and a preset that only
  // took effect on Apply would be a behaviour change nobody asked for.
  draftExprs: Record<number, string>;

  loading: boolean;
  busy: boolean;
  error: string;
}

const client = new ClassicladderClient(window.location.origin);

const state = reactive<LadderStoreState>({
  program: null,
  activeSection: 0,
  varClasses: [],
  expressionTexts: [],
  symbolMap: new Map(),
  symbolToVar: new Map(),
  halLinks: [],
  sequential: null,
  editRung: -1,
  draft: null,
  editTool: -1,
  selectedCell: null,
  draftExprs: {},
  loading: false,
  busy: false,
  error: '',
});

// reportError surfaces what the backend refused. Those messages are written to
// be shown — they name the rung, the cell and the reason.
function reportError(e: unknown) {
  state.error = e instanceof Error ? e.message : String(e);
}

// Other stores hold edit sessions of their own — the SFC chart draft. The
// structural operations below have to drop those sessions too, for the same
// reason they drop the rung draft: the program they were copied from is about
// to stop existing. They register here rather than being imported, because
// sfc.ts already imports this store.
const editCancelHooks: (() => void)[] = [];

export function registerEditCancelHook(hook: () => void) {
  editCancelHooks.push(hook);
}

function cancelAllEdits() {
  cancelEdit();
  for (const hook of editCancelHooks) hook();
}

async function fetchProgram() {
  state.loading = true;
  state.error = '';
  try {
    // The naming table describes how this PLC was sized, so it is fetched with
    // the program rather than once at start-up: loading a different project can
    // change how many of each variable exist.
    const [program, varClasses, exprTexts, halLinks, sequential] = await Promise.all([
      client.getProgram(),
      client.getVarClasses(),
      client.getExpressionsText(),
      client.getHalLinks(),
      client.getSequential(),
    ]);
    state.program = program;
    state.varClasses = varClasses;
    state.expressionTexts = exprTexts.map(e => e.text);
    state.halLinks = halLinks;
    state.sequential = sequential;
    buildSymbolMaps();
    // Keep the section the operator was looking at if it still exists.
    const sections = program.sections ?? [];
    if (!sections[state.activeSection]?.used) {
      const firstUsed = sections.findIndex(s => s.used);
      state.activeSection = firstUsed >= 0 ? firstUsed : 0;
    }
  } catch (e: unknown) {
    reportError(e);
  } finally {
    state.loading = false;
  }
}

// --- Variable naming, from the table the backend serves ---

// Blocks are named by their kind and number ("%T0"), not by a variable, so the
// prefix is borrowed from one of the variables that kind owns. Taking it from
// the same table is what keeps the two from disagreeing.
const blockPrefixSource: Record<number, number> = {
  [ELE_TIMER]: VAR_TIMER_DONE,
  [ELE_MONOSTABLE]: VAR_MONOSTABLE_RUNNING,
  [ELE_COUNTER]: VAR_COUNTER_DONE,
  [ELE_TIMER_IEC]: VAR_TIMER_IEC_DONE,
};

function classFor(varType: number): VarClass | undefined {
  return state.varClasses.find(c => c.varType === varType);
}

// formatVar writes a variable the way ClassicLadder does: "%I3", "%C0.D",
// "%X7.V". Returns '' when the table has not arrived yet, and '%?' when it has
// but does not describe this type.
export function formatVar(varType: number, offset: number): string {
  if (state.varClasses.length === 0) return '';
  const c = classFor(varType);
  if (!c) return '%?';
  return `%${c.prefix}${offset}${c.suffix}`;
}

export interface VarRef {
  varType: number;
  offset: number;
}

// parseVar reads a written variable name using the same served table, so the
// editor accepts exactly what the display produces.
//
// The API's rule is that the longest written form wins: "%X0.V" is a step time
// and not a step activity with stray characters, "%IW0" is a word input and not
// "%I0" with a leftover, "%TM0.Q" is an IEC timer and not "%T…". What settles
// almost all of those here is that what sits between prefix and suffix must be
// digits and nothing else, which leaves one candidate; the longest-form
// preference is the tie-break for a table that ever stops being that tidy.
//
// A symbol is accepted too, because that is what an operator who named their
// signals will type — 2.9's parser does the same.
export function parseVar(text: string): VarRef | null {
  let t = text.trim();
  if (t === '') return null;
  if (!t.startsWith('%')) {
    const mapped = state.symbolToVar.get(t);
    if (!mapped) return null;
    t = mapped;
  }
  const body = t.slice(1).toUpperCase();

  let best: { cls: VarClass; offset: number; len: number } | null = null;
  for (const c of state.varClasses) {
    const prefix = c.prefix.toUpperCase();
    const suffix = c.suffix.toUpperCase();
    if (!body.startsWith(prefix)) continue;
    let rest = body.slice(prefix.length);
    if (suffix !== '') {
      if (!rest.endsWith(suffix)) continue;
      rest = rest.slice(0, rest.length - suffix.length);
    }
    if (!/^\d+$/.test(rest)) continue;
    const offset = Number(rest);
    if (offset < 0 || offset >= c.count) continue;
    const len = prefix.length + suffix.length;
    if (!best || len > best.len) best = { cls: c, offset, len };
  }
  return best ? { varType: best.cls.varType, offset: best.offset } : null;
}

// varIsWritable reports whether a coil may drive this region. The backend
// refuses a coil on a read-only variable; knowing it here lets the editor say
// so before the write rather than after.
export function varIsWritable(varType: number): boolean {
  return classFor(varType)?.writable ?? false;
}

// elementLabel is what a cell shows: the variable a contact or coil works on,
// the block a timer refers to, or the expression a compare or operate holds.
export function elementLabel(el: Element): string {
  const blockVar = blockPrefixSource[el.type];
  if (blockVar !== undefined) {
    if (state.varClasses.length === 0) return '';
    const c = classFor(blockVar);
    return c ? `%${c.prefix}${el.varNum}` : '%?';
  }

  // A compare or operate holds an expression index, not a variable. Showing it
  // through the variable table would label it "%B3" for expression 3.
  //
  // The written form comes from the backend (GET /expressions/text) rather than
  // being rendered here: converting between "@200/0@>5" and "%W0>5" needs the
  // naming rules, and a second implementation of them is one that can drift
  // from the one checked against the original.
  if (el.type === ELE_COMPAR || el.type === ELE_OUTPUT_OPERATE) {
    return expressionText(el.varNum);
  }

  // A jump names a rung and a call names a sub-routine; neither is a variable.
  if (el.type === ELE_OUTPUT_JUMP) return `>${el.varNum}`;
  if (el.type === ELE_OUTPUT_CALL) return `SR${el.varNum}`;

  return formatVar(el.varType, el.varNum);
}

// blockValueText is what a block shows inside its box: the live value while the
// PLC runs, the preset while editing or when nothing is live. That is the rule
// 2.9 uses, and it is the difference between watching a timer count down and
// reading a static number.
export function blockValueText(el: Element, editing: boolean): string {
  const prog = state.program;
  if (!prog) return '';
  const n = el.varNum;
  const vars = liveStore.state.variables;
  const live = !editing && vars !== null;

  switch (el.type) {
    case ELE_TIMER:
      return String(live ? vars!.words.timerValues[n] ?? '' : prog.timers[n]?.preset ?? '');
    case ELE_MONOSTABLE:
      return String(live ? vars!.words.monostableValues[n] ?? '' : prog.monostables[n]?.preset ?? '');
    case ELE_COUNTER:
      return String(live ? vars!.words.counterValues[n] ?? '' : prog.counters[n]?.preset ?? '');
    case ELE_TIMER_IEC:
      return String(live ? vars!.words.timerIecValues[n] ?? '' : prog.timersIec[n]?.preset ?? '');
    default:
      return '';
  }
}

// halLinkFor reports the HAL wiring of a physical variable, so a rung can be
// annotated with the signal it actually reads.
export function halLinkFor(varType: number, offset: number): HalLink | undefined {
  return state.halLinks.find(l => l.varType === varType && l.offset === offset);
}

function buildSymbolMaps() {
  state.symbolMap.clear();
  state.symbolToVar.clear();
  if (!state.program?.symbols) return;
  for (const sym of state.program.symbols) {
    if (sym.varName && sym.symbol) {
      state.symbolMap.set(sym.varName, sym.symbol);
      state.symbolToVar.set(sym.symbol, sym.varName);
    }
  }
}

function setActiveSection(index: number) {
  if (state.editRung >= 0) cancelEdit();
  state.activeSection = index;
}

function setEditTool(type: number) {
  state.editTool = type;
}

// --- Section and rung structure ---
//
// The rung order is a per-section linked list the backend owns. Walking it to
// draw is reading the structure it sent; writing it is not something a client
// does, which is why insert/delete are calls rather than set_rung sequences.

export interface RungEntry {
  rung: Rung;
  index: number;
}

export function sectionRungs(sectionIndex: number): RungEntry[] {
  const prog = state.program;
  if (!prog) return [];
  const section = prog.sections[sectionIndex];
  if (!section?.used || section.language !== SectionLanguage.LADDER) return [];

  const out: RungEntry[] = [];
  const seen = new Set<number>();
  let idx = section.firstRung;
  while (idx >= 0 && idx < prog.rungs.length && !seen.has(idx)) {
    seen.add(idx);
    if (prog.rungs[idx].used) out.push({ rung: prog.rungs[idx], index: idx });
    if (idx === section.lastRung) break;
    idx = prog.rungs[idx].nextRung;
  }
  return out;
}

export function usedSections(): (Section & { index: number })[] {
  if (!state.program) return [];
  return state.program.sections
    .map((s, i) => ({ ...s, index: i }))
    .filter(s => s.used);
}

// --- The edit session ---

function currentRung(): Rung | null {
  if (state.editRung < 0) return null;
  return state.draft;
}

function beginEdit(rungIdx: number) {
  if (!state.program) return;
  const rung = state.program.rungs[rungIdx];
  if (!rung) return;
  state.editRung = rungIdx;
  // A JSON round-trip rather than structuredClone: the rung is reactive, and
  // structuredClone refuses a proxy outright — which would have made the first
  // click on Edit throw.
  state.draft = JSON.parse(JSON.stringify(rung)) as Rung;
  state.selectedCell = null;
  state.draftExprs = {};
  state.error = '';
}

function cancelEdit() {
  state.editRung = -1;
  state.draft = null;
  state.selectedCell = null;
  state.draftExprs = {};
  state.editTool = -1;
}

// expressionText is what a compare or an operate reads: what the operator has
// typed this session if anything, otherwise what the backend rendered.
export function expressionText(index: number): string {
  return state.draftExprs[index] ?? state.expressionTexts[index] ?? '';
}

// setDraftExpression stages an expression for the element being edited. It is
// written on Apply, so Cancel discards it — see draftExprs.
function setDraftExpression(index: number, text: string) {
  state.draftExprs[index] = text;
}

// stagedExpressions is what rides with the rung on Apply: the texts typed this
// session, and an empty text for every slot the edit stopped using, so a
// deleted compare's expression is released in the same write rather than
// leaking. A slot is only released when no element of the draft still reads it
// and no other rung does either — the server does not check cross-references,
// so that check lives here.
function stagedExpressions(): ExprSlot[] {
  const prog = state.program;
  const draft = state.draft;
  if (!prog || !draft) return [];

  const exprRefs = (rung: Rung, into: Set<number>) => {
    for (const el of rung.elements) {
      if (el.type === ELE_COMPAR || el.type === ELE_OUTPUT_OPERATE) into.add(el.varNum);
    }
  };
  const inDraft = new Set<number>();
  exprRefs(draft, inDraft);
  const elsewhere = new Set<number>();
  prog.rungs.forEach((rung, i) => {
    if (rung.used && i !== state.editRung) exprRefs(rung, elsewhere);
  });
  const original = new Set<number>();
  if (prog.rungs[state.editRung]) exprRefs(prog.rungs[state.editRung], original);

  const slots = new Map<number, string>();
  for (const [key, text] of Object.entries(state.draftExprs)) {
    const index = Number(key);
    if (inDraft.has(index)) slots.set(index, text);
    else if (!elsewhere.has(index)) slots.set(index, '');
  }
  for (const index of original) {
    if (!inDraft.has(index) && !elsewhere.has(index)) slots.set(index, '');
  }
  return [...slots.entries()]
    .sort(([a], [b]) => a - b)
    .map(([index, text]) => ({ index, text }));
}

// applyEdit writes the draft and its expressions as one call, which the server
// applies transactionally: everything parses and compiles first, and a refusal
// changes nothing on the controller. A refusal keeps the session open with the
// reason shown: the operator's work is in the draft, and throwing it away
// because the controller said no would be the worst possible response to a
// typo.
async function applyEdit(): Promise<boolean> {
  if (state.editRung < 0 || !state.draft) return false;
  state.busy = true;
  state.error = '';
  try {
    await client.setRung(state.editRung, state.draft, stagedExpressions());
    cancelEdit();
    await fetchProgram();
    return true;
  } catch (e: unknown) {
    reportError(e);
    return false;
  } finally {
    state.busy = false;
  }
}

// --- Placing and removing elements, in the draft ---

// Returns { cols, rows } footprint of an element type
export function elementSize(type: number): { cols: number; rows: number } {
  switch (type) {
    case ELE_TIMER: case ELE_MONOSTABLE: case ELE_TIMER_IEC: return { cols: 2, rows: 2 };
    case ELE_COUNTER: return { cols: 2, rows: 4 };
    case ELE_COMPAR: case ELE_OUTPUT_OPERATE: return { cols: 3, rows: 1 };
    default: return { cols: 1, rows: 1 };
  }
}

// The backend calls a block's body cells "unusable"; this app has always called
// them block bodies.
const ELE_BLOCK_BODY = ELE_UNUSABLE;

function getEl(rung: Rung, row: number, col: number): Element | null {
  if (row < 0 || row >= ROWS || col < 0 || col >= COLS) return null;
  return rung.elements[row * COLS + col] ?? null;
}

// Find the head element of a block that occupies (row, col).
// For body cells, searches nearby cells for the head.
function findBlockHead(rung: Rung, row: number, col: number): { row: number; col: number; type: number } | null {
  const el = getEl(rung, row, col);
  if (!el) return null;
  if (el.type !== ELE_BLOCK_BODY) {
    const sz = elementSize(el.type);
    if (sz.cols > 1 || sz.rows > 1) return { row, col, type: el.type };
    return null;
  }
  // The head is the cell nearest the right rail, at the top of the footprint.
  for (let r = Math.max(0, row - 3); r <= row; r++) {
    for (let c = col; c <= Math.min(COLS - 1, col + 2); c++) {
      const candidate = getEl(rung, r, c);
      if (!candidate) continue;
      const sz = elementSize(candidate.type);
      if (sz.cols <= 1 && sz.rows <= 1) continue;
      const leftCol = c - (sz.cols - 1);
      if (col >= leftCol && col <= c && row >= r && row < r + sz.rows) {
        return { row: r, col: c, type: candidate.type };
      }
    }
  }
  return null;
}

function clearCell(rung: Rung, row: number, col: number) {
  const el = getEl(rung, row, col);
  if (!el) return;
  el.type = ELE_FREE;
  el.connectedWithTop = 0;
  el.varType = 0;
  el.varNum = 0;
}

// Remove an entire block (head + all body cells)
function removeBlock(rung: Rung, headRow: number, headCol: number, type: number) {
  const sz = elementSize(type);
  const leftCol = headCol - (sz.cols - 1);
  for (let r = headRow; r < headRow + sz.rows; r++) {
    for (let c = leftCol; c <= headCol; c++) {
      clearCell(rung, r, c);
    }
  }
}

// Clear all elements in a footprint area, removing any overlapping blocks fully
function clearFootprint(rung: Rung, topRow: number, leftCol: number, cols: number, rows: number) {
  for (let r = topRow; r < topRow + rows; r++) {
    for (let c = leftCol; c < leftCol + cols; c++) {
      const el = getEl(rung, r, c);
      if (!el || el.type === ELE_FREE) continue;
      if (el.type === ELE_BLOCK_BODY) {
        const head = findBlockHead(rung, r, c);
        if (head) removeBlock(rung, head.row, head.col, head.type);
      } else {
        const sz = elementSize(el.type);
        if (sz.cols > 1 || sz.rows > 1) {
          removeBlock(rung, r, c, el.type);
        } else {
          clearCell(rung, r, c);
        }
      }
    }
  }
}

// allocExpression finds an expression slot nothing is using, the way 2.9's
// editor does when a compare or an operate is placed. Returns -1 when the
// table is full, which is a refusal the operator has to see: silently reusing
// a slot would rewrite an expression in another rung.
function allocExpression(): number {
  const prog = state.program;
  if (!prog) return -1;
  const taken = new Set<number>();
  const scan = (rung: Rung) => {
    for (const el of rung.elements) {
      if (el.type === ELE_COMPAR || el.type === ELE_OUTPUT_OPERATE) taken.add(el.varNum);
    }
  };
  for (const rung of prog.rungs) {
    if (rung.used) scan(rung);
  }
  if (state.draft) scan(state.draft);

  for (let i = 0; i < prog.arithmExprs.length; i++) {
    if (taken.has(i)) continue;
    if ((state.expressionTexts[i] ?? '') !== '') continue;
    return i;
  }
  return -1;
}

function selectCell(rungIdx: number, row: number, col: number) {
  // Outside an edit session a click selects nothing and changes nothing: the
  // view is a view.
  if (state.editRung !== rungIdx) return;
  const rung = currentRung();
  if (!rung) return;

  // Selecting: a block covers several cells but has one set of properties, so
  // clicking any of them selects the block. Only the head carries the type and
  // the number; a body cell has nothing to offer, and asking an operator to
  // find the corner nearest the right rail is asking them to know how a rung
  // is stored.
  if (state.editTool < 0) {
    const head = findBlockHead(rung, row, col);
    state.selectedCell = head
      ? { rungIdx, row: head.row, col: head.col }
      : { rungIdx, row, col };
    return;
  }

  state.selectedCell = { rungIdx, row, col };

  if (state.editTool === ELE_FREE) {
    const el = getEl(rung, row, col);
    if (!el) return;
    if (el.type === ELE_BLOCK_BODY) {
      const head = findBlockHead(rung, row, col);
      if (head) removeBlock(rung, head.row, head.col, head.type);
    } else {
      const sz = elementSize(el.type);
      if (sz.cols > 1 || sz.rows > 1) {
        removeBlock(rung, row, col, el.type);
      } else {
        clearCell(rung, row, col);
      }
    }
    return;
  }

  const sz = elementSize(state.editTool);
  // For multi-cell blocks the click marks the top-left of the footprint; the
  // head goes at its right column.
  const headCol = col + sz.cols - 1;
  const headRow = row;
  if (headCol >= COLS || row + sz.rows > ROWS) {
    state.error = 'That element does not fit here — it needs room to the right and below.';
    return;
  }

  // A compare or an operate needs an expression of its own before it is placed,
  // so that two of them never end up sharing one.
  let exprNum = 0;
  if (state.editTool === ELE_COMPAR || state.editTool === ELE_OUTPUT_OPERATE) {
    exprNum = allocExpression();
    if (exprNum < 0) {
      state.error = 'No free arithmetic expression left — delete one, or load the module with a larger numArithmExpr.';
      return;
    }
  }

  clearFootprint(rung, row, col, sz.cols, sz.rows);

  const headEl = getEl(rung, headRow, headCol);
  if (!headEl) return;
  headEl.type = state.editTool;
  headEl.varType = 0;
  headEl.varNum = exprNum;
  if (sz.cols > 1 || sz.rows > 1) {
    for (let r = row; r < row + sz.rows; r++) {
      for (let c = col; c < col + sz.cols; c++) {
        if (r === headRow && c === headCol) continue;
        const bodyEl = getEl(rung, r, c);
        if (bodyEl) {
          bodyEl.type = ELE_BLOCK_BODY;
          bodyEl.varType = 0;
          bodyEl.varNum = 0;
          bodyEl.connectedWithTop = 0;
        }
      }
    }
  }
  // Keep the newly placed element selected, so the property editor opens on it.
  state.selectedCell = { rungIdx, row: headRow, col: headCol };
}

function toggleTopConnection() {
  const rung = currentRung();
  if (!rung || !state.selectedCell) return;
  const { row, col } = state.selectedCell;
  if (row === 0) {
    state.error = 'The top row has nothing above it to connect to.';
    return;
  }
  const el = getEl(rung, row, col);
  if (!el) return;
  el.connectedWithTop = el.connectedWithTop ? 0 : 1;
}

// selectedElement is what the property editor edits — always a cell of the
// draft, never of the running program.
export function selectedElement(): Element | null {
  const rung = currentRung();
  if (!rung || !state.selectedCell) return null;
  return getEl(rung, state.selectedCell.row, state.selectedCell.col);
}

// --- Structural edits (server-owned) ---

async function withRefresh(fn: () => Promise<unknown>): Promise<boolean> {
  state.busy = true;
  state.error = '';
  try {
    await fn();
    await fetchProgram();
    return true;
  } catch (e: unknown) {
    reportError(e);
    return false;
  } finally {
    state.busy = false;
  }
}

async function insertRungAfter(afterIndex: number): Promise<boolean> {
  cancelAllEdits();
  return withRefresh(() => client.insertRung(afterIndex));
}

async function deleteRung(index: number): Promise<boolean> {
  cancelAllEdits();
  return withRefresh(() => client.deleteRung(index));
}

async function addSection(name: string, language: SectionLanguage, subRoutineNumber: number): Promise<boolean> {
  cancelAllEdits();
  return withRefresh(() => client.addSection(name, language, subRoutineNumber));
}

async function deleteSection(index: number): Promise<boolean> {
  cancelAllEdits();
  return withRefresh(() => client.deleteSection(index));
}

async function setSection(index: number, section: Section): Promise<boolean> {
  return withRefresh(() => client.setSection(index, section));
}

// --- Block parameters, symbols, expressions ---

async function setTimer(index: number, preset: number, base: number): Promise<boolean> {
  return withRefresh(() => client.setTimer(index, preset, base));
}

async function setMonostable(index: number, preset: number, base: number): Promise<boolean> {
  return withRefresh(() => client.setMonostable(index, preset, base));
}

async function setCounter(index: number, preset: number): Promise<boolean> {
  return withRefresh(() => client.setCounter(index, preset));
}

async function setTimerIec(index: number, preset: number, base: number, mode: TimerIECMode): Promise<boolean> {
  return withRefresh(() => client.setTimerIec(index, preset, base, mode));
}

async function setExpressionText(index: number, text: string): Promise<boolean> {
  return withRefresh(() => client.setExpressionText(index, text));
}

async function setSymbols(symbols: Program['symbols']): Promise<boolean> {
  return withRefresh(() => client.setSymbols(symbols));
}

// --- Project and run control ---

async function setState(s: LadderState) {
  state.error = '';
  try {
    await client.setState(s);
  } catch (e: unknown) {
    reportError(e);
  }
}

async function loadProject(path: string): Promise<boolean> {
  cancelAllEdits();
  return withRefresh(() => client.loadProject(path));
}

async function saveProject(path: string): Promise<boolean> {
  state.busy = true;
  state.error = '';
  try {
    await client.saveProject(path);
    return true;
  } catch (e: unknown) {
    reportError(e);
    return false;
  } finally {
    state.busy = false;
  }
}

async function setVariable(varType: number, offset: number, value: number) {
  state.error = '';
  try {
    await client.setVariable(varType, offset, value);
  } catch (e: unknown) {
    reportError(e);
  }
}

// --- Staying current with the controller ---
//
// The controller bumps Status.generation on every program mutation — edits by
// anyone, load_project, set_program — and pushes it with watch_status. When it
// moves past the last value seen here, somebody else changed the program and
// the view refetches. An open edit session is left alone: the draft is a copy
// by design, and the server validates it on Apply — the ruling is that the
// server's program is authoritative and the UI holds no state of its own.
//
// Synchronous flush, because these are store-level watchers with no render to
// batch against.

let lastGeneration: number | null = null;
let hadConnection = false;

watch(() => liveStore.state.connected, (connected) => {
  if (!connected) return;
  if (!hadConnection) {
    // The first connection of the session; the mount already fetches.
    hadConnection = true;
    return;
  }
  // A reconnect. Anything can have happened while the watch was down — even a
  // controller restart, whose generation counter starts over — so the program
  // is refetched and the counter re-learned rather than compared.
  lastGeneration = null;
  void fetchProgram();
}, { flush: 'sync' });

watch(() => liveStore.state.status?.generation, (generation) => {
  if (generation === undefined) return; // not connected — nothing to compare
  if (lastGeneration === null) {
    lastGeneration = generation;
    return;
  }
  if (generation === lastGeneration) return;
  lastGeneration = generation;
  void fetchProgram();
}, { flush: 'sync' });

export const ladderStore = {
  state,
  fetchProgram,
  setActiveSection,
  setEditTool,
  beginEdit,
  setDraftExpression,
  cancelEdit,
  applyEdit,
  selectCell,
  toggleTopConnection,
  insertRungAfter,
  deleteRung,
  addSection,
  deleteSection,
  setSection,
  setTimer,
  setMonostable,
  setCounter,
  setTimerIec,
  setExpressionText,
  setSymbols,
  setState,
  loadProject,
  saveProject,
  setVariable,
};
