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
};

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
  it('PUTs the entry envelope with all 16 fields, then reloads', async () => {
    stubFetch(c => c.method === 'PUT'
      ? jsonResponse({ ok: true, index: 5 })
      : jsonResponse([entry]));
    const store = await freshStore();
    const ok = await store.saveTool({ ...entry });
    expect(ok).toBe(true);
    expect(calls[0].method).toBe('PUT');
    expect(calls[0].url).toBe('http://localhost/api/v1/milltask/5');
    const sent = JSON.parse(calls[0].body!);
    expect(Object.keys(sent)).toEqual(['entry']);
    expect(sent.entry).toEqual(entry);
    expect(Object.keys(sent.entry)).toHaveLength(16);
    expect(calls[1]).toMatchObject({ method: 'GET', url: 'http://localhost/api/v1/milltask/' });
    expect(store.state.tools).toEqual([entry]);
    expect(store.state.stale).toBe(false);
    expect(store.state.error).toBeNull();
  });

  it('returns false and keeps the error on an HTTP failure', async () => {
    stubFetch(() => jsonResponse({ error: 'boom' }, 500));
    const store = await freshStore();
    const ok = await store.saveTool({ ...entry });
    expect(ok).toBe(false);
    expect(store.state.error).toContain('boom');
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
    stubFetch(() => jsonResponse([entry]));
    await store.loadTools();
    expect(store.state.stale).toBe(false);
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

describe('getTool', () => {
  it('GETs the single fresh entry', async () => {
    stubFetch(() => jsonResponse(entry));
    const store = await freshStore();
    const got = await store.getTool(5);
    expect(calls[0]).toMatchObject({ method: 'GET', url: 'http://localhost/api/v1/milltask/5' });
    expect(got).toEqual(entry);
  });

  it('returns null and sets the error on failure', async () => {
    stubFetch(() => jsonResponse({ error: 'no such tool' }, 404));
    const store = await freshStore();
    const got = await store.getTool(99);
    expect(got).toBeNull();
    expect(store.state.error).toContain('no such tool');
  });
});
