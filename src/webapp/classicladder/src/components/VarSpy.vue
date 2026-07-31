<script setup lang="ts">
// The spy windows — 2.9's spy_vars_gtk.c, which is two of them.
//
// A bit grid over %B, %I and %Q whose cells can be clicked to force the bit,
// and a watch list where an operator names any variable at all and reads or
// writes it. The app had the grid, read-only, over five hard-coded regions and
// nothing else: no timers, no counters, no s32, no floats, and no way to prove
// a rung by driving an input.
//
// Forcing is for probing. A coil's variable is written again on the very next
// scan, so a forced output holds only until the ladder disagrees — which is
// what makes it safe to offer and useless as a control.
import { ref, computed, watch } from 'vue';
import {
  VAR_MEM_BIT,
  VAR_PHYS_INPUT,
  VAR_PHYS_OUTPUT,
  VAR_ERROR_BIT,
} from '../generated/classicladder_client';
import { ladderStore, formatVar, parseVar, varIsWritable } from '../stores/ladder';
import { liveStore, variableValue, isBoolVar } from '../stores/live';

const state = ladderStore.state;

// --- Bit grid ---

const BIT_COLUMNS = [
  { varType: VAR_MEM_BIT, label: 'Memory bits' },
  { varType: VAR_PHYS_INPUT, label: 'Inputs' },
  { varType: VAR_PHYS_OUTPUT, label: 'Outputs' },
  { varType: VAR_ERROR_BIT, label: 'Error bits' },
];

// The grid shows a window into each region, as 2.9's offset entry does: a PLC
// configured with 500 bits should not paint 500 checkboxes.
const PAGE = 32;
const offsets = ref<Record<number, number>>({});

function countOf(varType: number): number {
  return state.varClasses.find(c => c.varType === varType)?.count ?? 0;
}

function offsetOf(varType: number): number {
  return offsets.value[varType] ?? 0;
}

function pageOf(varType: number): { offset: number; value: number | null }[] {
  const count = countOf(varType);
  const start = Math.min(offsetOf(varType), Math.max(0, count - 1));
  const out: { offset: number; value: number | null }[] = [];
  for (let i = start; i < Math.min(start + PAGE, count); i++) {
    out.push({ offset: i, value: variableValue(varType, i) });
  }
  return out;
}

function scroll(varType: number, by: number) {
  const count = countOf(varType);
  const next = Math.max(0, Math.min(offsetOf(varType) + by, Math.max(0, count - PAGE)));
  offsets.value = { ...offsets.value, [varType]: next };
}

async function toggleBit(varType: number, offset: number) {
  if (!varIsWritable(varType)) {
    state.error = `${formatVar(varType, offset)} is read-only.`;
    return;
  }
  const now = variableValue(varType, offset);
  if (now === null) return;
  await ladderStore.setVariable(varType, offset, now ? 0 : 1);
}

// --- The watch list ---
//
// Kept in the browser: it is the operator's scratch pad, not the program's, so
// it survives a reload here rather than being written into the controller.

const STORAGE_KEY = 'classicladder-watch-list';

interface WatchRow {
  text: string;      // what was typed
  varType: number;
  offset: number;
  format: 'dec' | 'hex' | 'bin';
}

const watchRows = ref<WatchRow[]>([]);
const newName = ref('');
// Half-typed values for the Set fields, keyed by the variable itself rather
// than by row position: removing a row above must not hand the value typed for
// %W0 to whatever row slides into its place.
const writeDrafts = ref<Record<string, string>>({});

function draftKey(row: WatchRow): string {
  return `${row.varType}-${row.offset}`;
}

function loadWatchList() {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return;
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return;
    watchRows.value = parsed.filter((r): r is WatchRow =>
      typeof r === 'object' && r !== null
      && typeof (r as WatchRow).text === 'string'
      && typeof (r as WatchRow).varType === 'number'
      && typeof (r as WatchRow).offset === 'number');
  } catch {
    // A corrupt scratch pad is not worth an error message; start empty.
  }
}

watch(watchRows, (rows) => {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(rows));
  } catch {
    // Storage full or disabled — the list still works for this session.
  }
}, { deep: true });

