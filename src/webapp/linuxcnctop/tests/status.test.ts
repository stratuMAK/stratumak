// Store behaviour: the watch subscription, change highlighting, and the
// connection-loss contract (never render a dead server's last values as live).
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installStubs, connectStore, flushMicrotasks, makeStat, FakeWebSocket } from './helpers';

beforeEach(() => {
  vi.resetModules();
  installStubs();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('watch subscription', () => {
  it('subscribes to get_stat at the selected rate', async () => {
    const { ws } = await connectStore();
    const subs = ws.sentJson().filter(m => m.action === 'subscribe');
    expect(subs).toHaveLength(1);
    expect(subs[0]).toMatchObject({
      api: 'emcstat', instance: 'milltask', func: 'get_stat', rate_ms: 100,
    });
  });

  it('resubscribes at the new rate when it is changed', async () => {
    const { store, ws } = await connectStore();
    store.setRate(500);
    await flushMicrotasks();

    const msgs = ws.sentJson();
    expect(msgs.filter(m => m.action === 'unsubscribe')).toHaveLength(1);
    const subs = msgs.filter(m => m.action === 'subscribe');
    expect(subs).toHaveLength(2);
    expect(subs[1].rate_ms).toBe(500);
  });
});

describe('snapshots', () => {
  it('renders rows from a pushed snapshot', async () => {
    const { store, ws } = await connectStore();
    expect(store.state.rows).toHaveLength(0);

    ws.pushStat(makeStat());
    expect(store.state.rows.length).toBeGreaterThan(50);
    expect(store.state.rows.find(r => r.key === 'task.state')?.value).toBe('ON');
  });

  it('does not highlight the first snapshot', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());
    const now = Date.now();
    expect(store.state.rows.every(r => !store.isHighlighted(r, now))).toBe(true);
  });

  it('highlights only the fields that changed', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());
    ws.pushStat(makeStat({ heartbeat: 1 }));

    const now = Date.now();
    const highlighted = store.state.rows.filter(r => store.isHighlighted(r, now));
    expect(highlighted.map(r => r.key)).toEqual(['heartbeat']);
  });

  it('stops highlighting after the decay window', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());
    ws.pushStat(makeStat({ heartbeat: 1 }));

    const row = store.state.rows.find(r => r.key === 'heartbeat')!;
    expect(store.isHighlighted(row, row.changedAt + 1999)).toBe(true);
    expect(store.isHighlighted(row, row.changedAt + 2001)).toBe(false);
  });

  it('carries the change stamp across snapshots that leave a field alone', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());
    ws.pushStat(makeStat({ heartbeat: 1 }));
    const stamp = store.state.rows.find(r => r.key === 'heartbeat')!.changedAt;

    ws.pushStat(makeStat({ heartbeat: 1, estop: 1 }));
    expect(store.state.rows.find(r => r.key === 'heartbeat')!.changedAt).toBe(stamp);
  });

  it('drops rows a later snapshot no longer has', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat({ joints: [{ homed: true }] } as never));
    expect(store.state.rows.some(r => r.key === 'joints.0.homed')).toBe(true);

    ws.pushStat(makeStat({ joints: [] }));
    expect(store.state.rows.some(r => r.key === 'joints.0.homed')).toBe(false);
  });
});

