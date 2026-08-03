// The server log-level control (the replacement for the retired Tk
// "Set Debug Level" tool). The value is process-global and any halcmd client
// can change it, so the contract under test is: always read it back from the
// server, never render the value the user picked as if it had taken effect.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  installStubs, connectStore, fetchCalls, setFetchOverride, res, flushMicrotasks,
} from './helpers';

beforeEach(() => {
  vi.resetModules();
  installStubs();
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function debugCalls() {
  return fetchCalls.filter(c => c.url.endsWith('/debug'));
}

describe('log level read', () => {
  it('is read from GET /api/v1/halcmd/debug on connect', async () => {
    const { store } = await connectStore();
    const gets = debugCalls().filter(c => c.method === 'GET');
    expect(gets.length).toBeGreaterThan(0);
    expect(gets[0].url).toBe('http://localhost:3000/api/v1/halcmd/debug');
    expect(store.state.logLevel).toBe(1);
  });

  it('reports null (not a stale number) when the server cannot be asked', async () => {
    const { store } = await connectStore();
    expect(store.state.logLevel).toBe(1);

    setFetchOverride((url, init) => {
      if (url.endsWith('/debug') && (init?.method ?? 'GET') === 'GET') {
        return res(500, { error: 'boom' });
      }
      return undefined;
    });

    expect(await store.refreshLogLevel()).toBeNull();
    expect(store.state.logLevel).toBeNull();
  });
});

describe('log level write', () => {
  it('PUTs the level and then re-reads it', async () => {
    const { store } = await connectStore();
    fetchCalls.length = 0;

    let served = 1;
    setFetchOverride((url, init) => {
      if (!url.endsWith('/debug')) return undefined;
      if ((init?.method ?? 'GET') === 'PUT') {
        served = JSON.parse(init!.body as string).level;
        return res(200, { success: true });
      }
      return res(200, served);
    });

    const result = await store.setLogLevel(0);
    expect(result.success).toBe(true);

    const puts = debugCalls().filter(c => c.method === 'PUT');
    expect(puts).toHaveLength(1);
    expect(JSON.parse(puts[0].body!)).toEqual({ level: 0 });
    // A read must follow the write — the write's own 200 is not evidence of
    // the resulting state.
    expect(debugCalls().filter(c => c.method === 'GET')).toHaveLength(1);
    expect(store.state.logLevel).toBe(0);
  });

  it('leaves the server value in place when the server refuses', async () => {
    const { store } = await connectStore();

    setFetchOverride((url, init) => {
      if (!url.endsWith('/debug')) return undefined;
      if ((init?.method ?? 'GET') === 'PUT') {
        // A CmdResult refusal: HTTP 200 carrying success:false, the shape that
        // has silently passed for success elsewhere in this codebase.
        return res(200, { success: false, error: 'HAL locked' });
      }
      return res(200, 1); // server still at INFO
    });

    const result = await store.setLogLevel(0);
    expect(result.success).toBe(false);
    expect(result.error).toBe('HAL locked');
    // The requested 0 must not be showing.
    expect(store.state.logLevel).toBe(1);
  });

  it('never sends an out-of-range level (client-side @max(3) validation)', async () => {
    const { store } = await connectStore();
    fetchCalls.length = 0;

    const result = await store.setLogLevel(7);
    expect(result.success).toBe(false);
    expect(debugCalls().filter(c => c.method === 'PUT')).toHaveLength(0);
    expect(store.state.logLevel).toBe(1);
  });

  it('leaves the server value in place when the write throws', async () => {
    const { store } = await connectStore();

    setFetchOverride((url, init) => {
      if (!url.endsWith('/debug')) return undefined;
      if ((init?.method ?? 'GET') === 'PUT') return res(503, { error: 'unavailable' });
      return res(200, 2);
    });

    const result = await store.setLogLevel(0);
    expect(result.success).toBe(false);
    expect(store.state.logLevel).toBe(2);
  });
});

describe('halcmd "debug" verb', () => {
  it('with no argument reports the current level', async () => {
    const { store } = await connectStore();
    const result = await store.parseAndExecute('debug');
    expect(result.success).toBe(true);
    expect(result.output).toContain('1');
    expect(result.output).toContain('INFO');
  });

  it('with a level sets it', async () => {
    const { store } = await connectStore();
    let served = 1;
    setFetchOverride((url, init) => {
      if (!url.endsWith('/debug')) return undefined;
      if ((init?.method ?? 'GET') === 'PUT') {
        served = JSON.parse(init!.body as string).level;
        return res(200, { success: true });
      }
      return res(200, served);
    });

    const result = await store.parseAndExecute('debug 3');
    expect(result.success).toBe(true);
    expect(store.state.logLevel).toBe(3);
  });

  it('rejects an out-of-range level without issuing a request', async () => {
    const { store } = await connectStore();
    fetchCalls.length = 0;

    for (const bad of ['4', '-1', 'x', '1.5']) {
      const result = await store.parseAndExecute(`debug ${bad}`);
      expect(result.success, bad).toBe(false);
      expect(result.error, bad).toContain('invalid debug level');
    }
    expect(debugCalls()).toHaveLength(0);
  });
});

describe('HalcmdPanel control', () => {
  it('renders the server level and writes the picked one', async () => {
    const { store } = await connectStore();
    const { default: HalcmdPanel } = await import('../src/components/HalcmdPanel.vue');
    const { createApp, nextTick } = await import('vue');

    const el = document.createElement('div');
    document.body.appendChild(el);
    const app = createApp(HalcmdPanel);
    app.mount(el);
    await nextTick();

    const select = el.querySelector('.loglevel-select') as HTMLSelectElement;
    expect(select).toBeTruthy();
    expect(select.value).toBe('1');

    let served = 1;
    setFetchOverride((url, init) => {
      if (!url.endsWith('/debug')) return undefined;
      if ((init?.method ?? 'GET') === 'PUT') {
        served = JSON.parse(init!.body as string).level;
        return res(200, { success: true });
      }
      return res(200, served);
    });

    select.value = '0';
    select.dispatchEvent(new Event('change'));
    await flushMicrotasks();
    await nextTick();

    expect(store.state.logLevel).toBe(0);
    expect(select.value).toBe('0');

    app.unmount();
    document.body.innerHTML = '';
  });
});
