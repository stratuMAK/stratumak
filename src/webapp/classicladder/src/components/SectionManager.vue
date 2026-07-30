<script setup lang="ts">
// 2.9's manager_gtk.c: the list of sections, and adding and removing them.
//
// The rung chain inside a section belongs to the controller — add_section
// creates a section already holding one empty rung, and delete_section frees
// what it held — so this is a thin surface over those calls plus renaming.
import { ref, computed } from 'vue';
import { LGT_SECTION_NAME, SectionLanguage } from '../generated/classicladder_client';
import { ladderStore, usedSections, sectionRungs } from '../stores/ladder';

// 'Show' has to leave this tab as well as choose the section, or it changes
// something the operator cannot see and reads as a dead button. Which tab is
// open is the app's business, so it is asked rather than reached into.
const emit = defineEmits<{ (e: 'show'): void }>();

const state = ladderStore.state;
const sections = computed(() => usedSections());

function showSection(index: number) {
  ladderStore.setActiveSection(index);
  emit('show');
}

const newName = ref('');
const newLanguage = ref<SectionLanguage>(SectionLanguage.LADDER);
const newIsSubRoutine = ref(false);
const newSubRoutineNumber = ref(0);

// The numbers already taken, so the form can offer one that is not.
const takenSubRoutines = computed(() =>
  new Set(sections.value.filter(s => s.subRoutineNumber >= 0).map(s => s.subRoutineNumber)));

function nextFreeSubRoutine(): number {
  let n = 0;
  while (takenSubRoutines.value.has(n)) n++;
  return n;
}

async function addSection() {
  const name = newName.value.trim();
  if (!name) {
    state.error = 'A section needs a name.';
    return;
  }
  const sr = newIsSubRoutine.value ? newSubRoutineNumber.value : -1;
  if (await ladderStore.addSection(name, newLanguage.value, sr)) {
    newName.value = '';
    newSubRoutineNumber.value = nextFreeSubRoutine();
  }
}

async function removeSection(index: number, name: string) {
  const rungs = sectionRungs(index).length;
  const what = rungs > 0 ? ` and its ${rungs} rung${rungs === 1 ? '' : 's'}` : '';
  if (!window.confirm(`Delete section "${name}"${what}? This cannot be undone.`)) return;
  await ladderStore.deleteSection(index);
}

// Renaming goes through set_section, which validates the whole section —
// including the rung chain — so the current values are sent back untouched.
const editingName = ref<number>(-1);
const nameDraft = ref('');

function startRename(index: number, current: string) {
  editingName.value = index;
  nameDraft.value = current;
}

async function commitRename(index: number) {
  const prog = state.program;
  if (!prog) return;
  const name = nameDraft.value.trim();
  editingName.value = -1;
  if (!name || name === prog.sections[index].name) return;
  await ladderStore.setSection(index, { ...prog.sections[index], name });
}

newSubRoutineNumber.value = 0;
</script>

<template>
  <div class="sections">
    <table class="section-table">
      <thead>
        <tr>
          <th>Name</th>
          <th>Language</th>
          <th>Kind</th>
          <th>Rungs</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="sec in sections" :key="sec.index"
            :class="{ current: state.activeSection === sec.index }">
          <td>
            <input v-if="editingName === sec.index" v-model="nameDraft"
                   :maxlength="LGT_SECTION_NAME - 1"
                   @blur="commitRename(sec.index)" @keyup.enter="commitRename(sec.index)" />
            <button v-else class="link" @click="startRename(sec.index, sec.name)">{{ sec.name }}</button>
          </td>
          <td>{{ sec.language === SectionLanguage.LADDER ? 'Ladder' : 'Sequential' }}</td>
          <td>
            <span v-if="sec.subRoutineNumber >= 0" class="sr">SR{{ sec.subRoutineNumber }}</span>
            <span v-else class="main">main</span>
          </td>
          <td class="num">{{ sec.language === SectionLanguage.LADDER ? sectionRungs(sec.index).length : '—' }}</td>
          <td class="actions">
            <button @click="showSection(sec.index)">Show</button>
            <button class="danger" :disabled="state.busy || sections.length < 2"
                    :title="sections.length < 2 ? 'A program needs at least one section' : 'Delete this section'"
                    @click="removeSection(sec.index, sec.name)">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>

    <div class="add-section">
      <h3>Add a section</h3>
      <div class="add-row">
        <div class="field">
          <label for="sec-name">Name</label>
          <input id="sec-name" v-model="newName" :maxlength="LGT_SECTION_NAME - 1"
                 placeholder="unique name" @keyup.enter="addSection" />
        </div>
        <div class="field">
          <label for="sec-lang">Language</label>
          <select id="sec-lang" v-model.number="newLanguage">
            <option :value="SectionLanguage.LADDER">Ladder</option>
            <option :value="SectionLanguage.SEQUENTIAL">Sequential (SFC)</option>
          </select>
        </div>
        <div class="field check">
          <label>
            <input type="checkbox" v-model="newIsSubRoutine" />
            Sub-routine
          </label>
        </div>
        <div class="field" v-if="newIsSubRoutine">
          <label for="sec-sr">Number</label>
          <input id="sec-sr" type="number" min="0" v-model.number="newSubRoutineNumber" />
        </div>
        <button class="btn" :disabled="state.busy" @click="addSection">Add</button>
      </div>
      <p class="hint">
        A ladder section is created holding one empty rung; a sequential one gets
        a free chart page. A sub-routine runs only when a (C) coil calls its
        number.
      </p>
    </div>
  </div>
</template>

<style scoped>
.section-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  margin-bottom: 20px;
}

.section-table th {
  text-align: left;
  font-size: 11px;
  text-transform: uppercase;
  color: #a6adc8;
  padding: 4px 8px;
  border-bottom: 1px solid #45475a;
  font-weight: 600;
}

.section-table td {
  padding: 4px 8px;
  border-bottom: 1px solid #45475a;
}

.section-table tr.current td { background: #1e1e2e; }
.section-table td.num { font-family: monospace; color: #a6adc8; }

.section-table input { width: 100%; }

button.link {
  background: none;
  border: none;
  color: #89b4fa;
  cursor: pointer;
  font-size: 13px;
  padding: 0;
  text-align: left;
}
button.link:hover { text-decoration: underline; }

.sr { color: #f9e2af; font-family: monospace; font-size: 11px; }
.main { color: #6c7086; font-size: 11px; }

.actions { text-align: right; white-space: nowrap; }
.actions button {
  background: #313244;
  border: 1px solid #45475a;
  color: #cdd6f4;
  padding: 2px 8px;
  border-radius: 3px;
  cursor: pointer;
  font-size: 11px;
  margin-left: 4px;
}
.actions button:hover { background: #45475a; }
.actions button:disabled { opacity: 0.4; cursor: default; }
.actions button.danger { color: #f38ba8; }

.add-section h3 { font-size: 13px; margin-bottom: 8px; color: #cdd6f4; }

.add-row {
  display: flex;
  gap: 10px;
  align-items: flex-end;
  flex-wrap: wrap;
}

.field { flex: 0 0 auto; }
.field input, .field select { width: 160px; }
.field.check { align-self: center; }
.field.check label { display: flex; align-items: center; gap: 6px; margin: 0; }
.field.check input { width: auto; }

.hint { font-size: 11px; color: #6c7086; margin-top: 8px; line-height: 1.4; }
</style>
