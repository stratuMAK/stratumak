<script setup lang="ts">
import { halshowStore } from '../stores/halshow';
</script>

<template>
  <div class="status-bar">
    <span class="status-indicator" :class="{ connected: halshowStore.state.restOk }">●</span>
    <span v-if="halshowStore.state.restOk">Connected</span>
    <span v-else-if="halshowStore.state.error" class="error">{{ halshowStore.state.error }}</span>
    <span v-else>Connecting...</span>
    <!-- H-3: watch health shown separately from REST health -->
    <template v-if="!halshowStore.state.watchOk">
      <span class="sep">|</span>
      <span v-if="halshowStore.state.watchReconnecting" class="warn">Reconnecting watch…</span>
      <span v-else class="warn">Watch unavailable</span>
    </template>
    <template v-if="halshowStore.state.status">
      <span class="sep">|</span>
      <span>{{ halshowStore.state.status.pins }} pins</span>
      <span class="sep">|</span>
      <span>{{ halshowStore.state.status.signals }} signals</span>
      <span class="sep">|</span>
      <span>{{ halshowStore.state.status.params }} params</span>
      <span class="sep">|</span>
      <span>{{ halshowStore.state.status.components }} components</span>
    </template>
  </div>
</template>

<style scoped>
.status-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 8px;
  background: #0a0a0a;
  border-top: 1px solid #333;
  font-size: 11px;
  color: #888;
}

.status-indicator {
  color: #f44;
  font-size: 8px;
}

.status-indicator.connected {
  color: #4f4;
}

.error {
  color: #f88;
}

.warn {
  color: #fa4;
}

.sep {
  color: #333;
}
</style>
