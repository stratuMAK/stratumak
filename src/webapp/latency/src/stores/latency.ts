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
const POLL_TIMEOUT_MS = 1500; // per-request timeout for every client call
const STALE_MS = 1500; // status silence after which the badge goes stale
const WATCHDOG_MS = 500; // staleness check cadence
const ENUM_RETRY_MS = 2000; // startup retry while no instances exist yet
const ENUM_REFRESH_MS = 3000; // running re-enumeration (runtime load/unload)

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
let retryTimer: ReturnType<typeof setTimeout> | null = null;

// Bumped by reset() to fence polls that were already in flight at reset time: a
// stale poll resolving after reset()'s pollAll() would otherwise overwrite the
// freshly-cleared state with the old distribution/timeline for one interval.
let resetGen = 0;

// A poll's result is stale (must be discarded) if the client or selected
// instance changed, or a reset happened, while it was in flight.
function stale(c: LatencyClient | null, inst: string, gen: number): boolean {
  return client !== c || state.selected !== inst || resetGen !== gen;
}

// Race a client call against a timeout so a hung server cannot pin a poller
// (or the badge) forever.  The race abandons the losing request; if it ever
// resolves late anyway, the per-poller sequence guard below discards it.
function withTimeout<T>(p: Promise<T>): Promise<T> {
  let t: ReturnType<typeof setTimeout> | undefined;
  const guard = new Promise<T>((_, reject) => {
    t = setTimeout(() => reject(new Error(`no response within ${POLL_TIMEOUT_MS} ms`)), POLL_TIMEOUT_MS);
  });
  return Promise.race([p, guard]).finally(() => clearTimeout(t));
}

// Per-poller in-flight and ordering state.  `busy` makes each poller
// single-flight: while a request is pending its interval ticks are skipped,
// and a timeout clears the flag so the next tick retries.  `seq` is assigned
// at request start and `applied` records the newest response actually taken,
// so a late resolution (e.g. after its race already timed out) can never
// overwrite fresher data.
interface Poller {
  busy: boolean;
  seq: number;
  applied: number;
}
const statusPoller: Poller = { busy: false, seq: 0, applied: 0 };
const histogramPoller: Poller = { busy: false, seq: 0, applied: 0 };
const historyPoller: Poller = { busy: false, seq: 0, applied: 0 };

// Staleness watchdog: lastOkMs advances on every successfully-applied status
// poll; once anything was ever applied, status silence beyond STALE_MS flips
// the badge to stale.  This is what guarantees frozen data is never shown as
// live - even if a poller's settle path were somehow wedged.
let lastOkMs = 0;
let everOk = false;

