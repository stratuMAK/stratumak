<script setup lang="ts">
import { ref, watch, computed, onMounted, onBeforeUnmount } from 'vue';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { scopeStore } from '../stores/scope';

const chartEl = ref<HTMLDivElement>();
let plot: uPlot | null = null;
let resizeObs: ResizeObserver | null = null;

const NUM_DIVS = 10; // 10 vertical divisions like original scope

// uPlot inserts its legend row INSIDE chartEl, below the canvas. Give the
// canvas the box height minus this reservation so the legend fits within the
// (now definite-height) container instead of overflowing it.
const LEGEND_H = 40;

// Plot dimensions for the current container box.
function plotSize(rect: { width: number; height: number }) {
  return {
    width: Math.max(rect.width, 200),
    height: Math.max(rect.height - LEGEND_H, 120),
  };
}

// --- Cursor helpers ---
const selCh = computed(() => scopeStore.state.selectedChannel);
const selUI = computed(() => selCh.value >= 0 ? scopeStore.channelUI[selCh.value] : null);

/** Convert a division-space value back to real units for the selected channel */
function divToReal(divVal: number): number | null {
  const ui = selUI.value;
  if (!ui) return null;
  return (divVal - ui.vOffset) * ui.vScale;
}

/** Find the real value of the selected channel at a given time */
function getRealValueAtIdx(idx: number): number | null {
  if (selCh.value < 0) return null;
  const s = scopeStore.state.samples.find(s => s.channel === selCh.value);
  if (!s || idx < 0 || idx >= s.data.length) return null;
  return s.data[idx];
}

/** Find the closest sample index for a given time */
function timeToIdx(time: number): number {
  const tb = scopeStore.state.timeBase;
  if (tb.length === 0) return -1;
  // Binary search for closest
  let lo = 0, hi = tb.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (tb[mid] < time) lo = mid + 1;
    else hi = mid;
  }
  // Check neighbors for closest
  if (lo > 0 && Math.abs(tb[lo - 1] - time) < Math.abs(tb[lo] - time)) lo--;
  return lo;
}

// --- Drag state (non-reactive, for mouse events) ---
let dragging = false;
let dragStartPx = { x: 0, y: 0 };
let dragCurPx = { x: 0, y: 0 };
let hoverPx = { x: 0, traceY: 0, active: false };
const rubberEl = ref<HTMLCanvasElement>();

