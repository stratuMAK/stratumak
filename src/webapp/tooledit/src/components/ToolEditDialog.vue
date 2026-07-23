<script setup lang="ts">
import { ref, watch } from 'vue';
import type { ToolEntry } from '../generated/tools_client';
import { toolStore } from '../stores/tools';
import { fields, toForm, validateForm, type Field, type ToolForm } from './toolform';

const props = defineProps<{
  tool: ToolEntry | null;
  isNew: boolean;
  existingToolnos: number[];
}>();

const emit = defineEmits<{
  save: [tool: ToolEntry];
  cancel: [];
}>();

const form = ref<ToolForm>(toForm({
  toolno: 0, pocketno: 0,
  x_offset: 0, y_offset: 0, z_offset: 0,
  a_offset: 0, b_offset: 0, c_offset: 0,
  u_offset: 0, v_offset: 0, w_offset: 0,
  diameter: 0, frontangle: 0, backangle: 0,
  orientation: 0, comment: '', updated: 0n,
}));

watch(() => props.tool, (t) => {
  if (t) {
    form.value = toForm(t);
  }
}, { immediate: true });

function onInput(field: Field, event: Event) {
  form.value[field.key] = (event.target as HTMLInputElement).value;
}

const errors = ref<string[]>([]);

function onSave() {
  const { entry, errors: e } = validateForm(form.value, props.isNew, props.existingToolnos);
  errors.value = e;
  if (entry) emit('save', entry);
}
</script>

<template>
  <div class="dialog-overlay" @click.self="emit('cancel')">
    <div class="dialog">
      <div class="dialog-header">
        <h2>{{ isNew ? 'Add Tool' : `Edit Tool ${form.toolno}` }}</h2>
        <button class="btn-close" @click="emit('cancel')">&#x2716;</button>
      </div>
      <div class="dialog-body">
        <div v-for="field in fields" :key="field.key" class="field-row">
          <label :for="'f-' + field.key">{{ field.label }}</label>
          <input
            :id="'f-' + field.key"
            type="text"
            :inputmode="field.type === 'text' ? undefined : 'decimal'"
            :class="{ num: field.type !== 'text' }"
            :value="form[field.key]"
            :readonly="field.readonly && !isNew"
            @input="onInput(field, $event)"
          />
        </div>
      </div>
      <div v-if="errors.length" class="dialog-errors">
        <div v-for="(err, i) in errors" :key="i">{{ err }}</div>
      </div>
      <div v-if="toolStore.state.error" class="dialog-errors">
        {{ toolStore.state.error }}
      </div>
      <div class="dialog-footer">
        <button class="btn-cancel" @click="emit('cancel')">Cancel</button>
        <button class="btn-save" @click="onSave">Save</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}

.dialog {
  background: #252526;
  border: 1px solid #555;
  border-radius: 6px;
  width: 420px;
  max-height: 85vh;
  display: flex;
  flex-direction: column;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
}

.dialog-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  border-bottom: 1px solid #404040;
}

.dialog-header h2 {
  font-size: 14px;
  font-weight: 600;
}

.btn-close {
  border: none;
  background: none;
  color: #888;
  font-size: 16px;
  cursor: pointer;
  padding: 2px 6px;
}

.btn-close:hover {
  color: #f48771;
}

.dialog-body {
  padding: 12px 16px;
  overflow-y: auto;
  flex: 1;
}

.field-row {
  display: flex;
  align-items: center;
  margin-bottom: 6px;
}

.field-row label {
  width: 110px;
  font-size: 12px;
  color: #aaa;
  flex-shrink: 0;
}

.field-row input {
  flex: 1;
  padding: 4px 8px;
  border: 1px solid #444;
  border-radius: 3px;
  background: #1e1e1e;
  color: #d4d4d4;
  font-size: 12px;
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
}

.field-row input.num {
  text-align: right;
}

.field-row input:focus {
  border-color: #007acc;
  outline: none;
}

.field-row input[readonly] {
  color: #888;
  background: #2a2a2a;
}

.dialog-errors {
  padding: 4px 16px;
  color: #f48771;
  font-size: 11px;
}

.dialog-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid #404040;
}

.btn-cancel {
  padding: 5px 14px;
  border: 1px solid #555;
  border-radius: 3px;
  background: #3c3c3c;
  color: #d4d4d4;
  cursor: pointer;
}

.btn-save {
  padding: 5px 14px;
  border: 1px solid #007acc;
  border-radius: 3px;
  background: #007acc;
  color: #fff;
  cursor: pointer;
}

.btn-save:hover {
  background: #1a8ad4;
}

.btn-cancel:hover {
  background: #4a4a4a;
}
</style>
