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

// Server-side buckets -> [elapsedSeconds, maxUs, meanUs].  x is the server's
// own timeline (seconds since the instance started), so it is absolute,
// monotonic, and identical for every client viewing the same data.
function buildData(): uPlot.AlignedData {
  const h = hist.value;
  if (!h || h.points.length === 0) return [[], [], []];
  return [
    h.points.map((p) => p.tMs / 1000),
    h.points.map((p) => p.maxNs / 1000),
    h.points.map((p) => p.meanNs / 1000),
  ];
}

// Format elapsed seconds as m:ss.  The raw number renders with a locale
// thousands separator ("1.109" for 1109 s), which reads like a decimal.  Fall
// back to decimals if uPlot picks sub-second ticks, so labels stay distinct.
function fmtElapsed(v: number, incr: number): string {
  if (incr < 1) return v.toFixed(1);
  const s = Math.round(v);
  const m = Math.floor(s / 60);
  const r = s % 60;
  return m > 0 ? `${m}:${String(r).padStart(2, '0')}` : `${r}s`;
}

function buildOpts(w: number, hgt: number): uPlot.Options {
  const c = themeColors();
  const axis = { stroke: c.text, grid: { stroke: c.grid, width: 1 }, ticks: { stroke: c.grid } };
  return {
    width: w,
    height: hgt,
    scales: { x: { time: false } },
    axes: [
      // uPlot picks the tick values; fmtElapsed renders them (and adapts to the
      // chosen increment, so short windows don't collapse into duplicates).
      {
        ...axis,
        label: 'elapsed (m:ss)',
        labelSize: 26,
        values: (_u, vals, _ai, _fs, incr) => vals.map((v) => fmtElapsed(v, incr ?? 1)),
      },
      { ...axis, label: 'latency (µs)', labelSize: 30 },
    ],
    series: [
      { label: 'elapsed' },
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
  // A single point has no x extent, so uPlot auto-ranges it to an absurd span.
  // Show the placeholder until there is a real series (e.g. just after a
  // reset, when the server history is refilling).
  if ((hist.value?.points.length ?? 0) < 2) {
    plot?.destroy();
    plot = null;
    return;
  }
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
    <div class="plotarea">
      <div ref="el" class="chart"></div>
      <!-- After a reset the server history is empty until the first buckets
           close; say so rather than showing a blank (or degenerate) chart. -->
      <div v-if="(hist?.points.length ?? 0) < 2" class="empty">
        waiting for data…
      </div>
    </div>
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
.plotarea { position: relative; flex: 1; min-height: 320px; display: flex; }
.chart { flex: 1; }
.empty {
  position: absolute; inset: 0; display: flex;
  align-items: center; justify-content: center;
  color: var(--text-secondary); font-size: 13px; pointer-events: none;
}
</style>
