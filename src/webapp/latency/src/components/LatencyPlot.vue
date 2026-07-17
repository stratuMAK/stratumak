<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, nextTick, computed } from 'vue';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { latencyStore, RANGES } from '../stores/latency';

const props = defineProps<{ active: boolean }>();
const el = ref<HTMLDivElement>();
const LEGEND_H = 44; // room below the canvas for uPlot's legend row
let plot: uPlot | null = null;
let ro: ResizeObserver | null = null;
let mq: MediaQueryList | null = null;
function onThemeChange() { plot?.destroy(); plot = null; render(); }

const store = latencyStore;
const hist = computed(() => store.state.history);

function themeColors() {
  const cs = getComputedStyle(document.documentElement);
  const get = (n: string, d: string) => cs.getPropertyValue(n).trim() || d;
  return {
    s1: get('--series-1', '#44aaff'),
    s2: get('--series-2', '#ffaa44'),
    text: get('--text-secondary', '#888'),
    grid: get('--grid', '#333'),
  };
}

// Server-side buckets -> [secondsAgo, maxUs, meanUs].  x is relative to the
// newest bucket so the window reads as "seconds ago" and is identical for
// every client viewing the same data.
function buildData(): uPlot.AlignedData {
  const h = hist.value;
  if (!h || h.points.length === 0) return [[], [], []];
  const newest = h.points[h.points.length - 1].tMs;
  return [
    h.points.map((p) => (p.tMs - newest) / 1000),
    h.points.map((p) => p.maxNs / 1000),
    h.points.map((p) => p.meanNs / 1000),
  ];
}

function buildOpts(w: number, hgt: number): uPlot.Options {
  const c = themeColors();
  const axis = { stroke: c.text, grid: { stroke: c.grid, width: 1 }, ticks: { stroke: c.grid } };
  return {
    width: w,
    height: hgt,
    scales: { x: { time: false } },
    axes: [
      { ...axis, values: (_u, vals) => vals.map((v) => v.toFixed(0) + 's') },
      { ...axis, label: 'latency (µs)', labelSize: 30 },
    ],
    series: [
      { label: 'ago' },
      { label: 'max', stroke: c.s2, width: 2 },
      { label: 'mean', stroke: c.s1, width: 2 },
    ],
  };
}

function createPlot() {
  if (!el.value) return;
  const w = el.value.clientWidth;
  const h = el.value.clientHeight;
  if (w === 0 || h === 0) return; // hidden; wait for activation
  plot?.destroy();
  plot = new uPlot(buildOpts(w, Math.max(120, h - LEGEND_H)), buildData(), el.value);
}

function render() {
  if (!props.active) return;
  if (!plot) { createPlot(); return; }
  plot.setData(buildData());
}

onMounted(() => {
  ro = new ResizeObserver(() => {
    if (plot && el.value) plot.setSize({ width: el.value.clientWidth, height: Math.max(120, el.value.clientHeight - LEGEND_H) });
    else if (props.active) createPlot();
  });
  if (el.value) ro.observe(el.value);
  mq = window.matchMedia('(prefers-color-scheme: dark)');
  mq.addEventListener('change', onThemeChange);
  if (props.active) nextTick(createPlot);
});

onBeforeUnmount(() => {
  ro?.disconnect();
  mq?.removeEventListener('change', onThemeChange);
  plot?.destroy();
  plot = null;
});

watch(() => props.active, (a) => { if (a) nextTick(render); });
watch(hist, render);
</script>

<template>
  <div class="wrap">
    <div class="toolbar">
      <span class="lbl">range</span>
      <button v-for="r in RANGES" :key="r.seconds"
              :class="{ active: store.state.rangeSec === r.seconds }"
              @click="store.setRange(r.seconds)">{{ r.label }}</button>
      <span class="meta" v-if="hist">
        {{ hist.bucketMs }} ms buckets · {{ hist.points.length }} points · server-side history
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
.meta { margin-left: 10px; }
.chart { flex: 1; min-height: 320px; }
</style>
