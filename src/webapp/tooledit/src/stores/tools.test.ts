import { describe, it, expect, vi, afterEach } from 'vitest';
import type { ToolEntry } from '../generated/tools_client';

// Request-level write-path tests: stub fetch, assert the JSON actually
// emitted on the wire (the entry envelope — GP-1 sent flat fields and the
// server zero-wrote with 200).

interface FetchCall {
  url: string;
  method: string;
  body: string | undefined;
}

let calls: FetchCall[] = [];

function stubFetch(impl: (call: FetchCall) => Response | Promise<Response>) {
  calls = [];
  vi.stubGlobal('fetch', vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const call: FetchCall = {
      url: String(url),
      method: init?.method ?? 'GET',
      body: typeof init?.body === 'string' ? init.body : undefined,
    };
    calls.push(call);
    return impl(call);
  }));
}

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const entry: ToolEntry = {
  toolno: 5, pocketno: 2,
  x_offset: 1, y_offset: 2, z_offset: 3.5,
  a_offset: 0.25, b_offset: 0, c_offset: 0,
  u_offset: 0, v_offset: 0, w_offset: 0,
  diameter: 6.35, frontangle: -30, backangle: 30,
  orientation: 7, comment: 'test tool',
  updated: 7n,
};

// the generated client serializes bigint via a JSON.stringify replacer, and the
// server sends the stamp as a JSON string that the client revives to bigint —
// so on the wire (both directions) `updated` is a string
const wireEntry = { ...entry, updated: String(entry.updated) };

