<script setup lang="ts">
import { computed, ref } from 'vue';
import type { Rung, Element } from '../generated/classicladder_client';
import {
  RUNG_WIDTH,
  RUNG_HEIGHT,
  CELL_STATE,
  CELL_INPUT,
  CELL_OUTPUT,
  ELE_FREE,
  ELE_INPUT,
  ELE_INPUT_NOT,
  ELE_RISING_INPUT,
  ELE_FALLING_INPUT,
  ELE_CONNECTION,
  ELE_TIMER,
  ELE_MONOSTABLE,
  ELE_COUNTER,
  ELE_TIMER_IEC,
  ELE_COMPAR,
  ELE_OUTPUT,
  ELE_OUTPUT_NOT,
  ELE_OUTPUT_SET,
  ELE_OUTPUT_RESET,
  ELE_OUTPUT_JUMP,
  ELE_OUTPUT_CALL,
  ELE_OUTPUT_OPERATE,
  ELE_UNUSABLE,
} from '../generated/classicladder_client';
import { ladderStore, elementSize, elementLabel, blockValueText } from '../stores/ladder';

const props = defineProps<{
  rung: Rung;
  rungIndex: number;
  symbols?: Map<string, string>;
  // Live cell state from the controller, RUNG_WIDTH * RUNG_HEIGHT of CELL_*
  // bits. Null while the PLC is unreachable, which draws everything cold —
  // showing the last known colours would claim a machine still running.
  cells?: number[] | null;
  // Editing draws no colours at all, as 2.9's edit mode does: a rung being
  // rewritten is not the rung the engine is scanning.
  editing?: boolean;
}>();

const emit = defineEmits<{
  (e: 'cellClick', row: number, col: number): void;
}>();

const COLS = RUNG_WIDTH;
const ROWS = RUNG_HEIGHT;
const CELL_W = 80;
const CELL_H = 40;
const RAIL_W = 4;

const ELE_BLOCK_BODY = ELE_UNUSABLE;

function getElement(row: number, col: number): Element {
  const idx = row * COLS + col;
  if (props.rung.elements && idx < props.rung.elements.length) {
    return props.rung.elements[idx];
  }
  return { type: 0, connectedWithTop: 0, varType: 0, varNum: 0 };
}

// --- Live state ---
//
// The engine works out, for every cell it scans, whether the element conducts
// (CELL_STATE), whether power reaches it (CELL_INPUT) and whether power leaves
// it (CELL_OUTPUT); this component only decides which of those colours which
// stroke. That split is the point: 2.9 draws an element with the energized pen
// when DynamicState is set, and its vertical branch link when DynamicInput is,
// and matching it here means matching it exactly rather than approximately.

function bitsAt(row: number, col: number): number {
  if (props.editing || !props.cells) return 0;
  return props.cells[row * COLS + col] ?? 0;
}

// live: the element itself carries power — its whole drawing lights up.
function live(row: number, col: number): boolean {
  return (bitsAt(row, col) & CELL_STATE) !== 0;
}

function livePowerIn(row: number, col: number): boolean {
  return (bitsAt(row, col) & CELL_INPUT) !== 0;
}

function livePowerOut(row: number, col: number): boolean {
  return (bitsAt(row, col) & CELL_OUTPUT) !== 0;
}

// A block's terminals are wired per row: input k arrives at the left column of
// the footprint, output k leaves from the head's column.
function blockInLive(headRow: number, headCol: number, row: number, cols: number): boolean {
  return livePowerIn(headRow + row, headCol - (cols - 1));
}

function blockOutLive(headRow: number, headCol: number, row: number): boolean {
  return livePowerOut(headRow + row, headCol);
}

// How a variable is written is the store's business, because the backend serves
// the table: see elementLabel in stores/ladder.ts. This component keeps only
// the decision of which cells carry a label at all.
function symbolOrVar(el: Element): string {
  if (el.type === ELE_FREE || el.type === ELE_CONNECTION || el.type === ELE_BLOCK_BODY) return '';
  const name = elementLabel(el);
  if (props.symbols?.has(name)) return props.symbols.get(name)!;
  return name;
}

