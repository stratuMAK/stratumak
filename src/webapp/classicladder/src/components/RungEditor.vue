<script setup lang="ts">
// The rung editor, as a dialog over the section display — which is where 2.9
// puts it too.
//
// It was inline under the section tabs, and on a program of any size that meant
// scrolling to the top to pick a tool and back down to place it, for every
// element. Editing a rung is modal anyway: only one rung can be open, and the
// structural buttons are disabled while it is. So the tools, the grid and the
// properties go in one place with nothing to scroll past.
import { ref, watch, onMounted, onUnmounted } from 'vue';
import LadderRung from './LadderRung.vue';
import EditToolbar from './EditToolbar.vue';
import ElementProperties from './ElementProperties.vue';
import { LGT_LABEL, LGT_COMMENT } from '../generated/classicladder_client';
import { ladderStore } from '../stores/ladder';

const state = ladderStore.state;

const draftLabel = ref('');
const draftComment = ref('');

watch(() => state.draft, (d) => {
  draftLabel.value = d?.label ?? '';
  draftComment.value = d?.comment ?? '';
}, { immediate: true });

watch(draftLabel, v => { if (state.draft) state.draft.label = v; });
watch(draftComment, v => { if (state.draft) state.draft.comment = v; });

function onCellClick(row: number, col: number) {
  ladderStore.selectCell(state.editRung, row, col);
}

// Escape is Cancel. The draft is exactly the thing Cancel discards, so the two
// mean the same; there is no half-state for Escape to leave behind.
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.preventDefault();
    ladderStore.cancelEdit();
  }
}

onMounted(() => window.addEventListener('keydown', onKey));
onUnmounted(() => window.removeEventListener('keydown', onKey));
</script>

<template>
  <div class="backdrop">
    <!-- Clicking the backdrop deliberately does nothing: it is one stray click
         away from every unsaved element in the rung. -->
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="rung-editor-title">
      <div class="dialog-head">
        <h2 id="rung-editor-title">Rung {{ state.editRung }}</h2>
        <EditToolbar />
      </div>

      <div class="dialog-body">
        <div class="meta">
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

        <div class="work">
          <div class="grid-pane">
            <LadderRung
              v-if="state.draft"
              :rung="state.draft"
              :rung-index="state.editRung"
              :symbols="state.symbolMap"
              :cells="null"
              :editing="true"
              @cell-click="onCellClick"
            >
              <template #header><span /></template>
            </LadderRung>
          </div>
          <div class="props-pane">
            <ElementProperties />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.backdrop {
  position: fixed;
  inset: 0;
  background: rgba(17, 17, 27, 0.75);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  z-index: 50;
}

.dialog {
  background: #313244;
  border: 1px solid #f9e2af;
  border-radius: 8px;
  width: min(1240px, 100%);
  max-height: 100%;
  display: flex;
  flex-direction: column;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
}

/* The head does not scroll: the tools have to be reachable from wherever the
   grid has been scrolled to, which is the whole point of the dialog. */
.dialog-head {
  flex: 0 0 auto;
  padding: 12px 14px 0;
  border-bottom: 1px solid #45475a;
}

.dialog-head h2 {
  font-size: 14px;
  font-weight: 600;
  color: #f9e2af;
  margin-bottom: 8px;
}

.dialog-body {
  flex: 1 1 auto;
  overflow-y: auto;
  padding: 12px 14px 14px;
}

.meta {
  display: flex;
  gap: 12px;
  margin-bottom: 12px;
}

.meta-field { flex: 0 0 200px; }
.meta-field.wide { flex: 1 1 auto; }

.work {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  flex-wrap: wrap;
}

.grid-pane {
  flex: 1 1 620px;
  min-width: 0;
}

.props-pane {
  flex: 0 0 300px;
}

@media (max-width: 900px) {
  .props-pane { flex: 1 1 100%; }
}
</style>