// the store creates its client and reactive state at module scope, so each
// test imports a fresh copy
async function freshStore() {
  vi.resetModules();
  return (await import('./tools')).toolStore;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('saveTool', () => {
  it('PUTs the entry envelope with all 17 fields, then reloads', async () => {
    stubFetch(c => c.method === 'PUT'
      ? jsonResponse({ ok: true, index: 5 })
      : jsonResponse([wireEntry]));
    const store = await freshStore();
    const ok = await store.saveTool({ ...entry });
    expect(ok).toBe(true);
    expect(calls[0].method).toBe('PUT');
    expect(calls[0].url).toBe('http://localhost/api/v1/milltask/5');
    const sent = JSON.parse(calls[0].body!);
    expect(Object.keys(sent)).toEqual(['entry']);
    expect(sent.entry).toEqual(wireEntry);
    expect(Object.keys(sent.entry)).toHaveLength(17);
    expect(calls[1]).toMatchObject({ method: 'GET', url: 'http://localhost/api/v1/milltask/' });
    expect(store.state.tools).toEqual([entry]);
    expect(store.state.stale).toBe(false);
    expect(store.state.error).toBeNull();
  });

  it('returns false and keeps the generic error on a non-409 HTTP failure', async () => {
    stubFetch(() => jsonResponse({ error: 'boom' }, 500));
    const store = await freshStore();
    const ok = await store.saveTool({ ...entry });
    expect(ok).toBe(false);
    expect(store.state.error).toContain('boom');
    expect(store.state.error).not.toContain('changed by another client');
    expect(calls).toHaveLength(1);
  });

  it('returns false and sets the error when fetch rejects', async () => {
    stubFetch(() => { throw new Error('network down'); });
    const store = await freshStore();
    const ok = await store.saveTool({ ...entry });
    expect(ok).toBe(false);
    expect(store.state.error).toContain('network down');
  });

  it('flags the table stale when the post-save reload fails', async () => {
    stubFetch(c => c.method === 'PUT'
      ? jsonResponse({ ok: true, index: 5 })
      : jsonResponse({ error: 'list broke' }, 500));
    const store = await freshStore();
    const ok = await store.saveTool({ ...entry });
    expect(ok).toBe(true);
    expect(store.state.stale).toBe(true);
    expect(store.state.error).toContain('list broke');
    stubFetch(() => jsonResponse([wireEntry]));
    await store.loadTools();
    expect(store.state.stale).toBe(false);
  });

  it('round-trips the updated stamp from getTool into the PUT body', async () => {
    const fresh = { ...wireEntry, updated: '9007199254740993' }; // 2^53+1: beyond Number precision
    stubFetch(c => c.method === 'PUT'
      ? jsonResponse({ ok: true, index: 5 })
      : jsonResponse(c.url.endsWith('/5') ? fresh : [fresh]));
    const store = await freshStore();
    const got = await store.getTool(5);
    expect(got!.updated).toBe(9007199254740993n);
    const ok = await store.saveTool(got!);
    expect(ok).toBe(true);
    const putCall = calls.find(c => c.method === 'PUT')!;
    const sent = JSON.parse(putCall.body!);
    expect(sent.entry.updated).toBe('9007199254740993');
  });

  it('sends updated=0 for an Add-mode entry (last-write-wins create)', async () => {
    stubFetch(c => c.method === 'PUT'
      ? jsonResponse({ ok: true, index: 9 })
      : jsonResponse([wireEntry]));
    const store = await freshStore();
    const ok = await store.saveTool({ ...entry, toolno: 9, updated: 0n });
    expect(ok).toBe(true);
    const sent = JSON.parse(calls[0].body!);
    expect(sent.entry.updated).toBe('0');
  });

  it('on 409 sets the distinct conflict error, returns false and reloads the table', async () => {
    const changed = { ...wireEntry, z_offset: 99, updated: '8' };
    stubFetch(c => c.method === 'PUT'
      ? jsonResponse({ error: 'tool 5 modified concurrently' }, 409)
      : jsonResponse([changed]));
    const store = await freshStore();
    const ok = await store.saveTool({ ...entry });
    expect(ok).toBe(false);
    expect(store.state.error).toContain('Tool 5 was changed by another client');
    expect(store.state.error).toContain('NOT saved');
    // a follow-up GET refreshed the table to the new server state
    expect(calls).toHaveLength(2);
    expect(calls[1]).toMatchObject({ method: 'GET', url: 'http://localhost/api/v1/milltask/' });
    expect(store.state.tools).toEqual([{ ...entry, z_offset: 99, updated: 8n }]);
  });

  it('keeps the conflict error even when the post-409 reload also fails', async () => {
    stubFetch(c => c.method === 'PUT'
      ? jsonResponse({ error: 'conflict' }, 409)
      : jsonResponse({ error: 'list broke' }, 500));
    const store = await freshStore();
    const ok = await store.saveTool({ ...entry });
    expect(ok).toBe(false);
    expect(store.state.error).toContain('changed by another client');
  });
});

describe('deleteTool', () => {
  it('DELETEs and reloads from the server on success', async () => {
    stubFetch(c => c.method === 'DELETE'
      ? jsonResponse({ ok: 'deleted' })
      : jsonResponse([]));
    const store = await freshStore();
    store.state.tools = [entry];
    await store.deleteTool(5);
    expect(calls[0]).toMatchObject({ method: 'DELETE', url: 'http://localhost/api/v1/milltask/5' });
    expect(calls[1]).toMatchObject({ method: 'GET', url: 'http://localhost/api/v1/milltask/' });
    expect(store.state.tools).toEqual([]);
  });

  it('keeps local state and sets the error on failure', async () => {
    stubFetch(() => jsonResponse({ error: 'in use' }, 409));
    const store = await freshStore();
    store.state.tools = [entry];
    await store.deleteTool(5);
    expect(store.state.error).toContain('in use');
    expect(calls).toHaveLength(1);
    expect(store.state.tools).toEqual([entry]);
  });
});

describe('loadUnits', () => {
  it('populates state.units from the server', async () => {
    stubFetch(() => jsonResponse({ linearScale: 1 / 25.4, metric: false }));
    const store = await freshStore();
    await store.loadUnits();
    expect(calls[0]).toMatchObject({ method: 'GET', url: 'http://localhost/api/v1/milltask/units' });
    expect(store.state.units.metric).toBe(false);
    expect(store.state.units.linearScale).toBeCloseTo(1 / 25.4, 12);
  });

  it('leaves the mm default and does not throw on failure', async () => {
    stubFetch(() => jsonResponse({ error: 'no units' }, 500));
    const store = await freshStore();
    await expect(store.loadUnits()).resolves.toBeUndefined();
    expect(store.state.units).toEqual({ linearScale: 1, metric: true });
  });

  it('defaults to the metric identity before any fetch', async () => {
    const store = await freshStore();
    expect(store.state.units).toEqual({ linearScale: 1, metric: true });
  });
});

describe('getTool', () => {
  it('GETs the single fresh entry and revives the updated stamp to bigint', async () => {
    stubFetch(() => jsonResponse(wireEntry));
    const store = await freshStore();
    const got = await store.getTool(5);
    expect(calls[0]).toMatchObject({ method: 'GET', url: 'http://localhost/api/v1/milltask/5' });
    expect(got).toEqual(entry);
    expect(typeof got!.updated).toBe('bigint');
  });

  it('returns null and sets the error on failure', async () => {
    stubFetch(() => jsonResponse({ error: 'no such tool' }, 404));
    const store = await freshStore();
    const got = await store.getTool(99);
    expect(got).toBeNull();
    expect(store.state.error).toContain('no such tool');
  });
});
