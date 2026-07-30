<script setup lang="ts">
// The sequential (SFC) chart — 2.9's drawing_sequential.c, as a viewer.
//
// Read-only by ruling: the chart loads, runs and saves with the rest of the
// project, and being able to watch which step a machine is sitting in is most
// of what anyone opens it for. Editing it is a separate surface the controller
// does not offer, and building one on evidence of demand beats guessing.
//
// The geometry follows 2.9: steps and transitions live on a 16x16 page grid,
// a step is a square at its cell, a transition a horizontal bar across one.
import { computed, ref } from 'vue';
import { MAX_SEQ_PAGES, VAR_STEP_ACTIVITY, VAR_STEP_TIME } from '../generated/classicladder_client';
import { ladderStore, formatVar } from '../stores/ladder';
import { liveStore, variableValue } from '../stores/live';

const state = ladderStore.state;

const CELL = 46;
const PAGE_W = 16;
const PAGE_H = 16;

const page = ref(0);

// Only pages that hold something are worth offering.
const usedPages = computed(() => {
  const seq = state.sequential;
  if (!seq) return [];
  const pages = new Set<number>();
  for (const s of seq.steps) if (s.used) pages.add(s.page);
  for (const t of seq.transitions) if (t.used) pages.add(t.page);
  for (const c of seq.comments) if (c.used) pages.add(c.page);
  return [...pages].filter(p => p >= 0 && p < MAX_SEQ_PAGES).sort((a, b) => a - b);
});

const steps = computed(() =>
  (state.sequential?.steps ?? [])
    .map((s, index) => ({ ...s, index }))
    .filter(s => s.used && s.page === page.value));

const transitions = computed(() =>
  (state.sequential?.transitions ?? [])
    .map((t, index) => ({ ...t, index }))
    .filter(t => t.used && t.page === page.value));

const comments = computed(() =>
  (state.sequential?.comments ?? []).filter(c => c.used && c.page === page.value));

// %X is indexed by the step *number* the author chose, not by the slot the step
// occupies — the API says so, and getting it wrong would light the wrong box.
function stepActive(stepNumber: number): boolean {
  return variableValue(VAR_STEP_ACTIVITY, stepNumber) === 1;
}

function stepSeconds(stepNumber: number): number | null {
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
  return variableValue(varType, varNum) === 1;
}

// A transition's link to the steps it deactivates (above) and activates
// (below). Steps are named by index here, unlike %X — the arrays hold slots.
function stepAt(index: number) {
  const s = state.sequential?.steps[index];
  return s?.used && s.page === page.value ? s : null;
}

interface Link { x1: number; y1: number; x2: number; y2: number; live: boolean }

const links = computed((): Link[] => {
  const out: Link[] = [];
  for (const t of transitions.value) {
    const tx = t.posiX * CELL + CELL / 2;
    const ty = t.posiY * CELL + CELL / 2;
    for (const idx of t.stepsToDeactivate) {
      const s = stepAt(idx);
      if (!s) continue;
      out.push({
        x1: s.posiX * CELL + CELL / 2, y1: s.posiY * CELL + CELL - 4,
        x2: tx, y2: ty,
        live: stepActive(s.stepNumber),
      });
    }
    for (const idx of t.stepsToActivate) {
      const s = stepAt(idx);
      if (!s) continue;
      out.push({
        x1: tx, y1: ty,
        x2: s.posiX * CELL + CELL / 2, y2: s.posiY * CELL + 4,
        live: stepActive(s.stepNumber),
      });
    }
  }
  return out;
});

const svgWidth = PAGE_W * CELL;
const svgHeight = PAGE_H * CELL;
</script>