function drawOverlay() {
  const canvas = rubberEl.value;
  if (!canvas || !plot) return;
  const over = plot.over;
  const rect = over.getBoundingClientRect();
  const chartRect = chartEl.value?.getBoundingClientRect();
  if (!chartRect) return;

  // Size canvas to match the chart container
  const ox = rect.left - chartRect.left;
  const oy = rect.top - chartRect.top;
  canvas.width = chartRect.width;
  canvas.height = chartRect.height;
  canvas.style.width = chartRect.width + 'px';
  canvas.style.height = chartRect.height + 'px';

  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  ctx.clearRect(0, 0, canvas.width, canvas.height);

  // Draw cursor dot on the selected channel's trace
  if (hoverPx.active) {
    const dotX = hoverPx.x + ox;
    const dotY = hoverPx.traceY + oy;
    const color = selUI.value?.color ?? '#4af';
    ctx.beginPath();
    ctx.arc(dotX, dotY, 4, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
    ctx.strokeStyle = '#fff';
    ctx.lineWidth = 1.5;
    ctx.stroke();
  }

  // Draw rubberband during drag measurement
  if (dragging) {
    const sx = dragStartPx.x + ox;
    const sy = dragStartPx.y + oy;
    const cx = dragCurPx.x + ox;
    const cy = dragCurPx.y + oy;

    ctx.setLineDash([4, 4]);
    ctx.strokeStyle = '#4af';
    ctx.lineWidth = 1;

    // Vertical lines at start and current X
    ctx.beginPath();
    ctx.moveTo(sx, 0);
    ctx.lineTo(sx, canvas.height);
    ctx.moveTo(cx, 0);
    ctx.lineTo(cx, canvas.height);

    // Horizontal lines at start and current Y
    ctx.moveTo(0, sy);
    ctx.lineTo(canvas.width, sy);
    ctx.moveTo(0, cy);
    ctx.lineTo(canvas.width, cy);
    ctx.stroke();

    // Filled rectangle between cursors
    ctx.fillStyle = 'rgba(68, 170, 255, 0.06)';
    ctx.fillRect(
      Math.min(sx, cx), Math.min(sy, cy),
      Math.abs(cx - sx), Math.abs(cy - sy),
    );
  }
}

function clearOverlay() {
  hoverPx.active = false;
  const canvas = rubberEl.value;
  if (!canvas) return;
  const ctx = canvas.getContext('2d');
  if (ctx) ctx.clearRect(0, 0, canvas.width, canvas.height);
}

function onPlotMouseMove(e: MouseEvent) {
  if (!plot || selCh.value < 0) {
    scopeStore.state.cursorTime = null;
    scopeStore.state.cursorValue = null;
    return;
  }

  const rect = plot.over.getBoundingClientRect();
  const chartRect = chartEl.value?.getBoundingClientRect();
  const x = e.clientX - rect.left;
  const y = e.clientY - rect.top;

  // Convert pixel to data coordinates
  const time = plot.posToVal(x, 'x');

  const idx = timeToIdx(time);
  const realVal = getRealValueAtIdx(idx);

  scopeStore.state.cursorTime = time;
  scopeStore.state.cursorValue = realVal;

  // Position tooltip relative to the chart container
  if (chartRect) {
    tooltipX.value = e.clientX - chartRect.left + 12;
    tooltipY.value = e.clientY - chartRect.top - 8;
  }

  // Compute trace Y position for the cursor dot
  if (realVal !== null && selUI.value) {
    const divVal = (realVal / (selUI.value.vScale || 1)) + selUI.value.vOffset;
    const traceY = plot.valToPos(divVal, 'y');
    hoverPx = { x, traceY, active: true };
  } else {
    hoverPx.active = false;
  }

  if (dragging) {
    scopeStore.state.dragDeltaTime = time - (scopeStore.state.dragStartTime ?? 0);
    const curRealY = divToReal(plot.posToVal(y, 'y'));
    const startRealY = scopeStore.state.dragStartValue;
    scopeStore.state.dragDeltaValue = (curRealY !== null && startRealY !== null) ? curRealY - startRealY : null;
    dragCurPx = { x, y };
  }

  drawOverlay();
}

function onPlotMouseDown(e: MouseEvent) {
  if (!plot || selCh.value < 0 || e.button !== 0) return;

  const rect = plot.over.getBoundingClientRect();
  const x = e.clientX - rect.left;
  const y = e.clientY - rect.top;
  const time = plot.posToVal(x, 'x');
  const startRealY = divToReal(plot.posToVal(y, 'y'));

  dragging = true;
  dragStartPx = { x, y };
  dragCurPx = { ...dragStartPx };
  scopeStore.state.isDragging = true;
  scopeStore.state.dragStartTime = time;
  scopeStore.state.dragStartValue = startRealY;
  scopeStore.state.dragDeltaTime = 0;
  scopeStore.state.dragDeltaValue = 0;

  // Prevent uPlot's built-in drag-to-select
  e.stopPropagation();
}

function onPlotMouseUp(_e: MouseEvent) {
  if (dragging) {
    dragging = false;
    scopeStore.state.isDragging = false;
    scopeStore.state.dragStartTime = null;
    scopeStore.state.dragStartValue = null;
    scopeStore.state.dragDeltaTime = null;
    scopeStore.state.dragDeltaValue = null;
    drawOverlay(); // redraw without rubberband but keep dot
  }
}

function onPlotMouseLeave(_e: MouseEvent) {
  scopeStore.state.cursorTime = null;
  scopeStore.state.cursorValue = null;
  if (dragging) {
    onPlotMouseUp(_e);
  }
  clearOverlay();
}

function attachPlotEvents() {
  if (!plot) return;
  const over = plot.over;
  over.addEventListener('mousemove', onPlotMouseMove);
  over.addEventListener('mousedown', onPlotMouseDown);
  over.addEventListener('mouseup', onPlotMouseUp);
  over.addEventListener('mouseleave', onPlotMouseLeave);
}

function detachPlotEvents() {
  if (!plot) return;
  const over = plot.over;
  over.removeEventListener('mousemove', onPlotMouseMove);
  over.removeEventListener('mousedown', onPlotMouseDown);
  over.removeEventListener('mouseup', onPlotMouseUp);
  over.removeEventListener('mouseleave', onPlotMouseLeave);
}

// --- Hover tooltip ---
const tooltipX = ref(0);
const tooltipY = ref(0);

const hoverText = computed(() => {
  const st = scopeStore.state;
  if (st.cursorTime === null || st.cursorValue === null || selCh.value < 0) return '';
  const t = scopeStore.formatTimeValue(st.cursorTime);
  const v = formatReal(st.cursorValue);
  return `${t}  ${v}`;
});

const tooltipStyle = computed(() => ({
  left: tooltipX.value + 'px',
  top: tooltipY.value + 'px',
}));

function formatReal(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1000) return (v / 1000).toFixed(2) + 'k';
  if (abs >= 1) return v.toFixed(3);
  if (abs >= 0.001) return (v * 1000).toFixed(2) + 'm';
  if (abs >= 0.000001) return (v * 1e6).toFixed(1) + 'µ';
  if (v === 0) return '0';
  return v.toExponential(2);
}

