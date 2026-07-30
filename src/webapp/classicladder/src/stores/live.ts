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
import {
  VAR_MEM_BIT,
  VAR_PHYS_INPUT,
  VAR_PHYS_OUTPUT,
  VAR_ERROR_BIT,
  VAR_STEP_ACTIVITY,
  VAR_TIMER_DONE,
  VAR_TIMER_RUNNING,
  VAR_MONOSTABLE_RUNNING,
  VAR_COUNTER_DONE,
  VAR_COUNTER_EMPTY,
  VAR_COUNTER_FULL,
  VAR_TIMER_IEC_DONE,
  VAR_MEM_WORD,
  VAR_PHYS_WORD_INPUT,
  VAR_PHYS_WORD_OUTPUT,
  VAR_STEP_TIME,
  VAR_TIMER_VALUE,
  VAR_MONOSTABLE_VALUE,
  VAR_COUNTER_VALUE,
  VAR_TIMER_IEC_VALUE,
  VAR_PHYS_FLOAT_INPUT,
  VAR_PHYS_FLOAT_OUTPUT,
} from '../generated/classicladder_client';

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

// --- Reading one variable ---
//
// A watch window lets an operator name any variable, so it needs the value of
// an arbitrary (varType, offset). The controller pushes every live region, so
// this is a lookup into what arrived rather than a request: one push serves the
// spy window, the block boxes and the SFC chart alike.
//
// Presets are deliberately not here. A preset is what the program says, not
// what the machine is doing; it comes from Program, and a caller that wants one
// asks the ladder store.
const boolRegion: Record<number, (v: Variables) => boolean[]> = {
  [VAR_MEM_BIT]: v => v.bools.bits,
  [VAR_PHYS_INPUT]: v => v.bools.physInputs,
  [VAR_PHYS_OUTPUT]: v => v.bools.physOutputs,
  [VAR_ERROR_BIT]: v => v.bools.errorBits,
  [VAR_STEP_ACTIVITY]: v => v.bools.stepActivity,
  [VAR_TIMER_DONE]: v => v.bools.timerDone,
  [VAR_TIMER_RUNNING]: v => v.bools.timerRunning,
  [VAR_MONOSTABLE_RUNNING]: v => v.bools.monostableRunning,
  [VAR_COUNTER_DONE]: v => v.bools.counterDone,
  [VAR_COUNTER_EMPTY]: v => v.bools.counterEmpty,
  [VAR_COUNTER_FULL]: v => v.bools.counterFull,
  [VAR_TIMER_IEC_DONE]: v => v.bools.timerIecDone,
};

const wordRegion: Record<number, (v: Variables) => number[]> = {
  [VAR_MEM_WORD]: v => v.words.words,
  [VAR_PHYS_WORD_INPUT]: v => v.words.physWordInputs,
  [VAR_PHYS_WORD_OUTPUT]: v => v.words.physWordOutputs,
  [VAR_STEP_TIME]: v => v.words.stepTimes,
  [VAR_TIMER_VALUE]: v => v.words.timerValues,
  [VAR_MONOSTABLE_VALUE]: v => v.words.monostableValues,
  [VAR_COUNTER_VALUE]: v => v.words.counterValues,
  [VAR_TIMER_IEC_VALUE]: v => v.words.timerIecValues,
  [VAR_PHYS_FLOAT_INPUT]: v => v.floats.physFloatInputs,
  [VAR_PHYS_FLOAT_OUTPUT]: v => v.floats.physFloatOutputs,
};

// isBoolVar reports whether a region holds bits, so a display knows to show
// 0/1 and an editor knows to offer a toggle rather than a number field.
export function isBoolVar(varType: number): boolean {
  return varType in boolRegion;
}

// variableValue returns the live value, or null when the region is not one the
// controller pushes or the offset is past the end of it.
export function variableValue(varType: number, offset: number): number | null {
  const v = state.variables;
  if (!v) return null;
  const bools = boolRegion[varType];
  if (bools) {
    const arr = bools(v);
    if (offset < 0 || offset >= arr.length) return null;
    return arr[offset] ? 1 : 0;
  }
  const words = wordRegion[varType];
  if (words) {
    const arr = words(v);
    if (offset < 0 || offset >= arr.length) return null;
    return arr[offset];
  }
  return null;
}

export const liveStore = {
  state,
  start,
  stop,
};
