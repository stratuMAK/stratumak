<script setup lang="ts">
// The sequential (SFC) chart — 2.9's drawing_sequential.c.
//
// The geometry follows 2.9: steps and transitions live on a page grid, a step
// is a square at its cell, a transition a horizontal bar across one. Every line
// between them is drawn by the transition, not by the step: a transition knows
// which steps it takes and which it gives, and a step knows nothing at all.
//
// The same component draws the running chart and the one being edited, from
// whichever chart it is handed. 2.9 does that too, and for the same reason: an
// editor that drew its own approximation of the chart would be showing the
// author something other than what they are about to apply.
//
// Which page is drawn comes from the section being displayed, not from a
// chooser of its own: a sequential section *is* one chart page, so the section
// tabs already say which. A second selector would be a second way to answer the
// same question, and the two could disagree.
import { computed, ref } from 'vue';
import {
  SEQ_PAGE_WIDTH,
  SEQ_PAGE_HEIGHT,
  VAR_STEP_ACTIVITY,
  VAR_STEP_TIME,
  type Sequential,
  type Step,
  type Transition,
} from '../generated/classicladder_client';
import { ladderStore, formatVar } from '../stores/ladder';
import { liveStore, variableValue } from '../stores/live';
import { ELE_SEQ_STEP, ELE_SEQ_TRANSITION, ELE_SEQ_COMMENT, type SeqSelection } from '../stores/sfc';

const props = defineProps<{
  page: number;
  // The chart to draw. Left out, the running one is drawn; an editor hands in
  // its draft instead.
  chart?: Sequential | null;
  editing?: boolean;
  selected?: SeqSelection | null;
}>();

const emit = defineEmits<{
  // Cell coordinates, and whether the click landed in the upper half of the
  // cell — which is how the link tool learns which end of a transition was
  // meant.
  (e: 'cell-click', x: number, y: number, top: boolean): void;
}>();

const state = ladderStore.state;

// The cell size, and the thirds and quarters of it 2.9 lays everything out in.
// Integer division there, so integer division here — the drawing is a grid of
// whole pixels and half a pixel of drift shows up as a bar that misses a stem.
const CELL = 46;
const S2 = Math.floor(CELL / 2);
const S3 = Math.floor(CELL / 3);
const S4 = Math.floor(CELL / 4);

const page = computed(() => props.page);

const chart = computed(() => (props.chart !== undefined ? props.chart : state.sequential));

const steps = computed(() =>
  (chart.value?.steps ?? [])
    .map((s, index) => ({ ...s, index }))
    .filter(s => s.used && s.page === page.value));

const transitions = computed(() =>
  (chart.value?.transitions ?? [])
    .map((t, index) => ({ ...t, index }))
    .filter(t => t.used && t.page === page.value));

const comments = computed(() =>
  (chart.value?.comments ?? [])
    .map((c, index) => ({ ...c, index }))
    .filter(c => c.used && c.page === page.value));

const isEmpty = computed(() =>
  steps.value.length === 0 && transitions.value.length === 0 && comments.value.length === 0);

// While editing, nothing is drawn live: the chart on screen is the draft, and
// its steps are not the ones the engine is running. Lighting one of them would
// be claiming the machine is somewhere it is not. 2.9 stops the animation in
// edit mode for the same reason.
function stepActive(stepNumber: number): boolean {
  if (props.editing) return false;
  return variableValue(VAR_STEP_ACTIVITY, stepNumber) === 1;
}

function stepSeconds(stepNumber: number): number | null {
  if (props.editing) return null;
  return variableValue(VAR_STEP_TIME, stepNumber);
}

// A transition is labelled with its condition, through the symbol table if the
// variable has a name — the same rule the ladder view uses, so the two do not
// call the same signal different things.
function conditionName(varType: number, varNum: number): string {
  const name = formatVar(varType, varNum);
  return state.symbolMap.get(name) ?? name;
}

function conditionTrue(varType: number, varNum: number): boolean {
  if (props.editing) return false;
  return variableValue(varType, varNum) === 1;
}

