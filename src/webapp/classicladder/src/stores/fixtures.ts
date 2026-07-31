// Shared fixtures and a fetch stub for the store tests.
//
// The stub records every request, so a test can assert what the editor sent as
// well as what it shows — the write path is where an editor gets a program
// wrong, and it is the half that used to have no coverage at all.
import { vi } from 'vitest';

export const varClasses = [
  { prefix: 'B', suffix: '', varType: 0, count: 20, writable: true },
  { prefix: 'T', suffix: '.D', varType: 10, count: 5, writable: false },
  { prefix: 'T', suffix: '.R', varType: 11, count: 5, writable: false },
  { prefix: 'T', suffix: '.P', varType: 230, count: 5, writable: true },
  { prefix: 'T', suffix: '.V', varType: 231, count: 5, writable: false },
  { prefix: 'TM', suffix: '.Q', varType: 15, count: 10, writable: false },
  { prefix: 'M', suffix: '.R', varType: 20, count: 5, writable: false },
  { prefix: 'C', suffix: '.D', varType: 25, count: 10, writable: false },
  { prefix: 'C', suffix: '.E', varType: 26, count: 10, writable: false },
  { prefix: 'C', suffix: '.F', varType: 27, count: 10, writable: false },
  { prefix: 'C', suffix: '.V', varType: 251, count: 10, writable: true },
  { prefix: 'X', suffix: '', varType: 30, count: 128, writable: false },
  { prefix: 'I', suffix: '', varType: 50, count: 10, writable: false },
  { prefix: 'Q', suffix: '', varType: 60, count: 10, writable: true },
  { prefix: 'W', suffix: '', varType: 200, count: 100, writable: true },
  { prefix: 'X', suffix: '.V', varType: 220, count: 128, writable: false },
  { prefix: 'IW', suffix: '', varType: 270, count: 10, writable: false },
  { prefix: 'QW', suffix: '', varType: 280, count: 10, writable: true },
];

// What the backend renders for the expression table. The conversion between
// "@200/0@>5" and "%W0>5" happens there, so these are fixtures rather than
// something this app computes.
export const expressionTexts = [
  { text: '%W0>5' },
  { text: '%W1:=%IW2+1' },
  { text: '' },
  { text: '@999/0@:=1' }, // a reference the backend could not render
  { text: '' },
];

export const emptyElement = () => ({ type: 0, connectedWithTop: 0, varType: 0, varNum: 0 });

export function emptyRung(overrides: Record<string, unknown> = {}) {
  return {
    used: true,
    prevRung: -1,
    nextRung: -1,
    label: '',
    comment: '',
    elements: Array.from({ length: 60 }, emptyElement),
    ...overrides,
  };
}

export const halLinks = [
  { varType: 50, offset: 0, pin: 'classicladder.0.in-00', signal: 'gui-estop', isInput: true },
  { varType: 60, offset: 0, pin: 'classicladder.0.out-00', signal: '', isInput: false },
];

export const sequential = {
  steps: [
    { used: true, initStep: true, stepNumber: 0, page: 0, posiX: 1, posiY: 0 },
    { used: true, initStep: false, stepNumber: 1, page: 0, posiX: 1, posiY: 2 },
    { used: false, initStep: false, stepNumber: 0, page: -1, posiX: 0, posiY: 0 },
  ],
  transitions: [
    {
      used: true, varTypeCondi: 50, varNumCondi: 3,
      stepsToActivate: [1, -1, -1, -1, -1, -1, -1, -1, -1, -1],
      stepsToDeactivate: [0, -1, -1, -1, -1, -1, -1, -1, -1, -1],
      transLinkedForStart: Array(10).fill(-1),
      transLinkedForEnd: Array(10).fill(-1),
      page: 0, posiX: 1, posiY: 1,
    },
  ],
  comments: [],
};

