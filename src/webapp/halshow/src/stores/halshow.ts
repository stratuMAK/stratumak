import { reactive } from 'vue';
import {
  HalcmdClient,
  type PinInfo,
  type ParamInfo,
  type SignalInfo,
  type ComponentInfo,
  type FunctionInfo,
  type ThreadInfo,
  type HalStatus,
  type CmdResult,
} from '../generated/halcmd_client';
import { HalcmdWatchClient } from '../generated/halcmd_watch_client';

// Tree node representing HAL hierarchy
export interface TreeNode {
  name: string;       // short name (last segment)
  fullPath: string;   // full dotted path
  children: TreeNode[];
  isLeaf: boolean;
  kind?: 'pin' | 'param' | 'signal' | 'component' | 'function' | 'thread';
  expanded?: boolean;
}

export type TabId = 'show' | 'watch' | 'cmd';

// H-9: HAL allows a signal to share a name with a pin (separate namespaces),
// so a bare name is ambiguous. Watch entries carry the kind the user actually
// watched; the SET path targets exactly that kind's endpoint.
export type WatchKind = 'pin' | 'param' | 'signal';

export interface WatchEntry {
  name: string;
  kind: WatchKind;
}

export interface WatchValueItem {
  name: string;
  type: string;
  dir: string;
  kind: string;
  value: string;
  owner: string;
  linked: boolean;
}

export interface CmdHistoryEntry {
  cmd: string;
  output?: string;
  error?: string;
}

export type TreeCategory = 'pins' | 'params' | 'signals' | 'components' | 'functions' | 'threads' | 'api';

export interface ApiFuncInfo {
  name: string;
  method?: string;
  path?: string;
}

export interface ApiWatchInfo {
  name: string;
  default_rate_ms: number;
}

export interface ApiInfo {
  api_name: string;
  instance: string;
  version: number;
  rest: boolean;
  functions?: ApiFuncInfo[];
  watches?: ApiWatchInfo[];
  commands?: string[];
  consumers?: string[];
}

interface HalshowState {
  // Connection (H-3: REST and watch health tracked separately)
  restOk: boolean;
  watchOk: boolean;
  watchStale: boolean;        // H-2: last watch values may be outdated (WS lost)
  watchReconnecting: boolean; // H-2: reconnect loop is active
  error: string;

  // HAL data
  pins: PinInfo[];
  params: ParamInfo[];
  signals: SignalInfo[];
  components: ComponentInfo[];
  functions: FunctionInfo[];
  threads: ThreadInfo[];
  status: HalStatus | null;

  // Tree
  treeCategory: TreeCategory;
  treeFilter: string;
  treeNodes: TreeNode[];
  selectedNode: TreeNode | null;

  // Detail (Show tab)
  selectedItem: PinInfo | ParamInfo | SignalInfo | ComponentInfo | FunctionInfo | ThreadInfo | null;
  selectedItemKind: TreeCategory | null;

  // Watch tab
  watchList: WatchEntry[]; // items being watched, as (name, kind) tuples (H-9)
  watchValues: WatchValueItem[];  // live values from WebSocket
  watchRate: number;       // ms

  // Halcmd tab
  cmdHistory: CmdHistoryEntry[];

  // Node overview
  nodeOverviewPins: PinInfo[];

  // API registry
  apiRegistry: ApiInfo[];
  selectedApi: ApiInfo | null;

  // Active tab
  activeTab: TabId;
}

const state = reactive<HalshowState>({
  restOk: false,
  watchOk: false,
  watchStale: false,
  watchReconnecting: false,
  error: '',

  pins: [],
  params: [],
  signals: [],
  components: [],
  functions: [],
  threads: [],
  status: null,

  treeCategory: 'pins',
  treeFilter: '',
  treeNodes: [],
  selectedNode: null,

  selectedItem: null,
  selectedItemKind: null,

  watchList: [],
  watchValues: [],
  watchRate: 100,

  cmdHistory: [],
  nodeOverviewPins: [],

  apiRegistry: [],
  selectedApi: null,

  activeTab: 'show',
});

