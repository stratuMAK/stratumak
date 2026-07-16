<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, nextTick } from 'vue';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { latencyStore } from '../stores/latency';

const props = defineProps<{ active: boolean }>();
const el = ref<HTMLDivElement>();
const LEGEND_H = 44; // room below the canvas for uPlot's legend row
let plot: uPlot | null = null;
let ro: ResizeObserver | null = null;
let mq: MediaQueryList | null = null;
function onThemeChange() { plot?.destroy(); plot = null; render(); }

function themeColors() {
  const cs = getComputedStyle(document.documentElement);
  const get = (n: string, d: string) => cs.getPropertyValue(n).trim() || d;
  return {
    s1: get('--series-1', '#2a78d6'),
    s2: get('--series-2', '#eb6834'),
    text: get('--text-secondary', '#888'),
    grid: get('--grid', '#ccc'),
  };
}

// [xs, lastUs, maxJitterUs] — latency in microseconds, x in seconds.
function buildData(): uPlot.AlignedData {
  const p = latencyStore.state.plot;
  return [
    p.map((d) => d.t),
    p.map((d) => d.lastNs / 1000),
    p.map((d) => d.maxJitterNs / 1000),
  ];
}

function buildOpts(w: number, h: number): uPlot.Options {
  const c = themeColors();
  const axis = { stroke: c.text, grid: { stroke: c.grid, width: 1 }, ticks: { stroke: c.grid } };
  return {
    width: w,
    height: h,
    scales: { x: { time: false } },
    axes: [
      { ...axis, values: (_u, vals) => vals.map((v) => v.toFixed(0) + 's') },
      { ...axis, label: 'latency (µs)', labelSize: 30 },
    ],
    series: [
      { label: 'elapsed' },
      { label: 'last', stroke: c.s1, width: 2 },
      { label: 'max jitter', stroke: c.s2, width: 2 },
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
watch(() => latencyStore.state.tick, render);
</script>

<template>
  <div class="wrap">
    <div ref="el" class="chart"></div>
  </div>
</template>

<style scoped>
.wrap { height: 100%; display: flex; }
.chart { flex: 1; min-height: 320px; }
</style>