// A chart with every slot present and none of them placed — what the controller
// serves for a program that has a sequential section and nothing drawn on it.
// The editor works on slots, so it needs all of them: a transition names a step
// by its index in this array.
export function emptySequential() {
  return {
    steps: Array.from({ length: 128 }, () => ({
      used: false, initStep: false, stepNumber: 0, page: -1, posiX: 0, posiY: 0,
    })),
    transitions: Array.from({ length: 256 }, () => ({
      used: false, varTypeCondi: 0, varNumCondi: 0,
      stepsToActivate: Array(10).fill(-1),
      stepsToDeactivate: Array(10).fill(-1),
      transLinkedForStart: Array(10).fill(-1),
      transLinkedForEnd: Array(10).fill(-1),
      page: -1, posiX: 0, posiY: 0,
    })),
    comments: Array.from({ length: 50 }, () => ({
      used: false, page: -1, posiX: 0, posiY: 0, comment: '',
    })),
  };
}

// Two rungs chained in one section, so the walk has something to walk.
export function makeProgram() {
  return {
    sizes: { nbrRungs: 4 },
    rungs: [
      emptyRung({ prevRung: -1, nextRung: 1, label: 'start', comment: 'first' }),
      emptyRung({ prevRung: 0, nextRung: 1 }),
      emptyRung({ used: false }),
      emptyRung({ used: false }),
    ],
    sections: [
      { used: true, name: 'main', language: 0, subRoutineNumber: -1, firstRung: 0, lastRung: 1, sequentialPage: -1 },
      { used: true, name: 'chart', language: 1, subRoutineNumber: -1, firstRung: 0, lastRung: 0, sequentialPage: 0 },
    ],
    symbols: [
      { varName: '%Q0', symbol: 'estop-ok', comment: 'chain healthy' },
    ],
    arithmExprs: [
      { expr: '@200/0@>5' },
      { expr: '@200/1@:=@270/2@+1' },
      { expr: '' },
      { expr: '@999/0@:=1' },
      { expr: '' },
    ],
    timersIec: [{ preset: 4, value: 0, base: 1, mode: 0, input: false, output: false }],
    timers: [
      { preset: 3, value: 0, base: 1, inputEnable: false, inputControl: false, outputDone: false, outputRunning: false },
      { preset: 9, value: 0, base: 2, inputEnable: false, inputControl: false, outputDone: false, outputRunning: false },
    ],
    monostables: [{ preset: 2, value: 0, base: 1, input: false, outputRunning: false }],
    counters: [{
      preset: 7, value: 0, inputReset: false, inputPreset: false, inputCountUp: false,
      inputCountDown: false, outputDone: false, outputEmpty: false, outputFull: false,
    }],
  };
}

export interface RecordedRequest {
  method: string;
  url: string;
  body: unknown;
}

export interface Stub {
  requests: RecordedRequest[];
  // Make the next request to a path fail, so a test can check what the editor
  // does with a refusal.
  failNext: (pathFragment: string, status: number, error: string) => void;
}

// installFetchStub answers the reads from the fixtures and records the writes.
export function installFetchStub(
  program: () => unknown = makeProgram,
  chart: () => unknown = () => sequential,
): Stub {
  const requests: RecordedRequest[] = [];
  const failures: { fragment: string; status: number; error: string }[] = [];

  const stub: Stub = {
    requests,
    failNext(fragment, status, error) {
      failures.push({ fragment, status, error });
    },
  };

  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';
    requests.push({
      method,
      url: u,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });

    const failIdx = failures.findIndex(f => u.includes(f.fragment));
    if (failIdx >= 0) {
      const f = failures.splice(failIdx, 1)[0];
      const body = { error: f.error };
      return {
        ok: false,
        status: f.status,
        statusText: f.error,
        headers: { get: () => 'application/json' },
        json: async () => body,
        text: async () => JSON.stringify(body),
      } as unknown as Response;
    }

    const body = u.includes('var_classes') ? varClasses
      : u.includes('expressions/text') ? expressionTexts
      : u.includes('hal_links') ? halLinks
      : u.includes('sequential') ? chart()
      : method === 'GET' ? program()
      : 0;

    return {
      ok: true,
      status: 200,
      headers: { get: () => 'application/json' },
      json: async () => body,
      text: async () => JSON.stringify(body),
    } as unknown as Response;
  }));

  return stub;
}
