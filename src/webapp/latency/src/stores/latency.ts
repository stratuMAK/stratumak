import { reactive } from 'vue';
import {
  LatencyClient,
  type LatencyStatus,
  type LatencyHistogram,
} from '../generated/latency_client';

// Rolling window for the live plot: ~60 s at the 200 ms status poll.
const PLOT_POINTS = 300;
const STATUS_MS = 200;
const HISTOGRAM_MS = 500;

// One point of live plot history (all times in seconds since page load,
// all latencies in nanoseconds).
export interface PlotPoint {
  t: number;
  lastNs: number;
  maxJitterNs: number;
}

interface State {
  instances: string[];
  selected: string;
  status: LatencyStatus | null;
  histogram: LatencyHistogram | null;
  plot: PlotPoint[];
  tick: number; // bumped on every status poll (drives the plot redraw)
  error: string;
  connected: boolean;
}

const state = reactive<State>({
  instances: [],
  selected: '',
  status: null,
  histogram: null,
  plot: [],
  tick: 0,
  error: '',
  connected: false,
});

let client: LatencyClient | null = null;
let statusTimer: ReturnType<typeof setInterval> | null = null;
let histTimer: ReturnType<typeof setInterval> | null = null;
const t0 = performance.now();

function queryInstance(): string {
  return new URLSearchParams(window.location.search).get('instance') || '';
}

// Enumerate all registered `latency` API instances from the server registry.
async function enumerateInstances(): Promise<string[]> {
  try {
    const resp = await fetch(window.location.origin + '/api/v1/_registry');
    if (!resp.ok) return [];
    const all = (await resp.json()) as Array<{ api_name: string; instance: string }>;
    return all.filter((e) => e.api_name === 'latency').map((e) => e.instance);
  } catch {
    return [];
  }
}

function makeClient(inst: string) {
  client = new LatencyClient(window.location.origin, inst);
}

async function pollStatus() {
  const c = client;
  const inst = state.selected;
  if (!c) return;
  try {
    const st = await c.getStatus();
    if (client !== c || state.selected !== inst) return; // instance switched mid-flight
    state.status = st;
    state.connected = true;
    state.error = '';
    state.plot.push({ t: (performance.now() - t0) / 1000, lastNs: st.lastNs, maxJitterNs: st.maxJitterNs });
    if (state.plot.length > PLOT_POINTS) state.plot.splice(0, state.plot.length - PLOT_POINTS);
    state.tick++;
  } catch (e) {
    if (client !== c || state.selected !== inst) return;
    state.connected = false;
    state.error = String(e);
  }
}

async function pollHistogram() {
  const c = client;
  const inst = state.selected;
  if (!c) return;
  try {
    const hist = await c.getHistogram();
    if (client !== c || state.selected !== inst) return; // instance switched mid-flight
    state.histogram = hist;
  } catch (e) {
    if (client !== c || state.selected !== inst) return;
    state.error = String(e);
  }
}

function clearTimers() {
  if (statusTimer) { clearInterval(statusTimer); statusTimer = null; }
  if (histTimer) { clearInterval(histTimer); histTimer = null; }
}

async function start() {
  state.instances = await enumerateInstances();
  const q = queryInstance();
  state.selected = q && state.instances.includes(q) ? q : state.instances[0] ?? '';
  if (!state.selected) {
    state.error = 'no latency instances found';
    return;
  }
  makeClient(state.selected);
  state.plot = [];
  await Promise.all([pollStatus(), pollHistogram()]);
  clearTimers();
  statusTimer = setInterval(pollStatus, STATUS_MS);
  histTimer = setInterval(pollHistogram, HISTOGRAM_MS);
}

function selectInstance(inst: string) {
  if (inst === state.selected) return;
  state.selected = inst;
  state.plot = [];
  state.status = null;
  state.histogram = null;
  makeClient(inst);
  Promise.all([pollStatus(), pollHistogram()]);
}

async function reset() {
  if (!client) return;
  try {
    await client.reset();
    state.plot = [];
    await Promise.all([pollStatus(), pollHistogram()]);
    state.error = '';
  } catch (e) {
    state.error = String(e);
  }
}

export const latencyStore = { state, start, selectInstance, reset };
