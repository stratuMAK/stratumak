<script setup lang="ts">
import { computed, ref } from 'vue';
import { toolStore } from '../stores/tools';
import type { ToolEntry } from '../generated/tools_client';
import { LINEAR_KEYS, fieldUnit } from './toolform';
import ToolEditDialog from './ToolEditDialog.vue';

interface Column {
  key: keyof ToolEntry;
  label: string;
  type: 'int' | 'float' | 'text';
}

// Base labels; the unit suffix (mm/in/deg) is appended dynamically from the
// machine units so a mm header becomes "(in)" on an inch machine.
const columns: Column[] = [
  { key: 'toolno', label: 'Tool', type: 'int' },
  { key: 'pocketno', label: 'Poc', type: 'int' },
  { key: 'x_offset', label: 'X', type: 'float' },
  { key: 'y_offset', label: 'Y', type: 'float' },
  { key: 'z_offset', label: 'Z', type: 'float' },
  { key: 'a_offset', label: 'A', type: 'float' },
  { key: 'b_offset', label: 'B', type: 'float' },
  { key: 'c_offset', label: 'C', type: 'float' },
  { key: 'u_offset', label: 'U', type: 'float' },
  { key: 'v_offset', label: 'V', type: 'float' },
  { key: 'w_offset', label: 'W', type: 'float' },
  { key: 'diameter', label: 'Diam', type: 'float' },
  { key: 'frontangle', label: 'Front', type: 'float' },
  { key: 'backangle', label: 'Back', type: 'float' },
  { key: 'orientation', label: 'Orient', type: 'int' },
  { key: 'comment', label: 'Comment', type: 'text' },
];

// Header text with the live unit suffix appended (linear -> mm/in, angular -> deg).
function colHeader(col: Column): string {
  if (col.type !== 'float') return col.label;
  const unit = fieldUnit(col.key as Exclude<keyof ToolEntry, 'updated'>, toolStore.state.units.metric);
  return unit ? `${col.label} (${unit})` : col.label;
}

const sortedTools = computed(() =>
  [...toolStore.state.tools].sort((a, b) => a.toolno - b.toolno)
);

const existingToolnos = computed(() => toolStore.state.tools.map(t => t.toolno));

const dialogTool = ref<ToolEntry | null>(null);
const dialogIsNew = ref(false);

async function editTool(tool: ToolEntry) {
  // re-fetch fresh so a stale row copy can't silently revert concurrent edits
  const fresh = await toolStore.getTool(tool.toolno);
  dialogTool.value = fresh ?? { ...tool };
  dialogIsNew.value = false;
}

function addTool() {
  dialogTool.value = {
    toolno: 0, pocketno: 0,
    x_offset: 0, y_offset: 0, z_offset: 0,
    a_offset: 0, b_offset: 0, c_offset: 0,
    u_offset: 0, v_offset: 0, w_offset: 0,
    diameter: 0, frontangle: 0, backangle: 0,
    orientation: 0, comment: '',
    // no baseline stamp on a create: 0 = last-write-wins on the server
    updated: 0n,
  };
  dialogIsNew.value = true;
}

async function onDialogSave(tool: ToolEntry) {
  if (await toolStore.saveTool(tool)) {
    dialogTool.value = null;
  }
}

function onDialogCancel() {
  dialogTool.value = null;
}

function deleteTool(tool: ToolEntry, event: Event) {
  event.stopPropagation();
  if (confirm(`Delete tool ${tool.toolno}?`)) {
    toolStore.deleteTool(tool.toolno);
  }
}

// Cell text. LINEAR columns are stored in mm and scaled to display units before
// the existing numeric formatting is applied; angular/count columns are shown
// as-is.
function fmtCell(tool: ToolEntry, col: Column): string {
  const raw = tool[col.key];
  if (col.type === 'text') return String(raw);
  let n = Number(raw);
  if (LINEAR_KEYS.has(col.key as Exclude<keyof ToolEntry, 'updated'>)) {
    n *= toolStore.state.units.linearScale;
  }
  if (n === 0) return '0';
  if (col.type === 'int') return String(n);
  return n.toFixed(4).replace(/0+$/, '').replace(/\.$/, '');
}

defineExpose({ addTool });
</script>

<template>
  <div class="table-wrapper">
    <div class="toolbar">
      <button class="btn-add" @click="addTool">+ Add Tool</button>
    </div>
    <div class="table-scroll">
      <table>
        <thead>
          <tr>
            <th v-for="col in columns" :key="col.key" :class="col.type">
              {{ colHeader(col) }}
            </th>
            <th class="col-actions"></th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="tool in sortedTools"
            :key="tool.toolno"
          >
            <td v-for="col in columns" :key="col.key" :class="col.type">
              {{ fmtCell(tool, col) }}
            </td>
            <td class="col-actions">
              <button class="btn-edit" @click="editTool(tool)" title="Edit">&#x270E;</button>
              <button class="btn-del" @click="deleteTool(tool, $event)" title="Delete">&#x2716;</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div
      v-if="toolStore.state.tools.length === 0 && !toolStore.state.error && !toolStore.state.loading"
      class="empty"
    >
      No tools loaded. Click "Add Tool" to create one.
    </div>
  </div>

  <ToolEditDialog
    v-if="dialogTool"
    :tool="dialogTool"
    :isNew="dialogIsNew"
    :existingToolnos="existingToolnos"
    @save="onDialogSave"
    @cancel="onDialogCancel"
  />
</template>

<style scoped>
.table-wrapper {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.toolbar {
  padding: 6px 8px;
  border-bottom: 1px solid #333;
  display: flex;
  gap: 8px;
}

.btn-add {
  padding: 4px 12px;
  border: 1px solid #007acc;
  border-radius: 3px;
  background: #007acc;
  color: #fff;
  cursor: pointer;
  font-size: 12px;
}

.btn-add:hover {
  background: #1a8ad4;
}

.table-scroll {
  flex: 1;
  overflow-y: auto;
  overflow-x: auto;
}

table {
  width: 100%;
  border-collapse: separate;
  border-spacing: 0;
  font-size: 12px;
}

thead {
  position: sticky;
  top: 0;
  z-index: 2;
}

thead th {
  padding: 5px 4px;
  text-align: center;
  background: #333;
  border-bottom: 2px solid #555;
  border-right: 1px solid #444;
  font-weight: 600;
  white-space: nowrap;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.3px;
}

th.int { width: 55px; }
th.float { width: 75px; }
th.text { min-width: 120px; width: auto; }
.col-actions { width: 56px; }

td {
  padding: 4px 6px;
  border-bottom: 1px solid #333;
  border-right: 1px solid #2a2a2a;
  white-space: nowrap;
}

td.int, td.float {
  text-align: right;
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
}

td.text {
  text-align: left;
}

tbody tr:hover {
  background: #2a2d35;
}

.col-actions {
  text-align: center;
  white-space: nowrap;
}

.btn-edit, .btn-del {
  padding: 2px 5px;
  border: none;
  background: none;
  cursor: pointer;
  font-size: 13px;
  color: #888;
}

.btn-edit:hover {
  color: #4ec9b0;
}

.btn-del:hover {
  color: #f48771;
}

.empty {
  padding: 32px;
  text-align: center;
  color: #888;
}
</style>