/**
 * The channels the chart renders, with labels from the decode-time snapshot
 * (S-4): a trace label always names the pin whose data is on screen, never
 * the live channel config.  In file-view mode (S-10) the series come purely
 * from the loaded file's snapshot.
 */
function seriesChannels(): { channel: number; label: string }[] {
  const st = scopeStore.state;
  if (st.fileView) {
    return st.samples.map(s => ({
      channel: s.channel,
      label: s.pinName || `Ch ${s.channel}`,
    }));
  }
  return st.status.channels.filter(c => c.enabled).map(ch => {
    const snap = st.samples.find(s => s.channel === ch.channel);
    return {
      channel: ch.channel,
      label: (snap ? snap.pinName : ch.pinName) || `Ch ${ch.channel}`,
    };
  });
}

function buildOpts(width: number, height: number): uPlot.Options {
  const channels = seriesChannels();
  const selChVal = scopeStore.state.selectedChannel;
  const selUIVal = selChVal >= 0 ? scopeStore.channelUI[selChVal] : null;

  const series: uPlot.Series[] = [
    { label: 'Time (s)' },
    ...channels.map((ch) => ({
      label: ch.label,
      stroke: scopeStore.channelUI[ch.channel]?.color ?? '#ffff00',
      width: ch.channel === selChVal ? 2 : 1,
      show: scopeStore.channelUI[ch.channel]?.visible ?? true,
    })),
  ];

  // Y axis: -5 to +5 divisions (fixed, like original scope)
  const halfDivs = NUM_DIVS / 2;

  return {
    width,
    height,
    cursor: { show: false }, // We handle cursor ourselves
    select: { show: false, left: 0, top: 0, width: 0, height: 0 }, // Disable drag select
    scales: {
      x: { time: false },
      y: {
        auto: false,
        range: [-halfDivs, halfDivs],
      },
    },
    axes: [
      {
        label: 'Time (s)',
        stroke: '#888',
        grid: { stroke: '#333', width: 1 },
        ticks: { stroke: '#444', width: 1 },
      },
      {
        stroke: '#888',
        grid: { stroke: '#333', width: 1 },
        ticks: { stroke: '#444', width: 1 },
        // Label Y axis ticks in the selected channel's real units
        values: (_self: uPlot, divs: number[]) => {
          if (!selUIVal) return divs.map(d => d.toFixed(1));
          // For bit channels (scale=1, offset centered on 0.5), show 0/1
          const isBit = selUIVal.vScale <= 1 && divs.some(d => {
            const r = (d - selUIVal.vOffset) * selUIVal.vScale;
            return Math.abs(r) < 0.01 || Math.abs(r - 1) < 0.01;
          });
          return divs.map(d => {
            const real = (d - selUIVal.vOffset) * selUIVal.vScale;
            if (isBit) return real.toFixed(1);
            // Format nicely
            if (Math.abs(real) >= 1000) return (real / 1000).toFixed(1) + 'k';
            if (Math.abs(real) >= 1) return real.toFixed(2);
            if (Math.abs(real) >= 0.001) return (real * 1000).toFixed(1) + 'm';
            return real.toExponential(1);
          });
        },
      },
    ],
    series,
  };
}

function buildData(): uPlot.AlignedData {
  const st = scopeStore.state;
  if (st.timeBase.length === 0 || st.samples.length === 0) {
    return [new Float64Array(0)];
  }

  const time = Array.from(st.timeBase) as unknown as number[];
  const data: (number[] | (number | null)[] | Float64Array)[] = [time];

  const channels = seriesChannels();
  for (const ch of channels) {
    const s = st.samples.find(s => s.channel === ch.channel);
    const ui = scopeStore.channelUI[ch.channel];
    if (s && ui) {
      // Transform to division space: div = (rawValue / vScale) + vOffset
      const transformed = new Float64Array(s.data.length);
      const scale = ui.vScale || 1;
      const offset = ui.vOffset;
      for (let i = 0; i < s.data.length; i++) {
        transformed[i] = (s.data[i] / scale) + offset;
      }
      data.push(transformed);
    } else {
      // S-6: no captured data for this channel — render a gap (nulls),
      // never a fabricated flat-zero trace.
      data.push(new Array<number | null>(st.timeBase.length).fill(null));
    }
  }

  return data as uPlot.AlignedData;
}

/** Apply the display window X range to the chart via uPlot scales */
function applyViewWindow() {
  if (!plot) return;
  const dw = scopeStore.calcDisplayWindow();
  plot.setScale('x', { min: dw.screenStartTime, max: dw.screenEndTime });
}

