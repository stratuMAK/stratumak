import { reactive } from 'vue';
import {
  HalscopeClient,
  type ScopeStatus,
  type ThreadInfo,
  type CaptureConfig,
  type TriggerConfig,
  type ChannelConfig,
  ScopeState,
  TrigEdge,
  HalType,
  MAX_CHANNELS,
} from '../generated/halscope_client';
import { HalscopeWatchClient } from '../generated/halscope_watch_client';

// Per-channel UI settings (colors, vertical scale, offset)
export interface ChannelUI {
  color: string;
  vScale: number;   // units per division
  vOffset: number;  // vertical offset in divisions
  visible: boolean;
}

// Decoded sample data per channel.  pinName/dataType are snapshotted at
// frame-decode time (S-4) so labels and CSV export always describe the data
// on screen, even if the live channel config changes afterwards.
export interface ChannelSamples {
  channel: number;
  pinName: string;
  dataType: number;
  data: Float64Array;
}

// Capture parameters snapshotted at frame-decode time (S-2).  The time base,
// display window, buffer indicator and CSV export are driven from this — not
// from the live (UI-editable) captureConfig or the current server status.
export interface CaptureMeta {
  periodNs: number;         // thread period of the displayed capture
  samplePeriodMult: number;
  recLen: number;
  preTrig: number;
}

const CHANNEL_COLORS = [
  '#ffff00', '#00ff00', '#ff4444', '#44aaff',
  '#ff44ff', '#44ffff', '#ff8800', '#88ff00',
  '#ff0088', '#0088ff', '#8800ff', '#00ff88',
  '#ffaa44', '#44ffaa', '#aa44ff', '#ff44aa',
];

interface ScopeStore {
  // Connection
  connected: boolean;
  error: string;
  stale: boolean;     // S-7: liveness watchdog lost contact with the server

  // File-view mode (S-10): a loaded capture file is displayed; live sample
  // pushes are not applied to the displayed data until the user returns to
  // live.
  fileView: boolean;

  // Status from server
  status: ScopeStatus;

  // Threads
  threads: ThreadInfo[];

  // Available pins
  pins: string[];
  pinFilter: string;

  // Capture config
  captureConfig: CaptureConfig;

  // Trigger config
  triggerConfig: TriggerConfig;

  // Channel UI settings
  channelUI: ChannelUI[];

  // Sample data
  samples: ChannelSamples[];
  timeBase: Float64Array; // time axis in seconds
  captureMeta: CaptureMeta | null;

  // UI state
  selectedThread: string;
  selectedChannel: number; // -1 = none selected

  // Horizontal display (ported from scope_horiz_t)
  zoomSetting: number;   // 1..9, 1 = fit record
  posSetting: number;    // 0.0..1.0, position within record

  // Cursor state (set by chart mouse events)
  cursorTime: number | null;     // hover time in seconds
  cursorValue: number | null;    // hover value in real units (selected channel)
  dragStartTime: number | null;  // drag anchor time
  dragStartValue: number | null; // drag anchor value
  dragDeltaTime: number | null;  // delta from drag start
  dragDeltaValue: number | null; // delta from drag start
  isDragging: boolean;
}

const state = reactive<ScopeStore>({
  connected: false,
  error: '',
  stale: false,
  fileView: false,

  status: {
    state: ScopeState.IDLE,
    samples: 0,
    recLen: 16000,
    preTrig: 8000,
    sampleLen: 0,
    maxChannels: 1,
    samplePeriodMult: 1,
    threadPeriodNs: 0n,
    threadName: '',
    trigChannel: -1,
    trigLevel: 0,
    trigEdge: TrigEdge.RISING,
    trigAutoTrig: false,
    generation: 0,
    continuous: false,
    channels: [],
    channelOptions: [],
  },

  threads: [],
  pins: [],
  pinFilter: '',

  captureConfig: {
    threadName: '',
    maxChannels: 1,
    samplePeriodMult: 1,
  },

  triggerConfig: {
    channel: -1,
    level: 0,
    edge: TrigEdge.RISING,
    autoTrig: true,
  },

  channelUI: Array.from({ length: MAX_CHANNELS }, (_, i) => ({
    color: CHANNEL_COLORS[i % CHANNEL_COLORS.length],
    vScale: 1,
    vOffset: 0,
    visible: true,
  })),

  samples: [],
  timeBase: new Float64Array(0),
  captureMeta: null,

  selectedThread: '',
  selectedChannel: -1,

  zoomSetting: 1,
  posSetting: 0.5,

  cursorTime: null,
  cursorValue: null,
  dragStartTime: null,
  dragStartValue: null,
  dragDeltaTime: null,
  dragDeltaValue: null,
  isDragging: false,
});

