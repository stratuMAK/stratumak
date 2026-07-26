<script setup lang="ts">
import { computed } from 'vue';
import { latencyStore } from '../stores/latency';

const s = computed(() => latencyStore.state.status);

// Format a nanosecond value as a readable us / ms string.
function ns(v: number | bigint | undefined): string {
  if (v === undefined || v === null) return '—';
  const n = Number(v);
  const a = Math.abs(n);
  if (a >= 1_000_000) return (n / 1_000_000).toFixed(3) + ' ms';
  if (a >= 1_000) return (n / 1_000).toFixed(2) + ' µs';
  return Math.round(n) + ' ns';
}
function count(v: number | bigint | undefined): string {
  return v === undefined || v === null ? '—' : v.toLocaleString();
}
</script>

<template>
  <div class="summary">
    <div class="hero">
      <div class="label">max jitter</div>
      <div class="value">{{ ns(s?.maxJitterNs) }}</div>
      <div class="sub">worst-case |latency| since reset</div>
    </div>

    <div class="grid">
      <div class="tile"><div class="k">min latency</div><div class="v">{{ ns(s?.minNs) }}</div></div>
      <div class="tile"><div class="k">max latency</div><div class="v">{{ ns(s?.maxNs) }}</div></div>
      <div class="tile"><div class="k">last latency</div><div class="v">{{ ns(s?.lastNs) }}</div></div>
      <div class="tile"><div class="k">mean |lat|</div><div class="v">{{ ns(s?.meanNs) }}</div></div>
      <div class="tile"><div class="k">std dev</div><div class="v">{{ ns(s?.stddevNs) }}</div></div>
      <div class="tile"><div class="k">thread period</div><div class="v">{{ ns(s?.periodNs) }}</div></div>
      <div class="tile"><div class="k">samples</div><div class="v">{{ count(s?.samples) }}</div></div>
      <div class="tile">
        <div class="k">dropped</div>
        <div class="v" :class="{ warn: (s?.dropped ?? 0) > 0 }">{{ count(s?.dropped) }}</div>
      </div>
      <div class="tile"><div class="k">histogram bin</div><div class="v">{{ ns(s?.binWidthNs) }}</div></div>
    </div>
  </div>
</template>

<style scoped>
.summary { max-width: 760px; }
.hero {
  padding: 18px 20px; margin-bottom: 18px;
  border: 1px solid var(--border); border-radius: 10px; background: var(--surface-2);
}
.hero .label { color: var(--text-secondary); font-size: 13px; text-transform: uppercase; letter-spacing: 0.04em; }
.hero .value { font-size: 40px; font-weight: 700; font-variant-numeric: tabular-nums; margin: 4px 0; }
.hero .sub { color: var(--text-secondary); font-size: 12px; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 10px; }
.tile { padding: 12px 14px; border: 1px solid var(--border); border-radius: 8px; }
.tile .k { color: var(--text-secondary); font-size: 12px; }
.tile .v { font-size: 20px; font-weight: 600; font-variant-numeric: tabular-nums; margin-top: 3px; }
.tile .v.warn { color: #c04; }
</style>