function isSelected(type: number, index: number): boolean {
  return props.selected?.type === type && props.selected.offset === index;
}

// --- The lines between the elements ---
//
// Slot -1 means "no link", and a transition's link arrays are read up to the
// first of them. A slot naming a step on another page cannot be drawn on this
// one, so it is skipped rather than drawn at whatever cell that step occupies
// here — that would be a line to a box that is not there.

function stepAt(index: number): (Step & { index: number }) | null {
  const s = chart.value?.steps[index];
  if (!s || !s.used || s.page !== page.value) return null;
  return { ...s, index };
}

function transitionAt(index: number): (Transition & { index: number }) | null {
  const t = chart.value?.transitions[index];
  if (!t || !t.used || t.page !== page.value) return null;
  return { ...t, index };
}

// linkedSteps reads one slot array up to its first empty slot, which is where
// the engine stops reading it too.
function linkedSteps(slots: number[]): (Step & { index: number })[] {
  const out: (Step & { index: number })[] = [];
  for (const idx of slots) {
    if (idx === -1) break;
    const s = stepAt(idx);
    if (s) out.push(s);
  }
  return out;
}

interface Seg { x1: number; y1: number; x2: number; y2: number }
// `atStep` labels sit beside a step rather than under a transition, and are
// written leftwards from their x: the chevron is drawn where 2.9 writes the
// number, and a number on top of a chevron is neither.
interface Label { x: number; y: number; text: string; atStep?: boolean }

interface ChartArt {
  segs: Seg[];       // single lines
  bars: Seg[];       // the doubled bars of a parallel branch
  labels: Label[];   // step numbers written beside a cross-step chevron
}