// What a block shows below its name: the value while the PLC runs, the preset
// while editing or when there is nothing live — the rule 2.9 uses.
function blockText(el: Element): string {
  return blockValueText(el, props.editing === true);
}

function isContact(type: number): boolean {
  return type >= ELE_INPUT && type <= ELE_FALLING_INPUT;
}

function isCoil(type: number): boolean {
  return type >= ELE_OUTPUT && type <= ELE_OUTPUT_CALL;
}

function isBlock(type: number): boolean {
  return type === ELE_TIMER || type === ELE_MONOSTABLE ||
         type === ELE_COUNTER || type === ELE_TIMER_IEC;
}

function blockName(type: number): string {
  switch (type) {
    case ELE_TIMER: return 'Timer';
    case ELE_MONOSTABLE: return 'Mono';
    case ELE_COUNTER: return 'Counter';
    case ELE_TIMER_IEC: return 'Timer IEC';
    default: return '';
  }
}

// How many rows does a block span?
function blockRows(type: number): number {
  if (type === ELE_COUNTER) return 4;
  return 2; // Timer, Mono, TimerIEC
}

const svgWidth = computed(() => COLS * CELL_W + 2 * RAIL_W);
const svgHeight = computed(() => {
  // While editing, always offer the full grid: a rung you cannot reach the
  // lower rows of is a rung you cannot add a branch to.
  if (props.editing) return ROWS * CELL_H;
  let lastRow = 0;
  for (let r = 0; r < ROWS; r++) {
    for (let c = 0; c < COLS; c++) {
      if (getElement(r, c).type !== ELE_FREE) lastRow = r;
    }
  }
  return Math.max((lastRow + 2), 2) * CELL_H;
});

interface CellInfo { row: number; col: number; el: Element; }

const gridCells = computed((): CellInfo[] => {
  const result: CellInfo[] = [];
  const usedRows = svgHeight.value / CELL_H;
  for (let r = 0; r < usedRows; r++) {
    for (let c = 0; c < COLS; c++) {
      result.push({ row: r, col: c, el: getElement(r, c) });
    }
  }
  return result;
});

// Coordinate helpers — match GTK: x = left edge of cell, y = top edge of cell
// W = CELL_W, H = CELL_H. GTK uses Width/3, Width/4, Height/2, Height/3, Height/4
const W = CELL_W;
const H = CELL_H;
const W3 = Math.round(W / 3);
const W4 = Math.round(W / 4);
const H2 = Math.round(H / 2);
const H3 = Math.round(H / 3);
const H4 = Math.round(H / 4);

function cx(col: number): number { return RAIL_W + col * W; }
function cy(row: number): number { return row * H; }
function cmy(row: number): number { return cy(row) + H2; }

const hoverCell = ref<{ row: number; col: number } | null>(null);

function onHover(row: number, col: number) {
  hoverCell.value = { row, col };
}
function onLeave() {
  hoverCell.value = null;
}

const hoverPreview = computed(() => {
  if (!props.editing || !hoverCell.value) return null;
  const tool = ladderStore.state.editTool;
  if (tool < 0) return null; // no tool selected
  const sz = elementSize(tool);
  const { row, col } = hoverCell.value;
  // Bounds check
  if (col + sz.cols > COLS || row + sz.rows > ROWS) return null;
  return { x: cx(col), y: cy(row), w: sz.cols * W, h: sz.rows * H };
});

// The selection outlines the whole element, not the cell that was clicked: a
// block covers several cells and is selected as one thing.
const selected = computed(() => {
  const sel = ladderStore.state.selectedCell;
  if (!props.editing || !sel || sel.rungIdx !== props.rungIndex) return null;
  const sz = elementSize(getElement(sel.row, sel.col).type);
  const leftCol = sel.col - (sz.cols - 1);
  return { x: cx(leftCol), y: cy(sel.row), w: sz.cols * W, h: sz.rows * H };
});
</script>

