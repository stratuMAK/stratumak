// Request-level write-path tests for the halscope store.
//
// fetch is stubbed at the network boundary so every assertion covers the real
// generated REST client (halscope_client.ts) plus the store logic on top of
// it: exact JSON wire bodies, rc-refusal handling (S-1), call ordering (S-8)
// and binary sample-frame decoding (S-2/S-4/S-12).
//
// 64-bit note: the generated client revives threadPeriodNs from a JSON
// STRING — the stubs below return the wire shape the real server sends
// (period as string, e.g. "1000000").

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { scopeStore, handleSampleFrame } from '../src/stores/scope';

// --- Fake WebSocket: connects "successfully" and swallows everything ---

class FakeWebSocket {
  binaryType = '';
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: unknown) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor(public url: string) {
    setTimeout(() => this.onopen?.(), 0);
  }
  send(_data: string) {}
  close() {}
}

// --- fetch stub: routes halscope REST endpoints, records every call ---

interface RecordedCall {
  path: string;
  method: string;
  body?: unknown;
}

const calls: RecordedCall[] = [];
const rcOverride: Record<string, number> = {};

// Full ScopeStatus in server wire shape (threadPeriodNs as STRING).
function wireStatus() {
  return {
    state: 0,
    samples: 0,
    recLen: 16000,
    preTrig: 8000,
    sampleLen: 3,
    maxChannels: 4,
    samplePeriodMult: 2,
    threadPeriodNs: '1000000',
    threadName: 'servo-thread',
    trigChannel: 0,
    trigLevel: 1.5,
    trigEdge: 0,
    trigAutoTrig: true,
    generation: 1,
    continuous: false,
    channels: [
      { channel: 0, pinName: 'pin.a', dataType: 2, enabled: true },
      { channel: 1, pinName: '', dataType: 0, enabled: false },
      { channel: 2, pinName: 'pin.c', dataType: 3, enabled: true },
    ],
    channelOptions: [
      { maxChannels: 4, recLen: 16000 },
      { maxChannels: 8, recLen: 8000 },
    ],
  };
}

let serverStatus = wireStatus();

function fakeResponse(body: unknown) {
  return {
    ok: true,
    status: 200,
    statusText: 'OK',
    text: async () => JSON.stringify(body),
    json: async () => body,
  };
}

function installFetch() {
  calls.length = 0;
  for (const k of Object.keys(rcOverride)) delete rcOverride[k];
  serverStatus = wireStatus();

  vi.stubGlobal('fetch', async (input: unknown, init?: RequestInit) => {
    const url = String(input);
    const path = url.replace(/^[a-z]+:\/\/[^/]+/, '').split('?')[0];
    const method = init?.method ?? 'GET';
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ path, method, body });

    const ep = path.replace('/api/v1/halscope', '');
    if (ep === '/threads') {
      return fakeResponse([{ name: 'servo-thread', periodNs: '1000000' }]);
    }
    if (ep === '/status') return fakeResponse(structuredClone(serverStatus));
    if (ep === '/configure') return fakeResponse(rcOverride['configure'] ?? 0);
    if (ep === '/trigger') return fakeResponse(rcOverride['trigger'] ?? 0);
    if (ep === '/arm') return fakeResponse(rcOverride['arm'] ?? 0);
    if (ep === '/set_continuous') return fakeResponse(rcOverride['set_continuous'] ?? 0);
    if (ep === '/reset') return fakeResponse(rcOverride['reset'] ?? 0);
    if (ep === '/force_trigger') return fakeResponse(rcOverride['force_trigger'] ?? 0);
    if (ep.startsWith('/channel')) return fakeResponse(rcOverride['channel'] ?? 0);
    if (ep === '/pins') return fakeResponse([]);
    throw new Error(`unexpected fetch: ${method} ${path}`);
  });
}

function callsTo(suffix: string): RecordedCall[] {
  return calls.filter(c => c.path.endsWith(suffix));
}

function callIndex(suffix: string): number {
  return calls.findIndex(c => c.path.endsWith(suffix));
}

// --- Binary sample frame builder (16-byte header + float64 LE data) ---

function makeFrame(
  sampleCount: number,
  sampleLen: number,
  startOffset: number,
  fill: (si: number, col: number) => number,
): ArrayBuffer {
  const buf = new ArrayBuffer(16 + sampleCount * sampleLen * 8);
  const dv = new DataView(buf);
  dv.setUint32(0, sampleCount, true);
  dv.setUint32(4, sampleLen, true);
  dv.setUint32(8, startOffset, true);
  dv.setUint32(12, 0, true);
  const f = new Float64Array(buf, 16);
  for (let si = 0; si < sampleCount; si++) {
    for (let col = 0; col < sampleLen; col++) {
      f[si * sampleLen + col] = fill(si, col);
    }
  }
  return buf;
}

// --- Test lifecycle ---

