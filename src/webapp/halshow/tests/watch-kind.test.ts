// H-9 regressions: watch entries are (name, kind) tuples so a signal that
// shares its name with a pin is set via the signal endpoint, not pin-first
// precedence. Covers tuple persistence round-trip, legacy string-format
// migration, kind-exact set targeting, and (name, kind) dedupe.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  installStubs, connectStore, fetchCalls, setFetchOverride, res,
} from './helpers';

const WATCH_STORAGE_KEY = 'halshow-watch-list';

beforeEach(() => {
  vi.resetModules();
  installStubs();
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function pin(name: string) {
  return { name, type: 'float', dir: 'IN', value: '0', owner: 'comp', linked: false, has_writer: false };
}

function param(name: string) {
  return { name, type: 'float', dir: 'RW', value: '0', owner: 'comp' };
}

function sig(name: string) {
  return { name, type: 'float', value: '0', writers: [], readers: [], bidirs: [] };
}

/** Serve a HAL snapshot for the REST list endpoints hit by refresh(). */
function installSnapshot(pins: unknown[], params: unknown[], signals: unknown[]) {
  setFetchOverride((url, init) => {
    if ((init?.method ?? 'GET') !== 'GET') return undefined;
    if (url.endsWith('/pins')) return res(200, pins);
    if (url.endsWith('/params')) return res(200, params);
    if (url.endsWith('/signals')) return res(200, signals);
    return undefined; // default route handles status + other lists
  });
}

function storedList(): unknown {
  return JSON.parse(localStorage.getItem(WATCH_STORAGE_KEY)!);
}

function putCalls() {
  return fetchCalls.filter(c => c.method === 'PUT');
}

describe('watch list (name, kind) tuples (H-9)', () => {
  it('persists tuples and round-trips them across a reboot', async () => {
    installSnapshot([pin('c.p')], [], [sig('s1')]);
    const { store } = await connectStore();

    store.addToWatch('c.p', 'pin');
    store.addToWatch('s1', 'signal');
    expect(storedList()).toEqual([
      { name: 'c.p', kind: 'pin' },
      { name: 's1', kind: 'signal' },
    ]);

    // Fresh boot: the tuples are restored as stored, not re-derived
    vi.resetModules();
    installStubs();
    installSnapshot([pin('c.p')], [], [sig('s1')]);
    const { store: store2 } = await connectStore();
    expect(store2.state.watchList).toEqual([
      { name: 'c.p', kind: 'pin' },
      { name: 's1', kind: 'signal' },
    ]);
    expect(store2.state.activeTab).toBe('watch');
  });

  it('migrates the legacy string format with pin → param → sig precedence and prunes dead names', async () => {
    // 'both' exists as pin AND signal → legacy precedence says pin.
    // 'gain' only exists as param, 's1' only as signal, 'gone' nowhere.
    localStorage.setItem(WATCH_STORAGE_KEY, JSON.stringify(['both', 'gain', 's1', 'gone']));
    installSnapshot([pin('both')], [param('gain')], [sig('both'), sig('s1')]);
    const { store } = await connectStore();

    expect(store.state.watchList).toEqual([
      { name: 'both', kind: 'pin' },
      { name: 'gain', kind: 'param' },
      { name: 's1', kind: 'signal' },
    ]);
    // localStorage is rewritten in the tuple format
    expect(storedList()).toEqual([
      { name: 'both', kind: 'pin' },
      { name: 'gain', kind: 'param' },
      { name: 's1', kind: 'signal' },
    ]);
  });

  it('validates persisted tuples: junk shapes, bad kinds and stale names are dropped', async () => {
    localStorage.setItem(WATCH_STORAGE_KEY, JSON.stringify([
      { name: 'c.p', kind: 'pin' },        // valid
      { name: 'c.p', kind: 'bogus' },      // bad kind
      { name: 42, kind: 'pin' },           // bad name
      { kind: 'signal' },                  // missing name
      null,                                // junk
      { name: 'gone', kind: 'signal' },    // no longer resolves
    ]));
    installSnapshot([pin('c.p')], [], []);
    const { store } = await connectStore();

    expect(store.state.watchList).toEqual([{ name: 'c.p', kind: 'pin' }]);
    expect(storedList()).toEqual([{ name: 'c.p', kind: 'pin' }]);
  });

  it('corrupt JSON in localStorage is ignored', async () => {
    localStorage.setItem(WATCH_STORAGE_KEY, '{not json[');
    installSnapshot([pin('c.p')], [], []);
    const { store } = await connectStore();
    expect(store.state.watchList).toEqual([]);
  });

  it('set targets the STORED kind: a watched signal shadowed by a same-name pin sets the signal endpoint', async () => {
    // Persisted from a previous session as the SIGNAL, name also exists as a pin
    localStorage.setItem(WATCH_STORAGE_KEY, JSON.stringify([{ name: 'both', kind: 'signal' }]));
    installSnapshot([pin('both')], [], [sig('both')]);
    const { store } = await connectStore();

    const entry = store.state.watchList[0];
    expect(entry).toEqual({ name: 'both', kind: 'signal' });

    const result = await store.setWatchValue(entry.name, '7', entry.kind);
    expect(result.success).toBe(true);
    const puts = putCalls();
    expect(puts).toHaveLength(1);
    expect(puts[0].url).toMatch(/\/api\/v1\/halcmd\/signal\/both$/);
    expect(JSON.parse(puts[0].body!)).toEqual({ value: '7' });
  });

  it('a failing endpoint for the stored kind surfaces the error — no fallback to another kind', async () => {
    installSnapshot([pin('both')], [], [sig('both')]);
    const { store } = await connectStore();
    store.addToWatch('both', 'signal');

    setFetchOverride((url, init) => {
      if (init?.method === 'PUT') {
        // signal endpoint refuses; the pin endpoint would succeed
        if (url.includes('/signal/')) return res(200, { success: false, error: 'signal has writers' });
        return res(200, { success: true });
      }
      return undefined;
    });

    const result = await store.setWatchValue('both', '7', 'signal');
    expect(result.success).toBe(false);
    expect(result.error).toBe('signal has writers');
    // exactly one PUT — no retry against the pin endpoint
    const puts = putCalls();
    expect(puts).toHaveLength(1);
    expect(puts[0].url).toMatch(/\/signal\/both$/);
  });

  it('dedupes by (name, kind): same name may be watched as pin AND signal, wire sends the bare name once', async () => {
    installSnapshot([pin('both')], [], [sig('both')]);
    const { store, ws } = await connectStore();

    store.addToWatch('both', 'pin');
    store.addToWatch('both', 'pin');     // duplicate → ignored
    store.addToWatch('both', 'signal');  // same name, different kind → kept
    expect(store.state.watchList).toEqual([
      { name: 'both', kind: 'pin' },
      { name: 'both', kind: 'signal' },
    ]);

    // Wire payload stays bare names, deduped
    const subs = ws.sentJson().filter(m => m.action === 'subscribe' && m.func === 'watch_items');
    expect(subs[subs.length - 1].args.names).toEqual(['both']);

    // removing one kind keeps the other
    store.removeFromWatch('both', 'pin');
    expect(store.state.watchList).toEqual([{ name: 'both', kind: 'signal' }]);
  });
});
