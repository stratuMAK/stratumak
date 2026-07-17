<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, nextTick, computed } from 'vue';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { latencyStore, RANGES } from '../stores/latency';

const props = defineProps<{ active: boolean }>();
const el = ref<HTMLDivElement>();
const LEGEND_H = 44; // room below the canvas for uPlot's legend row
let plot: uPlot | null = null;
let building = false; // true while uPlot is constructing (see createPlot)
let ro: ResizeObserver | null = null;
let mq: MediaQueryList | null = null;
function onThemeChange() { plot?.destroy(); plot = null; render(); }

const store = latencyStore;
const hist = computed(() => store.state.history);


// Is the live plot's DOM still inside our current container?  If Vue hands us
// a different div, uPlot keeps drawing into the old, detached one: the plot
// object looks healthy (correct size, setData succeeds, no error) but nothing
// is on screen, and only a reload clears it.
function plotOrphaned(): boolean {
  const root = (plot as unknown as { root?: HTMLElement } | null)?.root;
  return !!plot && (!root || !el.value || !el.value.contains(root));
}

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

// Always render elapsed time as m:ss, so the axis format never changes as time
// passes or the window is zoomed.  (The raw number would render with a locale
// thousands separator - "1.109" for 1109 s - which reads like a decimal.)
// Sub-second ticks get one decimal, still as m:ss.s, so labels stay distinct.
function fmtElapsed(v: number, incr: number): string {
  const sign = v < 0 ? '-' : '';
  const a = Math.abs(v);
  if (incr < 1) {
    // Round to the displayed precision BEFORE splitting, so 59.98 s renders
    // as 1:00.0 rather than 0:60.0.
    const total = Math.round(a * 10) / 10;
    const m = Math.floor(total / 60);
    return `${sign}${m}:${(total - m * 60).toFixed(1).padStart(4, '0')}`;
  }
  const total = Math.round(a);
  const m = Math.floor(total / 60);
  return `${sign}${m}:${String(total % 60).padStart(2, '0')}`;
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

function havePlottableData(): boolean {
  // A uPlot built from empty/1-point data never recovers via setData(), and a
  // single point has no x extent (it auto-ranges to an absurd span).  This is
  // exactly the state right after a reset, so refuse to build until there is a
  // real series - createPlot() is reached from onMounted and the
  // ResizeObserver too, not just render().
  return (hist.value?.points.length ?? 0) >= 2;
}

function destroyPlot() {
  try { plot?.destroy(); } catch { /* already gone */ }
  plot = null;
  if (el.value) el.value.innerHTML = ''; // drop any DOM left by a failed build
}

function createPlot() {
  // Reentrancy guard: uPlot inserts DOM while constructing, which fires our
  // ResizeObserver.  `plot` is not assigned yet at that moment, so the RO
  // would take its create branch and destroyPlot() would wipe the innerHTML of
  // the plot being built - leaving a non-null plot with no DOM, blank forever.
  if (building) return;
  if (!el.value || !havePlottableData()) return;
  const w = el.value.clientWidth;
  const hgt = el.value.clientHeight;
  if (w === 0 || hgt === 0) return; // hidden; wait for activation
  building = true;
  try {
    destroyPlot();
    plot = new uPlot(buildOpts(w, Math.max(120, hgt - LEGEND_H)), buildData(), el.value);
  } catch (err) {
    // Never wedge: leave plot null so the next data update retries, instead of
    // throwing out of the watcher forever and staying blank until a reload.
    console.error('latency plot: uPlot init failed', err);
    destroyPlot();
  } finally {
    building = false;
  }
}

function render() {
  if (!props.active) return;
  if (!havePlottableData()) { destroyPlot(); return; }
  if (plotOrphaned()) { destroyPlot(); createPlot(); return; }
  if (!plot) { createPlot(); return; }
  try {
    plot.setData(buildData());
  } catch (err) {
    console.error('latency plot: setData failed, rebuilding', err);
    destroyPlot();
  }
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
           close; say so rather than showing a blank (or degenerate) chart.
           v-show rather than v-if so this does not restructure .plotarea on
           every transition.  (Note: that alone does NOT stop Vue from handing
           us a fresh .chart div - plotOrphaned() in render() is what actually
           recovers from that.) -->
      <div v-show="(hist?.points.length ?? 0) < 2" class="empty">
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