beforeEach(async () => {
  installFetch();
  vi.stubGlobal('WebSocket', FakeWebSocket);

  // Reset displayed-data state left over from previous tests
  const st = scopeStore.state;
  st.samples = [];
  st.timeBase = new Float64Array(0);
  st.captureMeta = null;
  st.fileView = false;
  st.stale = false;
  st.error = '';

  await scopeStore.connect();

  // Known baseline config regardless of configSynced history
  scopeStore.state.selectedThread = 'servo-thread';
  scopeStore.captureConfig.threadName = 'servo-thread';
  scopeStore.captureConfig.maxChannels = 4;
  scopeStore.captureConfig.samplePeriodMult = 2;
  scopeStore.triggerConfig.channel = 0;
  scopeStore.triggerConfig.level = 1.5;
  scopeStore.triggerConfig.edge = 0;
  scopeStore.triggerConfig.autoTrig = true;

  calls.length = 0;
});

afterEach(() => {
  scopeStore.disconnect();
  vi.unstubAllGlobals();
});

// --- Tests ---

describe('trigger path', () => {
  it('setTrigger emits the exact JSON body from triggerConfig', async () => {
    scopeStore.triggerConfig.channel = 2;
    scopeStore.triggerConfig.level = 0.5;
    scopeStore.triggerConfig.edge = 1;
    scopeStore.triggerConfig.autoTrig = false;

    const ok = await scopeStore.setTrigger();

    expect(ok).toBe(true);
    const trig = callsTo('/trigger');
    expect(trig).toHaveLength(1);
    expect(trig[0].method).toBe('POST');
    expect(trig[0].body).toEqual({
      trig: { channel: 2, level: 0.5, edge: 1, autoTrig: false },
    });
    expect(scopeStore.state.error).toBe('');
  });

  it('refused setTrigger (-22) sets a readable error and resyncs from status', async () => {
    rcOverride['trigger'] = -22;
    scopeStore.triggerConfig.level = 99.9; // local edit the server refuses

    const ok = await scopeStore.setTrigger();

    expect(ok).toBe(false);
    expect(scopeStore.state.error).toContain('invalid');
    // resynced from getStatus (server has trigLevel 1.5)
    expect(callsTo('/status').length).toBeGreaterThan(0);
    expect(scopeStore.triggerConfig.level).toBe(1.5);
  });
});

describe('control path', () => {
  it('configure emits threadName/maxChannels/samplePeriodMult correctly', async () => {
    scopeStore.state.selectedThread = 'servo-thread';
    scopeStore.captureConfig.maxChannels = 8;
    scopeStore.captureConfig.samplePeriodMult = 5;

    const ok = await scopeStore.configure();

    expect(ok).toBe(true);
    const cfg = callsTo('/configure');
    expect(cfg).toHaveLength(1);
    expect(cfg[0].method).toBe('POST');
    expect(cfg[0].body).toEqual({
      config: {
        threadName: 'servo-thread',
        maxChannels: 8,
        samplePeriodMult: 5,
      },
    });
  });

  it('refused configure (-16) sets error, resyncs local config from status', async () => {
    rcOverride['configure'] = -16;
    scopeStore.captureConfig.maxChannels = 8; // refused edit

    const ok = await scopeStore.configure();

    expect(ok).toBe(false);
    expect(scopeStore.state.error).toContain('busy');
    // local config must NOT keep the refused value — server status says 4
    expect(scopeStore.captureConfig.maxChannels).toBe(4);
    expect(scopeStore.captureConfig.samplePeriodMult).toBe(2);
  });

  it('run() stops after a refused configure — no setContinuous, no arm', async () => {
    rcOverride['configure'] = -16;

    await scopeStore.run();

    expect(scopeStore.state.error).toContain('busy');
    expect(callsTo('/set_continuous')).toHaveLength(0);
    expect(callsTo('/arm')).toHaveLength(0);
  });

  it('run() stops after a refused setTrigger — no setContinuous, no arm', async () => {
    rcOverride['trigger'] = -16;

    await scopeStore.run();

    expect(scopeStore.state.error).toContain('busy');
    expect(callsTo('/set_continuous')).toHaveLength(0);
    expect(callsTo('/arm')).toHaveLength(0);
  });
});

describe('S-8: single-shot vs continuous', () => {
  it('arm() sends setContinuous(false) before arm', async () => {
    await scopeStore.arm();

    const idxCont = callIndex('/set_continuous');
    const idxArm = callIndex('/arm');
    expect(idxCont).toBeGreaterThanOrEqual(0);
    expect(idxArm).toBeGreaterThanOrEqual(0);
    expect(idxCont).toBeLessThan(idxArm);
    expect(callsTo('/set_continuous')[0].body).toEqual({ enabled: false });
    expect(scopeStore.state.error).toBe('');
  });

  it('run() sends setContinuous(true) then arm; failed arm rolls continuous back', async () => {
    rcOverride['arm'] = -16;

    await scopeStore.run();

    const contCalls = callsTo('/set_continuous');
    expect(contCalls).toHaveLength(2);
    expect(contCalls[0].body).toEqual({ enabled: true });
    expect(contCalls[1].body).toEqual({ enabled: false }); // rollback
    const idxArm = callIndex('/arm');
    expect(idxArm).toBeGreaterThan(callIndex('/set_continuous'));
    // the rollback happened AFTER the failed arm
    const idxRollback = calls.map(c => c.path).lastIndexOf(contCalls[1].path);
    expect(idxRollback).toBeGreaterThan(idxArm);
    expect(scopeStore.state.error).toContain('busy');
  });

  it('run() success keeps continuous enabled (no rollback)', async () => {
    await scopeStore.run();

    const contCalls = callsTo('/set_continuous');
    expect(contCalls).toHaveLength(1);
    expect(contCalls[0].body).toEqual({ enabled: true });
    expect(callsTo('/arm')).toHaveLength(1);
    expect(scopeStore.state.error).toBe('');
  });
});