// The stored list names variables by type and offset, which only mean something
// once the naming table has arrived; re-render the text from it so a project
// with different sizes does not show names it no longer has.
watch(() => state.varClasses.length, (n) => {
  if (n === 0) return;
  for (const row of watchRows.value) {
    const name = formatVar(row.varType, row.offset);
    if (name !== '%?') row.text = name;
  }
});

loadWatchList();

function addWatch() {
  const ref_ = parseVar(newName.value);
  if (!ref_) {
    state.error = `"${newName.value}" is not a variable of this PLC.`;
    return;
  }
  const name = formatVar(ref_.varType, ref_.offset);
  if (watchRows.value.some(r => r.varType === ref_.varType && r.offset === ref_.offset)) {
    newName.value = '';
    return;
  }
  watchRows.value.push({ text: name, varType: ref_.varType, offset: ref_.offset, format: 'dec' });
  newName.value = '';
}

function removeWatch(index: number) {
  const [removed] = watchRows.value.splice(index, 1);
  if (removed) delete writeDrafts.value[draftKey(removed)];
}

function render(row: WatchRow): string {
  const v = variableValue(row.varType, row.offset);
  if (v === null) return '—';
  switch (row.format) {
    case 'hex': return `0x${(v >>> 0).toString(16).toUpperCase()}`;
    case 'bin': return (v >>> 0).toString(2);
    default: return String(v);
  }
}

async function writeWatch(index: number) {
  const row = watchRows.value[index];
  const text = (writeDrafts.value[draftKey(row)] ?? '').trim();
  if (text === '') return;
  const value = text.startsWith('0x') ? Number.parseInt(text.slice(2), 16) : Number(text);
  if (!Number.isFinite(value)) {
    state.error = `"${text}" is not a number.`;
    return;
  }
  if (!varIsWritable(row.varType)) {
    state.error = `${row.text} is read-only.`;
    return;
  }
  await ladderStore.setVariable(row.varType, row.offset, Math.trunc(value));
  writeDrafts.value = { ...writeDrafts.value, [draftKey(row)]: '' };
}

const symbolFor = computed(() => (varType: number, offset: number) =>
  state.symbolMap.get(formatVar(varType, offset)) ?? '');
</script>

<template>
  <div class="spy">
    <p class="stale-note" v-if="!liveStore.state.connected">
      Not connected to the controller — no values are shown.
    </p>

    <!-- Bit grid -->
    <div class="bit-columns">
      <div class="bit-column" v-for="col in BIT_COLUMNS" :key="col.varType"
           v-show="countOf(col.varType) > 0">
        <div class="col-head">
          <span class="col-label">{{ col.label }}</span>
          <span class="col-count">{{ countOf(col.varType) }}</span>
          <button :disabled="offsetOf(col.varType) === 0" @click="scroll(col.varType, -PAGE)">◀</button>
          <button :disabled="offsetOf(col.varType) + PAGE >= countOf(col.varType)"
                  @click="scroll(col.varType, PAGE)">▶</button>
        </div>
        <div class="bit-grid">
          <button
            v-for="item in pageOf(col.varType)"
            :key="item.offset"
            class="bit"
            :class="{ on: item.value === 1, unknown: item.value === null, ro: !varIsWritable(col.varType) }"
            :title="symbolFor(col.varType, item.offset) || formatVar(col.varType, item.offset)"
            @click="toggleBit(col.varType, item.offset)"
          >
            <span class="bit-name">{{ formatVar(col.varType, item.offset) }}</span>
            <span class="bit-val">{{ item.value === null ? '·' : item.value }}</span>
          </button>
        </div>
      </div>
    </div>

    <p class="hint">
      Click a bit to force it. A forced value holds only until the ladder writes
      it again, which for a coil's variable is the next scan — this is for
      probing, not for control. Read-only regions refuse the write.
    </p>

    <!-- Watch list -->
    <h3 class="watch-title">Watch list</h3>
    <div class="watch-add">
      <input v-model="newName" placeholder="%T0.V, %C1.D, %IW2 — or a symbol"
             @keyup.enter="addWatch" />
      <button class="btn" @click="addWatch">Watch</button>
    </div>

    <table class="watch-table" v-if="watchRows.length">
      <thead>
        <tr>
          <th>Variable</th>
          <th>Symbol</th>
          <th>Value</th>
          <th>Format</th>
          <th>Write</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row, index) in watchRows" :key="`${row.varType}-${row.offset}`">
          <td class="mono">{{ row.text }}</td>
          <td class="sym">{{ symbolFor(row.varType, row.offset) }}</td>
          <td class="mono val" :class="{ on: isBoolVar(row.varType) && variableValue(row.varType, row.offset) === 1 }">
            {{ render(row) }}
          </td>
          <td>
            <select v-model="row.format" :disabled="isBoolVar(row.varType)">
              <option value="dec">Dec</option>
              <option value="hex">Hex</option>
              <option value="bin">Bin</option>
            </select>
          </td>
          <td>
            <div class="write-cell" v-if="varIsWritable(row.varType)">
              <input v-model="writeDrafts[draftKey(row)]" :placeholder="isBoolVar(row.varType) ? '0 or 1' : 'value'"
                     @keyup.enter="writeWatch(index)" />
              <button @click="writeWatch(index)">Set</button>
            </div>
            <span class="ro-note" v-else>read-only</span>
          </td>
          <td class="actions">
            <button @click="removeWatch(index)" title="Stop watching">✕</button>
          </td>
        </tr>
      </tbody>
    </table>
    <p class="hint" v-else>
      Nothing watched yet. Any variable works — a timer's remaining time
      (%T0.V), a counter (%C1.V), an s32 pin (%IW2), a float (%QF0).
    </p>
  </div>