<template>
  <div class="sfc">
    <div class="sfc-toolbar" v-if="usedPages.length > 1">
      <span class="group-label">Page</span>
      <button v-for="p in usedPages" :key="p" :class="{ active: page === p }" @click="page = p">
        {{ p }}
      </button>
    </div>

    <p class="empty" v-if="!state.sequential">Loading chart…</p>
    <p class="empty" v-else-if="usedPages.length === 0">
      This program has no sequential chart. Add a section whose language is
      Sequential to start one.
    </p>

    <div class="sfc-scroll" v-else>
      <svg :width="svgWidth" :height="svgHeight" class="sfc-svg">
        <!-- Links first, so the boxes sit on top of them -->
        <line v-for="(l, i) in links" :key="`l-${i}`"
              :x1="l.x1" :y1="l.y1" :x2="l.x2" :y2="l.y2"
              class="link" :class="{ live: l.live }"/>

        <!-- Transitions: a bar across the cell, with its condition beside it -->
        <g v-for="t in transitions" :key="`t-${t.index}`" class="transition">
          <line :x1="t.posiX * CELL + 8" :y1="t.posiY * CELL + CELL / 2"
                :x2="t.posiX * CELL + CELL - 8" :y2="t.posiY * CELL + CELL / 2"
                class="trans-bar"
                :class="{ live: conditionTrue(t.varTypeCondi, t.varNumCondi) }"/>
          <text :x="t.posiX * CELL + CELL - 4" :y="t.posiY * CELL + CELL / 2 - 5"
                class="trans-lbl"
                :class="{ live: conditionTrue(t.varTypeCondi, t.varNumCondi) }">
            {{ conditionName(t.varTypeCondi, t.varNumCondi) }}
          </text>
        </g>

        <!-- Steps: a square holding the step number, doubled if it is initial -->
        <g v-for="s in steps" :key="`s-${s.index}`" class="step">
          <rect :x="s.posiX * CELL + 4" :y="s.posiY * CELL + 4"
                :width="CELL - 8" :height="CELL - 8"
                class="step-box" :class="{ live: stepActive(s.stepNumber) }"/>
          <rect v-if="s.initStep"
                :x="s.posiX * CELL + 7" :y="s.posiY * CELL + 7"
                :width="CELL - 14" :height="CELL - 14"
                class="step-init"/>
          <text :x="s.posiX * CELL + CELL / 2" :y="s.posiY * CELL + CELL / 2"
                class="step-num" :class="{ live: stepActive(s.stepNumber) }">
            {{ s.stepNumber }}
          </text>
          <text v-if="stepActive(s.stepNumber) && stepSeconds(s.stepNumber) !== null"
                :x="s.posiX * CELL + CELL / 2" :y="s.posiY * CELL + CELL - 8"
                class="step-time">
            {{ stepSeconds(s.stepNumber) }}s
          </text>
        </g>

        <!-- Author's comments -->
        <text v-for="(c, i) in comments" :key="`c-${i}`"
              :x="c.posiX * CELL + 4" :y="c.posiY * CELL + CELL / 2"
              class="seq-comment">{{ c.comment }}</text>
      </svg>
    </div>

    <p class="stale-note" v-if="state.sequential && !liveStore.state.connected">
      Not connected to the controller — no step is shown active.
    </p>
  </div>
</template>

<style scoped>
.sfc { display: flex; flex-direction: column; }

.sfc-toolbar {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 10px;
}

.group-label {
  font-size: 9px;
  color: #a6adc8;
  text-transform: uppercase;
  margin-right: 4px;
}

.sfc-toolbar button {
  background: #313244;
  border: 1px solid #45475a;
  color: #cdd6f4;
  padding: 3px 10px;
  border-radius: 3px;
  cursor: pointer;
  font-size: 12px;
}
.sfc-toolbar button.active {
  background: #89b4fa;
  color: #1e1e2e;
  border-color: #89b4fa;
}

.sfc-scroll { overflow: auto; }
.sfc-svg { background: #181825; display: block; }

.link { stroke: #9399b2; stroke-width: 1.5; }
.link.live { stroke: #f38ba8; stroke-width: 2.5; }

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
.trans-lbl { fill: #a6adc8; font-size: 9px; text-anchor: end; font-family: monospace; }
.trans-lbl.live { fill: #f38ba8; }

.seq-comment { fill: #6c7086; font-size: 10px; font-style: italic; }

.empty { color: #a6adc8; padding: 40px 0; text-align: center; }
.stale-note { font-size: 11px; color: #f9e2af; padding: 6px 2px 0; }
</style>