const art = computed((): ChartArt => {
  const segs: Seg[] = [];
  const bars: Seg[] = [];
  const labels: Label[] = [];
  const line = (x1: number, y1: number, x2: number, y2: number) => segs.push({ x1, y1, x2, y2 });

  // Several transitions can cross to the same step, and their labels would
  // land on top of each other. 2.9 shifts each one along and separates them
  // with a semicolon; the offsets are per step and reset every redraw.
  const crossOffsetAbove = new Map<number, number>();
  const crossOffsetBelow = new Map<number, number>();
  const stackLabel = (
    offsets: Map<number, number>, stepIndex: number, x: number, y: number, text: string,
  ) => {
    const off = offsets.get(stepIndex) ?? 0;
    labels.push({ x: x - off, y, text: off === 0 ? text : `${text};`, atStep: true });
    offsets.set(stepIndex, off + (off !== 0 ? 3 : 0) + 15);
  };

  for (const t of transitions.value) {
    const x = t.posiX * CELL;
    const y = t.posiY * CELL;

    // The step this transition takes, and the one it gives to, when they are
    // where a reader expects them: directly above and directly below. When they
    // are not, the stem stops short and a chevron says where the link goes.
    const above = linkedSteps(t.stepsToDeactivate)[0] ?? null;
    const below = linkedSteps(t.stepsToActivate)[0] ?? null;
    const directAbove = above !== null && above.posiX === t.posiX && above.posiY === t.posiY - 1;
    const directBelow = below !== null && below.posiX === t.posiX && below.posiY === t.posiY + 1;

    // The vertical stem through the cell, run out to the neighbouring cells
    // where a step is waiting there.
    line(x + S2, directAbove ? y : y + S3, x + S2, directBelow ? y + CELL + 1 : y + CELL - S3);

    // A parallel branch: the second and later steps get a doubled bar spanning
    // from this transition out to them, and a stub down (or up) into each.
    for (const s of linkedSteps(t.stepsToActivate).slice(1)) {
      const sx = s.posiX * CELL;
      bars.push({ x1: x + S3, y1: y + CELL - S4 + 2, x2: sx + CELL - S3, y2: y + CELL - S4 + 2 });
      bars.push({ x1: x + S3, y1: y + CELL - S4 + 4, x2: sx + CELL - S3, y2: y + CELL - S4 + 4 });
      line(sx + S2, y + CELL - S4 + 4, sx + S2, y + CELL + 1);
    }
    for (const s of linkedSteps(t.stepsToDeactivate).slice(1)) {
      const sx = s.posiX * CELL;
      bars.push({ x1: x + S3, y1: y + S4 - 2, x2: sx + CELL - S3, y2: y + S4 - 2 });
      bars.push({ x1: x + S3, y1: y + S4 - 4, x2: sx + CELL - S3, y2: y + S4 - 4 });
      line(sx + S2, y, sx + S2, y + S4 - 4);
    }

    // Transitions linked to each other: one branch of the chart is taken and
    // the others are not, so they share a single bar rather than a doubled one.
    for (const idx of t.transLinkedForStart) {
      if (idx === -1) break;
      const other = transitionAt(idx);
      if (other) line(x + S2, y + S3, other.posiX * CELL + S2, y + S3);
    }
    for (const idx of t.transLinkedForEnd) {
      if (idx === -1) break;
      const other = transitionAt(idx);
      if (other) line(x + S2, y + CELL - S3, other.posiX * CELL + S2, y + CELL - S3);
    }

    // A cross step: the transition gives to a step somewhere else on the page,
    // which is how a chart returns to its beginning. Drawing the line itself
    // would run it across everything in between, so both ends get a chevron and
    // the number of the step at the other end.
    //
    // Not when the transition is one of several linked for the end of a branch:
    // there the bar already says where the flow goes.
    if (above && below && !directBelow && t.transLinkedForEnd[0] === -1) {
      line(x + S3, y + CELL - S3, x + S2, y + CELL - S3 + S4);
      line(x + CELL - S3, y + CELL - S3, x + S2, y + CELL - S3 + S4);
      line(x + S2, y + CELL - S3, x + S2, y + CELL - S3 + S4);
      labels.push({
        x: x + S3, y: y + CELL - S3 + S4 + 13, text: String(below.stepNumber),
      });

      const sx = below.posiX * CELL;
      const sy = below.posiY * CELL;
      line(sx + S3, sy - S2, sx + S2, sy - S4);
      line(sx + CELL - S3, sy - S2, sx + S2, sy - S4);
      line(sx + S2, sy - S4, sx + S2, sy + 1);
      stackLabel(crossOffsetAbove, below.index, sx + S3 - 3, sy - S4 - 4, String(above.stepNumber));
    }

    // The same the other way up: a transition that takes a step from somewhere
    // else on the page. 2.9 leaves this undrawn (a TODO in
    // drawing_sequential.c), which makes a link the editor can create invisible
    // — so it is drawn here, mirrored, where 2.9 draws nothing at all.
    if (above && below && !directAbove && t.transLinkedForStart[0] === -1) {
      line(x + S3, y + S3, x + S2, y + S3 - S4);
      line(x + CELL - S3, y + S3, x + S2, y + S3 - S4);
      line(x + S2, y + S3, x + S2, y + S3 - S4);
      labels.push({
        x: x + S3, y: y + S3 - S4 - 3, text: String(above.stepNumber),
      });

      const sx = above.posiX * CELL;
      const sy = above.posiY * CELL;
      line(sx + S3, sy + CELL + S2, sx + S2, sy + CELL + S4);
      line(sx + CELL - S3, sy + CELL + S2, sx + S2, sy + CELL + S4);
      line(sx + S2, sy + CELL + S4, sx + S2, sy + CELL - 1);
      stackLabel(crossOffsetBelow, above.index, sx + S3 - 3, sy + CELL + S4 + 4, String(below.stepNumber));
    }
  }

  return { segs, bars, labels };
});

// --- Clicking a cell ---

const svgEl = ref<SVGSVGElement | null>(null);