let client: HalcmdClient;
let watchClient: HalcmdWatchClient;

// H-2: watch WS reconnect loop with backoff (1s doubling to 10s cap).
const WATCH_RECONNECT_MIN_MS = 1000;
const WATCH_RECONNECT_MAX_MS = 10000;
let watchReconnectDelay = WATCH_RECONNECT_MIN_MS;
let watchReconnectActive = false; // timer pending or connect attempt in flight

function scheduleWatchReconnect() {
  if (watchReconnectActive) return; // guard against concurrent reconnect loops
  watchReconnectActive = true;
  state.watchReconnecting = true;
  setTimeout(() => {
    void halshowStore.connectWatch();
  }, watchReconnectDelay);
  watchReconnectDelay = Math.min(watchReconnectDelay * 2, WATCH_RECONNECT_MAX_MS);
}

const WATCH_STORAGE_KEY = 'halshow-watch-list';

const WATCH_KINDS: readonly string[] = ['pin', 'param', 'signal'];

function isWatchEntry(v: unknown): v is WatchEntry {
  return typeof v === 'object' && v !== null
    && typeof (v as { name?: unknown }).name === 'string'
    && WATCH_KINDS.includes((v as { kind?: unknown }).kind as string);
}

function saveWatchList(entries: WatchEntry[]) {
  try {
    localStorage.setItem(WATCH_STORAGE_KEY,
      JSON.stringify(entries.map(e => ({ name: e.name, kind: e.kind }))));
  } catch { /* quota or private mode — ignore */ }
}

/** H-9: accepts both the legacy format (array of bare-name strings) and the
 *  current {name, kind} tuple format; anything else is dropped. Legacy
 *  strings are migrated to tuples in restoreWatchList(), which has the HAL
 *  snapshot needed to back-derive the kind. */
function loadWatchList(): (string | WatchEntry)[] {
  try {
    const raw = localStorage.getItem(WATCH_STORAGE_KEY);
    if (raw) {
      const arr = JSON.parse(raw);
      if (Array.isArray(arr)) {
        return arr.filter((v): v is string | WatchEntry =>
          typeof v === 'string' || isWatchEntry(v));
      }
    }
  } catch { /* corrupt data — ignore */ }
  return [];
}

function buildTree(items: { name: string }[], kind: TreeCategory): TreeNode[] {
  const root: TreeNode[] = [];
  const map = new Map<string, TreeNode>();

  const leafKind = kind === 'pins' ? 'pin' : kind === 'params' ? 'param'
    : kind === 'signals' ? 'signal' : kind === 'components' ? 'component'
    : kind === 'functions' ? 'function' : 'thread';

  for (const item of items) {
    const parts = item.name.split('.');
    let parent = root;
    let path = '';

    for (let i = 0; i < parts.length; i++) {
      const segment = parts[i];
      path = path ? path + '.' + segment : segment;
      const isLeaf = i === parts.length - 1;

      let node = map.get(path);
      if (!node) {
        node = {
          name: segment,
          fullPath: path,
          children: [],
          isLeaf,
          kind: isLeaf ? leafKind : undefined,
          expanded: false,
        };
        map.set(path, node);
        parent.push(node);
      } else if (isLeaf && !node.isLeaf) {
        // H-6: item name collides with an existing interior node (a.b vs
        // a.b.c) — mark the interior node as leaf too so the item is visible.
        // The reverse order (leaf first, deeper item later) just gains
        // children below. TreeNodeItem renders a self-row for such nodes.
        node.isLeaf = true;
        node.kind = leafKind;
      }
      parent = node.children;
    }
  }

  return root;
}