function watchdog() {
  if (!everOk) return;
  const age = Date.now() - lastOkMs;
  if (age <= STALE_MS) return;
  state.connected = false;
  state.error = `no response from server (${(age / 1000).toFixed(1)}s)`;
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
//
// Returns null when the registry itself is unreachable (fetch failed or
// non-OK) and [] when it is reachable but lists no latency instances, so
// callers can tell "server down" from "nothing loaded".
async function enumerateInstances(): Promise<InstanceOption[] | null> {
  try {
    const origin = window.location.origin;
    const resp = await withTimeout(fetch(origin + '/api/v1/_registry'));
    if (!resp.ok) return null;
    const all = (await resp.json()) as Array<{ api_name: string; instance: string }>;
    const names = all.filter((e) => e.api_name === 'latency').map((e) => e.instance);

    return Promise.all(
      names.map(async (value): Promise<InstanceOption> => {
        try {
          const st = await withTimeout(new LatencyClient(origin, value).getStatus());
          return { value, label: st.label || value };
        } catch {
          return { value, label: value };
        }
      }),
    );
  } catch {
    return null;
  }
}

// Keep the instance selector in step with runtime load/unload.  Only replace
// the reactive list when it actually changed, so the <select> doesn't churn
// every tick; the current selection is left untouched even if it vanished
// from the list - its pollers keep failing visibly instead of the UI silently
// jumping to another thread.
let enumBusy = false;
async function refreshInstances() {
  if (enumBusy) return;
  enumBusy = true;
  try {
    const found = await enumerateInstances();
    if (found === null) return; // registry unreachable: keep the last known list
    const same =
      found.length === state.instances.length &&
      found.every((f, i) => f.value === state.instances[i]?.value && f.label === state.instances[i]?.label);
    if (!same) state.instances = found;
  } finally {
    enumBusy = false;
  }
}

function makeClient(inst: string) {
  client = new LatencyClient(window.location.origin, inst);
}

async function pollStatus() {
  const c = client;
  const inst = state.selected;
  const gen = resetGen;
  if (!c || statusPoller.busy) return;
  statusPoller.busy = true;
  const seq = ++statusPoller.seq;
  try {
    const st = await withTimeout(c.getStatus());
    if (stale(c, inst, gen) || seq <= statusPoller.applied) return;
    statusPoller.applied = seq;
    state.status = st;
    state.connected = true;
    state.error = '';
    lastOkMs = Date.now();
    everOk = true;
  } catch (e) {
    if (stale(c, inst, gen) || seq <= statusPoller.applied) return;
    state.connected = false;
    state.error = String(e);
  } finally {
    statusPoller.busy = false;
  }
}

async function pollHistogram() {
  const c = client;
  const inst = state.selected;
  const gen = resetGen;
  if (!c || histogramPoller.busy) return;
  histogramPoller.busy = true;
  const seq = ++histogramPoller.seq;
  try {
    const hist = await withTimeout(c.getHistogram());
    if (stale(c, inst, gen) || seq <= histogramPoller.applied) return;
    histogramPoller.applied = seq;
    if (!hist.bins) hist.bins = []; // server marshals an empty array as null
    state.histogram = hist;
    state.connected = true;
    state.error = '';
  } catch (e) {
    if (stale(c, inst, gen) || seq <= histogramPoller.applied) return;
    state.connected = false;
    state.error = String(e);
  } finally {
    histogramPoller.busy = false;
  }
}

// The plot series lives on the server (the drainer buckets every sample), so
// every client renders the same data and a late connect gets the retained
// history.  We just fetch the selected window.
async function pollHistory() {
  const c = client;
  const inst = state.selected;
  const gen = resetGen;
  const range = state.rangeSec; // fence: a range switched mid-flight is stale
  if (!c || historyPoller.busy) return;
  historyPoller.busy = true;
  const seq = ++historyPoller.seq;
  try {
    const h = await withTimeout(c.getHistory(range));
    if (stale(c, inst, gen) || state.rangeSec !== range || seq <= historyPoller.applied) return;
    historyPoller.applied = seq;
    // An empty history comes back as points:null (a Go nil slice marshals to
    // null, not []); normalise so consumers never hit `null.length`.  This is
    // the reset-blank-history TypeError.
    if (!h.points) h.points = [];
    state.history = h;
    state.connected = true;
    state.error = '';
  } catch (e) {
    if (stale(c, inst, gen) || state.rangeSec !== range || seq <= historyPoller.applied) return;
    state.connected = false;
    state.error = String(e);
  } finally {
    historyPoller.busy = false;
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
  // Re-arm-after-completion retry: if the page loaded before gomc-server (or
  // before any latency instance was created), keep trying instead of dying
  // into a permanently blank app.
  if (retryTimer) {
    clearTimeout(retryTimer);
    retryTimer = null;
  }
  const found = await enumerateInstances();
  if (found === null || found.length === 0) {
    state.error = found === null ? 'server unreachable' : 'no latency instances found';
    retryTimer = setTimeout(start, ENUM_RETRY_MS);
    return;
  }
  state.instances = found;
  const q = queryInstance();
  const has = (v: string) => state.instances.some((i) => i.value === v);
  state.selected = q && has(q) ? q : state.instances[0]?.value ?? '';
  state.error = '';
  makeClient(state.selected);
  await pollAll();
  clearTimers();
  timers = [
    setInterval(pollStatus, STATUS_MS),
    setInterval(pollHistogram, HISTOGRAM_MS),
    setInterval(pollHistory, HISTORY_MS),
    setInterval(refreshInstances, ENUM_REFRESH_MS),
    setInterval(watchdog, WATCHDOG_MS),
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
    await withTimeout(c.configure(ns));
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
    await withTimeout(c.reset());
    if (client !== c) return; // instance switched mid-request
    await pollAll();
    state.error = '';
  } catch (e) {
    if (client !== c) return;
    state.error = String(e);
  }
}

export const latencyStore = { state, start, selectInstance, setRange, setBinWidth, reset };
