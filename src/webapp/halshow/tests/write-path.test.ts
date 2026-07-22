// Request-level write-path tests: the exact REST calls emitted by the set
// paths, error surfacing (H-4) and the H-1 capture-at-open regression.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  installStubs, connectStore, fetchCalls, setFetchOverride, res, flushMicrotasks,
} from './helpers';

let unmountApp: (() => void) | null = null;

beforeEach(() => {
  vi.resetModules();
  installStubs();
  localStorage.clear();
});

afterEach(() => {
  unmountApp?.();
  unmountApp = null;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

function putCalls() {
  return fetchCalls.filter(c => c.method === 'PUT');
}

function pin(name: string) {
  return { name, type: 'float', dir: 'IN', value: '1.0', owner: 'comp', linked: false, has_writer: false };
}

async function mountDetailPanel() {
  const { halshowStore } = await import('../src/stores/halshow');
  const { default: DetailPanel } = await import('../src/components/DetailPanel.vue');
  const { createApp, nextTick } = await import('vue');
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp(DetailPanel);
  app.mount(el);
  unmountApp = () => app.unmount();
  return { halshowStore, nextTick };
}

function findButton(text: string): HTMLButtonElement {
  const btn = [...document.querySelectorAll('button')].find(b => b.textContent?.trim() === text);
  if (!btn) throw new Error(`button "${text}" not found`);
  return btn as HTMLButtonElement;
}

describe('store.setValue REST calls', () => {
  it('pin → PUT /api/v1/halcmd/pin/:name with {"value":...}', async () => {
    const { store } = await connectStore();
    const result = await store.setValue('comp.0.out', '3.5', 'pin');
    expect(result.success).toBe(true);
    const puts = putCalls();
    expect(puts).toHaveLength(1);
    expect(puts[0].url).toMatch(/\/api\/v1\/halcmd\/pin\/comp\.0\.out$/);
    expect(JSON.parse(puts[0].body!)).toEqual({ value: '3.5' });
  });

  it('param → PUT /api/v1/halcmd/param/:name with {"value":...}', async () => {
    const { store } = await connectStore();
    await store.setValue('comp.0.gain', '2', 'param');
    const puts = putCalls();
    expect(puts).toHaveLength(1);
    expect(puts[0].url).toMatch(/\/api\/v1\/halcmd\/param\/comp\.0\.gain$/);
    expect(JSON.parse(puts[0].body!)).toEqual({ value: '2' });
  });

  it('signal → PUT /api/v1/halcmd/signal/:name with {"value":...}', async () => {
    const { store } = await connectStore();
    await store.setValue('spindle-speed', '1200', 'signal');
    const puts = putCalls();
    expect(puts).toHaveLength(1);
    expect(puts[0].url).toMatch(/\/api\/v1\/halcmd\/signal\/spindle-speed$/);
    expect(JSON.parse(puts[0].body!)).toEqual({ value: '1200' });
  });
});

describe('DetailPanel set dialog', () => {
  it('CmdResult{success:false} surfaces the error (no silence)', async () => {
    await connectStore();
    const { halshowStore, nextTick } = await mountDetailPanel();
    halshowStore.state.selectedItem = pin('comp.0.out');
    halshowStore.state.selectedItemKind = 'pins';
    await nextTick();

    findButton('Set Value').click();
    await nextTick();
    setFetchOverride((_url, init) =>
      init?.method === 'PUT' ? res(200, { success: false, error: 'value out of range' }) : undefined);
    findButton('Set').click();
    await flushMicrotasks();
    await nextTick();

    expect(document.querySelector('.result-msg')?.textContent).toContain('value out of range');
    // dialog stays open on failure
    expect(document.querySelector('.edit-overlay')).not.toBeNull();
  });

  it('H-4: a thrown APIError lands in the error slot', async () => {
    await connectStore();
    const { halshowStore, nextTick } = await mountDetailPanel();
    halshowStore.state.selectedItem = pin('comp.0.out');
    halshowStore.state.selectedItemKind = 'pins';
    await nextTick();

    findButton('Set Value').click();
    await nextTick();
    setFetchOverride((_url, init) =>
      init?.method === 'PUT' ? res(500, { error: 'backend exploded' }) : undefined);
    findButton('Set').click();
    await flushMicrotasks();
    await nextTick();

    const msg = document.querySelector('.result-msg')?.textContent ?? '';
    expect(msg).toContain('backend exploded');
    expect(msg).toContain('HTTP 500');
  });

  it('H-1: the write targets the item captured at dialog-open time', async () => {
    await connectStore();
    const { halshowStore, nextTick } = await mountDetailPanel();
    halshowStore.state.selectedItem = pin('compA.out');
    halshowStore.state.selectedItemKind = 'pins';
    await nextTick();

    findButton('Set Value').click();
    await nextTick();
    expect(document.querySelector('.edit-title')?.textContent).toContain('compA.out');

    // Operator clicks another tree node while the dialog is up
    halshowStore.state.selectedItem = pin('compB.in');
    await nextTick();
    // dialog header still names the captured target
    expect(document.querySelector('.edit-title')?.textContent).toContain('compA.out');

    findButton('Set').click();
    await flushMicrotasks();

    const puts = putCalls();
    expect(puts).toHaveLength(1);
    expect(puts[0].url).toMatch(/\/pin\/compA\.out$/);
  });
});
