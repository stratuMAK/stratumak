<script setup lang="ts">
// The chart editor's tool palette — 2.9's sequential toolbar.
//
// The five tools at the right take two clicks: the first names one end, the
// second the other, and only then does anything change. Each says so in its
// title, because a tool that does nothing on the first click is otherwise
// indistinguishable from one that is broken.
import {
  ELE_SEQ_STEP,
  ELE_SEQ_TRANSITION,
  ELE_SEQ_COMMENT,
  EDIT_SEQ_INIT_STEP,
  EDIT_SEQ_STEP_AND_TRANS,
  EDIT_SEQ_START_MANY_STEPS,
  EDIT_SEQ_END_MANY_STEPS,
  EDIT_SEQ_START_MANY_TRANS,
  EDIT_SEQ_END_MANY_TRANS,
  EDIT_SEQ_LINK,
  EDIT_SEQ_POINTER,
  EDIT_SEQ_ERASER,
  sfcStore,
} from '../stores/sfc';

const state = sfcStore.state;

const drawTools = [
  { tool: ELE_SEQ_STEP, label: '□', title: 'Step — click an odd row to place one, click it again to remove it' },
  { tool: EDIT_SEQ_INIT_STEP, label: '◫', title: 'Initial step — the step the chart starts in' },
  { tool: ELE_SEQ_TRANSITION, label: '┼', title: 'Transition — click an even row' },
  { tool: EDIT_SEQ_STEP_AND_TRANS, label: '⊟', title: 'Step with a transition below it' },
  { tool: ELE_SEQ_COMMENT, label: 'abc', title: 'Comment — takes four cells across' },
];

const branchTools = [
  { tool: EDIT_SEQ_START_MANY_STEPS, label: '╤', title: 'Start parallel steps: click the first and last step of the row below a transition' },
  { tool: EDIT_SEQ_END_MANY_STEPS, label: '╧', title: 'End parallel steps: click the first and last step of the row above a transition' },
  { tool: EDIT_SEQ_START_MANY_TRANS, label: '┬', title: 'Start alternative branches: click the first and last transition of a row' },
  { tool: EDIT_SEQ_END_MANY_TRANS, label: '┴', title: 'End alternative branches: click the first and last transition of a row' },
  { tool: EDIT_SEQ_LINK, label: '↝', title: 'Link: click a transition and the step it should take or give to' },
];
</script>

<template>
  <div class="sfc-toolbar">
    <div class="tool-group">
      <span class="group-label">Draw</span>
      <button v-for="t in drawTools" :key="t.tool"
              :class="{ active: state.tool === t.tool }" :title="t.title"
              @click="sfcStore.setTool(t.tool)">{{ t.label }}</button>
    </div>
    <div class="tool-group">
      <span class="group-label">Branch</span>
      <button v-for="t in branchTools" :key="t.tool"
              :class="{ active: state.tool === t.tool }" :title="t.title"
              @click="sfcStore.setTool(t.tool)">{{ t.label }}</button>
    </div>
    <div class="tool-group">
      <button class="delete-btn" :class="{ active: state.tool === EDIT_SEQ_ERASER }"
              title="Delete what you click on"
              @click="sfcStore.setTool(EDIT_SEQ_ERASER)">✕</button>
      <button class="action-btn" title="Stop placing; clicking only selects"
              :class="{ active: state.tool === EDIT_SEQ_POINTER }"
              @click="sfcStore.setTool(EDIT_SEQ_POINTER)">↖</button>
    </div>
    <div class="tool-group right">
      <span class="pending" v-if="state.pending">Now click the other end…</span>
      <button class="action-btn apply" :disabled="state.busy" @click="sfcStore.applyEdit()">
        Apply
      </button>
      <button class="action-btn cancel" :disabled="state.busy" @click="sfcStore.cancelEdit()">
        Cancel
      </button>
    </div>
  </div>
</template>

<style scoped>
.sfc-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px;
  background: #1e1e2e;
  border: 1px solid #f9e2af;
  border-radius: 4px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}

.tool-group { display: flex; align-items: center; gap: 2px; }
.tool-group.right { margin-left: auto; gap: 6px; }

.group-label {
  font-size: 9px;
  color: #a6adc8;
  text-transform: uppercase;
  margin-right: 4px;
}

.tool-group button {
  background: #313244;
  border: 1px solid #45475a;
  color: #cdd6f4;
  padding: 4px 8px;
  border-radius: 3px;
  cursor: pointer;
  font-family: monospace;
  font-size: 12px;
  font-weight: 700;
  min-width: 32px;
  text-align: center;
}

.tool-group button:hover { background: #45475a; }

.tool-group button.active {
  background: #89b4fa;
  color: #1e1e2e;
  border-color: #89b4fa;
}

.pending { font-size: 11px; color: #f9e2af; }

.delete-btn { color: #f38ba8 !important; }
.delete-btn.active {
  background: #f38ba8 !important;
  color: #1e1e2e !important;
  border-color: #f38ba8 !important;
}

.action-btn.apply {
  background: #a6e3a1;
  color: #1e1e2e;
  border-color: #a6e3a1;
  font-family: inherit;
}

.action-btn.cancel { font-family: inherit; }
.action-btn:disabled { opacity: 0.4; cursor: default; }
</style>
