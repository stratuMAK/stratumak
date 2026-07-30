<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import LadderRung from './LadderRung.vue';
import EditToolbar from './EditToolbar.vue';
import ElementProperties from './ElementProperties.vue';
import { LGT_LABEL, LGT_COMMENT, SectionLanguage } from '../generated/classicladder_client';
import { ladderStore, sectionRungs, usedSections } from '../stores/ladder';
import { liveStore, cellsOf } from '../stores/live';

const state = ladderStore.state;

const rungs = computed(() => sectionRungs(state.activeSection));
const sections = computed(() => usedSections());
const activeSection = computed(() => state.program?.sections[state.activeSection] ?? null);

// The label and comment belong to the rung, so they are edited with it — in
// the header, where they are read.
const draftLabel = ref('');
const draftComment = ref('');

watch(() => state.editRung, () => {
  draftLabel.value = state.draft?.label ?? '';
  draftComment.value = state.draft?.comment ?? '';
});

watch(draftLabel, v => { if (state.draft) state.draft.label = v; });
watch(draftComment, v => { if (state.draft) state.draft.comment = v; });

function onCellClick(rungIdx: number, row: number, col: number) {
  ladderStore.selectCell(rungIdx, row, col);
}

async function addRungAfter(index: number) {
  await ladderStore.insertRungAfter(index);
}

async function removeRung(index: number) {
  if (!window.confirm(`Delete rung ${index}? This cannot be undone.`)) return;
  await ladderStore.deleteRung(index);
}
</script>

<template>
  <div class="ladder-view">
    <!-- Section tabs -->
    <div class="section-tabs" v-if="sections.length > 0">
      <button
        v-for="sec in sections"
        :key="sec.index"
        :class="{ active: state.activeSection === sec.index }"
        @click="ladderStore.setActiveSection(sec.index)"
      >
        {{ sec.name || `Section ${sec.index}` }}
        <span class="lang-badge">{{ sec.language === SectionLanguage.LADDER ? 'LD' : 'SFC' }}</span>
        <span class="sr-badge" v-if="sec.subRoutineNumber >= 0">SR{{ sec.subRoutineNumber }}</span>
      </button>
    </div>

    <!-- The edit session, when there is one -->
    <template v-if="state.editRung >= 0">
      <EditToolbar />
      <div class="edit-pane">
        <div class="edit-meta">
          <div class="meta-field">
            <label for="rung-label">Label</label>
            <input id="rung-label" v-model="draftLabel" :maxlength="LGT_LABEL - 1"
                   placeholder="jump target name" />
          </div>
          <div class="meta-field wide">
            <label for="rung-comment">Comment</label>
            <input id="rung-comment" v-model="draftComment" :maxlength="LGT_COMMENT - 1"
                   placeholder="what this rung is for" />
          </div>
        </div>
        <ElementProperties />
      </div>
    </template>

    <!-- Ladder content -->
    <div class="rungs-container" v-if="rungs.length > 0">
      <div v-for="entry in rungs" :key="entry.index" class="rung-slot">
        <LadderRung
          :rung="state.editRung === entry.index && state.draft ? state.draft : entry.rung"
          :rung-index="entry.index"
          :symbols="state.symbolMap"
          :cells="cellsOf(entry.index)"
          :editing="state.editRung === entry.index"
          @cell-click="(r, c) => onCellClick(entry.index, r, c)"
        >
          <template #header>
            <div class="rung-header">
              <span class="rung-num">{{ entry.index }}</span>
              <span class="rung-label" v-if="entry.rung.label">{{ entry.rung.label }}</span>
              <span class="rung-comment" v-if="entry.rung.comment">{{ entry.rung.comment }}</span>
              <span class="rung-actions">
                <button v-if="state.editRung !== entry.index" :disabled="state.busy"
                        @click="ladderStore.beginEdit(entry.index)">Edit</button>
                <button :disabled="state.busy" @click="addRungAfter(entry.index)"
                        title="Insert an empty rung below this one">+ Rung</button>
                <button class="danger" :disabled="state.busy" @click="removeRung(entry.index)"
                        title="Delete this rung">Delete</button>
              </span>
            </div>
          </template>
        </LadderRung>
      </div>
    </div>

    <div v-else class="empty-state">
      <p v-if="!state.program">Loading program…</p>
      <p v-else-if="activeSection?.language === SectionLanguage.SEQUENTIAL">
        This section is a sequential chart — see the SFC tab.
      </p>
      <p v-else>No rungs in this section.</p>
    </div>

    <p class="stale-note" v-if="state.program && !liveStore.state.connected">
      Not connected to the controller — the ladder is shown unpowered.
    </p>
  </div>
</template>

<style scoped>
.ladder-view {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.section-tabs {
  display: flex;
  gap: 2px;
  padding: 0 0 8px;
  border-bottom: 1px solid #45475a;
  margin-bottom: 8px;
  flex-wrap: wrap;
}

.section-tabs button {
  background: #1e1e2e;
  border: 1px solid #45475a;
  color: #a6adc8;
  padding: 4px 12px;
  border-radius: 4px;
  cursor: pointer;
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 6px;
}

.section-tabs button.active {
  background: #313244;
  color: #cdd6f4;
  border-color: #89b4fa;
}

.lang-badge, .sr-badge {
  font-size: 9px;
  padding: 1px 4px;
  border-radius: 3px;
  background: #45475a;
  color: #cba6f7;
  font-weight: 600;
}
.sr-badge { color: #f9e2af; }

.edit-pane {
  display: grid;
  grid-template-columns: 1fr 280px;
  gap: 10px;
  margin-bottom: 10px;
  align-items: start;
}

.edit-meta {
  display: flex;
  flex-direction: column;
  gap: 8px;
  background: #1e1e2e;
  border: 1px solid #45475a;
  border-radius: 4px;
  padding: 10px 12px;
}

.meta-field.wide { flex: 1; }

.rungs-container {
  flex: 1;
  overflow-y: auto;
  padding-right: 4px;
}

.rung-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  background: #1e1e2e;
  border-bottom: 1px solid #45475a;
  font-size: 11px;
}

.rung-num { font-weight: 700; color: #89b4fa; min-width: 24px; }
.rung-label { color: #a6e3a1; font-weight: 600; }
.rung-comment { color: #a6adc8; font-style: italic; }

.rung-actions {
  margin-left: auto;
  display: flex;
  gap: 4px;
}

.rung-actions button {
  background: #313244;
  border: 1px solid #45475a;
  color: #cdd6f4;
  padding: 2px 8px;
  border-radius: 3px;
  cursor: pointer;
  font-size: 11px;
}

.rung-actions button:hover { background: #45475a; }
.rung-actions button:disabled { opacity: 0.4; cursor: default; }
.rung-actions button.danger { color: #f38ba8; }

.empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 200px;
  color: #a6adc8;
}

.stale-note {
  font-size: 11px;
  color: #f9e2af;
  padding: 6px 2px 0;
}
</style>
