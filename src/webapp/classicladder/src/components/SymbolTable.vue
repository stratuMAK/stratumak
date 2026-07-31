<script setup lang="ts">
// 2.9's symbols_gtk.c: the operator-facing names for variables.
//
// A symbol is what turns "%Q0" into "estop-ok" everywhere the ladder is drawn,
// so this is worth more than its size suggests on a machine somebody else
// wired. Rows are edited locally and the whole table is written at once,
// because that is the shape of set_symbols — the table is one thing.
import { ref, computed, watch } from 'vue';
import {
  LGT_VAR_NAME,
  LGT_SYMBOL_STRING,
  LGT_SYMBOL_COMMENT,
  type Symbol as ClSymbol,
} from '../generated/classicladder_client';
import { ladderStore, parseVar, formatVar } from '../stores/ladder';

const state = ladderStore.state;

// The edit buffer. Only rows that name something are kept; the trailing blank
// row is how a new one is added, so there is no "add" button to hunt for.
const rows = ref<ClSymbol[]>([]);
const filter = ref('');
// What the last load produced, so in-progress edits can be told apart from a
// refetch — see the program watcher below.
const loadedRows = ref('[]');
const changedOnServer = ref(false);

function serverRows(): ClSymbol[] {
  const symbols = state.program?.symbols ?? [];
  const out = symbols
    .filter(s => s.varName !== '' || s.symbol !== '')
    .map(s => ({ ...s }));
  out.push({ varName: '', symbol: '', comment: '' });
  return out;
}

function load() {
  rows.value = serverRows();
  loadedRows.value = JSON.stringify(rows.value);
  changedOnServer.value = false;
}

const dirty = computed(() => JSON.stringify(rows.value) !== loadedRows.value);

// The program is refetched for many reasons that are not this table's — the
// header Reload button, another client's edit moving the generation. A clean
// buffer just reloads; one holding edits is kept, with a note when the table
// really moved on the server, rather than silently discarded.
watch(() => state.program, () => {
  if (!dirty.value) {
    load();
    return;
  }
  if (JSON.stringify(serverRows()) !== loadedRows.value) changedOnServer.value = true;
});

load();

const visible = computed(() => {
  const f = filter.value.trim().toLowerCase();
  if (!f) return rows.value.map((row, index) => ({ row, index }));
  return rows.value
    .map((row, index) => ({ row, index }))
    .filter(({ row }) =>
      row.varName.toLowerCase().includes(f)
      || row.symbol.toLowerCase().includes(f)
      || row.comment.toLowerCase().includes(f));
});

// A variable name that does not exist would be a symbol nothing can ever
// resolve, so it is flagged as it is typed rather than silently kept.
function nameProblem(row: ClSymbol): string {
  if (row.varName === '') return '';
  const ref_ = parseVar(row.varName);
  if (!ref_) return 'no such variable';
  const canonical = formatVar(ref_.varType, ref_.offset);
  if (canonical !== row.varName) return `write it ${canonical}`;
  return '';
}

function onRowInput(index: number) {
  // Typing into the last row means a new one is wanted below it.
  const last = rows.value[rows.value.length - 1];
  if (index === rows.value.length - 1 && (last.varName !== '' || last.symbol !== '')) {
    rows.value.push({ varName: '', symbol: '', comment: '' });
  }
}

function removeRow(index: number) {
  rows.value.splice(index, 1);
  if (rows.value.length === 0) rows.value.push({ varName: '', symbol: '', comment: '' });
}

const problems = computed(() => rows.value.filter(r => nameProblem(r) !== '').length);

async function save() {
  const symbols = rows.value
    .filter(r => r.varName !== '' && r.symbol !== '')
    .map(r => ({
      varName: r.varName,
      symbol: r.symbol,
      comment: r.comment,
    }));
  if (await ladderStore.setSymbols(symbols)) load();
}
</script>

<template>
  <div class="symbols">
    <div class="sym-toolbar">
      <input v-model="filter" placeholder="Filter…" class="filter-input" />
      <button class="btn" :disabled="state.busy || problems > 0" @click="save"
              :title="problems > 0 ? 'Some rows name a variable that does not exist' : ''">
        Save symbols
      </button>
      <button class="btn btn-plain" :disabled="state.busy" @click="load">Revert</button>
    </div>

    <p class="server-note" v-if="changedOnServer">
      The symbol table changed on the controller while you were editing — Save
      overwrites it with what is shown here, Revert discards your edits and
      reloads it.
    </p>

    <table class="sym-table">
      <thead>
        <tr>
          <th>Variable</th>
          <th>Symbol</th>
          <th>Comment</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="{ row, index } in visible" :key="index">
          <td>
            <input v-model="row.varName" :maxlength="LGT_VAR_NAME - 1" placeholder="%Q0"
                   :class="{ bad: nameProblem(row) !== '' }" @input="onRowInput(index)" />
            <span class="err" v-if="nameProblem(row)">{{ nameProblem(row) }}</span>
          </td>
          <td>
            <input v-model="row.symbol" :maxlength="LGT_SYMBOL_STRING - 1" placeholder="estop-ok"
                   @input="onRowInput(index)" />
          </td>
          <td>
            <input v-model="row.comment" :maxlength="LGT_SYMBOL_COMMENT - 1"
                   placeholder="what it means" @input="onRowInput(index)" />
          </td>
          <td class="actions">
            <button v-if="row.varName || row.symbol" @click="removeRow(index)" title="Remove">✕</button>
          </td>
        </tr>
      </tbody>
    </table>

    <p class="hint">
      Symbols replace variable names wherever the ladder is drawn. A row with no
      symbol is dropped when saved.
    </p>
  </div>
</template>

<style scoped>
.sym-toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  align-items: center;
}

.filter-input { flex: 1; }

.btn-plain {
  background: #313244;
  color: #cdd6f4;
}
.btn-plain:hover { background: #45475a; }
.btn:disabled { opacity: 0.4; cursor: default; }

.sym-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.sym-table th {
  text-align: left;
  font-size: 11px;
  text-transform: uppercase;
  color: #a6adc8;
  padding: 4px 6px;
  border-bottom: 1px solid #45475a;
  font-weight: 600;
}

.sym-table td {
  padding: 2px 6px;
  border-bottom: 1px solid #313244;
  vertical-align: top;
}

.sym-table td:first-child { width: 130px; }
.sym-table td:nth-child(2) { width: 150px; }

.sym-table input { width: 100%; font-size: 12px; padding: 3px 6px; }
.sym-table input.bad { border-color: #f38ba8; }

.err { font-size: 10px; color: #f38ba8; display: block; padding: 1px 2px 0; }

.actions { width: 28px; text-align: right; }
.actions button {
  background: none;
  border: none;
  color: #f38ba8;
  cursor: pointer;
  font-size: 12px;
  padding: 3px;
}

.hint { font-size: 11px; color: #6c7086; margin-top: 10px; }
.server-note { font-size: 11px; color: #f9e2af; margin-bottom: 10px; }
</style>