<template>
  <div class="rung" :class="{ editing }">
    <slot name="header">
      <div class="rung-header">
        <span class="rung-num">{{ rungIndex }}</span>
        <span class="rung-label" v-if="rung.label">{{ rung.label }}</span>
        <span class="rung-comment" v-if="rung.comment">{{ rung.comment }}</span>
      </div>
    </slot>
    <svg :width="svgWidth" :height="svgHeight + 8" :viewBox="`0 -8 ${svgWidth} ${svgHeight + 8}`" class="rung-svg">
      <!-- Power rails -->
      <line :x1="RAIL_W/2" y1="-8" :x2="RAIL_W/2" :y2="svgHeight" class="rail"/>
      <line :x1="svgWidth - RAIL_W/2" y1="-8" :x2="svgWidth - RAIL_W/2" :y2="svgHeight" class="rail"/>

      <template v-for="cell in gridCells" :key="`${cell.row}-${cell.col}`">
        <!-- Vertical top-connection: at LEFT edge of cell, from cmy(row) up to cmy(row-1).
             2.9 colours this from the lower cell's DynamicInput. -->
        <line v-if="cell.el.connectedWithTop && cell.row > 0 && cell.el.type !== ELE_BLOCK_BODY"
              :x1="cx(cell.col)" :y1="cmy(cell.row)"
              :x2="cx(cell.col)" :y2="cmy(cell.row - 1)" class="wire"
              :class="{ live: livePowerIn(cell.row, cell.col) }"/>

        <!-- Connection (horizontal wire through full cell) -->
        <line v-if="cell.el.type === ELE_CONNECTION"
              :x1="cx(cell.col)" :y1="cmy(cell.row)"
              :x2="cx(cell.col) + W" :y2="cmy(cell.row)" class="wire"
              :class="{ live: live(cell.row, cell.col) }"/>

        <!-- Contacts: bars at W/3 and 2*W/3, wires from edges to bars -->
        <g v-if="isContact(cell.el.type)" class="clickable" :class="{ live: live(cell.row, cell.col) }">
          <!-- Horizontal wires -->
          <line :x1="cx(cell.col)" :y1="cmy(cell.row)"
                :x2="cx(cell.col) + W3" :y2="cmy(cell.row)" class="wire"/>
          <line :x1="cx(cell.col) + 2*W3" :y1="cmy(cell.row)"
                :x2="cx(cell.col) + W" :y2="cmy(cell.row)" class="wire"/>
          <!-- Vertical bars -->
          <line :x1="cx(cell.col) + W3" :y1="cy(cell.row) + H4"
                :x2="cx(cell.col) + W3" :y2="cy(cell.row) + H - H4" class="contact-bar"/>
          <line :x1="cx(cell.col) + 2*W3" :y1="cy(cell.row) + H4"
                :x2="cx(cell.col) + 2*W3" :y2="cy(cell.row) + H - H4" class="contact-bar"/>
          <!-- Negation slash -->
          <line v-if="cell.el.type === ELE_INPUT_NOT"
                :x1="cx(cell.col) + W3" :y1="cy(cell.row) + H - H4"
                :x2="cx(cell.col) + 2*W3" :y2="cy(cell.row) + H4" class="negation"/>
          <!-- Rising/Falling edge markers -->
          <g v-if="cell.el.type === ELE_RISING_INPUT">
            <line :x1="cx(cell.col) + W3" :y1="cy(cell.row) + 2*H3"
                  :x2="cx(cell.col) + W/2" :y2="cy(cell.row) + H3" class="contact-bar"/>
            <line :x1="cx(cell.col) + W/2" :y1="cy(cell.row) + H3"
                  :x2="cx(cell.col) + 2*W3" :y2="cy(cell.row) + 2*H3" class="contact-bar"/>
          </g>
          <g v-if="cell.el.type === ELE_FALLING_INPUT">
            <line :x1="cx(cell.col) + W3" :y1="cy(cell.row) + H3"
                  :x2="cx(cell.col) + W/2" :y2="cy(cell.row) + 2*H3" class="contact-bar"/>
            <line :x1="cx(cell.col) + W/2" :y1="cy(cell.row) + 2*H3"
                  :x2="cx(cell.col) + 2*W3" :y2="cy(cell.row) + H3" class="contact-bar"/>
          </g>
          <!-- Variable name label (above, clear of element) -->
          <text :x="cx(cell.col) + W/2" :y="cy(cell.row) + H4 - 4" class="lbl contact-lbl">{{ symbolOrVar(cell.el) }}</text>
        </g>

        <!-- Coils: arcs at W/4 to 3W/4, wires from edges -->
        <g v-else-if="isCoil(cell.el.type)" class="clickable" :class="{ live: live(cell.row, cell.col) }">
          <!-- Horizontal wires (stop at arc edges W/4 and 3W/4) -->
          <line :x1="cx(cell.col)" :y1="cmy(cell.row)"
                :x2="cx(cell.col) + W4" :y2="cmy(cell.row)" class="wire"/>
          <line :x1="cx(cell.col) + W - W4" :y1="cmy(cell.row)"
                :x2="cx(cell.col) + W" :y2="cmy(cell.row)" class="wire"/>
          <!-- Coil parentheses as arcs -->
          <path :d="`M ${cx(cell.col)+W4+W4/2} ${cy(cell.row)+H4} A ${W4/2} ${H4} 0 0 0 ${cx(cell.col)+W4+W4/2} ${cy(cell.row)+H-H4}`" class="coil-arc"/>
          <path :d="`M ${cx(cell.col)+W-W4-W4/2} ${cy(cell.row)+H4} A ${W4/2} ${H4} 0 0 1 ${cx(cell.col)+W-W4-W4/2} ${cy(cell.row)+H-H4}`" class="coil-arc"/>
          <!-- S/R/J/C letter -->
          <text v-if="cell.el.type === ELE_OUTPUT_NOT" :x="cx(cell.col)+W/2" :y="cmy(cell.row)" class="coil-t">/</text>
          <text v-if="cell.el.type === ELE_OUTPUT_SET" :x="cx(cell.col)+W/2" :y="cmy(cell.row)" class="coil-t">S</text>
          <text v-if="cell.el.type === ELE_OUTPUT_RESET" :x="cx(cell.col)+W/2" :y="cmy(cell.row)" class="coil-t">R</text>
          <text v-if="cell.el.type === ELE_OUTPUT_JUMP" :x="cx(cell.col)+W/2" :y="cmy(cell.row)" class="coil-t">J</text>
          <text v-if="cell.el.type === ELE_OUTPUT_CALL" :x="cx(cell.col)+W/2" :y="cmy(cell.row)" class="coil-t">C</text>
          <!-- Variable name label -->
          <text :x="cx(cell.col) + W/2" :y="cy(cell.row) + H4 - 4" class="lbl coil-lbl">{{ symbolOrVar(cell.el) }}</text>
        </g>

        <!-- Timer/Monostable/Counter/TimerIEC blocks
             Head element is at the RIGHT column of the 2-col block.
             Block box extends LEFT by one cell width from the head.
             Each terminal wire is coloured on its own, as 2.9 does: the box
             itself never lights up, only the power arriving and leaving. -->
        <g v-else-if="isBlock(cell.el.type)" class="clickable">
          <!-- Box: x from cx(col)+W/3-W, width W+W/3, height depends on block type -->
          <rect :x="cx(cell.col) + W3 - W" :y="cy(cell.row) + H3"
                :width="W + W3" :height="blockRows(cell.el.type) === 4 ? 3*H + H3 : H + H3" class="block-rect"/>
          <!-- Block title text (centered in box: box starts at cy+H3, height H+H3 for timer) -->
          <text :x="cx(cell.col) + W3 - W + (W+W3)/2" :y="cy(cell.row) + H3 + (blockRows(cell.el.type) === 4 ? (3*H+H3)/2 - 8 : (H+H3)/2 - 5)" class="blk-title">{{ blockName(cell.el.type) }}</text>
          <!-- Variable/symbol name -->
          <text :x="cx(cell.col) + W3 - W + (W+W3)/2" :y="cy(cell.row) + H3 + (blockRows(cell.el.type) === 4 ? (3*H+H3)/2 + 6 : (H+H3)/2 + 7)" class="blk-var">{{ symbolOrVar(cell.el) }}</text>
          <!-- Live value (or preset while editing), as 2.9 shows inside the box -->
          <text :x="cx(cell.col) + W3 - W + (W+W3)/2" :y="cy(cell.row) + H3 + (blockRows(cell.el.type) === 4 ? (3*H+H3)/2 + 18 : (H+H3)/2 + 18)" class="blk-val">{{ blockText(cell.el) }}</text>

          <!-- Timer/Mono: E input, C input, D output, R output -->
          <template v-if="cell.el.type === ELE_TIMER || cell.el.type === ELE_MONOSTABLE">
            <!-- E input wire + label -->
            <line :x1="cx(cell.col) - W" :y1="cmy(cell.row)"
                  :x2="cx(cell.col) - W + W3" :y2="cmy(cell.row)" class="wire"
                  :class="{ live: blockInLive(cell.row, cell.col, 0, 2) }"/>
            <text :x="cx(cell.col) - W + W3 + 4" :y="cmy(cell.row)" class="term">{{ cell.el.type === ELE_MONOSTABLE ? 'I^' : 'E' }}</text>
            <!-- C input wire + label (timer only; a monostable has one input) -->
            <template v-if="cell.el.type === ELE_TIMER">
              <line :x1="cx(cell.col) - W" :y1="cmy(cell.row + 1)"
                    :x2="cx(cell.col) - W + W3" :y2="cmy(cell.row + 1)" class="wire"
                    :class="{ live: blockInLive(cell.row, cell.col, 1, 2) }"/>
              <text :x="cx(cell.col) - W + W3 + 4" :y="cmy(cell.row + 1)" class="term">C</text>
            </template>
            <!-- D output wire + label -->
            <line :x1="cx(cell.col) + 2*W3" :y1="cmy(cell.row)"
                  :x2="cx(cell.col) + W" :y2="cmy(cell.row)" class="wire"
                  :class="{ live: blockOutLive(cell.row, cell.col, 0) }"/>
            <text :x="cx(cell.col) + 2*W3 - 4" :y="cmy(cell.row)" class="term-r">{{ cell.el.type === ELE_MONOSTABLE ? 'R' : 'D' }}</text>
            <!-- R output wire + label (timer only) -->
            <template v-if="cell.el.type === ELE_TIMER">
              <line :x1="cx(cell.col) + 2*W3" :y1="cmy(cell.row + 1)"
                    :x2="cx(cell.col) + W" :y2="cmy(cell.row + 1)" class="wire"
                    :class="{ live: blockOutLive(cell.row, cell.col, 1) }"/>
              <text :x="cx(cell.col) + 2*W3 - 4" :y="cmy(cell.row + 1)" class="term-r">R</text>
            </template>
          </template>

          <!-- Timer IEC: I input, Q output (single row of I/O) -->
          <template v-if="cell.el.type === ELE_TIMER_IEC">
            <line :x1="cx(cell.col) - W" :y1="cmy(cell.row)"
                  :x2="cx(cell.col) - W + W3" :y2="cmy(cell.row)" class="wire"
                  :class="{ live: blockInLive(cell.row, cell.col, 0, 2) }"/>
            <text :x="cx(cell.col) - W + W3 + 4" :y="cmy(cell.row)" class="term">I</text>
            <line :x1="cx(cell.col) + 2*W3" :y1="cmy(cell.row)"
                  :x2="cx(cell.col) + W" :y2="cmy(cell.row)" class="wire"
                  :class="{ live: blockOutLive(cell.row, cell.col, 0) }"/>
            <text :x="cx(cell.col) + 2*W3 - 4" :y="cmy(cell.row)" class="term-r">Q</text>
          </template>

          <!-- Counter: R/P/U/D inputs at rows 0-3, E/D/F outputs at rows 0-2 -->
          <template v-if="cell.el.type === ELE_COUNTER">
            <template v-for="(name, row) in ['R', 'P', 'U', 'D']" :key="`cin-${row}`">
              <line :x1="cx(cell.col) - W" :y1="cmy(cell.row + row)"
                    :x2="cx(cell.col) - W + W3" :y2="cmy(cell.row + row)" class="wire"
                    :class="{ live: blockInLive(cell.row, cell.col, row, 2) }"/>
              <text :x="cx(cell.col) - W + W3 + 4" :y="cmy(cell.row + row)" class="term">{{ name }}</text>
            </template>
            <template v-for="(name, row) in ['E', 'D', 'F']" :key="`cout-${row}`">
              <line :x1="cx(cell.col) + 2*W3" :y1="cmy(cell.row + row)"
                    :x2="cx(cell.col) + W" :y2="cmy(cell.row + row)" class="wire"
                    :class="{ live: blockOutLive(cell.row, cell.col, row) }"/>
              <text :x="cx(cell.col) + 2*W3 - 4" :y="cmy(cell.row + row)" class="term-r">{{ name }}</text>
            </template>
          </template>
        </g>

        <!-- Compare: spans 3 cells wide (extends 2 cells LEFT from head).
             2.9 draws both its wires with the energized pen when the
             comparison is true, and never colours the box. -->
        <g v-else-if="cell.el.type === ELE_COMPAR" class="clickable" :class="{ live: live(cell.row, cell.col) }">
          <rect :x="cx(cell.col) + W3 - 2*W" :y="cy(cell.row) + H4"
                :width="2*W + W3" :height="2*H4" class="block-rect"/>
          <line :x1="cx(cell.col) - 2*W" :y1="cmy(cell.row)"
                :x2="cx(cell.col) - 2*W + W3" :y2="cmy(cell.row)" class="wire"/>
          <line :x1="cx(cell.col) + 2*W3" :y1="cmy(cell.row)"
                :x2="cx(cell.col) + W" :y2="cmy(cell.row)" class="wire"/>
          <text :x="cx(cell.col) + W3 - 2*W + 4" :y="cy(cell.row) + H4 + 2" class="term">COMPARISON</text>
          <text :x="cx(cell.col) + W3 - 2*W + (2*W+W3)/2" :y="cmy(cell.row) + 4" class="blk-lbl">{{ symbolOrVar(cell.el) }}</text>
        </g>

        <!-- Operate output: spans 3 cells wide (extends 2 cells LEFT from head) -->
        <g v-else-if="cell.el.type === ELE_OUTPUT_OPERATE" class="clickable" :class="{ live: live(cell.row, cell.col) }">
          <rect :x="cx(cell.col) + W3 - 2*W" :y="cy(cell.row) + H4"
                :width="2*W + W3" :height="2*H4" class="block-rect"/>
          <line :x1="cx(cell.col) - 2*W" :y1="cmy(cell.row)"
                :x2="cx(cell.col) - 2*W + W3" :y2="cmy(cell.row)" class="wire"/>
          <line :x1="cx(cell.col) + 2*W3" :y1="cmy(cell.row)"
                :x2="cx(cell.col) + W" :y2="cmy(cell.row)" class="wire"/>
          <text :x="cx(cell.col) + W3 - 2*W + 4" :y="cy(cell.row) + H4 + 2" class="term">ASSIGNMENT</text>
          <text :x="cx(cell.col) + W3 - 2*W + (2*W+W3)/2" :y="cmy(cell.row) + 4" class="blk-lbl">{{ symbolOrVar(cell.el) }}</text>
        </g>
      </template>

      <!-- Selected cell, while editing -->
      <rect v-if="selected" :x="selected.x" :y="selected.y"
            :width="selected.w" :height="selected.h" class="selected-cell"/>

      <!-- Clickable grid overlay for editing -->
      <template v-for="cell in gridCells" :key="`click-${cell.row}-${cell.col}`">
        <rect :x="cx(cell.col)" :y="cy(cell.row)" :width="W" :height="H"
              class="click-overlay"
              @click="emit('cellClick', cell.row, cell.col)"
              @mouseenter="onHover(cell.row, cell.col)"
              @mouseleave="onLeave()"/>
      </template>

      <!-- Hover preview showing footprint of element to be placed -->
      <rect v-if="hoverPreview"
            :x="hoverPreview.x" :y="hoverPreview.y"
            :width="hoverPreview.w" :height="hoverPreview.h"
            class="hover-preview"/>
    </svg>
  </div>