describe('frame decode (S-2, S-4, S-12)', () => {
  it('extracts per-channel data and snapshots capture meta + pin names', () => {
    // channels 0 (pin.a) and 2 (pin.c) enabled, sampleLen 3 columns
    const frame = makeFrame(4, 3, 1, (si, col) => si * 10 + col);

    handleSampleFrame(frame);

    const st = scopeStore.state;
    expect(st.samples).toHaveLength(2);

    const ch0 = st.samples.find(s => s.channel === 0)!;
    expect(ch0).toBeDefined();
    expect(ch0.pinName).toBe('pin.a'); // S-4 snapshot
    expect(ch0.dataType).toBe(2);
    expect(Array.from(ch0.data)).toEqual([0, 10, 20, 30]);

    const ch2 = st.samples.find(s => s.channel === 2)!;
    expect(ch2).toBeDefined();
    expect(ch2.pinName).toBe('pin.c');
    expect(ch2.dataType).toBe(3);
    expect(Array.from(ch2.data)).toEqual([2, 12, 22, 32]);

    // S-2: capture parameters snapshotted at decode time
    expect(st.captureMeta).toEqual({
      periodNs: 1000000, // Number(threadPeriodNs bigint from "1000000")
      samplePeriodMult: 2,
      recLen: 16000,
      preTrig: 8000,
    });

    // time base: dt = 1e6 ns * 2 / 1e9 = 2 ms, t0 = -startOffset*dt
    expect(st.timeBase).toHaveLength(4);
    expect(st.timeBase[0]).toBeCloseTo(-0.002, 9);
    expect(st.timeBase[1]).toBeCloseTo(0, 9);
    expect(st.timeBase[3]).toBeCloseTo(0.004, 9);
  });

  it('captureMeta stays with the displayed capture when live config changes afterwards (S-2)', () => {
    handleSampleFrame(makeFrame(4, 3, 1, () => 0));

    // operator edits config after the capture — snapshot must not move
    scopeStore.state.status.samplePeriodMult = 10;
    scopeStore.state.status.threadPeriodNs = 25000000n;
    expect(scopeStore.state.captureMeta).toEqual({
      periodNs: 1000000,
      samplePeriodMult: 2,
      recLen: 16000,
      preTrig: 8000,
    });
    // display window math is driven by the snapshot
    expect(scopeStore.getSamplePeriod()).toBeCloseTo(0.002, 12);
  });

  it('drops frames whose header disagrees with byteLength, with one deduped error (S-12)', () => {
    const st = scopeStore.state;

    // header claims 100 samples of 3 columns; buffer only holds 2 doubles
    const bad = new ArrayBuffer(16 + 2 * 8);
    const dv = new DataView(bad);
    dv.setUint32(0, 100, true);
    dv.setUint32(4, 3, true);
    dv.setUint32(8, 0, true);

    handleSampleFrame(bad);
    expect(st.samples).toHaveLength(0);
    expect(st.captureMeta).toBeNull();
    expect(st.error).toContain('malformed');

    // dedupe: a second bad frame must not re-set the error
    st.error = '';
    handleSampleFrame(bad);
    expect(st.error).toBe('');

    // a good frame re-arms reporting
    handleSampleFrame(makeFrame(2, 3, 0, () => 1));
    expect(st.samples.length).toBeGreaterThan(0);
    handleSampleFrame(bad);
    expect(st.error).toContain('malformed');
  });

  it('drops frames not 8-byte aligned after the header (S-12)', () => {
    const st = scopeStore.state;
    // good frame first: re-arms the deduped error reporting
    handleSampleFrame(makeFrame(1, 3, 0, () => 0));
    st.samples = [];
    st.error = '';

    const bad = new ArrayBuffer(16 + 12); // 12 % 8 != 0
    const dv = new DataView(bad);
    dv.setUint32(0, 1, true);
    dv.setUint32(4, 1, true);

    handleSampleFrame(bad);
    expect(st.samples).toHaveLength(0);
    expect(st.error).toContain('malformed');
  });

  it('drops frames shorter than the header (S-12)', () => {
    const st = scopeStore.state;
    handleSampleFrame(makeFrame(1, 3, 0, () => 0));
    st.samples = [];
    st.error = '';

    handleSampleFrame(new ArrayBuffer(8));
    expect(st.samples).toHaveLength(0);
    expect(st.error).toContain('malformed');
  });
});
