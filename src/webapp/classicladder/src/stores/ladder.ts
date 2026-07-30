import { reactive } from 'vue';
import {
  ClassicladderClient,
  type Program,
  type VarClass,
  type Element,
  LadderState,
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

export interface LadderStoreState {
  program: Program | null;
  activeSection: number;
  editTool: number; // element type to place, 0 = delete
  selectedCell: { rungIdx: number; row: number; col: number } | null;
  dirty: boolean;
  symbolMap: Map<string, string>;
  // How variables are written, served by the backend. Held here rather than
  // hard-coded in a component: the prefixes and suffixes are part of the
  // program's meaning, not of its presentation, and a copy in the UI drifts —
  // it had an old-style timer labelled with the IEC prefix and the IEC timer
  // labelled with one that does not exist.
  varClasses: VarClass[];
  loading: boolean;
  error: string;
}

const COLS = 10;

const client = new ClassicladderClient(window.location.origin);

const state = reactive<LadderStoreState>({
  program: null,
  activeSection: 0,
  editTool: -1, // -1 = no tool selected
  selectedCell: null,
  dirty: false,
  symbolMap: new Map(),
  varClasses: [],
  loading: false,
  error: '',
});

async function fetchProgram() {
  state.loading = true;
  state.error = '';
  try {
    // The naming table describes how this PLC was sized, so it is fetched with
    // the program rather than once at start-up: loading a different project can
    // change how many of each variable exist.
    const [program, varClasses] = await Promise.all([
      client.getProgram(),
      client.getVarClasses(),
    ]);
    state.program = program;
    state.varClasses = varClasses;
    buildSymbolMap();
    state.dirty = false;
    // Set active section to first used section
    if (state.program.sections) {
      const firstUsed = state.program.sections.findIndex(s => s.used);
      if (firstUsed >= 0) state.activeSection = firstUsed;
    }
  } catch (e: unknown) {
    state.error = (e as Error).message;
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

// exprToNames rewrites a stored expression into written names for display:
// "@200/0@>5" becomes "%W0>5". A reference the table cannot resolve is left as
// it was rather than dropped, so an expression never silently loses a term.
export function exprToNames(expr: string): string {
  let out = '';
  let i = 0;
  while (i < expr.length) {
    if (expr[i] !== '@') {
      out += expr[i++];
      continue;
    }
    const end = expr.indexOf('@', i + 1);
    if (end < 0) {
      out += expr.slice(i);
      break;
    }
    const inner = expr.slice(i + 1, end);
    const name = refToName(inner);
    out += name ?? expr.slice(i, end + 1);
    i = end + 1;
  }
  return out;
}

// refToName converts the inside of an @...@ reference, keeping any index.
function refToName(inner: string): string | null {
  let index = '';
  const open = inner.indexOf('[');
  if (open >= 0) {
    if (!inner.endsWith(']')) return null;
    const idx = refToName(inner.slice(open + 1, inner.length - 1));
    if (idx === null) return null;
    index = `[${idx}]`;
    inner = inner.slice(0, open);
  }
  const slash = inner.indexOf('/');
  if (slash < 0) return null;
  const varType = Number(inner.slice(0, slash));
  const offset = Number(inner.slice(slash + 1));
  if (!Number.isInteger(varType) || !Number.isInteger(offset)) return null;
  const c = classFor(varType);
  if (!c) return null;
  return `%${c.prefix}${offset}${c.suffix}${index}`;
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
  if (el.type === ELE_COMPAR || el.type === ELE_OUTPUT_OPERATE) {
    const expr = state.program?.arithmExprs?.[el.varNum]?.expr ?? '';
    return expr === '' ? '' : exprToNames(expr);
  }

  // A jump names a rung and a call names a sub-routine; neither is a variable.
  if (el.type === ELE_OUTPUT_JUMP) return `>${el.varNum}`;
  if (el.type === ELE_OUTPUT_CALL) return `SR${el.varNum}`;

  return formatVar(el.varType, el.varNum);
}

function buildSymbolMap() {
  state.symbolMap.clear();
  if (!state.program?.symbols) return;
  for (const sym of state.program.symbols) {
    if (sym.varName && sym.symbol) {
      // varName is like "%B0", "%I3", etc.
      state.symbolMap.set(sym.varName, sym.symbol);
    }
  }
}

function setActiveSection(index: number) {
  state.activeSection = index;
}

function setEditTool(type: number) {
  state.editTool = type;
}

const ROWS = 6;
// The backend calls a block's body cells "unusable"; this app has always called
// them block bodies.
const ELE_BLOCK_BODY = ELE_UNUSABLE;

// Returns { cols, rows } footprint of an element type
export function elementSize(type: number): { cols: number; rows: number } {
  switch (type) {
    case ELE_TIMER: case ELE_MONOSTABLE: case ELE_TIMER_IEC: return { cols: 2, rows: 2 };
    case ELE_COUNTER: return { cols: 2, rows: 4 };
    case ELE_COMPAR: case ELE_OUTPUT_OPERATE: return { cols: 3, rows: 1 };
    default: return { cols: 1, rows: 1 };
  }
}

function getEl(rung: { elements: { type: number; connectedWithTop: number; varType: number; varNum: number }[] }, row: number, col: number) {
  if (row < 0 || row >= ROWS || col < 0 || col >= COLS) return null;
  return rung.elements[row * COLS + col] ?? null;
}

// Find the head element of a block that occupies (row, col).
// For type 99 body cells, searches nearby cells for the head.
function findBlockHead(rung: { elements: { type: number; connectedWithTop: number; varType: number; varNum: number }[] }, row: number, col: number): { row: number; col: number; type: number } | null {
  const el = getEl(rung, row, col);
  if (!el) return null;
  if (el.type !== ELE_BLOCK_BODY) {
    const sz = elementSize(el.type);
    if (sz.cols > 1 || sz.rows > 1) return { row, col, type: el.type };
    return null;
  }
  // Search for the head: head is at top-right of block (for 2-col blocks)
  // or rightmost cell (for 3-col blocks). Scan nearby cells.
  for (let r = Math.max(0, row - 3); r <= row; r++) {
    for (let c = col; c <= Math.min(COLS - 1, col + 2); c++) {
      const candidate = getEl(rung, r, c);
      if (!candidate) continue;
      const sz = elementSize(candidate.type);
      if (sz.cols <= 1 && sz.rows <= 1) continue;
      // Check if (row, col) falls within this block's footprint
      // Head is at (r, c), footprint extends left by (sz.cols-1) and down by (sz.rows-1)
      const leftCol = c - (sz.cols - 1);
      if (col >= leftCol && col <= c && row >= r && row < r + sz.rows) {
        return { row: r, col: c, type: candidate.type };
      }
    }
  }
  return null;
}

// Clear a single cell
function clearCell(rung: { elements: { type: number; connectedWithTop: number; varType: number; varNum: number }[] }, row: number, col: number) {
  const el = getEl(rung, row, col);
  if (!el) return;
  el.type = ELE_FREE;
  el.connectedWithTop = 0;
  el.varType = 0;
  el.varNum = 0;
}

// Remove an entire block (head + all body cells)
function removeBlock(rung: { elements: { type: number; connectedWithTop: number; varType: number; varNum: number }[] }, headRow: number, headCol: number, type: number) {
  const sz = elementSize(type);
  const leftCol = headCol - (sz.cols - 1);
  for (let r = headRow; r < headRow + sz.rows; r++) {
    for (let c = leftCol; c <= headCol; c++) {
      clearCell(rung, r, c);
    }
  }
}

// Clear all elements in a footprint area, removing any overlapping blocks fully
function clearFootprint(rung: { elements: { type: number; connectedWithTop: number; varType: number; varNum: number }[] }, topRow: number, leftCol: number, cols: number, rows: number) {
  for (let r = topRow; r < topRow + rows; r++) {
    for (let c = leftCol; c < leftCol + cols; c++) {
      const el = getEl(rung, r, c);
      if (!el || el.type === ELE_FREE) continue;
      if (el.type === ELE_BLOCK_BODY) {
        // Find and remove the parent block
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

function selectCell(rungIdx: number, row: number, col: number) {
  state.selectedCell = { rungIdx, row, col };

  if (state.editTool >= 0 && state.program) {
    const rung = state.program.rungs[rungIdx];
    if (!rung) return;

    if (state.editTool === ELE_FREE) {
      // Delete: if clicking on a block body, remove the whole block
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
    } else {
      const sz = elementSize(state.editTool);
      // For multi-cell blocks: click = top-left of footprint
      // Head goes at top-right (col + cols - 1) for blocks, rightmost for compare/operate
      const headCol = col + sz.cols - 1;
      const headRow = row;

      // Bounds check
      if (headCol >= COLS || row + sz.rows > ROWS) return;

      // Clear the footprint (removes overlapping blocks)
      clearFootprint(rung, row, col, sz.cols, sz.rows);

      // Place head
      const headEl = getEl(rung, headRow, headCol);
      if (!headEl) return;
      headEl.type = state.editTool;
      // For single-cell elements, we're done
      if (sz.cols > 1 || sz.rows > 1) {
        // Fill body cells with type 99
        for (let r = row; r < row + sz.rows; r++) {
          for (let c = col; c < col + sz.cols; c++) {
            if (r === headRow && c === headCol) continue;
            const bodyEl = getEl(rung, r, c);
            if (bodyEl) {
              bodyEl.type = ELE_BLOCK_BODY;
              bodyEl.varType = 0;
              bodyEl.varNum = 0;
              // Set connectedWithTop on cells below the top row of the block (left column)
              bodyEl.connectedWithTop = (r > row && c === col) ? 1 : 0;
            }
          }
        }
      }
    }
    state.dirty = true;
  }
}

function toggleTopConnection() {
  if (!state.selectedCell || !state.program) return;
  const { rungIdx, row, col } = state.selectedCell;
  const rung = state.program.rungs[rungIdx];
  if (!rung) return;
  const elIdx = row * COLS + col;
  if (elIdx >= rung.elements.length) return;
  rung.elements[elIdx].connectedWithTop = rung.elements[elIdx].connectedWithTop ? 0 : 1;
  state.dirty = true;
}

async function saveProgram() {
  if (!state.program || !state.dirty) return;
  state.error = '';
  try {
    await client.setProgram(state.program);
    state.dirty = false;
  } catch (e: unknown) {
    state.error = (e as Error).message;
  }
}

async function setState(s: LadderState) {
  try {
    await client.setState(s);
  } catch (e: unknown) {
    state.error = (e as Error).message;
  }
}

async function setVariable(varType: number, offset: number, value: number) {
  try {
    await client.setVariable(varType, offset, value);
  } catch (e: unknown) {
    state.error = (e as Error).message;
  }
}

export const ladderStore = {
  state,
  fetchProgram,
  setActiveSection,
  setEditTool,
  selectCell,
  toggleTopConnection,
  saveProgram,
  setState,
  setVariable,
};
