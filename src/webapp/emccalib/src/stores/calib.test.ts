// Request-level write-path tests for the emccalib store.
//
// The store talks to the generated EmccalibClient, which does real fetch()
// calls against window.location.origin. These tests stub globalThis.fetch to
// capture (url, method, body) and drive the store's public API, asserting the
// exact wire traffic and the state transitions the client fixes guarantee:
//
//   C-1: non-finite edits ("Infinity") must never reach the wire — JSON would
//        serialize Infinity as null and the server would zero a live gain.
//   C-4: a 200 response whose body is `false` is a refusal, not a success.
//   C-6: saveMessage is cleared by a subsequent test/revert.
//   C-8: a stale getTunables response resolving late must not overwrite
//        newer sections (generation fencing).

import { afterEach, describe, expect, it, vi } from 'vitest';
import type { TunableItem } from '../generated/emccalib_client';

// --- fetch stubbing helpers -------------------------------------------------

interface StubResponse {
  ok: boolean;
  status: number;
  statusText: string;
  json(): Promise<unknown>;
  text(): Promise<string>;
}

function jsonResponse(body: unknown, status = 200): StubResponse {
  const text = JSON.stringify(body);
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: `HTTP ${status}`,
    json: async () => JSON.parse(text),
    text: async () => text,
  };
}

interface CapturedRequest {
  url: string;
  method: string;
  // JSON-parsed request body, or undefined when no body was sent.
  body: unknown;
}

type Router = (url: string) => StubResponse | Promise<StubResponse>;

// Installs a fetch stub that records every request and answers via `route`.
// Returns the (live) list of captured requests.
function installFetch(route: Router): CapturedRequest[] {
  const calls: CapturedRequest[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown, init?: RequestInit) => {
      calls.push({
        url: String(input),
        method: init?.method ?? 'GET',
        body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
      });
      return route(String(input));
    }),
  );
  return calls;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

// The store module holds a reactive singleton created at import time, so each
// test re-imports it after vi.resetModules() to get pristine state.
async function freshStore() {
  vi.resetModules();
  return await import('./calib');
}

const apiBase = () => `${window.location.origin}/api/v1/emccalib`;

