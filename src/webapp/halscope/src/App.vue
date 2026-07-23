<script setup lang="ts">
import { computed } from 'vue';
import ScopeToolbar from './components/ScopeToolbar.vue';
import ScopeChart from './components/ScopeChart.vue';
import ChannelPanel from './components/ChannelPanel.vue';
import BufferIndicator from './components/BufferIndicator.vue';
import VerticalControls from './components/VerticalControls.vue';
import { scopeStore } from './stores/scope';

function formatReal(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1000) return (v / 1000).toFixed(2) + 'k';
  if (abs >= 1) return v.toFixed(3);
  if (abs >= 0.001) return (v * 1000).toFixed(2) + 'm';
  if (abs >= 0.000001) return (v * 1e6).toFixed(1) + 'µ';
  if (v === 0) return '0';
  return v.toExponential(2);
}

const dragInfo = computed(() => {
  const st = scopeStore.state;
  if (!st.isDragging) return '';
  const parts: string[] = [];
  if (st.dragDeltaTime !== null) {
    parts.push('Δt=' + scopeStore.formatTimeValue(st.dragDeltaTime));
  }
  if (st.dragDeltaValue !== null) {
    parts.push('Δy=' + formatReal(st.dragDeltaValue));
  }
  if (st.dragStartTime !== null && st.dragStartValue !== null) {
    parts.push('from ' + scopeStore.formatTimeValue(st.dragStartTime) + ' / ' + formatReal(st.dragStartValue));
  }
  return parts.join('   ');
});
</script>

<template>
  <div class="app">
    <ScopeToolbar />
    <!-- S-10: file-view mode banner -->
    <div v-if="scopeStore.state.fileView" class="file-banner">
      <span>viewing loaded file &mdash; live paused</span>
      <button class="btn-live" @click="scopeStore.returnToLive()">Return to live</button>
    </div>
    <div class="main-area">
      <div class="chart-area">
        <div class="chart-stack" :class="{ stale: scopeStore.state.stale }">
          <BufferIndicator />
          <ScopeChart />
          <div class="cursor-bar">{{ dragInfo || '\u00A0' }}</div>
        </div>
        <!-- S-7: staleness watchdog overlay -->
        <div v-if="scopeStore.state.stale" class="stale-overlay">connection lost</div>
      </div>
      <VerticalControls />
      <div class="side-panel">
        <ChannelPanel />
      </div>
    </div>
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

.main-area {
  flex: 1;
  display: flex;
  overflow: hidden;
}

.chart-area {
  flex: 1;
  padding: 8px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  position: relative;
}

/* The chart stack must fill the chart-area with a DEFINITE height: ScopeChart
 * sizes uPlot from its own box and feeds that back through a ResizeObserver, so
 * a content-driven height here makes the plot grow every frame (Firefox pegs a
 * CPU on the loop; WebKit caps it but settles oversized). flex + min-height:0
 * pins the height to the viewport, breaking the loop. */
.chart-stack {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

/* S-7: greyed-out chart while the connection is stale. The `stale` class is on
 * .chart-stack, so the overlay (a sibling in .chart-area) stays crisp above it. */
.chart-stack.stale {
  opacity: 0.45;
  filter: grayscale(0.8);
  pointer-events: none;
}

.stale-overlay {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  background: rgba(0, 0, 0, 0.85);
  color: #f84;
  border: 1px solid #f84;
  border-radius: 4px;
  padding: 8px 20px;
  font-size: 14px;
  font-weight: 600;
  z-index: 20;
  pointer-events: none;
}

/* S-10: file-view banner */
.file-banner {
  display: flex;
  align-items: center;
  gap: 12px;
  background: #2a2a1a;
  color: #ff4;
  border-bottom: 1px solid #664;
  padding: 4px 12px;
  font-size: 12px;
  flex-shrink: 0;
}

.btn-live {
  background: #333;
  color: #ff4;
  border: 1px solid #886;
  border-radius: 3px;
  cursor: pointer;
  padding: 2px 10px;
  font-size: 12px;
}

.btn-live:hover {
  background: #443;
}

.side-panel {
  width: 280px;
  flex-shrink: 0;
  padding: 8px;
  border-left: 1px solid #333;
  overflow-y: auto;
}

.cursor-bar {
  background: #1a1a1a;
  border: 1px solid #333;
  border-radius: 3px;
  padding: 2px 8px;
  font-family: monospace;
  font-size: 12px;
  color: #4af;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex-shrink: 0;
}
</style>
