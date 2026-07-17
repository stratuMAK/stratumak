<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, nextTick, computed } from 'vue';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { latencyStore } from '../stores/latency';

const props = defineProps<{ active: boolean }>();
const el = ref<HTMLDivElement>();
const logScale = ref(false);
const store = latencyStore;

// Server-side bin width choices (0 = autoscale to the bulk).
const BINS: { label: string; ns: number }[] = [
  { label: 'auto', ns: 0 },
  { label: '1µs', ns: 1000 },
  { label: '5µs', ns: 5000 },
  { label: '20µs', ns: 20000 },
  { label: '100µs', ns: 100000 },
];
function binActive(ns: number): boolean {
  const hh = h.value;
  if (!hh) return false;
  return ns === 0 ? hh.autoscale : !hh.autoscale && hh.binWidthNs === ns;
}
let plot: uPlot | null = null;
let building = false; // true while uPlot is constructing (see createPlot)
let ro: ResizeObserver | null = null;
let mq: MediaQueryList | null = null;

const h = computed(() => latencyStore.state.histogram);

function themeColors() {
  const cs = getComputedStyle(document.documentElement);
  const get = (n: string, d: string) => cs.getPropertyValue(n).trim() || d;
  return {
    s1: get('--series-1', '#2a78d6'),
    text: get('--text-secondary', '#888'),
    grid: get('--grid', '#ccc'),
  };
}

// x = bin center (µs), y = count.
function buildData(): uPlot.AlignedData {
  const hs = h.value;
  if (!hs) return [[], []];
  const xs = hs.bins.map((_, i) => (hs.baseNs + (i + 0.5) * hs.binWidthNs) / 1000);
  return [xs, hs.bins];
}

function buildOpts(w: number, hgt: number): uPlot.Options {
  const c = themeColors();
  const axis = { stroke: c.text, grid: { stroke: c.grid, width: 1 }, ticks: { stroke: c.grid } };
  const log = logScale.value;
  return {
    width: w,
    height: hgt,
    scales: {
      x: { time: false },
      y: log
        ? { distr: 3, range: (_u, _min, max) => [0.8, (max || 10) * 1.5] }
        : { range: (_u, _min, max) => [0, (max || 1) * 1.05] },
    },
    axes: [
      { ...axis, label: 'latency (µs)', labelSize: 26 },
      { ...axis, label: 'count' },
    ],
    series: [
      { label: 'latency (µs)' },
      {
        label: 'count',
        stroke: c.s1,
        fill: c.s1 + 'cc',
        paths: uPlot.paths.bars!({ size: [1.0, Infinity], align: 0 }),
        points: { show: false },
      },
    ],
    legend: { show: false }, // single series - the axis labels name it
    cursor: { points: { show: false } },
  };
}

// Same defence as the plot: if Vue hands us a different container, uPlot keeps
// drawing into the old, detached one - healthy object, no error, nothing on
// screen - so detect it and rebuild into the current div.
function plotOrphaned(): boolean {
  const root = (plot as unknown as { root?: HTMLElement } | null)?.root;
  return !!plot && (!root || !el.value || !el.value.contains(root));
}

function createPlot() {
  if (building) return; // uPlot's DOM inserts fire our ResizeObserver mid-build
  if (!el.value) return;
  const w = el.value.clientWidth;
  const hgt = el.value.clientHeight;
  if (w === 0 || hgt === 0) return;
  building = true;
  try {
    try { plot?.destroy(); } catch { /* already gone */ }
    plot = null;
    el.value.innerHTML = '';
    plot = new uPlot(buildOpts(w, hgt), buildData(), el.value);
  } catch (err) {
    console.error('latency histogram: uPlot init failed', err);
    plot = null;
  } finally {
    building = false;
  }
}

function render() {
  if (!props.active) return;
  if (plotOrphaned()) { createPlot(); return; }
  if (!plot) { createPlot(); return; }
  try {
    plot.setData(buildData());
  } catch (err) {
    console.error('latency histogram: setData failed, rebuilding', err);
    try { plot?.destroy(); } catch { /* ignore */ }
    plot = null;
  }
}

function rebuild() { plot?.destroy(); plot = null; render(); }

onMounted(() => {
  ro = new ResizeObserver(() => {
    if (plot && el.value) plot.setSize({ width: el.value.clientWidth, height: el.value.clientHeight });
    else if (props.active) createPlot();
  });
  if (el.value) ro.observe(el.value);
  mq = window.matchMedia('(prefers-color-scheme: dark)');
  mq.addEventListener('change', rebuild);
  if (props.active) nextTick(createPlot);
});

onBeforeUnmount(() => {
  ro?.disconnect();
  mq?.removeEventListener('change', rebuild);
  plot?.destroy();
  plot = null;
});

watch(() => props.active, (a) => { if (a) nextTick(render); });
watch(logScale, rebuild);
watch(h, render, { deep: true });
</script>

<template>
  <div class="wrap">
    <div class="toolbar">
      <span class="lbl">bin</span>
      <button v-for="b in BINS" :key="b.ns"
              :class="{ active: binActive(b.ns) }"
              @click="store.setBinWidth(b.ns)">{{ b.label }}</button>
      <label class="chk"><input type="checkbox" v-model="logScale" /> log scale</label>
      <span class="meta" v-if="h">
        bin {{ (h.binWidthNs / 1000).toFixed(2) }} µs ·
        under {{ h.underflow }} · over {{ h.overflow }} ·
        {{ h.samples.toLocaleString() }} samples
      </span>
    </div>
    <div ref="el" class="chart"></div>
  </div>
</template>

<style scoped>
.wrap { height: 100%; display: flex; flex-direction: column; }
.toolbar { display: flex; align-items: center; gap: 6px; margin-bottom: 10px; color: var(--text-secondary); font-size: 12px; }
.lbl { margin-right: 2px; }
.toolbar button {
  background: transparent; color: var(--text-secondary);
  border: 1px solid var(--border); border-radius: 4px;
  padding: 3px 10px; cursor: pointer; font-size: 12px;
}
.toolbar button.active { color: var(--accent); border-color: var(--accent); }
.chk { display: flex; align-items: center; gap: 5px; cursor: pointer; color: var(--text-primary); margin-left: 10px; }
.meta { margin-left: 10px; }
.chart { flex: 1; min-height: 320px; }
</style>
