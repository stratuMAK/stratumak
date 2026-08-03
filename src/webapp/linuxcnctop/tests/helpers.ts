// Test doubles: stubbed fetch + fake WebSocket, and a StatFull builder.
import { vi } from 'vitest';
import type { StatFull } from '../src/generated/emcstat_client';

export class FakeWebSocket {
  static instances: FakeWebSocket[] = [];

  url: string;
  readyState = 0; // CONNECTING
  binaryType = '';
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  send(data: string) {
    if (this.readyState !== 1) {
      throw new Error('InvalidStateError: WebSocket is not open');
    }
    this.sent.push(data);
  }

  close() {
    this.readyState = 3;
    this.onclose?.();
  }

  open() {
    this.readyState = 1;
    this.onopen?.();
  }

  serverClose() {
    this.readyState = 3;
    this.onclose?.();
  }

  /** Deliver a watch update frame for get_stat. 64-bit fields go over the wire
   *  as JSON strings (the client revives them to bigint), so the replacer here
   *  is what the server does, not a test shortcut. */
  pushStat(stat: StatFull) {
    this.onmessage?.({
      data: JSON.stringify({
        type: 'update', api: 'emcstat', instance: 'milltask',
        func: 'get_stat', data: stat,
      }, (_k, v) => typeof v === 'bigint' ? String(v) : v),
    });
  }

  /** Deliver a @watch_delta follow-up frame: only the changed top-level keys,
   *  which is what every push after the first actually carries. */
  pushStatDelta(partial: Record<string, unknown>) {
    this.onmessage?.({
      data: JSON.stringify({
        type: 'update', api: 'emcstat', instance: 'milltask',
        func: 'get_stat', data: partial,
      }, (_k, v) => typeof v === 'bigint' ? String(v) : v),
    });
  }

  sentJson(): Record<string, unknown>[] {
    return this.sent.map(s => JSON.parse(s));
  }
}

export function installStubs() {
  FakeWebSocket.instances.length = 0;
  vi.stubGlobal('fetch', async () => ({
    ok: true,
    status: 200,
    statusText: '',
    json: async () => ({ peers: {}, caps: {} }),
    text: async () => JSON.stringify({ peers: {}, caps: {} }),
  }) as unknown as Response);
  vi.stubGlobal('WebSocket', FakeWebSocket);
}

export async function flushMicrotasks(rounds = 30) {
  for (let i = 0; i < rounds; i++) await Promise.resolve();
}

/** Import a fresh store, connect it and open the watch WebSocket. */
export async function connectStore() {
  const { statusStore } = await import('../src/stores/status');
  const p = statusStore.connect();
  await flushMicrotasks();
  const ws = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  if (!ws) throw new Error('watch WebSocket was not created');
  ws.open();
  await p;
  await flushMicrotasks();
  return { store: statusStore, ws };
}

function zeros(n: number): number[] {
  return new Array(n).fill(0);
}

function pos(x = 0, y = 0, z = 0) {
  return { x, y, z, a: 0, b: 0, c: 0, u: 0, v: 0, w: 0 };
}

/** A StatFull with every field present, shaped like a 3-axis XYZ mill.
 *  Overrides are shallow-merged so a test can state only what it cares about. */
export function makeStat(over: Partial<StatFull> = {}): StatFull {
  const stat = {
    task: {
      mode: 1, state: 4, interp_state: 1, exec_state: 2,
      file: '', source_file: '', filtering: false, filter_progress: 0,
      command: '', line: 0, motion_line: 0, current_line: 0, read_line: 0,
      motion_file: '', queued_mdi_commands: 0, optional_stop: false,
      block_delete: false, task_paused: false, g5x_index: 1,
      program_units: 2, delay_left: 0, call_level: 0, input_timeout: 0,
    },
    motion: {
      mode: 1, enabled: true, in_position: true, paused: false,
      feedrate: 1, rapidrate: 1, max_velocity: 100, velocity: 0,
      distance_to_go: 0, dtg: pos(), current_vel: 0, motion_id: 0,
      motion_line: 0, motion_type: 0, feed_override_enabled: true,
      adaptive_feed_enabled: false, feed_hold_enabled: true,
      queue: 0, queue_full: false,
    },
    position: pos(1, 2, 3),
    actual_position: pos(1, 2, 3),
    joint_actual_position: zeros(16),
    joint_position: zeros(16),
    probed_position: pos(),
    g5x_offset: pos(),
    g92_offset: pos(),
    tool_offset: pos(),
    rotation_xy: 0,
    joints: [],
    spindle: [],
    axis: [],
    // [0] is the interpreter's sequence number, not a code; -1 = inactive group.
    active_gcodes: [12, 10, -1, 170, -1, 900, 940, -1],
    active_mcodes: [12, 5, -1, 9, -1],
    active_settings: [12, 100, 0, 0.01, 0.02],
    kinematics_type: 1,
    joints_count: 3,
    num_extrajoints: 0,
    axis_mask: 0b111, // X Y Z
    flood: false, mist: false, lube_on: false,
    tool_in_spindle: 0, pocket_prepped: -1, tool_from_pocket: 0,
    linear_units: 1,
    homed: new Array(16).fill(false),
    limit: zeros(16),
    state: 1, rcs_status: 1, debug: 0,
    jog_axis: 0, jog_increment: 0, jog_speed: 10, ajog_speed: 10,
    preview_seq: 0, boot_id: 1n, heartbeat: 0,
    angular_units: 1, cycle_time: 0.001, acceleration: 0,
    max_acceleration: 500, active_queue: 0, estop: 0,
    interpreter_errcode: 0, lube_level: 0,
    probe_tripped: false, probe_val: 0, probing: false,
    ain: zeros(64), aout: zeros(64), din: zeros(64), dout: zeros(64),
  } as unknown as StatFull;
  return { ...stat, ...over };
}