</template>

<style scoped>
.rung { margin-bottom: 8px; border: 1px solid #45475a; border-radius: 4px; overflow: hidden; }
.rung.editing { border-color: #f9e2af; box-shadow: 0 0 0 1px #f9e2af inset; }
.rung-header { display: flex; align-items: center; gap: 8px; padding: 4px 8px; background: #1e1e2e; border-bottom: 1px solid #45475a; font-size: 11px; }
.rung-num { font-weight: 700; color: #89b4fa; min-width: 24px; }
.rung-label { color: #a6e3a1; font-weight: 600; }
.rung-comment { color: #a6adc8; font-style: italic; }
/* The grid is ten cells wide whatever the window is, so the drawing scales to
   the pane rather than leaving a dead strip beside it. The viewBox does the
   work; cell coordinates, and so hit-testing, are unaffected. */
.rung-svg { display: block; background: #181825; width: 100%; height: auto; }
.rail { stroke: #89b4fa; stroke-width: 3; }
.wire { stroke: #9399b2; stroke-width: 1.5; }
.contact-bar { stroke: #a6e3a1; stroke-width: 2; }
.negation { stroke: #f38ba8; stroke-width: 1.5; }
.coil-arc { fill: none; stroke: #f9e2af; stroke-width: 1.5; }
.coil-t { fill: #f9e2af; font-size: 10px; font-weight: 700; text-anchor: middle; dominant-baseline: middle; }
.lbl { font-size: 9px; text-anchor: middle; fill: #a6adc8; }
.contact-lbl { fill: #f38ba8; }
.coil-lbl { fill: #89b4fa; }
.block-rect { fill: #313244; stroke: #9399b2; stroke-width: 1; rx: 2; }
.blk-title { fill: #cba6f7; font-size: 10px; font-weight: 700; text-anchor: middle; }
.blk-var { fill: #a6adc8; font-size: 9px; text-anchor: middle; }
.blk-val { fill: #f9e2af; font-size: 9px; text-anchor: middle; font-family: monospace; }
.blk-lbl { fill: #a6adc8; font-size: 9px; text-anchor: middle; dominant-baseline: middle; }
.term { fill: #a6adc8; font-size: 8px; text-anchor: start; dominant-baseline: middle; }
.term-r { fill: #a6adc8; font-size: 8px; text-anchor: end; dominant-baseline: middle; }
.clickable { cursor: pointer; }

/* Energized: 2.9 draws powered elements in one bright colour and everything
   else in the default pen. One class on the element's group carries it, so a
   contact and its wires light together the way they do there. */
.live .wire, line.wire.live { stroke: #f38ba8; stroke-width: 2.5; }
.live .contact-bar { stroke: #f38ba8; }
.live .coil-arc { stroke: #f38ba8; stroke-width: 2.5; }
.live .coil-t { fill: #f38ba8; }
.live .block-rect { stroke: #f38ba8; }

.clickable:hover .wire { stroke: #89b4fa; }
.clickable:hover .contact-bar { stroke: #b5f0c7; }
.clickable:hover .coil-arc { stroke: #fce8b2; }
.click-overlay { fill: transparent; cursor: pointer; }
.click-overlay:hover { fill: rgba(137, 180, 250, 0.03); }
.hover-preview { fill: rgba(137, 180, 250, 0.12); stroke: #89b4fa; stroke-width: 1; stroke-dasharray: 3 2; pointer-events: none; }
.selected-cell { fill: rgba(249, 226, 175, 0.10); stroke: #f9e2af; stroke-width: 1.5; pointer-events: none; }
</style>
