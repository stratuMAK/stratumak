// Shared test doubles: stubbed fetch (request-recording) + fake WebSocket.
import { vi } from 'vitest';

// --- Fake WebSocket ---

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

  // --- test controls ---

  /** Server accepted the connection. */
  open() {
    this.readyState = 1;
    this.onopen?.();
  }

  /** Connection attempt failed (error + close). */
  fail() {
    this.readyState = 3;
    this.onerror?.();
    this.onclose?.();
  }

  /** Server dropped an established connection. */
  serverClose() {
    this.readyState = 3;
    this.onclose?.();
  }

  /** Deliver a JSON message from the server. */
  message(obj: unknown) {
    this.onmessage?.({ data: JSON.stringify(obj) });
  }

  sentJson(): Record<string, unknown>[] {
    return this.sent.map(s => JSON.parse(s));
  }
}

// --- Stubbed fetch ---

export interface FetchCall {
  url: string;
  method: string;
  body?: string;
}

export const fetchCalls: FetchCall[] = [];

export interface FakeResponse {
  ok: boolean;
  status: number;
  statusText: string;
  json(): Promise<unknown>;
  text(): Promise<string>;
}

export function res(status: number, body: unknown): FakeResponse {
  const text = JSON.stringify(body);
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: '',
    json: async () => JSON.parse(text),
    text: async () => text,
  };
}

export type FetchOverride = (url: string, init?: RequestInit) => FakeResponse | undefined;

let fetchOverride: FetchOverride | null = null;

export function setFetchOverride(fn: FetchOverride | null) {
  fetchOverride = fn;
}

function defaultRoute(url: string, init?: RequestInit): FakeResponse {
  const method = init?.method ?? 'GET';
  if (method === 'GET') {
    if (url.endsWith('/status')) {
      return res(200, {
        rt_lock: false, mem_lock: false, threads_running: false,
        components: 0, pins: 0, signals: 0, params: 0, threads: 0, functions: 0,
      });
    }
    if (url.endsWith('/debug')) {
      return res(200, 1); // INFO, the gomc-server default
    }
    return res(200, []);
  }
  return res(200, { success: true });
}

/** Install fetch + WebSocket stubs and reset recorded state. */
export function installStubs() {
  fetchCalls.length = 0;
  FakeWebSocket.instances.length = 0;
  fetchOverride = null;
  vi.stubGlobal('fetch', async (url: unknown, init?: RequestInit) => {
    fetchCalls.push({ url: String(url), method: init?.method ?? 'GET', body: init?.body as string | undefined });
    return (fetchOverride?.(String(url), init) ?? defaultRoute(String(url), init)) as unknown as Response;
  });
  vi.stubGlobal('WebSocket', FakeWebSocket);
}

export async function flushMicrotasks(rounds = 30) {
  for (let i = 0; i < rounds; i++) await Promise.resolve();
}

/** Import a fresh store (call vi.resetModules() in beforeEach) and connect it,
 *  opening the watch WebSocket. */
export async function connectStore() {
  const { halshowStore } = await import('../src/stores/halshow');
  const p = halshowStore.connect();
  await flushMicrotasks(); // let refresh() settle so the WS gets created
  const ws = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  if (!ws) throw new Error('watch WebSocket was not created');
  ws.open();
  await p;
  await flushMicrotasks();
  return { store: halshowStore, ws };
}
