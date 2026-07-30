// What the machine is doing, as opposed to what the program says — this store
// holds only values the controller pushes, and drops all of them the moment the
// connection goes.
//
// The app used to poll two endpoints on setInterval while both watch channels
// sat unused. Polling cannot animate a ladder: the interesting states are the
// short ones, and a 500ms poll walks straight past them.
import { reactive } from 'vue';
import {
  ClassicladderWatchClient,
} from '../generated/classicladder_watch_client';
import type { Status, Variables, RungState } from '../generated/classicladder_client';

// Backoff for the reconnect loop, matching the other gomc webapps.
const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 10000;

export interface LiveState {
  connected: boolean;
  reconnecting: boolean;
  // Everything below is the controller's, held only while connected. A stopped
  // PLC and an unreachable one look different to an operator and must look
  // different here: keeping the last values would show a machine still running
  // minutes after it stopped answering.
  status: Status | null;
  variables: Variables | null;
  // Keyed by rung index, as the watch sends it. Merged rather than replaced,
  // because after the first message the server sends only the rungs that
  // changed. A rung that stops being used simply stops being mentioned; the
  // stale entry is never read, since the view draws the rungs the program has.
  rungStates: Record<string, RungState>;
}

const state = reactive<LiveState>({
  connected: false,
  reconnecting: false,
  status: null,
  variables: null,
  rungStates: {},
});

let client: ClassicladderWatchClient | null = null;
let reconnectDelay = RECONNECT_MIN_MS;
let reconnectPending = false;
let stopped = false;

function watchUrl(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}/api/v1/watch`;
}

function dropLiveData() {
  state.status = null;
  state.variables = null;
  state.rungStates = {};
}

function scheduleReconnect() {
  if (reconnectPending || stopped) return;
  reconnectPending = true;
  state.reconnecting = true;
  setTimeout(() => {
    reconnectPending = false;
    void connect();
  }, reconnectDelay);
  reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_MS);
}

async function connect(): Promise<void> {
  if (stopped) return;
  client = new ClassicladderWatchClient(watchUrl());
  try {
    await client.connect();
  } catch {
    state.connected = false;
    dropLiveData();
    scheduleReconnect();
    return;
  }

  client.onClose = () => {
    state.connected = false;
    dropLiveData();
    scheduleReconnect();
  };

  state.connected = true;
  state.reconnecting = false;
  reconnectDelay = RECONNECT_MIN_MS;

  client.subscribeWatchStatus((s) => { state.status = s; });
  client.subscribeWatchVariables((v) => { state.variables = v; });
  client.subscribeWatchRungStates((delta) => {
    // Merge: after the first message these are only the rungs that moved.
    for (const [key, rs] of Object.entries(delta)) {
      state.rungStates[key] = rs;
    }
  });
}

function start() {
  stopped = false;
  void connect();
}

function stop() {
  stopped = true;
  client?.close();
  client = null;
  state.connected = false;
  state.reconnecting = false;
  dropLiveData();
}

// cellsOf returns the live cells of one rung, or null when nothing is known
// about it — which is what the view draws in its unpowered colours.
export function cellsOf(rungIndex: number): number[] | null {
  return state.rungStates[String(rungIndex)]?.cells ?? null;
}

export const liveStore = {
  state,
  start,
  stop,
};