// get_stat is @watch_delta: the first push of a connection is the whole
// StatFull, every later one carries only the changed top-level keys. This is
// the regression that froze the display on its first frame under a green
// "Connected" badge — the generated reviver threw BigInt(undefined) on the
// partial object, so the callback never ran.
describe('delta frames', () => {
  it('applies a delta on top of the full snapshot', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat({ heartbeat: 1 }));
    const full = store.state.rows.length;

    ws.pushStatDelta({ heartbeat: 2 });

    // The row that changed moved...
    expect(store.state.rows.find(r => r.key === 'heartbeat')?.value).toBe('2');
    // ...and nothing else was lost.
    expect(store.state.rows).toHaveLength(full);
    expect(store.state.rows.find(r => r.key === 'task.state')?.value).toBe('ON');
  });

  it('highlights only the delta keys', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());
    ws.pushStatDelta({ estop: 1 });

    const now = Date.now();
    expect(store.state.rows.filter(r => store.isHighlighted(r, now)).map(r => r.key))
      .toEqual(['estop']);
  });

  it('accumulates successive deltas', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());
    ws.pushStatDelta({ heartbeat: 5 });
    ws.pushStatDelta({ estop: 1 });

    // The earlier delta must survive the later one.
    expect(store.state.rows.find(r => r.key === 'heartbeat')?.value).toBe('5');
    expect(store.state.rows.find(r => r.key === 'estop')?.value).toBe('1');
  });

  it('applies a delta that changes a nested field', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());
    ws.pushStatDelta({ task: { ...makeStat().task, state: 1 } });
    expect(store.state.rows.find(r => r.key === 'task.state')?.value).toBe('ESTOP');
  });

  it('does not carry the merge base across a reconnect', async () => {
    vi.useFakeTimers();
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat({ heartbeat: 9 }));
    ws.serverClose();

    await vi.advanceTimersByTimeAsync(1000);
    const next = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    next.open();
    await flushMicrotasks();

    // The new connection opens with its own full frame.
    next.pushStat(makeStat({ heartbeat: 1 }));
    expect(store.state.rows.find(r => r.key === 'heartbeat')?.value).toBe('1');
  });
});

describe('freeze', () => {
  it('holds the display while pushes keep arriving', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat({ heartbeat: 1 }));
    store.toggleFrozen();

    ws.pushStat(makeStat({ heartbeat: 2 }));
    expect(store.state.rows.find(r => r.key === 'heartbeat')?.value).toBe('1');

    store.toggleFrozen();
    ws.pushStat(makeStat({ heartbeat: 3 }));
    expect(store.state.rows.find(r => r.key === 'heartbeat')?.value).toBe('3');
  });
});

describe('connection loss', () => {
  it('marks the values stale and starts reconnecting when the WS drops', async () => {
    vi.useFakeTimers();
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());
    expect(store.state.watchOk).toBe(true);

    ws.serverClose();
    expect(store.state.watchOk).toBe(false);
    expect(store.state.watchStale).toBe(true);
    expect(store.state.watchReconnecting).toBe(true);
    // The last values stay on screen — the stale flag is what stops them
    // reading as live.
    expect(store.state.rows.length).toBeGreaterThan(0);
  });

  it('clears stale once a reconnect succeeds', async () => {
    vi.useFakeTimers();
    const { store, ws } = await connectStore();
    ws.serverClose();
    expect(store.state.watchStale).toBe(true);

    await vi.advanceTimersByTimeAsync(1000);
    const next = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    expect(next).not.toBe(ws);
    next.open();
    await flushMicrotasks();

    expect(store.state.watchOk).toBe(true);
    expect(store.state.watchStale).toBe(false);
    // The resubscription is what makes values live again.
    expect(next.sentJson().filter(m => m.action === 'subscribe')).toHaveLength(1);
  });
});

describe('filter', () => {
  it('matches on field path and on value', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());

    store.setFilter('exec_state');
    expect(store.filteredRows().map(r => r.key)).toContain('task.exec_state');

    store.setFilter('IDENTITY');
    expect(store.filteredRows().map(r => r.key)).toEqual(['kinematics_type']);

    store.setFilter('');
    expect(store.filteredRows().length).toBe(store.state.rows.length);
  });
});

describe('exports', () => {
  it('asJson survives the bigint boot_id', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat({ boot_id: 42n }));
    expect(() => store.asJson()).not.toThrow();
    expect(JSON.parse(store.asJson()).boot_id).toBe('42');
  });

  it('asText exports the filtered view', async () => {
    const { store, ws } = await connectStore();
    ws.pushStat(makeStat());
    store.setFilter('kinematics_type');
    expect(store.asText()).toBe('kinematics_type\tIDENTITY');
  });
});