const item: TunableItem = {
  section: 'AXIS_0',
  key: 'P',
  hal_pin: 'pid.0.Pgain',
  value: 1,
  ini_value: 1,
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// --- 1. parseTunableValue ---------------------------------------------------

describe('parseTunableValue', () => {
  const cases: Array<[string, number | null]> = [
    ['0,5', 0.5], // single decimal comma normalized
    [' 1.5 ', 1.5], // surrounding whitespace trimmed
    ['1e3', 1000], // scientific notation accepted
    ['', null],
    ['-', null],
    ['Infinity', null],
    ['1e999', null], // overflows to Infinity
    ['1.5abc', null], // parseFloat would truncate to 1.5
    ['0x1A', null], // Number() would read hex
    ['1,2,3', null], // multiple commas are not a decimal comma
  ];

  it.each(cases)('parses %j as %j', async (input, want) => {
    const { parseTunableValue } = await freshStore();
    expect(parseTunableValue(input)).toBe(want);
  });
});

// --- 2./3. testValue wire shape --------------------------------------------

describe('testValue', () => {
  it('PUTs the parsed numeric value to /pin', async () => {
    const calls = installFetch((url) => {
      if (url.endsWith('/pin')) return jsonResponse(true);
      if (url.endsWith('/tunables')) return jsonResponse([]);
      throw new Error(`unexpected request: ${url}`);
    });
    const { calibStore } = await freshStore();

    calibStore.setEdit(item.section, item.key, '2.5');
    await calibStore.testValue(item);

    expect(calibStore.state.error).toBe('');
    const pinCalls = calls.filter((c) => c.url.endsWith('/pin'));
    expect(pinCalls).toHaveLength(1);
    const req = pinCalls[0];
    expect(req.method).toBe('PUT');
    expect(req.url).toBe(`${apiBase()}/pin`);
    const body = req.body as { section: string; key: string; value: number };
    expect(body.section).toBe('AXIS_0');
    expect(body.key).toBe('P');
    // Exactly 2.5, and a real finite JSON number — Infinity would have
    // arrived here as null (C-1).
    expect(body.value).toBe(2.5);
    expect(typeof body.value).toBe('number');
    expect(Number.isFinite(body.value)).toBe(true);
    // Successful test clears the (unretyped) edit.
    expect(calibStore.getEdit(item.section, item.key)).toBeUndefined();
  });

  it('emits no request and sets error for an "Infinity" edit (C-1)', async () => {
    const calls = installFetch((url) => {
      throw new Error(`no request expected, got: ${url}`);
    });
    const { calibStore } = await freshStore();

    calibStore.setEdit(item.section, item.key, 'Infinity');
    await calibStore.testValue(item);

    expect(calls).toHaveLength(0);
    expect(calibStore.state.error).toContain('Invalid number');
    expect(calibStore.state.error).toContain('Infinity');
    // The rejected edit stays visible so the operator can correct it.
    expect(calibStore.getEdit(item.section, item.key)).toBe('Infinity');
  });
});

// --- 4. 200 + body `false` is a refusal (C-4) -------------------------------

describe('a 200 response with body `false`', () => {
  it('from setPin sets error and keeps the edit', async () => {
    installFetch((url) => {
      if (url.endsWith('/pin')) return jsonResponse(false);
      if (url.endsWith('/tunables')) return jsonResponse([]);
      throw new Error(`unexpected request: ${url}`);
    });
    const { calibStore } = await freshStore();

    calibStore.setEdit(item.section, item.key, '2.5');
    await calibStore.testValue(item);

    expect(calibStore.state.error).toContain('refused');
    expect(calibStore.getEdit(item.section, item.key)).toBe('2.5');
  });

  it('from saveIni sets error and no saveMessage', async () => {
    installFetch((url) => {
      if (url.endsWith('/save')) return jsonResponse(false);
      if (url.endsWith('/tunables')) return jsonResponse([]);
      throw new Error(`unexpected request: ${url}`);
    });
    const { calibStore } = await freshStore();

    await calibStore.saveAll();

    expect(calibStore.state.error).toContain('save_ini refused');
    expect(calibStore.state.saveMessage).toBe('');
    expect(calibStore.state.saving).toBe(false);
  });

  it('from revert sets error', async () => {
    installFetch((url) => {
      if (url.endsWith('/revert')) return jsonResponse(false);
      if (url.endsWith('/tunables')) return jsonResponse([]);
      throw new Error(`unexpected request: ${url}`);
    });
    const { calibStore } = await freshStore();

    await calibStore.revertValue(item);

    expect(calibStore.state.error).toContain('refused');
  });
});

// --- 5. APIError keeps the operator's edit ----------------------------------

describe('HTTP 500 from setPin', () => {
  it('sets error and keeps the edit', async () => {
    const calls = installFetch((url) => {
      if (url.endsWith('/pin')) return jsonResponse({ error: 'internal explosion' }, 500);
      throw new Error(`unexpected request: ${url}`);
    });
    const { calibStore } = await freshStore();

    calibStore.setEdit(item.section, item.key, '2.5');
    await calibStore.testValue(item);

    expect(calibStore.state.error).toContain('internal explosion');
    expect(calibStore.state.error).toContain('500');
    expect(calibStore.getEdit(item.section, item.key)).toBe('2.5');
    // Failure short-circuits before the reload.
    expect(calls.filter((c) => c.url.endsWith('/tunables'))).toHaveLength(0);
  });
});

// --- 6. saveMessage lifecycle (C-6) -----------------------------------------

describe('saveMessage', () => {
  it('is set by saveAll success and cleared by a subsequent testValue', async () => {
    installFetch((url) => {
      if (url.endsWith('/save')) return jsonResponse(true);
      if (url.endsWith('/pin')) return jsonResponse(true);
      if (url.endsWith('/tunables')) return jsonResponse([]);
      throw new Error(`unexpected request: ${url}`);
    });
    const { calibStore } = await freshStore();

    await calibStore.saveAll();
    expect(calibStore.state.error).toBe('');
    expect(calibStore.state.saveMessage).toBe('INI file saved successfully');

    calibStore.setEdit(item.section, item.key, '3.5');
    await calibStore.testValue(item);
    expect(calibStore.state.saveMessage).toBe('');
  });

  it('is cleared by a subsequent revert', async () => {
    installFetch((url) => {
      if (url.endsWith('/save')) return jsonResponse(true);
      if (url.endsWith('/revert')) return jsonResponse(true);
      if (url.endsWith('/tunables')) return jsonResponse([]);
      throw new Error(`unexpected request: ${url}`);
    });
    const { calibStore } = await freshStore();

    await calibStore.saveAll();
    expect(calibStore.state.saveMessage).toBe('INI file saved successfully');

    await calibStore.revertValue(item);
    expect(calibStore.state.saveMessage).toBe('');
  });
});

// --- 7. out-of-order getTunables (C-8) --------------------------------------

describe('loadTunables generation fencing', () => {
  it('a stale first response resolving last does not overwrite newer sections', async () => {
    const first = deferred<StubResponse>();
    const second = deferred<StubResponse>();
    const pending = [first, second];
    installFetch((url) => {
      if (url.endsWith('/tunables')) {
        const d = pending.shift();
        if (!d) throw new Error('unexpected extra /tunables request');
        return d.promise;
      }
      throw new Error(`unexpected request: ${url}`);
    });
    const { calibStore } = await freshStore();

    const p1 = calibStore.loadTunables();
    const p2 = calibStore.loadTunables();

    // Newer request's response arrives first and commits.
    second.resolve(jsonResponse([{ name: 'NEW', items: [] }]));
    await p2;
    expect(calibStore.state.sections.map((s) => s.name)).toEqual(['NEW']);
    expect(calibStore.state.loading).toBe(false);

    // Stale response arrives late — must be dropped on the floor.
    first.resolve(jsonResponse([{ name: 'OLD', items: [] }]));
    await p1;
    expect(calibStore.state.sections.map((s) => s.name)).toEqual(['NEW']);
    expect(calibStore.state.activeTab).toBe('NEW');
    expect(calibStore.state.loading).toBe(false);
  });
});