// The click is turned into a cell here rather than put on each element: an
// author places things on empty cells, and an empty cell has nothing to hang a
// handler on. The element under the pointer does not matter — the editor
// searches the chart for what is at that cell, which is what 2.9 does.
function onClick(e: MouseEvent) {
  if (!props.editing || !svgEl.value) return;
  const box = svgEl.value.getBoundingClientRect();
  const px = e.clientX - box.left;
  const py = e.clientY - box.top;
  const x = Math.floor(px / CELL);
  const y = Math.floor(py / CELL);
  if (x < 0 || x >= SEQ_PAGE_WIDTH || y < 0 || y >= SEQ_PAGE_HEIGHT) return;
  emit('cell-click', x, y, py - y * CELL < CELL / 2);
}

const gridRows = Array.from({ length: SEQ_PAGE_HEIGHT + 1 }, (_, i) => i);
const gridCols = Array.from({ length: SEQ_PAGE_WIDTH + 1 }, (_, i) => i);

const svgWidth = SEQ_PAGE_WIDTH * CELL;
const svgHeight = SEQ_PAGE_HEIGHT * CELL;
</script>

<template>
  <div class="sfc">
    <p class="empty" v-if="!chart">Loading chart…</p>
    <p class="empty" v-else-if="isEmpty && !editing">
      This chart has nothing on it yet.
    </p>

    <div class="sfc-scroll" v-else>
      <svg ref="svgEl" :width="svgWidth" :height="svgHeight" class="sfc-svg"
           :class="{ editing }" @click="onClick">
        <!-- The grid, only while editing: it is what says where a click will
             put something, and it is noise on a chart nobody is editing. -->
        <g v-if="editing" class="grid">
          <line v-for="r in gridRows" :key="`gr-${r}`"
                :x1="0" :y1="r * CELL" :x2="svgWidth" :y2="r * CELL"/>
          <line v-for="c in gridCols" :key="`gc-${c}`"
                :x1="c * CELL" :y1="0" :x2="c * CELL" :y2="svgHeight"/>
        </g>

        <!-- Links next, so the boxes sit on top of them -->
        <line v-for="(l, i) in art.segs" :key="`g-${i}`"
              :x1="l.x1" :y1="l.y1" :x2="l.x2" :y2="l.y2" class="link"/>
        <line v-for="(l, i) in art.bars" :key="`b-${i}`"
              :x1="l.x1" :y1="l.y1" :x2="l.x2" :y2="l.y2" class="link bar"/>
        <text v-for="(l, i) in art.labels" :key="`x-${i}`"
              :x="l.x" :y="l.y" class="cross-lbl" :class="{ 'at-step': l.atStep }">{{ l.text }}</text>

        <!-- Transitions: a bar across the cell, with its condition beside it -->
        <g v-for="t in transitions" :key="`t-${t.index}`" class="transition">
          <rect v-if="isSelected(ELE_SEQ_TRANSITION, t.index)"
                :x="t.posiX * CELL + 1" :y="t.posiY * CELL + 1"
                :width="CELL - 2" :height="CELL - 2" class="sel-box"/>
          <line :x1="t.posiX * CELL + S3" :y1="t.posiY * CELL + S2"
                :x2="t.posiX * CELL + CELL - S3" :y2="t.posiY * CELL + S2"
                class="trans-bar"
                :class="{ live: conditionTrue(t.varTypeCondi, t.varNumCondi) }"/>
          <!-- The condition starts at three quarters across, as 2.9 puts it,
               and runs right. Centred over the bar it would sit where a cross
               step writes the number of the step it returns to. -->
          <text :x="t.posiX * CELL + 3 * S4" :y="t.posiY * CELL + S2"
                class="trans-lbl"
                :class="{ live: conditionTrue(t.varTypeCondi, t.varNumCondi) }">
            {{ conditionName(t.varTypeCondi, t.varNumCondi) }}
          </text>
        </g>

        <!-- Steps: a square holding the step number, doubled if it is initial -->
        <g v-for="s in steps" :key="`s-${s.index}`" class="step">
          <rect v-if="isSelected(ELE_SEQ_STEP, s.index)"
                :x="s.posiX * CELL + 1" :y="s.posiY * CELL + 1"
                :width="CELL - 2" :height="CELL - 2" class="sel-box"/>
          <rect :x="s.posiX * CELL + 4" :y="s.posiY * CELL + 4"
                :width="CELL - 8" :height="CELL - 8"
                class="step-box" :class="{ live: stepActive(s.stepNumber) }"/>
          <rect v-if="s.initStep"
                :x="s.posiX * CELL + 7" :y="s.posiY * CELL + 7"
                :width="CELL - 14" :height="CELL - 14"
                class="step-init"/>
          <text :x="s.posiX * CELL + S2" :y="s.posiY * CELL + S2"
                class="step-num" :class="{ live: stepActive(s.stepNumber) }">
            {{ s.stepNumber }}
          </text>
          <text v-if="stepActive(s.stepNumber) && stepSeconds(s.stepNumber) !== null"
                :x="s.posiX * CELL + S2" :y="s.posiY * CELL + CELL - 8"
                class="step-time">
            {{ stepSeconds(s.stepNumber) }}s
          </text>
        </g>

        <!-- Author's comments, four cells wide -->
        <g v-for="c in comments" :key="`c-${c.index}`">
          <rect :x="c.posiX * CELL + 2" :y="c.posiY * CELL + 2"
                :width="4 * CELL - 4" :height="CELL - 4" class="comment-box"
                :class="{ selected: isSelected(ELE_SEQ_COMMENT, c.index) }"/>
          <text :x="c.posiX * CELL + 6" :y="c.posiY * CELL + S2"
                class="seq-comment">{{ c.comment }}</text>
        </g>
      </svg>
    </div>

    <p class="stale-note" v-if="chart && !editing && !liveStore.state.connected">
      Not connected to the controller — no step is shown active.
    </p>
  </div>
