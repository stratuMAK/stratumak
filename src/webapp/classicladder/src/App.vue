<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue';
import SectionView from './components/SectionView.vue';
import SectionManager from './components/SectionManager.vue';
import SymbolTable from './components/SymbolTable.vue';
import ProjectBar from './components/ProjectBar.vue';
import VarSpy from './components/VarSpy.vue';
import ComParamsPanel from './components/ComParamsPanel.vue';
import RequestsPanel from './components/RequestsPanel.vue';
import StatusPanel from './components/StatusPanel.vue';
import { ladderStore } from './stores/ladder';
import { liveStore } from './stores/live';
import { modbusStore } from './stores/modbus';
import { LadderState } from './generated/classicladder_client';

type Tab = 'program' | 'sections' | 'symbols' | 'variables'
  | 'modbus-params' | 'modbus-requests' | 'modbus-status';

const activeTab = ref<Tab>('program');

// The controller's status, pushed. Null when it is unreachable — a stopped PLC
// and one that is not answering must not look the same, so the last known state
// is dropped rather than kept.
const status = computed(() => liveStore.state.status);
const running = computed(() => status.value?.state === LadderState.RUN);

onMounted(() => {
  liveStore.start();
  ladderStore.fetchProgram();
  modbusStore.fetchAll();
  modbusStore.startPolling();
});

onUnmounted(() => {
  liveStore.stop();
  modbusStore.stopPolling();
});

async function toggleRun() {
  await ladderStore.setState(running.value ? LadderState.STOP : LadderState.RUN);
}

const scanText = computed(() => {
  const s = status.value;
  if (!s) return '';
  return `${s.scanTimeUs} µs / ${s.periodicRefreshMs} ms`;
});
</script>

<template>
  <div class="app">
    <header class="header">
      <h1>Classic Ladder</h1>
      <div class="header-actions">
        <span class="scan" v-if="status" :title="'scan time / thread period'">{{ scanText }}</span>
        <div class="status-indicator">
          <span :class="status ? (running ? 'dot-green' : 'dot-red') : 'dot-grey'"></span>
          <template v-if="!status">
            {{ liveStore.state.reconnecting ? 'Reconnecting…' : 'Not connected' }}
          </template>
          <template v-else>{{ running ? 'Running' : 'Stopped' }}</template>
        </div>
        <button class="btn btn-sm" :disabled="!status" @click="toggleRun">
          {{ running ? 'Stop' : 'Run' }}
        </button>
        <button class="btn btn-sm" @click="ladderStore.fetchProgram()">Reload</button>
      </div>
    </header>

    <ProjectBar />

    <nav class="tabs">
      <button :class="{ active: activeTab === 'program' }" @click="activeTab = 'program'">Program</button>
      <button :class="{ active: activeTab === 'sections' }" @click="activeTab = 'sections'">Sections</button>
      <button :class="{ active: activeTab === 'symbols' }" @click="activeTab = 'symbols'">Symbols</button>
      <button :class="{ active: activeTab === 'variables' }" @click="activeTab = 'variables'">Variables</button>
      <button :class="{ active: activeTab === 'modbus-params' }" @click="activeTab = 'modbus-params'">Modbus Config</button>
      <button :class="{ active: activeTab === 'modbus-requests' }" @click="activeTab = 'modbus-requests'">Modbus I/O</button>
      <button :class="{ active: activeTab === 'modbus-status' }" @click="activeTab = 'modbus-status'">Modbus Status</button>
    </nav>

    <div class="error-bar" v-if="ladderStore.state.error || modbusStore.state.error">
      {{ ladderStore.state.error || modbusStore.state.error }}
      <button @click="ladderStore.state.error = ''; modbusStore.state.error = ''">✕</button>
    </div>

    <main class="content">
      <SectionView v-if="activeTab === 'program'" />
      <SectionManager v-else-if="activeTab === 'sections'" @show="activeTab = 'program'" />
      <SymbolTable v-else-if="activeTab === 'symbols'" />
      <VarSpy v-else-if="activeTab === 'variables'" />
      <ComParamsPanel v-else-if="activeTab === 'modbus-params'" />
      <RequestsPanel v-else-if="activeTab === 'modbus-requests'" />
      <StatusPanel v-else-if="activeTab === 'modbus-status'" />
    </main>
  </div>
</template>

<style>
* {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}

body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  font-size: 14px;
  background: #1e1e2e;
  color: #cdd6f4;
}

.app {
  max-width: 1100px;
  margin: 0 auto;
  padding: 16px;
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.header h1 {
  font-size: 18px;
  font-weight: 600;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.scan {
  font-size: 11px;
  color: #6c7086;
  font-family: monospace;
}

.btn-sm {
  padding: 4px 10px !important;
  font-size: 12px !important;
}

.btn:disabled { opacity: 0.4; cursor: default; }

.status-indicator {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
}

.dot-green, .dot-red, .dot-grey {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
}
.dot-green { background: #a6e3a1; }
.dot-red { background: #f38ba8; }
.dot-grey { background: #6c7086; }

.tabs {
  display: flex;
  gap: 2px;
  margin-bottom: 16px;
  border-bottom: 1px solid #45475a;
  flex-wrap: wrap;
}

.tabs button {
  background: none;
  border: none;
  color: #a6adc8;
  padding: 8px 14px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  font-size: 13px;
}

.tabs button.active {
  color: #cdd6f4;
  border-bottom-color: #89b4fa;
}

.tabs button:hover {
  color: #cdd6f4;
}

.error-bar {
  background: #f38ba8;
  color: #1e1e2e;
  padding: 8px 12px;
  border-radius: 4px;
  margin-bottom: 12px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
}

.error-bar button {
  background: none;
  border: none;
  font-size: 16px;
  cursor: pointer;
  color: #1e1e2e;
  flex: 0 0 auto;
}

.content {
  background: #313244;
  border-radius: 8px;
  padding: 16px;
}

/* Form elements */
label {
  display: block;
  font-size: 12px;
  color: #a6adc8;
  margin-bottom: 4px;
}

input, select {
  background: #1e1e2e;
  border: 1px solid #45475a;
  color: #cdd6f4;
  padding: 6px 10px;
  border-radius: 4px;
  font-size: 13px;
  width: 100%;
}

input:focus, select:focus {
  outline: none;
  border-color: #89b4fa;
}

button.btn {
  background: #89b4fa;
  color: #1e1e2e;
  border: none;
  padding: 8px 16px;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
  font-weight: 500;
  white-space: nowrap;
}

button.btn:hover {
  background: #b4d0fb;
}

button.btn-danger {
  background: #f38ba8;
}

button.btn-danger:hover {
  background: #f5a3b8;
}
</style>