function createPlot() {
  if (!chartEl.value) return;
  detachPlotEvents();
  plot?.destroy();

  const { width: w, height: h } = plotSize(chartEl.value.getBoundingClientRect());

  const opts = buildOpts(w, h);
  const data = buildData();
  plot = new uPlot(opts, data, chartEl.value);
  applyViewWindow();
  attachPlotEvents();

  // Add scroll-to-zoom on the chart (like original scope_disp.c change_zoom)
  plot.over.addEventListener('wheel', (e: WheelEvent) => {
    e.preventDefault();
    const dir = e.deltaY < 0 ? 1 : -1;
    scopeStore.setHorizZoom(scopeStore.state.zoomSetting + dir);
  });
}

function updateData() {
  if (!plot) {
    createPlot();
    return;
  }

  const channels = seriesChannels();
  const data = buildData();

  // If channel count changed, rebuild the plot (series config differs)
  if (channels.length + 1 !== plot.series.length) {
    createPlot();
    return;
  }

  plot.setData(data);
  applyViewWindow();
}

// Watch for sample data changes
watch(
  () => scopeStore.state.samples,
  () => updateData(),
);

// Watch for view window changes (pan/zoom) — just update X scale, not rebuild
watch(
  () => [scopeStore.state.zoomSetting, scopeStore.state.posSetting],
  () => applyViewWindow(),
);

// Watch for selected channel or channelUI changes — rebuild plot for Y axis labels
watch(
  () => scopeStore.state.selectedChannel,
  () => createPlot(),
);

// Watch for per-channel scale/offset changes — update data transform
watch(
  () => {
    const sel = scopeStore.state.selectedChannel;
    if (sel < 0) return null;
    const ui = scopeStore.channelUI[sel];
    return ui ? `${ui.vScale}:${ui.vOffset}` : null;
  },
  () => {
    if (plot) {
      plot.setData(buildData());
      // Rebuild to update Y axis label values
      createPlot();
    }
  },
);

// S-4: rebuild the plot unconditionally whenever the rendered channel list
// (identity, not just count) changes — labels and series config must always
// match the channel set.
watch(
  () => seriesChannels().map(c => `${c.channel}:${c.label}`).join('\0')
    + (scopeStore.state.fileView ? '\0file' : ''),
  () => createPlot(),
);

// S-13: channel visibility toggles take effect immediately.
watch(
  () => scopeStore.channelUI.map(u => (u.visible ? '1' : '0')).join(''),
  () => {
    if (!plot) return;
    const channels = seriesChannels();
    channels.forEach((ch, i) => {
      plot!.setSeries(i + 1, {
        show: scopeStore.channelUI[ch.channel]?.visible ?? true,
      });
    });
  },
);

onMounted(() => {
  createPlot();
  resizeObs = new ResizeObserver(() => {
    if (chartEl.value && plot) {
      plot.setSize(plotSize(chartEl.value.getBoundingClientRect()));
    }
  });
  if (chartEl.value) resizeObs.observe(chartEl.value);
});

onBeforeUnmount(() => {
  resizeObs?.disconnect();
  detachPlotEvents();
  plot?.destroy();
});
</script>

<template>
  <div class="scope-chart" ref="chartEl">
    <canvas ref="rubberEl" class="rubberband-canvas"></canvas>
    <!-- Hover tooltip overlay -->
    <div v-if="hoverText" class="cursor-tooltip" :style="tooltipStyle">
      {{ hoverText }}
    </div>
  </div>
</template>

<style scoped>
.scope-chart {
  position: relative;
  width: 100%;
  /* Definite, flex-derived height (parent .chart-stack is flex:1/min-height:0).
   * NOT height:100% against an auto parent — that made the box content-sized,
   * so feeding it back into uPlot.setSize grew it every frame (the Firefox
   * Y-zoom CPU loop). min-height is only a floor and never drives the size. */
  flex: 1 1 0;
  min-height: 200px;
  background: #111;
  border: 1px solid #333;
  border-radius: 4px;
  overflow: hidden;
}

.cursor-tooltip {
  position: absolute;
  pointer-events: none;
  background: rgba(0, 0, 0, 0.8);
  color: #eee;
  font-family: monospace;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 3px;
  border: 1px solid #555;
  white-space: nowrap;
  z-index: 10;
}

.rubberband-canvas {
  position: absolute;
  top: 0;
  left: 0;
  pointer-events: none;
  z-index: 5;
}

/* uPlot dark theme overrides */
.scope-chart :deep(.u-wrap) {
  background: #111;
}
.scope-chart :deep(.u-legend) {
  background: #1a1a1a;
  color: #ccc;
  font-size: 12px;
  padding: 4px 8px;
}
.scope-chart :deep(.u-legend .u-series) {
  padding: 0 8px;
}
</style>