</template>

<style scoped>
.sfc { display: flex; flex-direction: column; }

.sfc-scroll { overflow: auto; }
.sfc-svg { background: #181825; display: block; }
.sfc-svg.editing { cursor: crosshair; }

.grid line { stroke: #313244; stroke-width: 1; }

/* Links carry no state: which step is active is said by the step's own box, and
   a bar spanning a parallel branch belongs to all of them at once. */
.link { stroke: #9399b2; stroke-width: 1.5; }
.link.bar { stroke-width: 1; }
.cross-lbl { fill: #cdd6f4; font-size: 10px; font-family: monospace; }
.cross-lbl.at-step { text-anchor: end; dominant-baseline: middle; }

.sel-box { fill: none; stroke: #f9e2af; stroke-width: 2; stroke-dasharray: 3 2; }

.step-box { fill: #313244; stroke: #9399b2; stroke-width: 1.5; }
.step-box.live { stroke: #f38ba8; stroke-width: 2.5; }
.step-init { fill: none; stroke: #cba6f7; stroke-width: 1; }
.step-num {
  fill: #cdd6f4; font-size: 13px; font-weight: 700;
  text-anchor: middle; dominant-baseline: middle;
}
.step-num.live { fill: #f38ba8; }
.step-time { fill: #f9e2af; font-size: 9px; text-anchor: middle; font-family: monospace; }

.trans-bar { stroke: #9399b2; stroke-width: 2.5; }
.trans-bar.live { stroke: #f38ba8; }
.trans-lbl {
  fill: #a6adc8; font-size: 9px; font-family: monospace;
  dominant-baseline: middle;
}
.trans-lbl.live { fill: #f38ba8; }

.comment-box { fill: none; stroke: #45475a; stroke-width: 1; }
.comment-box.selected { stroke: #f9e2af; stroke-width: 2; }
.seq-comment { fill: #6c7086; font-size: 10px; font-style: italic; dominant-baseline: middle; }

.empty { color: #a6adc8; padding: 40px 0; text-align: center; }
.stale-note { font-size: 11px; color: #f9e2af; padding: 6px 2px 0; }
</style>
