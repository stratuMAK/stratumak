<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, nextTick, computed } from 'vue';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { latencyStore } from '../stores/latency';

const props = defineProps<{ active: boolean }>();
const el = ref<HTMLDivElement>();
const logScale = ref(false);
let plot: uPlot | null = null;
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

function createPlot() {
  if (!el.value) return;
  const w = el.value.clientWidth;
  const hgt = el.value.clientHeight;
  if (w === 0 || hgt === 0) return;
  plot?.destroy();
  plot = new uPlot(buildOpts(w, hgt), buildData(), el.value);
}

function render() {
  if (!props.active) return;
  if (!plot) { createPlot(); return; }
  plot.setData(buildData());
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
.toolbar { display: flex; align-items: center; gap: 16px; margin-bottom: 10px; color: var(--text-secondary); font-size: 12px; }
.chk { display: flex; align-items: center; gap: 5px; cursor: pointer; color: var(--text-primary); }
.chart { flex: 1; min-height: 320px; }
</style>