let restClient: HalscopeClient | null = null;
let wsClient: HalscopeWatchClient | null = null;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let reconnectDelay = 1000;
let configSynced = false;

function getBaseUrl(): string {
  return window.location.origin;
}

function getWsUrl(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}/api/v1/watch`;
}

// --- Command result checking (S-1) ---
//
// Provider commands return HTTP 200 with an int rc: 0 = ok, negative errno =
// refusal (e.g. -EBUSY while a capture is running).  Every command rc must be
// checked or the UI silently diverges from what the RT actually accepted.

function rcText(rc: number): string {
  if (rc === -16) return 'busy';    // -EBUSY
  if (rc === -22) return 'invalid'; // -EINVAL
  return `error ${rc}`;
}

function checkRc(rc: number, what: string): boolean {
  if (rc !== 0) {
    state.error = `${what} failed: ${rcText(rc)}`;
    return false;
  }
  return true;
}

/** Force capture/trigger config back to what the server actually has. */
function applyConfigFromStatus(status: ScopeStatus) {
  if (status.maxChannels > 0) {
    state.captureConfig.maxChannels = status.maxChannels;
  }
  if (status.samplePeriodMult > 0) {
    state.captureConfig.samplePeriodMult = status.samplePeriodMult;
  }
  if (status.threadName) {
    state.captureConfig.threadName = status.threadName;
    state.selectedThread = status.threadName;
  }
  state.triggerConfig.channel = status.trigChannel;
  state.triggerConfig.level = status.trigLevel;
  state.triggerConfig.edge = status.trigEdge;
  state.triggerConfig.autoTrig = status.trigAutoTrig;
}

/**
 * After a refused configure/setTrigger, resync the UI config from a fresh
 * getStatus() so the UI shows what the RT actually accepted (S-1).
 * Never clobbers the refusal message already in state.error.
 */
async function resyncConfig() {
  if (!restClient) return;
  try {
    const status = await restClient.getStatus();
    onStatusUpdate(status);
    applyConfigFromStatus(status);
  } catch {
    // keep the refusal message; resync will happen on the next status poll
  }
}

// --- Actions ---

async function connect() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }

  try {
    if (!restClient) {
      restClient = new HalscopeClient(getBaseUrl());
    }

    // Load initial data via REST immediately (don't wait for WS)
    const [threads, status] = await Promise.all([
      restClient.listThreads(),
      restClient.getStatus(),
    ]);
    state.threads = threads;
    onStatusUpdate(status);

    if (threads.length > 0 && !state.selectedThread) {
      state.selectedThread = threads[0].name;
      state.captureConfig.threadName = threads[0].name;
    }
  } catch (e) {
    state.error = `REST connection failed: ${e}`;
    restClient = null;
    scheduleReconnect();
    return;
  }

  // S-7: liveness watchdog — REST is up, keep checking it independently of
  // the WS so a dead connection can't display a frozen capture as live.
  startLivenessWatchdog();

  // Connect WS for live updates — failures here don't block the UI
  try {
    wsClient = new HalscopeWatchClient(getWsUrl());
    await wsClient.connect();
    wsClient.onClose = onWsClose;
    state.connected = true;
    state.error = '';
    reconnectDelay = 1000;

    // Subscribe to state updates
    wsClient.subscribeWatchState(onStatusUpdate, 100);

    // Subscribe to sample data
    wsClient.subscribeWatchSamples(handleSampleFrame, 100);
  } catch (_e) {
    state.error = `WebSocket failed, retrying…`;
    state.connected = false;
    scheduleReconnect();
  }
}

function scheduleReconnect() {
  if (reconnectTimer) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    connect();
  }, reconnectDelay);
  reconnectDelay = Math.min(reconnectDelay * 2, 10000);
}

function disconnect() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  stopLivenessWatchdog();
  wsClient?.close();
  wsClient = null;
  restClient = null;
  state.connected = false;
}

// --- Liveness watchdog (S-7 client half) ---
//
// Polls getStatus via REST every ~2s independent of the WS.  On failure or
// timeout the UI is marked stale (chart greyed, "connection lost") until a
// poll succeeds again.  Server keepalive frames are a deferred shared
// apiserver change.

const LIVENESS_INTERVAL_MS = 2000;
const LIVENESS_TIMEOUT_MS = 1800;

let livenessTimer: ReturnType<typeof setInterval> | null = null;
let livenessBusy = false;

function startLivenessWatchdog() {
  if (livenessTimer) return;
  livenessTimer = setInterval(pollLiveness, LIVENESS_INTERVAL_MS);
}

function stopLivenessWatchdog() {
  if (livenessTimer) {
    clearInterval(livenessTimer);
    livenessTimer = null;
  }
  state.stale = false;
}

async function pollLiveness() {
  if (!restClient || livenessBusy) return;
  livenessBusy = true;
  try {
    const status = await Promise.race([
      restClient.getStatus(),
      new Promise<never>((_, reject) =>
        setTimeout(() => reject(new Error('status poll timeout')), LIVENESS_TIMEOUT_MS)),
    ]);
    state.stale = false;
    onStatusUpdate(status);
  } catch {
    state.stale = true;
  } finally {
    livenessBusy = false;
  }
}

function channelsChanged(a: typeof state.status.channels, b: typeof state.status.channels): boolean {
  if (a.length !== b.length) return true;
  for (let i = 0; i < a.length; i++) {
    if (a[i].channel !== b[i].channel || a[i].pinName !== b[i].pinName || a[i].enabled !== b[i].enabled)
      return true;
  }
  return false;
}

function onStatusUpdate(status: Partial<ScopeStatus>) {
  // WS delta updates only include changed fields — only update fields
  // that are actually present to avoid clobbering with undefined.
  if ('state' in status) state.status.state = status.state!;
  if ('samples' in status) state.status.samples = status.samples!;
  if ('recLen' in status) state.status.recLen = status.recLen!;
  if ('preTrig' in status) state.status.preTrig = status.preTrig!;
  if ('sampleLen' in status) state.status.sampleLen = status.sampleLen!;
  if ('maxChannels' in status) state.status.maxChannels = status.maxChannels!;
  if ('samplePeriodMult' in status) state.status.samplePeriodMult = status.samplePeriodMult!;
  if ('threadPeriodNs' in status) state.status.threadPeriodNs = status.threadPeriodNs!;
  if ('threadName' in status) state.status.threadName = status.threadName!;
  if ('trigChannel' in status) state.status.trigChannel = status.trigChannel!;
  if ('generation' in status) state.status.generation = status.generation!;
  if ('continuous' in status) state.status.continuous = status.continuous!;
  if ('channelOptions' in status) state.status.channelOptions = status.channelOptions!;

  // Only replace channels array if present and content actually changed
  if ('channels' in status) {
    const channels = status.channels ?? [];
    if (channelsChanged(state.status.channels, channels)) {
      state.status.channels = channels;
    }
  }

  // Sync capture/trigger config from server only on first status after
  // (re-)connect.  Once synced, the UI owns these values — the watch
  // stream must not clobber in-flight edits.
  if (!configSynced) {
    configSynced = true;
    if ('maxChannels' in status && status.maxChannels! > 0) {
      state.captureConfig.maxChannels = status.maxChannels!;
    }
    if ('samplePeriodMult' in status && status.samplePeriodMult! > 0) {
      state.captureConfig.samplePeriodMult = status.samplePeriodMult!;
    }
    if ('threadName' in status && status.threadName) {
      state.captureConfig.threadName = status.threadName;
      state.selectedThread = status.threadName;
    }
    if ('trigChannel' in status) {
      state.triggerConfig.channel = status.trigChannel!;
    }
    if ('trigLevel' in status) {
      state.triggerConfig.level = status.trigLevel!;
    }
    if ('trigEdge' in status) {
      state.triggerConfig.edge = status.trigEdge!;
    }
    if ('trigAutoTrig' in status) {
      state.triggerConfig.autoTrig = status.trigAutoTrig!;
    }
  }
}

function onWsClose() {
  state.connected = false;
  wsClient = null;
  configSynced = false;
  scheduleReconnect();
}

// --- Sample frame decode (S-2, S-4, S-12) ---

// S-12: malformed-frame errors are reported once, not per frame.
let frameErrorReported = false;

function reportBadFrame(detail: string) {
  if (frameErrorReported) return;
  frameErrorReported = true;
  state.error = `Dropped malformed sample frame (${detail})`;
}

/**
 * Decode a binary sample frame from the watch WS and publish it to the store.
 *
 * Binary layout: sample_header_t (16 bytes, 4× uint32 LE) + sample data
 * (float64 LE).  Header: { sample_count, sample_len, start_offset, reserved }.
 *
 * Exported for tests.
 */
export function handleSampleFrame(buf: ArrayBuffer) {
  // S-10: while viewing a loaded file, live pushes must not clobber the
  // displayed data.
  if (state.fileView) return;

  // S-12: validate the header against the actual buffer size before
  // constructing any views.
  const byteLength = buf.byteLength;
  if (byteLength < 16) {
    reportBadFrame(`${byteLength} bytes < header size`);
    return;
  }
  if ((byteLength - 16) % 8 !== 0) {
    reportBadFrame(`${byteLength} bytes not 8-aligned after header`);
    return;
  }

  const view = new DataView(buf);
  const sampleCount = view.getUint32(0, true);
  const sampleLen = view.getUint32(4, true);
  const startOffset = view.getUint32(8, true);

  if (sampleLen === 0 || sampleCount === 0) return;

  if (16 + sampleCount * sampleLen * 8 > byteLength) {
    reportBadFrame(`header claims ${sampleCount}×${sampleLen} samples, got ${byteLength} bytes`);
    return;
  }
  frameErrorReported = false;

  const channels = state.status.channels.filter(c => c.enabled);

  // Float64Array view over the data portion (header is 16 bytes = aligned to 8)
  const allSamples = new Float64Array(buf, 16, sampleCount * sampleLen);

  const decoded: ChannelSamples[] = [];
  for (const ch of channels) {
    // Fixed-column layout: channel N is at column index N
    // Sample layout is [s0c0, s0c1, ..., s0cN, s1c0, s1c1, ...]
    if (ch.channel >= sampleLen) continue;
    const data = new Float64Array(sampleCount);
    for (let si = 0; si < sampleCount; si++) {
      data[si] = allSamples[si * sampleLen + ch.channel];
    }
    // S-4: snapshot pin identity together with the data
    decoded.push({ channel: ch.channel, pinName: ch.pinName, dataType: ch.dataType, data });
  }

  // S-2: snapshot the capture parameters that produced this frame.  No
  // silent fallback: if the thread period is unknown at decode time, surface
  // a warning.
  let periodNs = Number(state.status.threadPeriodNs);
  if (!(periodNs > 0)) {
    const t = state.threads.find(t => t.name === state.status.threadName);
    periodNs = Number(t?.periodNs ?? 0);
    state.error = 'Thread period unknown at capture-decode time — time axis may be wrong';
  }
  const meta: CaptureMeta = {
    periodNs,
    samplePeriodMult: state.status.samplePeriodMult,
    recLen: state.status.recLen,
    preTrig: state.status.preTrig,
  };

  // Build time base from the snapshotted parameters
  const dt = (meta.periodNs * meta.samplePeriodMult) / 1e9;
  const tb = new Float64Array(sampleCount);
  const t0 = -(startOffset * dt);
  for (let i = 0; i < sampleCount; i++) {
    tb[i] = t0 + i * dt;
  }

  state.samples = decoded;
  state.timeBase = tb;
  state.captureMeta = meta;
}

/** Clear displayed capture data (S-4: on any channel-set edit). */
function clearSampleData() {
  state.samples = [];
  state.timeBase = new Float64Array(0);
  state.captureMeta = null;
}

function getSelectedThreadPeriod(): number {
  const t = state.threads.find(t => t.name === state.captureConfig.threadName);
  return Number(t?.periodNs ?? 0);
}

async function configure(): Promise<boolean> {
  if (!restClient) return false;
  try {
    state.captureConfig.threadName = state.selectedThread;
    // preTrig is hardcoded server-side (recLen/2, like original halscope); the
    // client does not send it (CaptureConfig has no preTrig field).
    const rc = await restClient.configure(state.captureConfig);
    if (!checkRc(rc, 'Configure')) {
      await resyncConfig();
      return false;
    }
    // Re-fetch status so recLen/preTrig reflect the new maxChannels
    const status = await restClient.getStatus();
    onStatusUpdate(status);
    return true;
  } catch (e) {
    state.error = `Configure failed: ${e}`;
    return false;
  }
}

async function setTrigger(): Promise<boolean> {
  if (!restClient) return false;
  try {
    const rc = await restClient.setTrigger(state.triggerConfig);
    if (!checkRc(rc, 'Set trigger')) {
      await resyncConfig();
      return false;
    }
    return true;
  } catch (e) {
    state.error = `Set trigger failed: ${e}`;
    return false;
  }
}

async function addChannel(pinName: string, channel: number) {
  if (!restClient) return;
  state.error = '';
  try {
    // S-9: remember the previous pin type on this slot — if the trigger
    // sources this slot and the type changes, the trigger must be re-sent.
    const prev = state.status.channels.find(c => c.channel === channel && c.enabled);
    const prevType = prev?.dataType;

    const ch: ChannelConfig = { channel, pinName: pinName };
    const rc = await restClient.setChannel(ch);
    if (!checkRc(rc, 'Set channel')) return;

    // Refresh status to see the new channel
    const status = await restClient.getStatus();
    onStatusUpdate(status);

    // S-4: the channel set changed — the displayed capture no longer
    // matches it.
    clearSampleData();

    // S-9: re-send the trigger if its source channel changed pin type
    if (state.triggerConfig.channel === channel) {
      const now = state.status.channels.find(c => c.channel === channel && c.enabled);
      if (now && prevType !== undefined && now.dataType !== prevType) {
        await setTrigger();
      }
    }
  } catch (e) {
    state.error = `Set channel failed: ${e}`;
  }
}

async function removeChannel(channel: number) {
  if (!restClient) return;
  state.error = '';
  try {
    const rc = await restClient.clearChannel(channel);
    if (!checkRc(rc, 'Clear channel')) return;

    // Refresh status to see the channel gone
    const status = await restClient.getStatus();
    onStatusUpdate(status);

    // S-4: the channel set changed
    clearSampleData();

    // S-9: don't leave a dangling trigger on the removed channel — retarget
    // to another enabled channel (or none) and push it to the server.
    if (state.triggerConfig.channel === channel) {
      const remaining = state.status.channels.filter(c => c.enabled);
      state.triggerConfig.channel = remaining.length > 0 ? remaining[0].channel : -1;
      await setTrigger();
    }
  } catch (e) {
    state.error = `Clear channel failed: ${e}`;
  }
}

async function arm() {
  if (!restClient) return;
  state.error = '';
  try {
    // Always send current config + trigger before arming
    if (!(await configure())) return;
    if (!(await setTrigger())) return;
    // S-8: Single is a one-shot capture — clear continuous mode first so a
    // leftover continuous=1 can't turn Single into free-run.
    if (!checkRc(await restClient.setContinuous(false), 'Set continuous')) return;
    if (!checkRc(await restClient.arm(), 'Arm')) return;
  } catch (e) {
    state.error = `Arm failed: ${e}`;
  }
}

async function stop() {
  if (!restClient) return;
  state.error = '';
  try {
    if (!checkRc(await restClient.setContinuous(false), 'Set continuous')) return;
    checkRc(await restClient.reset(), 'Reset');
  } catch (e) {
    state.error = `Reset failed: ${e}`;
  }
}

async function fullReset() {
  if (!restClient) return;
  state.error = '';
  try {
    // Stop any running capture
    checkRc(await restClient.setContinuous(false), 'Set continuous');
    checkRc(await restClient.reset(), 'Reset');
    // Clear all channels on server.  S-14: snapshot the enabled-channel list
    // before awaiting — the live reactive array can be replaced mid-loop by a
    // status update.
    const toClear = state.status.channels.filter(c => c.enabled).map(c => c.channel);
    for (const ch of toClear) {
      checkRc(await restClient.clearChannel(ch), `Clear channel ${ch}`);
    }
    // Reset local UI state
    clearSampleData();
    state.fileView = false;
    state.selectedChannel = -1;
    state.triggerConfig.channel = -1;
    state.triggerConfig.level = 0;
    state.triggerConfig.edge = TrigEdge.RISING;
    state.triggerConfig.autoTrig = true;
    // Refresh status
    const status = await restClient.getStatus();
    onStatusUpdate(status);
  } catch (e) {
    state.error = `Full reset failed: ${e}`;
  }
}

async function run() {
  if (!restClient) return;
  state.error = '';
  try {
    // S-1: stop on any refusal — do not proceed to continuous/arm with a
    // config the RT never accepted.
    if (!(await configure())) return;
    if (!(await setTrigger())) return;
    if (!checkRc(await restClient.setContinuous(true), 'Set continuous')) return;
    const rc = await restClient.arm();
    if (rc !== 0) {
      checkRc(rc, 'Arm');
      // S-8: don't leave continuous=1 armed server-side after a failed run —
      // the next Single would free-run.  Best-effort rollback; keep the arm
      // error visible.
      try {
        await restClient.setContinuous(false);
      } catch {
        // rollback failed — the arm error stays on screen
      }
      return;
    }
  } catch (e) {
    state.error = `Run failed: ${e}`;
  }
}

async function forceTrigger() {
  if (!restClient) return;
  state.error = '';
  try {
    checkRc(await restClient.forceTrigger(), 'Force trigger');
  } catch (e) {
    state.error = `Force trigger failed: ${e}`;
  }
}

// Send config immediately if not capturing (for live parameter changes)
async function applyConfig() {
  const s = state.status.state;
  if (s === ScopeState.IDLE || s === ScopeState.DONE) {
    state.error = '';
    if (!(await configure())) return;
    await setTrigger();
  }
}

function isCapturing(): boolean {
  const s = state.status.state;
  return s === ScopeState.INIT || s === ScopeState.PRE_TRIG ||
    s === ScopeState.TRIG_WAIT || s === ScopeState.POST_TRIG;
}

async function searchPins(pattern?: string, kind?: string) {
  if (!restClient) return;
  try {
    state.pins = await restClient.listPins(pattern || undefined, kind || undefined) ?? [];
    state.error = '';
  } catch (e) {
    state.error = `List pins failed: ${e}`;
  }
}

/**
 * Capture parameters for the data currently on screen (S-2): the decode-time
 * snapshot when a capture is displayed, otherwise the live server status
 * (nothing on screen yet — show what the next capture would use).
 */
function getCaptureParams(): CaptureMeta {
  if (state.captureMeta) return state.captureMeta;
  return {
    periodNs: Number(state.status.threadPeriodNs) || getSelectedThreadPeriod(),
    samplePeriodMult: state.status.samplePeriodMult || state.captureConfig.samplePeriodMult,
    recLen: state.status.recLen,
    preTrig: state.status.preTrig,
  };
}

/**
 * Calculate display scale (seconds per division) — exact port of
 * calc_horiz_scaling() from scope_horiz.c.
 *
 * Uses 1-2-5 sequence: at zoom=1, disp_scale shows the full record
 * across 10 divisions. Each zoom step divides by one 1-2-5 step.
 */
function calcDispScale(): number {
  const p = getCaptureParams();
  const samplePeriod = (p.periodNs * p.samplePeriodMult) / 1e9;
  if (samplePeriod === 0) return 0;
  const totalRecTime = p.recLen * samplePeriod;
  if (totalRecTime < 0.000010) return 0.000001;

  const desiredUsecPerDiv = (totalRecTime / 10.0) * 1000000.0;

  // Find 1-2-5 value >= desired
  let decade = 1;
  let subDecade = 1;
  let actual = decade * subDecade;
  while (actual < desiredUsecPerDiv) {
    if (subDecade === 1) subDecade = 2;
    else if (subDecade === 2) subDecade = 5;
    else { subDecade = 1; decade *= 10; }
    actual = decade * subDecade;
  }

  // Zoom in: each step divides by one 1-2-5 step
  for (let n = 1; n < state.zoomSetting; n++) {
    if (subDecade === 1) { subDecade = 5; decade = Math.floor(decade / 10); }
    else if (subDecade === 2) subDecade = 1;
    else subDecade = 2;
  }
  if (decade === 0) { decade = 1; subDecade = 1; }

  return (decade * subDecade) / 1000000.0;
}

function getSamplePeriod(): number {
  const p = getCaptureParams();
  return (p.periodNs * p.samplePeriodMult) / 1e9;
}

/**
 * Compute display window parameters — port of scope_disp.c display calc.
 * Driven by the decode-time capture snapshot (S-2) so window, indicator and
 * chart always describe the capture on screen.
 */
function calcDisplayWindow() {
  const p = getCaptureParams();
  const samplePeriod = (p.periodNs * p.samplePeriodMult) / 1e9;
  const dispScale = calcDispScale();
  const recLen = p.recLen || 1;
  // A decode-time snapshot's preTrig is authoritative (may legitimately be
  // 0 for a loaded file); for live status the server always sets recLen/2.
  const preTrig = state.captureMeta
    ? p.preTrig
    : (p.preTrig > 0 ? p.preTrig : Math.round(recLen / 2));

  // Record boundaries relative to trigger (t=0)
  const recStart = -preTrig * samplePeriod;
  const recEnd = (recLen - preTrig) * samplePeriod;

  // posSetting 0..1 maps across the record, trigger-relative
  const screenCenterTime = recStart + (recEnd - recStart) * state.posSetting;
  const screenStartTime = screenCenterTime - 5.0 * dispScale;
  const screenEndTime = screenCenterTime + 5.0 * dispScale;

  return {
    samplePeriod,
    dispScale,
    recLen,
    preTrig,
    screenCenterTime,
    screenStartTime,
    screenEndTime,
  };
}

function setHorizZoom(setting: number) {
  state.zoomSetting = Math.max(1, Math.min(9, Math.round(setting)));
}

function setHorizPos(setting: number) {
  state.posSetting = Math.max(0, Math.min(1, setting));
}

const HAL_TYPE_NAMES: Record<number, string> = {
  [HalType.BIT]: 'BIT',
  [HalType.FLOAT]: 'FLOAT',
  [HalType.S32]: 'S32',
  [HalType.U32]: 'U32',
};

/** Export the current capture as semicolon-separated CSV and trigger a download. */
function saveCapture() {
  if (state.samples.length === 0 || state.timeBase.length === 0) {
    state.error = 'No capture data to save';
    return;
  }

  // S-2/S-4: header and columns come from the decode-time snapshot, not the
  // live config.
  const sampleCount = state.timeBase.length;
  const periodNs = Math.round(getSamplePeriod() * 1e9);

  // Build comment header
  const lines: string[] = [];
  lines.push(`# halscope capture ${new Date().toISOString()}`);
  lines.push(`# sample_period_ns=${periodNs}`);
  const trigCh = state.triggerConfig.channel;
  if (trigCh >= 0) {
    const edgeName = state.triggerConfig.edge === TrigEdge.RISING ? 'rising' : 'falling';
    lines.push(`# trigger_channel=${trigCh} trigger_level=${state.triggerConfig.level} trigger_edge=${edgeName}`);
  }

  // Column header: time + snapshotted channel names with type annotation
  const colHeaders = ['time_s'];
  for (const s of state.samples) {
    const typeName = HAL_TYPE_NAMES[s.dataType] ?? `TYPE${s.dataType}`;
    colHeaders.push(`${s.pinName}[${typeName}]`);
  }
  lines.push(colHeaders.join(';'));

  // Data rows
  for (let i = 0; i < sampleCount; i++) {
    const row = [state.timeBase[i].toFixed(9)];
    for (const s of state.samples) {
      const v = i < s.data.length ? s.data[i] : 0;
      row.push(v.toFixed(14));
    }
    lines.push(row.join(';'));
  }

  const csv = lines.join('\n') + '\n';
  const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `halscope_${new Date().toISOString().replace(/[:.]/g, '-')}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

/** Load a previously saved CSV capture file and display it. */
function loadCapture() {
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = '.csv,.txt';
  input.onchange = () => {
    const file = input.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      try {
        parseAndLoadCapture(reader.result as string);
      } catch (e) {
        state.error = `Failed to load capture: ${e}`;
      }
    };
    reader.readAsText(file);
  };
  input.click();
}

/**
 * Parse a saved CSV capture and enter file-view mode (S-10): the file data is
 * displayed via the samples/timeBase/captureMeta snapshot and live pushes are
 * paused; server status fields are never overwritten by the file.
 *
 * Exported for tests.
 */
export function parseAndLoadCapture(text: string) {
  const lines = text.split('\n').filter(l => l.length > 0);

  // Parse comment headers
  let periodNs = 0;
  let dataStart = 0;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].startsWith('#')) {
      dataStart = i + 1;
      const m = lines[i].match(/sample_period_ns=(\d+)/);
      if (m) periodNs = Number(m[1]);
    } else {
      break;
    }
  }

  if (dataStart >= lines.length) throw new Error('No data found');

  // Parse column header
  const header = lines[dataStart].split(';');
  dataStart++;

  // Detect columns: first is time_s, rest are channels
  const hasTimeCol = header[0].toLowerCase().startsWith('time');
  const chanStart = hasTimeCol ? 1 : 0;

  // Parse channel names and types from header like "pin.name[FLOAT]"
  const chanNames: string[] = [];
  const chanTypes: number[] = [];
  const typeMap: Record<string, number> = { BIT: 1, FLOAT: 2, S32: 3, U32: 4 };
  for (let i = chanStart; i < header.length; i++) {
    const col = header[i].trim();
    const tm = col.match(/^(.+)\[(\w+)\]$/);
    if (tm) {
      chanNames.push(tm[1]);
      chanTypes.push(typeMap[tm[2]] ?? 2);
    } else {
      chanNames.push(col);
      chanTypes.push(2); // default FLOAT
    }
  }

  // S-16: cap at what the UI can render (channelUI is MAX_CHANNELS deep)
  let truncError = '';
  if (chanNames.length > MAX_CHANNELS) {
    truncError = `File has ${chanNames.length} channels — showing only the first ${MAX_CHANNELS}`;
    chanNames.length = MAX_CHANNELS;
    chanTypes.length = MAX_CHANNELS;
  }

  const sampleCount = lines.length - dataStart;
  if (sampleCount === 0) throw new Error('No sample rows');

  // Parse data
  const timeArr = new Float64Array(sampleCount);
  const chanData: Float64Array[] = chanNames.map(() => new Float64Array(sampleCount));

  for (let si = 0; si < sampleCount; si++) {
    const cols = lines[dataStart + si].split(';');
    if (hasTimeCol) {
      timeArr[si] = Number(cols[0]);
    }
    for (let ci = 0; ci < chanNames.length; ci++) {
      chanData[ci][si] = Number(cols[chanStart + ci]);
    }
  }

  // If no time column, reconstruct from period
  if (!hasTimeCol && periodNs > 0) {
    const dt = periodNs / 1e9;
    for (let i = 0; i < sampleCount; i++) {
      timeArr[i] = i * dt;
    }
  }

  // Infer the period from the time column if the file lacks the header
  if (periodNs <= 0 && hasTimeCol && sampleCount > 1) {
    periodNs = Math.round((timeArr[1] - timeArr[0]) * 1e9);
  }

  // File-local display model (S-10): samples/timeBase/captureMeta describe
  // the file; state.status is left alone.
  const samples: ChannelSamples[] = chanNames.map((name, ci) => ({
    channel: ci,
    pinName: name,
    dataType: chanTypes[ci],
    data: chanData[ci],
  }));

  // preTrig = number of samples before t=0
  let preTrig = 0;
  while (preTrig < sampleCount && timeArr[preTrig] < 0) preTrig++;

  state.samples = samples;
  state.timeBase = timeArr;
  state.captureMeta = {
    periodNs: periodNs > 0 ? periodNs : 0,
    samplePeriodMult: 1,
    recLen: sampleCount,
    preTrig,
  };
  state.fileView = true;
  state.selectedChannel = 0;
  state.error = truncError;
}

/** Leave file-view mode: clear the file data and resync from the server. */
async function returnToLive() {
  state.fileView = false;
  clearSampleData();
  state.selectedChannel = -1;
  state.error = '';
  if (!restClient) return;
  try {
    const status = await restClient.getStatus();
    onStatusUpdate(status);
  } catch (e) {
    state.error = `Status refresh failed: ${e}`;
  }
}

function formatTimeValue(seconds: number): string {
  const sign = seconds < 0 ? '-' : '';
  let val = Math.abs(seconds) * 1e9; // to nanoseconds
  let units = 'ns';
  if (val >= 1000) { val /= 1000; units = 'µs'; }
  if (val >= 1000) { val /= 1000; units = 'ms'; }
  if (val >= 1000) { val /= 1000; units = 's'; }
  const decimals = val >= 100 ? 0 : val >= 10 ? 1 : 2;
  return `${sign}${val.toFixed(decimals)} ${units}`;
}

// --- Exported store ---

export const scopeStore = {
  state,
  connect,
  disconnect,
  configure,
  addChannel,
  removeChannel,
  setTrigger,
  arm,
  run,
  stop,
  fullReset,
  forceTrigger,
  searchPins,
  applyConfig,
  isCapturing,
  returnToLive,

  // Mutable config refs for v-model binding
  captureConfig: state.captureConfig,
  triggerConfig: state.triggerConfig,
  channelUI: state.channelUI,

  // Direct state mutation helpers
  setSelectedThread(name: string) {
    state.selectedThread = name;
  },
  setPinFilter(f: string) {
    state.pinFilter = f;
  },
  setHorizZoom,
  setHorizPos,
  setSelectedChannel(ch: number) {
    state.selectedChannel = ch;
  },
  calcDispScale,
  calcDisplayWindow,
  getSamplePeriod,
  formatTimeValue,
  saveCapture,
  loadCapture,
};
