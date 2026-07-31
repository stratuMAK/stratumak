<script setup lang="ts">
// The chart editor, as a dialog over the section display — the same shape the
// rung editor has, and for the same reason: a page is sixteen cells square, and
// picking a tool at the top and placing it at the bottom should not mean
// scrolling past the rest of the application between every element.
import { onMounted, onUnmounted } from 'vue';
import SfcView from './SfcView.vue';
import SfcToolbar from './SfcToolbar.vue';
import SfcProperties from './SfcProperties.vue';
import { sfcStore } from '../stores/sfc';

const state = sfcStore.state;

function onCellClick(x: number, y: number, top: boolean) {
  sfcStore.clickCell(x, y, top);
}

// Escape is Cancel. The draft is exactly the thing Cancel discards, so the two
// mean the same; there is no half-state for Escape to leave behind.
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.preventDefault();
    sfcStore.cancelEdit();
  }
}

onMounted(() => window.addEventListener('keydown', onKey));
onUnmounted(() => window.removeEventListener('keydown', onKey));
</script>

<template>
  <div class="backdrop">
    <!-- Clicking the backdrop deliberately does nothing: it is one stray click
         away from every unsaved element in the chart. -->
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="sfc-editor-title">
      <div class="dialog-head">
        <h2 id="sfc-editor-title">Chart page {{ state.page }}</h2>
        <SfcToolbar />
      </div>

      <div class="dialog-body">
        <p class="err" v-if="state.error">{{ state.error }}</p>

        <div class="work">
          <div class="chart-pane">
            <SfcView :page="state.page" :chart="state.draft" :editing="true"
                     :selected="state.selected" @cell-click="onCellClick" />
          </div>
          <div class="props-pane">
            <SfcProperties />
            <p class="hint">
              Steps go on odd rows and transitions on even ones, so a step and
              the transition below it are always a row apart. Placing a step
              numbers it and joins it to the transitions above and below.
            </p>
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
   chart has been scrolled to, which is the whole point of the dialog. */
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

.work {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  flex-wrap: wrap;
}

.chart-pane {
  flex: 1 1 620px;
  min-width: 0;
}

.props-pane { flex: 0 0 300px; }

.err {
  font-size: 12px;
  color: #f38ba8;
  background: #1e1e2e;
  border: 1px solid #f38ba8;
  border-radius: 4px;
  padding: 6px 10px;
  margin-bottom: 10px;
}

.hint { font-size: 11px; color: #6c7086; margin-top: 10px; line-height: 1.4; }

@media (max-width: 900px) {
  .props-pane { flex: 1 1 100%; }
}
</style>