</template>

<style scoped>
.spy { display: flex; flex-direction: column; }

.bit-columns {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
}

.bit-column { min-width: 0; }

.col-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
  font-size: 11px;
}

.col-label { color: #cdd6f4; font-weight: 600; }
.col-count { color: #6c7086; font-family: monospace; margin-right: auto; }

.col-head button {
  background: #313244;
  border: 1px solid #45475a;
  color: #cdd6f4;
  border-radius: 3px;
  cursor: pointer;
  font-size: 10px;
  padding: 1px 6px;
}
.col-head button:disabled { opacity: 0.3; cursor: default; }

.bit-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(78px, 1fr));
  gap: 3px;
}

.bit {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 3px 6px;
  background: #1e1e2e;
  border: 1px solid #45475a;
  border-radius: 3px;
  font-size: 11px;
  cursor: pointer;
  color: #cdd6f4;
}

.bit.on { background: #1e3a2e; border-color: #a6e3a1; }
.bit.on .bit-val { color: #a6e3a1; }
.bit.unknown { opacity: 0.45; }
.bit.ro { cursor: default; }

.bit-name { color: #89b4fa; font-family: monospace; font-size: 10px; }
.bit-val { font-weight: 700; font-family: monospace; }

.watch-title { font-size: 13px; margin: 20px 0 8px; color: #cdd6f4; }

.watch-add {
  display: flex;
  gap: 8px;
  margin-bottom: 10px;
}
.watch-add input { flex: 1; }

.watch-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.watch-table th {
  text-align: left;
  font-size: 11px;
  text-transform: uppercase;
  color: #a6adc8;
  padding: 4px 6px;
  border-bottom: 1px solid #45475a;
  font-weight: 600;
}

.watch-table td {
  padding: 3px 6px;
  border-bottom: 1px solid #313244;
}

.watch-table .mono { font-family: monospace; }
.watch-table .val { font-weight: 700; }
.watch-table .val.on { color: #a6e3a1; }
.watch-table .sym { color: #a6e3a1; }
.watch-table select { width: 70px; padding: 2px 4px; font-size: 11px; }

.write-cell { display: flex; gap: 4px; }
.write-cell input { width: 80px; padding: 2px 6px; font-size: 11px; }
.write-cell button {
  background: #313244;
  border: 1px solid #45475a;
  color: #cdd6f4;
  border-radius: 3px;
  cursor: pointer;
  font-size: 11px;
  padding: 2px 8px;
}

.ro-note { color: #6c7086; font-size: 11px; }

.actions { width: 28px; text-align: right; }
.actions button {
  background: none;
  border: none;
  color: #f38ba8;
  cursor: pointer;
  font-size: 12px;
}

.hint { font-size: 11px; color: #6c7086; margin-top: 10px; line-height: 1.4; }
.stale-note { font-size: 11px; color: #f9e2af; margin-bottom: 10px; }
</style>
