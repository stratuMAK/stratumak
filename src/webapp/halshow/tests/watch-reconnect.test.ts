// H-2/H-8/H-5 regressions: watch WS reconnect loop with backoff, stale
// flagging, subscribe-while-connecting guard, meta re-resolve value clearing.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installStubs, connectStore, FakeWebSocket, flushMicrotasks } from './helpers';

beforeEach(() => {
  vi.resetModules();
  installStubs();
  localStorage.clear();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function metaMsg(
  meta: Array<{ name: string; type: string; dir: string; kind: string; owner: string; linked: boolean }>,
  values: Record<string, string>,
) {
  return { type: 'update', func: 'watch_items', data: { meta, values } };
}

const pinMeta = (name: string) =>
  ({ name, type: 'float', dir: 'IN', kind: 'pin', owner: 'comp', linked: false });

describe('watch reconnect (H-2)', () => {
  it('reconnects after close, flips watchStale, and resubscribes', async () => {
    const { store, ws } = await connectStore();
    expect(store.state.watchOk).toBe(true);

    store.addToWatch('c.p', 'pin');
    ws.message(metaMsg([pinMeta('c.p')], { 'c.p': '42' }));
    expect(store.state.watchValues[0].value).toBe('42');
    expect(store.state.watchStale).toBe(false);

    // Server drops the connection
    ws.serverClose();
    expect(store.state.watchOk).toBe(false);
    expect(store.state.watchStale).toBe(true);
    expect(store.state.watchReconnecting).toBe(true);
    expect(FakeWebSocket.instances).toHaveLength(1);

    // First attempt is scheduled at 1s
    await vi.advanceTimersByTimeAsync(999);
    expect(FakeWebSocket.instances).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(FakeWebSocket.instances).toHaveLength(2);

    const ws2 = FakeWebSocket.instances[1];
    ws2.open();
    await flushMicrotasks();

    expect(store.state.watchOk).toBe(true);
    expect(store.state.watchStale).toBe(false);
    expect(store.state.watchReconnecting).toBe(false);
    // resubscribed with the client-side watch list
    const sub = ws2.sentJson().find(m => m.action === 'subscribe' && m.func === 'watch_items');
    expect(sub).toBeDefined();
    expect(sub!.args.names).toEqual(['c.p']);
  });

  it('backs off 1s doubling to 10s cap, resets after success', async () => {
    const { ws } = await connectStore();
    ws.serverClose();

    const delays = [1000, 2000, 4000, 8000, 10000, 10000];
    let expected = 1;
    for (const delay of delays) {
      await vi.advanceTimersByTimeAsync(delay - 1);
      expect(FakeWebSocket.instances).toHaveLength(1 + expected - 1);
      await vi.advanceTimersByTimeAsync(1);
      expect(FakeWebSocket.instances).toHaveLength(1 + expected);
      FakeWebSocket.instances[expected].fail();
      await flushMicrotasks();
      expected++;
    }

    // A successful reconnect resets the backoff to 1s
    await vi.advanceTimersByTimeAsync(10000);
    const wsOk = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    wsOk.open();
    await flushMicrotasks();
    const count = FakeWebSocket.instances.length;
    wsOk.serverClose();
    await vi.advanceTimersByTimeAsync(1000);
    expect(FakeWebSocket.instances).toHaveLength(count + 1);
  });

  it('guards against multiple concurrent reconnect loops', async () => {
    const { ws } = await connectStore();
    ws.serverClose();
    // A second close event must not schedule a second timer
    ws.serverClose();
    expect(vi.getTimerCount()).toBe(1);
  });

  it('H-8: addToWatch during CONNECTING does not throw and subscribes on open', async () => {
    const { store, ws } = await connectStore();
    store.addToWatch('c.p', 'pin');
    ws.serverClose();

    await vi.advanceTimersByTimeAsync(1000);
    const ws2 = FakeWebSocket.instances[1];
    expect(ws2.readyState).toBe(0); // CONNECTING

    // send would throw on a CONNECTING socket — must be skipped, not thrown
    expect(() => store.addToWatch('other.pin', 'pin')).not.toThrow();
    expect(ws2.sent).toHaveLength(0);

    ws2.open();
    await flushMicrotasks();
    const sub = ws2.sentJson().find(m => m.action === 'subscribe' && m.func === 'watch_items');
    expect(sub!.args.names).toEqual(['c.p', 'other.pin']);
  });
});

describe('watch meta re-resolve (H-5)', () => {
  it('clears stale values; kind "unknown" renders as dead value', async () => {
    const { store, ws } = await connectStore();
    store.addToWatch('c.p', 'pin');
    ws.message(metaMsg([pinMeta('c.p')], { 'c.p': '42' }));
    expect(store.state.watchValues[0].value).toBe('42');

    // Item deleted server-side: re-resolve reports kind unknown, no value
    ws.message(metaMsg([{ name: 'c.p', type: '', dir: '', kind: 'unknown', owner: '', linked: false }], {}));
    expect(store.state.watchValues[0].kind).toBe('unknown');
    expect(store.state.watchValues[0].value).toBe('—');
  });
});