function filterTree(nodes: TreeNode[], filter: string): TreeNode[] {
  if (!filter) return nodes;
  const lower = filter.toLowerCase();
  const result: TreeNode[] = [];
  for (const node of nodes) {
    if (node.fullPath.toLowerCase().includes(lower)) {
      result.push(node);
    } else if (node.children.length > 0) {
      const filteredChildren = filterTree(node.children, filter);
      if (filteredChildren.length > 0) {
        result.push({ ...node, children: filteredChildren, expanded: true });
      }
    }
  }
  return result;
}

export const halshowStore = {
  state,

  async connect() {
    const origin = window.location.origin;
    client = new HalcmdClient(origin);

    await this.refreshSafe();

    // Connect WebSocket for watch
    await this.connectWatch();
    // Restore saved watch list (items are subscribed once the WS is up)
    this.restoreWatchList();
  },

  /** Connect (or reconnect) the watch WebSocket. On success re-runs
   *  updateWatch() to resubscribe — subscription state is client-side. */
  async connectWatch() {
    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${wsProto}//${window.location.host}/api/v1/watch`;
    watchClient = new HalcmdWatchClient(wsUrl);
    try {
      await watchClient.connect();
      watchClient.onClose = () => {
        state.watchOk = false;
        state.watchStale = true;
        scheduleWatchReconnect();
      };
      watchReconnectActive = false;
      watchReconnectDelay = WATCH_RECONNECT_MIN_MS;
      state.watchOk = true;
      state.watchReconnecting = false;
      this.updateWatch();
      state.watchStale = false;
    } catch {
      // Watch is optional — REST still works; keep retrying with backoff
      watchReconnectActive = false;
      state.watchOk = false;
      scheduleWatchReconnect();
    }
  },

  /** refresh() with transport errors routed into state (H-4). */
  async refreshSafe() {
    try {
      await this.refresh();
      state.restOk = true;
      state.error = '';
    } catch (e) {
      state.restOk = false;
      state.error = e instanceof Error ? e.message : String(e);
    }
  },

  async refresh() {
    const [pins, params, signals, components, functions, threads, status] = await Promise.all([
      client.listPins(),
      client.listParams(),
      client.listSignals(),
      client.listComponents(),
      client.listFunctions(),
      client.listThreads(),
      client.getStatus(),
    ]);
    state.pins = pins;
    state.params = params;
    state.signals = signals;
    state.components = components;
    state.functions = functions;
    state.threads = threads;
    state.status = status;
    this.rebuildTree();
  },

  rebuildTree() {
    const items = this.getCategoryItems(state.treeCategory);
    const raw = buildTree(items, state.treeCategory);
    state.treeNodes = filterTree(raw, state.treeFilter);
  },

  getCategoryItems(cat: TreeCategory): { name: string }[] {
    switch (cat) {
      case 'pins': return state.pins;
      case 'params': return state.params;
      case 'signals': return state.signals;
      case 'components': return state.components;
      case 'functions': return state.functions;
      case 'threads': return state.threads;
      case 'api': return [];
    }
  },

  setCategory(cat: TreeCategory) {
    state.treeCategory = cat;
    state.selectedNode = null;
    state.selectedItem = null;
    state.selectedItemKind = null;
    if (cat === 'api') {
      this.refreshApiRegistry();
    } else {
      this.rebuildTree();
    }
  },

  setFilter(filter: string) {
    state.treeFilter = filter;
    this.rebuildTree();
  },

  async selectNode(node: TreeNode) {
    state.selectedNode = node;
    if (!node.isLeaf) {
      // Non-leaf: show overview of all child pins
      state.nodeOverviewPins = [];
      state.selectedItem = null;
      state.selectedItemKind = null;
      try {
        const allLeaves = this.collectLeaves(node);
        if (state.treeCategory === 'pins') {
          const pins = await Promise.all(allLeaves.map(n => client.getPin(n.fullPath)));
          state.nodeOverviewPins = pins;
        } else if (state.treeCategory === 'params') {
          // Show params as PinInfo-compatible for the overview table
          const params = await Promise.all(allLeaves.map(n => client.getParam(n.fullPath)));
          state.nodeOverviewPins = params.map(p => ({
            name: p.name, type: p.type, dir: p.dir, value: p.value,
            owner: p.owner, linked: false, has_writer: false,
          }));
        }
      } catch (e) {
        state.error = e instanceof Error ? e.message : String(e);
      }
      return;
    }

    state.nodeOverviewPins = [];
    state.selectedItemKind = state.treeCategory;
    try {
      switch (state.treeCategory) {
        case 'pins':
          state.selectedItem = await client.getPin(node.fullPath);
          break;
        case 'params':
          state.selectedItem = await client.getParam(node.fullPath);
          break;
        case 'signals':
          state.selectedItem = await client.getSignal(node.fullPath);
          break;
        default:
          // For components/functions/threads, find from local data
          state.selectedItem = this.getCategoryItems(state.treeCategory)
            .find(i => i.name === node.fullPath) as typeof state.selectedItem;
      }
    } catch (e) {
      state.error = e instanceof Error ? e.message : String(e);
    }
  },

  toggleNode(node: TreeNode) {
    node.expanded = !node.expanded;
  },

  collectLeaves(node: TreeNode): TreeNode[] {
    const leaves: TreeNode[] = [];
    const visit = (n: TreeNode) => {
      if (n.isLeaf) leaves.push(n);
      // H-6: a leaf may also have children (name-collision node)
      for (const child of n.children) visit(child);
    };
    visit(node);
    return leaves;
  },

  // --- Watch Tab ---

  addToWatch(name: string, kind: WatchKind) {
    // H-9: dedupe by (name, kind) — the same name may legitimately be
    // watched twice, once as a pin and once as a same-named signal.
    if (!state.watchList.some(e => e.name === name && e.kind === kind)) {
      state.watchList.push({ name, kind });
      saveWatchList(state.watchList);
      this.updateWatch();
    }
  },

  isWatched(name: string, kind: WatchKind): boolean {
    return state.watchList.some(e => e.name === name && e.kind === kind);
  },

  removeFromWatch(name: string, kind: WatchKind) {
    const idx = state.watchList.findIndex(e => e.name === name && e.kind === kind);
    if (idx >= 0) {
      state.watchList.splice(idx, 1);
      saveWatchList(state.watchList);
      this.updateWatch();
    }
  },

  clearWatch() {
    state.watchList = [];
    state.watchValues = [];
    saveWatchList(state.watchList);
    if (state.watchOk) watchClient?.unsubscribeWatchItems();
  },

  /** Restore watch list from localStorage, dropping items that no longer
   *  exist. H-9: legacy bare-name entries are migrated to {name, kind}
   *  tuples by back-deriving the kind from the current snapshot with the
   *  historical pin → param → signal precedence; names that no longer
   *  resolve are pruned (as before). The list is always re-persisted in the
   *  tuple format. */
  restoreWatchList() {
    const saved = loadWatchList();
    if (saved.length === 0) return;

    const pinNames = new Set(state.pins.map(p => p.name));
    const paramNames = new Set(state.params.map(p => p.name));
    const sigNames = new Set(state.signals.map(s => s.name));
    const exists = (e: WatchEntry) =>
      e.kind === 'pin' ? pinNames.has(e.name)
        : e.kind === 'param' ? paramNames.has(e.name)
          : sigNames.has(e.name);

    const entries: WatchEntry[] = [];
    for (const item of saved) {
      let entry: WatchEntry | null;
      if (typeof item === 'string') {
        // Legacy string entry: the old set path resolved pin → param →
        // signal, so migrate with the same precedence.
        entry = pinNames.has(item) ? { name: item, kind: 'pin' }
          : paramNames.has(item) ? { name: item, kind: 'param' }
            : sigNames.has(item) ? { name: item, kind: 'signal' }
              : null; // no longer resolves — prune
      } else {
        entry = exists(item) ? { name: item.name, kind: item.kind } : null;
      }
      if (entry && !entries.some(e => e.name === entry!.name && e.kind === entry!.kind)) {
        entries.push(entry);
      }
    }

    saveWatchList(entries); // migrate format + prune stale entries
    if (entries.length > 0) {
      state.watchList = entries;
      state.activeTab = 'watch';
      this.updateWatch();
    }
  },

  updateWatch() {
    if (state.watchList.length === 0) {
      if (state.watchOk) watchClient?.unsubscribeWatchItems();
      state.watchValues = [];
      return;
    }

    // H-8: socket not open (CONNECTING/closed) — send would throw. Skip;
    // connectWatch() re-runs updateWatch() once the socket is open.
    if (!state.watchOk) return;

    // H-9 residual limitation (recorded in the findings doc): the subscribe
    // wire payload is bare names and the server-side watch resolve still
    // matches pin-first, so the DISPLAYED live value/meta of a signal
    // shadowed by a same-name pin is the pin's — fixing that needs the kind
    // on the wire. Only the SET path (setWatchValue) is kind-exact. A name
    // watched under two kinds is sent once.
    const wireNames = [...new Set(state.watchList.map(e => e.name))];

    // Seed maps from existing watchValues so old items stay visible during
    // re-subscription.  The new subscription's meta response will replace
    // these with fresh data once it arrives.
    const metaMap = new Map<string, { type: string; dir: string; kind: string; owner: string; linked: boolean }>();
    const valueMap = new Map<string, string>();
    for (const v of state.watchValues) {
      metaMap.set(v.name, { type: v.type, dir: v.dir, kind: v.kind, owner: v.owner, linked: v.linked });
      valueMap.set(v.name, v.value);
    }

    watchClient?.subscribeWatchItems((data: unknown) => {
      const msg = data as Record<string, unknown>;

      if (msg.meta && Array.isArray(msg.meta)) {
        // First message (or structure change): contains metadata + initial values.
        // H-5: clear valueMap too — a deleted item must not keep showing its
        // last value as live (safe: the same message re-delivers every live
        // item's value because the server inverts the shadows).
        metaMap.clear();
        valueMap.clear();
        for (const m of msg.meta as Array<{ name: string; type: string; dir: string; kind: string; owner: string; linked: boolean }>) {
          metaMap.set(m.name, { type: m.type, dir: m.dir ?? '', kind: m.kind ?? '', owner: m.owner, linked: m.linked });
        }
        const values = (msg.values ?? {}) as Record<string, string>;
        for (const [name, value] of Object.entries(values)) {
          valueMap.set(name, value);
        }
      } else {
        // Subsequent messages: only changed name→value pairs
        for (const [name, value] of Object.entries(msg)) {
          valueMap.set(name, value as string);
        }
      }

      // Rebuild watchValues array from metadata + current values.
      // Items not yet in metaMap (newly added, waiting for server meta) are
      // excluded — the template shows them from watchList with '—' fallback.
      // Note: `kind` here is the SERVER-resolved kind of the bare name (see
      // the H-9 residual-limitation note above), not the stored watch kind.
      state.watchValues = wireNames
        .filter(n => metaMap.has(n))
        .map(n => {
          const meta = metaMap.get(n)!;
          return {
            name: n,
            type: meta.type,
            dir: meta.dir,
            kind: meta.kind,
            value: valueMap.get(n) ?? '—',
            owner: meta.owner,
            linked: meta.linked,
          };
        });
    }, state.watchRate, wireNames);
  },

  // --- Mutations ---

  async setValue(name: string, value: string, kind: WatchKind): Promise<CmdResult> {
    let result: CmdResult;
    switch (kind) {
      case 'pin':
        result = await client.setPin(name, value);
        break;
      case 'param':
        result = await client.setParam(name, value);
        break;
      case 'signal':
        result = await client.setSignal(name, value);
        break;
    }
    return result;
  },

  async unlinkPin(name: string): Promise<CmdResult> {
    return await client.unlink(name);
  },

  // --- Node overview: add all to watch ---

  addAllNodePinsToWatch() {
    for (const pin of state.nodeOverviewPins) {
      if (!state.watchList.some(e => e.name === pin.name && e.kind === 'pin')) {
        state.watchList.push({ name: pin.name, kind: 'pin' });
      }
    }
    saveWatchList(state.watchList);
    this.updateWatch();
  },

  // --- Watch: set value ---

  /** H-9: the write targets exactly the kind stored in the watch entry — no
   *  pin → param → signal precedence guessing (a signal may share its name
   *  with a pin). Endpoint failures surface to the caller unchanged; there
   *  is deliberately no fallback to another kind. */
  async setWatchValue(name: string, value: string, kind: WatchKind): Promise<CmdResult> {
    return await this.setValue(name, value, kind);
  },

  // --- Halcmd console ---

  async executeHalcmd(cmdLine: string) {
    const entry: CmdHistoryEntry = { cmd: cmdLine };
    state.cmdHistory.push(entry);

    try {
      const result = await this.parseAndExecute(cmdLine);
      if (result.output) entry.output = result.output;
      if (!result.success) entry.error = result.error ?? 'Failed';
    } catch (e) {
      entry.error = e instanceof Error ? e.message : String(e);
    }
    // Force reactivity by replacing the array
    state.cmdHistory = [...state.cmdHistory];
  },

  clearCmdHistory() {
    state.cmdHistory = [];
  },

  async parseAndExecute(cmdLine: string): Promise<CmdResult> {
    const tokens = cmdLine.trim().split(/\s+/);
    if (tokens.length === 0) return { success: true };
    const cmd = tokens[0];
    const args = tokens.slice(1);

    switch (cmd) {
      case 'show': {
        const what = args[0] ?? 'pin';
        const pattern = args[1];
        let output = '';
        if (what === 'pin' || what === 'pins') {
          const pins = await client.listPins(pattern);
          output = pins.map(p =>
            `${p.name.padEnd(40)} ${p.type.padEnd(6)} ${p.dir.padEnd(4)} ${p.value.padEnd(15)} ${p.linked ? '=> ' + p.signal : ''}`
          ).join('\n');
        } else if (what === 'param' || what === 'params') {
          const params = await client.listParams(pattern);
          output = params.map(p =>
            `${p.name.padEnd(40)} ${p.type.padEnd(6)} ${p.dir.padEnd(4)} ${p.value}`
          ).join('\n');
        } else if (what === 'sig' || what === 'signal' || what === 'signals') {
          const sigs = await client.listSignals(pattern);
          output = sigs.map(s =>
            `${s.name.padEnd(40)} ${s.type.padEnd(6)} ${s.value}`
          ).join('\n');
        } else if (what === 'comp' || what === 'components') {
          const comps = await client.listComponents(pattern);
          output = comps.map(c =>
            `${c.name.padEnd(30)} ${String(c.id).padEnd(6)} ${c.type.padEnd(12)} ${c.state}`
          ).join('\n');
        } else if (what === 'funct' || what === 'functions') {
          const funcs = await client.listFunctions(pattern);
          output = funcs.map(f =>
            `${f.name.padEnd(40)} ${f.owner.padEnd(20)} ${f.fp ? 'FP' : 'NO'}`
          ).join('\n');
        } else if (what === 'thread' || what === 'threads') {
          const threads = await client.listThreads(pattern);
          output = threads.map(t =>
            `${t.name.padEnd(30)} ${String(t.period).padEnd(12)} ${t.fp ? 'FP' : 'NO'}${t.functions.length > 0 ? '\n  ' + t.functions.join('\n  ') : ''}`
          ).join('\n');
        } else {
          return { success: false, error: `Unknown show type: ${what}` };
        }
        return { success: true, output: output || '(no results)' };
      }

      case 'getp':
      case 'gets': {
        const name = args[0];
        if (!name) return { success: false, error: `Usage: ${cmd} <name>` };
        if (cmd === 'getp') {
          // H-7: real halcmd getp reads pins AND params
          try {
            const pin = await client.getPin(name);
            return { success: true, output: pin.value };
          } catch (pinErr) {
            try {
              const param = await client.getParam(name);
              return { success: true, output: param.value };
            } catch {
              throw pinErr;
            }
          }
        } else {
          const sig = await client.getSignal(name);
          return { success: true, output: sig.value };
        }
      }

      case 'setp': {
        if (args.length < 2) return { success: false, error: 'Usage: setp <name> <value>' };
        // Try pin first, then param
        let result = await client.setPin(args[0], args[1]);
        if (!result.success) {
          result = await client.setParam(args[0], args[1]);
        }
        return result;
      }

      case 'sets': {
        if (args.length < 2) return { success: false, error: 'Usage: sets <signal> <value>' };
        return await client.setSignal(args[0], args[1]);
      }

      case 'net': {
        if (args.length < 2) return { success: false, error: 'Usage: net <signal> <pin> [pin...]' };
        return await client.net(args[0], args.slice(1));
      }

      case 'linkps': {
        if (args.length < 2) return { success: false, error: 'Usage: linkps <pin> <signal>' };
        return await client.link(args[0], args[1]);
      }

      case 'unlinkp': {
        if (args.length < 1) return { success: false, error: 'Usage: unlinkp <pin>' };
        return await client.unlink(args[0]);
      }

      case 'newsig': {
        if (args.length < 2) return { success: false, error: 'Usage: newsig <name> <type>' };
        return await client.newSignal(args[0], args[1]);
      }

      case 'delsig': {
        if (args.length < 1) return { success: false, error: 'Usage: delsig <name>' };
        return await client.deleteSignal(args[0]);
      }

      case 'loadrt': {
        if (args.length < 1) return { success: false, error: 'Usage: loadrt <module> [args...]' };
        return await client.load(args[0], args.slice(1));
      }

      case 'unloadrt': {
        if (args.length < 1) return { success: false, error: 'Usage: unloadrt <module>' };
        return await client.unload(args[0]);
      }

      case 'start':
        return await client.start();

      case 'stop':
        return await client.stop();

      case 'status': {
        const st = await client.getStatus();
        return {
          success: true,
          output: `Components: ${st.components}  Pins: ${st.pins}  Signals: ${st.signals}  Params: ${st.params}  Threads: ${st.threads}  Functions: ${st.functions}\nRT lock: ${st.rt_lock}  Mem lock: ${st.mem_lock}  Running: ${st.threads_running}`,
        };
      }

      case 'help':
        return {
          success: true,
          output: [
            'show pin|param|sig|comp|funct|thread [pattern]',
            'getp <pin>           gets <signal>',
            'setp <name> <value>  sets <signal> <value>',
            'net <signal> <pin> [pin...]',
            'linkps <pin> <signal>  unlinkp <pin>',
            'newsig <name> <type>   delsig <name>',
            'loadrt <module> [args...]  unloadrt <module>',
            'start  stop  status',
          ].join('\n'),
        };

      default:
        return { success: false, error: `Unknown command: ${cmd}. Type "help" for available commands.` };
    }
  },

  setActiveTab(tab: TabId) {
    state.activeTab = tab;
  },

  async refreshApiRegistry() {
    try {
      const origin = window.location.origin;
      const resp = await fetch(origin + '/api/v1/_registry');
      if (resp.ok) {
        state.apiRegistry = await resp.json();
      }
    } catch {
      // Silently ignore — API tab shows empty
    }
  },

  selectApi(api: ApiInfo) {
    state.selectedApi = api;
  },
};
