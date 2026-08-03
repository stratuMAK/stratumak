<script setup lang="ts">
import { ref } from 'vue';
import StatusTable from './components/StatusTable.vue';
import StatusBar from './components/StatusBar.vue';
import { statusStore, RATES } from './stores/status';

const copied = ref(false);

function onFilterInput(e: Event) {
  statusStore.setFilter((e.target as HTMLInputElement).value);
}

function onRateChange(e: Event) {
  statusStore.setRate(Number((e.target as HTMLSelectElement).value));
}

async function copyAll() {
  const text = statusStore.state.showJson ? statusStore.asJson() : statusStore.asText();
  try {
    await navigator.clipboard.writeText(text);
    copied.value = true;
    setTimeout(() => { copied.value = false; }, 1200);
  } catch {
    // Clipboard blocked (insecure context / no permission) — the text is
    // selectable in the page, so this is not worth an error banner.
  }
}
</script>

<template>
  <div class="app">
    <div class="toolbar">
      <input
        class="filter-input"
        type="text"
        placeholder="Filter fields or values..."
        :value="statusStore.state.filter"
        @input="onFilterInput"
      />
      <label class="rate">
        Rate
        <select :value="statusStore.state.rateMs" @change="onRateChange">
          <option v-for="r in RATES" :key="r" :value="r">{{ r }} ms</option>
        </select>
      </label>
      <button
        :class="{ active: statusStore.state.frozen }"
        @click="statusStore.toggleFrozen()"
      >{{ statusStore.state.frozen ? 'Resume' : 'Freeze' }}</button>
      <button
        :class="{ active: statusStore.state.showJson }"
        @click="statusStore.toggleJson()"
      >JSON</button>
      <button @click="copyAll()">{{ copied ? 'Copied' : 'Copy All' }}</button>
    </div>
    <div class="main-area">
      <pre v-if="statusStore.state.showJson" class="json">{{ statusStore.asJson() }}</pre>
      <StatusTable v-else />
    </div>
    <StatusBar />
  </div>
</template>

<style>
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

html, body, #app {
  width: 100%;
  height: 100%;
  background: #111;
  color: #ccc;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
  font-size: 13px;
}

.app {
  display: flex;
  flex-direction: column;
  height: 100vh;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  background: #1a1a1a;
  border-bottom: 1px solid #333;
}

.filter-input {
  flex: 1;
  max-width: 320px;
  background: #222;
  border: 1px solid #333;
  border-radius: 3px;
  padding: 4px 8px;
  color: #ccc;
  font-size: 12px;
}

.filter-input:focus {
  outline: none;
  border-color: #4a8abf;
}

.toolbar .rate {
  display: flex;
  align-items: center;
  gap: 4px;
  color: #999;
  font-size: 12px;
}

.toolbar select {
  background: #222;
  color: #ccc;
  border: 1px solid #333;
  border-radius: 3px;
  padding: 3px 6px;
  font-size: 12px;
}

.toolbar button {
  background: #222;
  color: #999;
  border: 1px solid #333;
  border-radius: 3px;
  padding: 4px 10px;
  font-size: 12px;
  cursor: pointer;
}

.toolbar button.active {
  background: #2a4a6a;
  color: #fff;
  border-color: #4a8abf;
}

.toolbar button:hover:not(.active) {
  background: #2a2a2a;
  color: #ccc;
}

.main-area {
  flex: 1;
  overflow-y: auto;
}

.json {
  padding: 8px;
  font-family: ui-monospace, 'DejaVu Sans Mono', monospace;
  font-size: 12px;
  color: #ccc;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
