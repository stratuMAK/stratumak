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
      <span v-if="store.state.error" class="err" :title="store.state.error">{{ store.state.error }}</span>
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
/* Fixed dark theme, matching halscope. */
:root {
  --surface: #111;
  --surface-2: #1a1a1a;
  --text-primary: #e6e6e6;
  --text-secondary: #888;
  --border: #333;
  --ticks: #444;
  --grid: #333;
  --series-1: #44aaff;
  --series-2: #ffaa44;
  --accent: #44aaff;
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
.status.ok { color: #3fb950; background: color-mix(in srgb, #3fb950 16%, transparent); }
.status.bad { color: #f85149; background: color-mix(in srgb, #f85149 16%, transparent); }
.err {
  color: #f85149; font-size: 12px;
  max-width: 46ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.spacer { flex: 1; }
.reset {
  background: transparent; color: var(--accent); border: 1px solid var(--accent);
  border-radius: 4px; padding: 5px 14px; cursor: pointer; font-weight: 600;
}
.reset:hover { background: var(--accent); color: #111; }
.tabs { display: flex; gap: 2px; padding: 6px 10px 0; border-bottom: 1px solid var(--border); }
.tabs button {
  background: none; border: none; color: var(--text-secondary);
  padding: 7px 16px; cursor: pointer; border-bottom: 2px solid transparent;
  font-size: 13px;
}
.tabs button.active { color: var(--text-primary); border-bottom-color: var(--accent); font-weight: 600; }
.panel { flex: 1; min-height: 0; padding: 16px; overflow: auto; }
</style>
