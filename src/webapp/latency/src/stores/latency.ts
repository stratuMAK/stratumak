import { reactive } from 'vue';
import {
  LatencyClient,
  type LatencyStatus,
  type LatencyHistogram,
  type LatencyHistory,
} from '../generated/latency_client';

const STATUS_MS = 200;
const HISTOGRAM_MS = 500;
const HISTORY_MS = 500;

// Selectable plot window (seconds; 0 = everything the server retains).
export const RANGES: { label: string; seconds: number }[] = [
  { label: '30s', seconds: 30 },
  { label: '1m', seconds: 60 },
  { label: '5m', seconds: 300 },
  { label: 'all', seconds: 0 },
];

// A selectable instance: `value` is the GMI instance name used for API calls,
// `label` is what the selector shows (the cmod's `label=` option, defaulting to
// the instance name).
export interface InstanceOption {
  value: string;
  label: string;
}

interface State {
  instances: InstanceOption[];
  selected: string;
  status: LatencyStatus | null;
  histogram: LatencyHistogram | null;
  history: LatencyHistory | null;
  rangeSec: number;
  error: string;
  connected: boolean;
}

const state = reactive<State>({
  instances: [],
  selected: '',
  status: null,
  histogram: null,
  history: null,
  rangeSec: 60,
  error: '',
  connected: false,
});

let client: LatencyClient | null = null;
let timers: ReturnType<typeof setInterval>[] = [];

// Bumped by reset() to fence polls that were already in flight at reset time: a
// stale poll resolving after reset()'s pollAll() would otherwise overwrite the
// freshly-cleared state with the old distribution/timeline for one interval.
let resetGen = 0;

// A poll's result is stale (must be discarded) if the client or selected
// instance changed, or a reset happened, while it was in flight.
function stale(c: LatencyClient | null, inst: string, gen: number): boolean {
  return client !== c || state.selected !== inst || resetGen !== gen;
}

function queryInstance(): string {
  return new URLSearchParams(window.location.search).get('instance') || '';
}

// Enumerate the registered `latency` API instances.  The instance name is the
// stable API identity; the display label comes from each instance's own status
// (the cmod's `label=` option, which defaults to the instance name).  This is
// explicit and correct for any config - unlike inferring a thread name from the
// HAL function map, which only held for the 1-instance-per-thread standalone
// layout.  Falls back to the instance name if a status fetch fails.
async function enumerateInstances(): Promise<InstanceOption[]> {
  try {
    const origin = window.location.origin;
    const resp = await fetch(origin + '/api/v1/_registry');
    if (!resp.ok) return [];
    const all = (await resp.json()) as Array<{ api_name: string; instance: string }>;
    const names = all.filter((e) => e.api_name === 'latency').map((e) => e.instance);

    return Promise.all(
      names.map(async (value): Promise<InstanceOption> => {
        try {
          const st = await new LatencyClient(origin, value).getStatus();
          return { value, label: st.label || value };
        } catch {
          return { value, label: value };
        }
      }),
    );
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
  const gen = resetGen;
  if (!c) return;
  try {
    const st = await c.getStatus();
    if (stale(c, inst, gen)) return;
    state.status = st;
    state.connected = true;
    state.error = '';
  } catch (e) {
    if (stale(c, inst, gen)) return;
    state.connected = false;
    state.error = String(e);
  }
}

async function pollHistogram() {
  const c = client;
  const inst = state.selected;
  const gen = resetGen;
  if (!c) return;
  try {
    const hist = await c.getHistogram();
    if (stale(c, inst, gen)) return;
    if (!hist.bins) hist.bins = []; // server marshals an empty array as null
    state.histogram = hist;
    state.connected = true;
    state.error = '';
  } catch (e) {
    if (stale(c, inst, gen)) return;
    state.connected = false;
    state.error = String(e);
  }
}

// The plot series lives on the server (the drainer buckets every sample), so
// every client renders the same data and a late connect gets the retained
// history.  We just fetch the selected window.
async function pollHistory() {
  const c = client;
  const inst = state.selected;
  const gen = resetGen;
  if (!c) return;
  try {
    const h = await c.getHistory(state.rangeSec);
    if (stale(c, inst, gen)) return;
    // An empty history comes back as points:null (a Go nil slice marshals to
    // null, not []); normalise so consumers never hit `null.length`.  This is
    // the reset-blank-history TypeError.
    if (!h.points) h.points = [];
    state.history = h;
    state.connected = true;
    state.error = '';
  } catch (e) {
    if (stale(c, inst, gen)) return;
    state.connected = false;
    state.error = String(e);
  }
}

function clearTimers() {
  timers.forEach(clearInterval);
  timers = [];
}

function pollAll() {
  return Promise.all([pollStatus(), pollHistogram(), pollHistory()]);
}

async function start() {
  state.instances = await enumerateInstances();
  const q = queryInstance();
  const has = (v: string) => state.instances.some((i) => i.value === v);
  state.selected = q && has(q) ? q : state.instances[0]?.value ?? '';
  if (!state.selected) {
    state.error = 'no latency instances found';
    return;
  }
  makeClient(state.selected);
  await pollAll();
  clearTimers();
  timers = [
    setInterval(pollStatus, STATUS_MS),
    setInterval(pollHistogram, HISTOGRAM_MS),
    setInterval(pollHistory, HISTORY_MS),
  ];
}

function selectInstance(inst: string) {
  if (inst === state.selected) return;
  state.selected = inst;
  state.status = null;
  state.histogram = null;
  state.history = null;
  makeClient(inst);
  pollAll();
}

function setRange(seconds: number) {
  state.rangeSec = seconds;
  pollHistory();
}

// Pin the histogram bin width server-side (0 = autoscale).  The server clears
// the histogram, since past counts cannot be re-binned to a different width.
async function setBinWidth(ns: number) {
  const c = client;
  if (!c) return;
  try {
    await c.configure(ns);
    if (client !== c) return; // instance switched mid-request
    await pollHistogram();
    state.error = '';
  } catch (e) {
    if (client !== c) return;
    state.error = String(e);
  }
}

async function reset() {
  const c = client;
  if (!c) return;
  // Fence polls already in flight (they captured the old resetGen) so their
  // stale pre-reset data can't land after the clear.
  resetGen++;
  try {
    await c.reset();
    if (client !== c) return; // instance switched mid-request
    await pollAll();
    state.error = '';
  } catch (e) {
    if (client !== c) return;
    state.error = String(e);
  }
}

export const latencyStore = { state, start, selectInstance, setRange, setBinWidth, reset };
