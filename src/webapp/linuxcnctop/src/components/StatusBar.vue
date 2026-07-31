<script setup lang="ts">
import { statusStore } from '../stores/status';
</script>

<template>
  <div class="status-bar">
    <span class="status-indicator" :class="{ connected: statusStore.state.watchOk }">●</span>
    <span v-if="statusStore.state.watchOk">Connected</span>
    <span v-else-if="statusStore.state.watchReconnecting" class="warn">Reconnecting…</span>
    <span v-else-if="statusStore.state.error" class="error">{{ statusStore.state.error }}</span>
    <span v-else>Connecting…</span>
    <!-- Values kept on screen after the WS dropped are explicitly marked; a
         frozen display would otherwise read as a live idle machine. -->
    <template v-if="statusStore.state.watchStale">
      <span class="sep">|</span>
      <span class="warn">Values stale</span>
    </template>
    <template v-if="statusStore.state.frozen">
      <span class="sep">|</span>
      <span class="warn">Display frozen</span>
    </template>
    <span class="sep">|</span>
    <span>{{ statusStore.state.rows.length }} fields</span>
    <span class="sep">|</span>
    <span>lengths in mm</span>
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
