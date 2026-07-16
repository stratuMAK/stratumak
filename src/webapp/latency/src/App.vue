<script setup lang="ts">
import { ref } from 'vue';
import { latencyStore } from './stores/latency';
import SummaryPanel from './components/SummaryPanel.vue';
import LatencyPlot from './components/LatencyPlot.vue';
import LatencyHistogram from './components/LatencyHistogram.vue';

type Tab = 'summary' | 'plot' | 'histogram';
const tabs: Tab[] = ['summary', 'plot', 'histogram'];
const initialTab = new URLSearchParams(window.location.search).get('tab') as Tab | null;
const tab = ref<Tab>(initialTab && tabs.includes(initialTab) ? initialTab : 'summary');
const store = latencyStore;

function onSelect(e: Event) {
  store.selectInstance((e.target as HTMLSelectElement).value);
}
</script>

<template>
  <div class="app">
    <header class="bar">
      <h1>RT Latency</h1>
      <label class="inst">
        thread
        <select :value="store.state.selected" @change="onSelect"
                :disabled="store.state.instances.length === 0">
          <option v-for="i in store.state.instances" :key="i" :value="i">{{ i }}</option>
        </select>
      </label>
      <span class="status" :class="{ ok: store.state.connected, bad: !store.state.connected }">
        {{ store.state.connected ? 'live' : 'no data' }}
      </span>
      <span v-if="store.state.error" class="err" :title="store.state.error">error</span>
      <span class="spacer"></span>
      <button class="reset" @click="store.reset()">Reset</button>
    </header>

    <nav class="tabs">
      <button :class="{ active: tab === 'summary' }" @click="tab = 'summary'">Summary</button>
      <button :class="{ active: tab === 'plot' }" @click="tab = 'plot'">Plot</button>
      <button :class="{ active: tab === 'histogram' }" @click="tab = 'histogram'">Histogram</button>
    </nav>

    <main class="panel">
      <SummaryPanel v-show="tab === 'summary'" />
      <LatencyPlot v-show="tab === 'plot'" :active="tab === 'plot'" />
      <LatencyHistogram v-show="tab === 'histogram'" :active="tab === 'histogram'" />
    </main>
  </div>
</template>

<style>
:root {
  --surface: #fcfcfb;
  --surface-2: #f2f2f0;
  --text-primary: #0b0b0b;
  --text-secondary: #52514e;
  --border: #d8d8d4;
  --series-1: #2a78d6;
  --series-2: #eb6834;
  --grid: #e4e4e0;
  --accent: #2a78d6;
  color-scheme: light;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme='light']) {
    --surface: #1a1a19;
    --surface-2: #262624;
    --text-primary: #ffffff;
    --text-secondary: #c3c2b7;
    --border: #3a3a37;
    --series-1: #3987e5;
    --series-2: #d95926;
    --grid: #333330;
    --accent: #3987e5;
    color-scheme: dark;
  }
}
:root[data-theme='dark'] {
  --surface: #1a1a19;
  --surface-2: #262624;
  --text-primary: #ffffff;
  --text-secondary: #c3c2b7;
  --border: #3a3a37;
  --series-1: #3987e5;
  --series-2: #d95926;
  --grid: #333330;
  --accent: #3987e5;
  color-scheme: dark;
}

* { box-sizing: border-box; }
html, body { margin: 0; height: 100%; }
body {
  background: var(--surface);
  color: var(--text-primary);
  font: 14px/1.4 system-ui, -apple-system, Segoe UI, Roboto, sans-serif;
}
</style>

<style scoped>
.app { display: flex; flex-direction: column; height: 100vh; }
.bar {
  display: flex; align-items: center; gap: 14px;
  padding: 8px 14px; border-bottom: 1px solid var(--border);
  background: var(--surface-2);
}
h1 { font-size: 15px; font-weight: 600; margin: 0; }
.inst { display: flex; align-items: center; gap: 6px; color: var(--text-secondary); }
select {
  background: var(--surface); color: var(--text-primary);
  border: 1px solid var(--border); border-radius: 5px; padding: 3px 6px;
}
.status { font-size: 12px; padding: 2px 8px; border-radius: 10px; }
.status.ok { color: #0a7a3f; background: color-mix(in srgb, #0a7a3f 14%, transparent); }
.status.bad { color: #b23; background: color-mix(in srgb, #b23 14%, transparent); }
.err { color: #b23; font-size: 12px; }
.spacer { flex: 1; }
.reset {
  background: var(--accent); color: #fff; border: none;
  border-radius: 5px; padding: 5px 14px; cursor: pointer; font-weight: 600;
}
.reset:hover { filter: brightness(1.08); }
.tabs { display: flex; gap: 2px; padding: 6px 10px 0; border-bottom: 1px solid var(--border); }
.tabs button {
  background: none; border: none; color: var(--text-secondary);
  padding: 7px 16px; cursor: pointer; border-bottom: 2px solid transparent;
  font-size: 13px;
}
.tabs button.active { color: var(--text-primary); border-bottom-color: var(--accent); font-weight: 600; }
.panel { flex: 1; min-height: 0; padding: 16px; overflow: auto; }
</style>
