<script setup lang="ts">
import { computed } from 'vue';
import { statusStore } from '../stores/status';

// state.tick is bumped by the repaint timer; reading it here is what makes the
// highlight decay without a push arriving.
const rows = computed(() => {
  const now = Date.now();
  void statusStore.state.tick;
  return statusStore.filteredRows().map(r => ({
    key: r.key,
    value: r.value,
    changed: statusStore.isHighlighted(r, now),
  }));
});
</script>

<template>
  <div class="status-table">
    <div v-if="!statusStore.state.rows.length" class="empty">
      Waiting for status…
    </div>
    <div v-for="row in rows" :key="row.key" class="row">
      <span class="key">{{ row.key }}</span>
      <span class="value" :class="{ changed: row.changed }">{{ row.value }}</span>
    </div>
  </div>
</template>

<style scoped>
.status-table {
  padding: 4px 8px;
}

.empty {
  color: #666;
  padding: 12px 0;
}

.row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  line-height: 1.5;
}

.key {
  flex: 0 0 260px;
  color: #6aa9e0;
  overflow-wrap: anywhere;
}

.value {
  flex: 1;
  min-width: 0;
  font-family: ui-monospace, 'DejaVu Sans Mono', monospace;
  color: #ccc;
  overflow-wrap: anywhere;
  padding: 0 3px;
  border-radius: 2px;
}

/* The Tk version flashed changed values on a red background for 2 s. */
.value.changed {
  background: #7a1e1e;
  color: #fff;
}
</style>
