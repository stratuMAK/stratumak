<script setup lang="ts">
import { computed, ref } from 'vue';
import { toolStore } from '../stores/tools';
import type { ToolEntry } from '../generated/tools_client';
import ToolEditDialog from './ToolEditDialog.vue';

interface Column {
  key: keyof ToolEntry;
  label: string;
  type: 'int' | 'float' | 'text';
}

const columns: Column[] = [
  { key: 'toolno', label: 'Tool', type: 'int' },
  { key: 'pocketno', label: 'Poc', type: 'int' },
  { key: 'x_offset', label: 'X (mm)', type: 'float' },
  { key: 'y_offset', label: 'Y (mm)', type: 'float' },
  { key: 'z_offset', label: 'Z (mm)', type: 'float' },
  { key: 'a_offset', label: 'A (deg)', type: 'float' },
  { key: 'b_offset', label: 'B (deg)', type: 'float' },
  { key: 'c_offset', label: 'C (deg)', type: 'float' },
  { key: 'u_offset', label: 'U (mm)', type: 'float' },
  { key: 'v_offset', label: 'V (mm)', type: 'float' },
  { key: 'w_offset', label: 'W (mm)', type: 'float' },
  { key: 'diameter', label: 'Diam (mm)', type: 'float' },
  { key: 'frontangle', label: 'Front (deg)', type: 'float' },
  { key: 'backangle', label: 'Back (deg)', type: 'float' },
  { key: 'orientation', label: 'Orient', type: 'int' },
  { key: 'comment', label: 'Comment', type: 'text' },
];

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

function fmtNum(val: unknown, type: string): string {
  if (type === 'text') return String(val);
  const n = Number(val);
  if (n === 0) return '0';
  if (type === 'int') return String(n);
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
              {{ col.label }}
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
              {{ fmtNum(tool[col.key], col.type) }}
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
