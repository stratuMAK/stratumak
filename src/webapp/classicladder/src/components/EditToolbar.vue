<script setup lang="ts">
import {
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
} from '../generated/classicladder_client';
import { ladderStore } from '../stores/ladder';

const contactTypes = [
  { type: ELE_INPUT, label: '| |', title: 'Normally open contact' },
  { type: ELE_INPUT_NOT, label: '|/|', title: 'Normally closed contact' },
  { type: ELE_RISING_INPUT, label: '|P|', title: 'Rising edge contact' },
  { type: ELE_FALLING_INPUT, label: '|N|', title: 'Falling edge contact' },
];

const coilTypes = [
  { type: ELE_OUTPUT, label: '( )', title: 'Coil' },
  { type: ELE_OUTPUT_NOT, label: '(/)', title: 'Negated coil' },
  { type: ELE_OUTPUT_SET, label: '(S)', title: 'Set coil' },
  { type: ELE_OUTPUT_RESET, label: '(R)', title: 'Reset coil' },
  { type: ELE_OUTPUT_JUMP, label: '(J)', title: 'Jump to a rung' },
  { type: ELE_OUTPUT_CALL, label: '(C)', title: 'Call a sub-routine' },
];

const otherTypes = [
  { type: ELE_CONNECTION, label: '───', title: 'Horizontal connection' },
  { type: ELE_TIMER, label: 'TMR', title: 'Timer' },
  { type: ELE_MONOSTABLE, label: 'MON', title: 'Monostable' },
  { type: ELE_COUNTER, label: 'CTR', title: 'Counter' },
  { type: ELE_TIMER_IEC, label: 'IEC', title: 'IEC timer' },
  { type: ELE_COMPAR, label: 'CMP', title: 'Comparison' },
  { type: ELE_OUTPUT_OPERATE, label: 'OPR', title: 'Assignment' },
];

const state = ladderStore.state;
</script>

<template>
  <div class="edit-toolbar">
    <div class="tool-group">
      <span class="group-label">Contacts</span>
      <button
        v-for="t in contactTypes"
        :key="t.type"
        :class="{ active: state.editTool === t.type }"
        :title="t.title"
        @click="ladderStore.setEditTool(t.type)"
      >{{ t.label }}</button>
    </div>
    <div class="tool-group">
      <span class="group-label">Coils</span>
      <button
        v-for="t in coilTypes"
        :key="t.type"
        :class="{ active: state.editTool === t.type }"
        :title="t.title"
        @click="ladderStore.setEditTool(t.type)"
      >{{ t.label }}</button>
    </div>
    <div class="tool-group">
      <span class="group-label">Blocks</span>
      <button
        v-for="t in otherTypes"
        :key="t.type"
        :class="{ active: state.editTool === t.type }"
        :title="t.title"
        @click="ladderStore.setEditTool(t.type)"
      >{{ t.label }}</button>
    </div>
    <div class="tool-group">
      <button class="delete-btn" :class="{ active: state.editTool === ELE_FREE }"
              title="Delete the element in the cell you click" @click="ladderStore.setEditTool(ELE_FREE)">✕</button>
      <button class="action-btn" title="Join this cell to the one above (parallel branch)"
              @click="ladderStore.toggleTopConnection()">┬</button>
      <button class="action-btn" title="Stop placing; clicking only selects"
              :class="{ active: state.editTool === -1 }" @click="ladderStore.setEditTool(-1)">↖</button>
    </div>
    <div class="tool-group right">
      <button class="action-btn apply" :disabled="state.busy" @click="ladderStore.applyEdit()">
        Apply
      </button>
      <button class="action-btn cancel" :disabled="state.busy" @click="ladderStore.cancelEdit()">
        Cancel
      </button>
    </div>
  </div>
</template>

<style scoped>
.edit-toolbar {
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

.tool-group {
  display: flex;
  align-items: center;
  gap: 2px;
}

.tool-group.right {
  margin-left: auto;
  gap: 6px;
}

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

.tool-group button:hover {
  background: #45475a;
}

.tool-group button.active {
  background: #89b4fa;
  color: #1e1e2e;
  border-color: #89b4fa;
}

.delete-btn {
  color: #f38ba8 !important;
}

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

.action-btn.cancel {
  font-family: inherit;
}

.action-btn:disabled {
  opacity: 0.4;
  cursor: default;
}
</style>
