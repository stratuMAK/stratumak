import { reactive } from 'vue';
import { EmcstatClient, type StatFull, type TaskInfo } from '../generated/emcstat_client';
import { EmcstatWatchClient } from '../generated/emcstat_watch_client';
import { flattenStat, rowsToText, type StatusRow } from './format';

/** How long a changed row stays highlighted, in ms. Same 2 s the Tk version
 *  used (linuxcnctop.py: changetime[k] = time.time() + 2). */
export const HIGHLIGHT_MS = 2000;

/** Selectable push rates. 100 ms is what the Tk version polled at; the server
 *  suppresses unchanged pushes, so a fast rate costs nothing when idle. */
export const RATES = [50, 100, 250, 500, 1000] as const;

export interface DisplayRow extends StatusRow {
  /** Wall-clock ms when this row's value last changed; 0 = never seen change. */
  changedAt: number;
}

interface StatusState {
  /** Watch WebSocket is up. */
  watchOk: boolean;
  /** The displayed values may be outdated (WS lost). */
  watchStale: boolean;
  /** Reconnect loop is active. */
  watchReconnecting: boolean;
  error: string;

  rows: DisplayRow[];
  /** Latest raw snapshot, for the JSON view. Null until the first push. */
  raw: StatFull | null;
  info: TaskInfo | null;

  filter: string;
  rateMs: number;
  /** Frozen: pushes keep arriving but the display is not updated. */
  frozen: boolean;
  showJson: boolean;

  /** Bumped by a repaint timer so highlight decay re-renders. */
  tick: number;
}

const state = reactive<StatusState>({
  watchOk: false,
  watchStale: false,
  watchReconnecting: false,
  error: '',

  rows: [],
  raw: null,
  info: null,

  filter: '',
  rateMs: 100,
  frozen: false,
  showJson: false,

  tick: 0,
});

const instance = new URLSearchParams(window.location.search).get('instance') || 'milltask';

let client: EmcstatClient;
let watchClient: EmcstatWatchClient | null = null;

// Watch WS reconnect loop with backoff (1s doubling to 10s cap), matching
// halshow: a pulled cable must not leave stale values rendered as live.
const WATCH_RECONNECT_MIN_MS = 1000;
const WATCH_RECONNECT_MAX_MS = 10000;
let watchReconnectDelay = WATCH_RECONNECT_MIN_MS;
let watchReconnectActive = false; // timer pending or connect attempt in flight

function scheduleWatchReconnect() {
  if (watchReconnectActive) return; // guard against concurrent reconnect loops
  watchReconnectActive = true;
  state.watchReconnecting = true;
  setTimeout(() => {
    void statusStore.connectWatch();
  }, watchReconnectDelay);
  watchReconnectDelay = Math.min(watchReconnectDelay * 2, WATCH_RECONNECT_MAX_MS);
}

/** Merge a fresh snapshot into the row list, carrying each row's last-changed
 *  stamp across. Rows are keyed by path, so a snapshot that gains or loses
 *  rows (joint count changes on a reconfigure) is handled without stale keys
 *  surviving. */
function applySnapshot(stat: StatFull, now: number) {
  const previous = new Map(state.rows.map(r => [r.key, r]));
  state.rows = flattenStat(stat).map(row => {
    const prev = previous.get(row.key);
    if (prev && prev.value === row.value) {
      return { ...row, changedAt: prev.changedAt };
    }
    // A row seen for the first time is not a "change" — highlighting the whole
    // table on connect would drown the one field the operator is watching.
    return { ...row, changedAt: prev ? now : 0 };
  });
  state.raw = stat;
}

export const statusStore = {
  state,

  async connect() {
    const origin = window.location.origin;
    client = new EmcstatClient(origin, instance);

    // Machine description is read once and is not part of the stat push.
    try {
      state.info = await client.getInfo();
      state.error = '';
    } catch (e) {
      // Non-fatal: /info only feeds the peer/caps panel.
      state.error = e instanceof Error ? e.message : String(e);
    }

    await this.connectWatch();

    // Repaint timer: decays the change highlight when no push arrives (the
    // server suppresses unchanged snapshots, so an idle machine sends none).
    setInterval(() => { state.tick++; }, 250);
  },

  /** Connect (or reconnect) the watch WebSocket and (re)subscribe. */
  async connectWatch() {
    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${wsProto}//${window.location.host}/api/v1/watch`;
    watchClient = new EmcstatWatchClient(wsUrl, 'emcstat', instance);
    try {
      await watchClient.connect();
      watchClient.onClose = () => {
        state.watchOk = false;
        state.watchStale = true;
        scheduleWatchReconnect();
      };
      watchReconnectActive = false;
      watchReconnectDelay = WATCH_RECONNECT_MIN_MS;
      state.watchOk = true;
      state.watchReconnecting = false;
      state.error = '';
      this.subscribe();
      // watchStale stays set until the first frame of this connection arrives
      // (see subscribe): a reconnected-but-silent socket must not relabel the
      // pre-disconnect values as live.
    } catch (e) {
      watchReconnectActive = false;
      state.watchOk = false;
      state.error = e instanceof Error ? e.message : String(e);
      scheduleWatchReconnect();
    }
  },

  subscribe() {
    watchClient?.subscribeGetStat((stat) => {
      // A frame is the proof of liveness — only here does stale clear, so a
      // reconnect that never delivers keeps the stale banner up.
      state.watchStale = false;
      if (state.frozen) return;
      applySnapshot(stat, Date.now());
    }, state.rateMs);
  },

  setRate(rateMs: number) {
    state.rateMs = rateMs;
    if (state.watchOk) {
      watchClient?.unsubscribeGetStat();
      this.subscribe();
    }
  },

  setFilter(f: string) {
    state.filter = f;
  },

  toggleFrozen() {
    state.frozen = !state.frozen;
  },

  toggleJson() {
    state.showJson = !state.showJson;
  },

  /** Rows after the filter, which matches on path and on value so an operator
   *  can search for a value ("estop") as well as a field name. */
  filteredRows(): DisplayRow[] {
    const f = state.filter.trim().toLowerCase();
    if (!f) return state.rows;
    return state.rows.filter(r =>
      r.key.toLowerCase().includes(f) || r.value.toLowerCase().includes(f));
  },

  /** True while the row should render highlighted. */
  isHighlighted(row: DisplayRow, now: number): boolean {
    return row.changedAt > 0 && now - row.changedAt < HIGHLIGHT_MS;
  },

  /** The current (filtered) view as text, for Copy All. */
  asText(): string {
    return rowsToText(this.filteredRows());
  },

  /** The latest snapshot as pretty JSON. bigint (boot_id) needs a replacer —
   *  JSON.stringify throws on it. */
  asJson(): string {
    if (!state.raw) return '';
    return JSON.stringify(state.raw, (_k, v) => typeof v === 'bigint' ? String(v) : v, 2);
  },
};
