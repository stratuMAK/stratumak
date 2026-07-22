import { reactive } from 'vue';
import { EmccalibClient, type TunableSection, type TunableItem } from '../generated/emccalib_client';

const baseUrl = window.location.origin;
const client = new EmccalibClient(baseUrl);

interface CalibState {
  sections: TunableSection[];
  activeTab: string;
  loading: boolean;
  error: string;
  // Track pending edits: "SECTION\0KEY" → edited value string
  edits: Map<string, string>;
  saving: boolean;
  saveMessage: string;
}

const state = reactive<CalibState>({
  sections: [],
  activeTab: '',
  loading: false,
  error: '',
  edits: new Map(),
  saving: false,
  saveMessage: '',
});

// Drops out-of-order getTunables responses: only the latest load may commit.
let loadGen = 0;

function itemKey(section: string, key: string): string {
  return `${section}\0${key}`;
}

// Strict numeric parse for values that end up on live HAL pins. parseFloat is
// not usable here: it truncates trailing garbage ("1.5abc" → 1.5), reads a
// decimal comma as 0 ("0,5" → 0), and lets Infinity through — which JSON
// serializes as null and the server then applies as 0.0. A single decimal
// comma with no dot is accepted as the locale spelling and normalized.
export function parseTunableValue(raw: string): number | null {
  let s = raw.trim();
  if (s.includes(',') && !s.includes('.') && s.indexOf(',') === s.lastIndexOf(',')) {
    s = s.replace(',', '.');
  }
  if (!/^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/.test(s)) return null;
  const n = Number(s);
  return Number.isFinite(n) ? n : null;
}

export const calibStore = {
  state,

  async loadTunables() {
    const gen = ++loadGen;
    state.loading = true;
    state.error = '';
    try {
      const sections = await client.getTunables();
      if (gen !== loadGen) return;
      state.sections = sections;
      if (state.sections.length > 0 && !state.activeTab) {
        state.activeTab = state.sections[0].name;
      }
    } catch (e: unknown) {
      if (gen !== loadGen) return;
      state.error = e instanceof Error ? e.message : String(e);
    } finally {
      if (gen === loadGen) state.loading = false;
    }
  },

  getEdit(section: string, key: string): string | undefined {
    return state.edits.get(itemKey(section, key));
  },

  setEdit(section: string, key: string, value: string) {
    if (value.trim() === '') {
      state.edits.delete(itemKey(section, key));
    } else {
      state.edits.set(itemKey(section, key), value);
    }
  },

  clearEdit(section: string, key: string) {
    state.edits.delete(itemKey(section, key));
  },

  async testValue(item: TunableItem) {
    const k = itemKey(item.section, item.key);
    const editVal = state.edits.get(k);
    if (editVal === undefined) return;
    const numVal = parseTunableValue(editVal);
    if (numVal === null) {
      state.error = `Invalid number: "${editVal}"`;
      return;
    }
    state.error = '';
    state.saveMessage = '';
    try {
      if (!(await client.setPin(item.section, item.key, numVal))) {
        throw new Error(`set_pin ${item.section}/${item.key} refused`);
      }
      // Only clear the edit if the operator has not retyped meanwhile.
      if (state.edits.get(k) === editVal) state.edits.delete(k);
      await this.loadTunables();
    } catch (e: unknown) {
      state.error = e instanceof Error ? e.message : String(e);
    }
  },

  async revertValue(item: TunableItem) {
    state.error = '';
    state.saveMessage = '';
    try {
      if (!(await client.revert(item.section, item.key))) {
        throw new Error(`revert ${item.section}/${item.key} refused`);
      }
      state.edits.delete(itemKey(item.section, item.key));
      await this.loadTunables();
    } catch (e: unknown) {
      state.error = e instanceof Error ? e.message : String(e);
    }
  },

  async saveAll() {
    state.saving = true;
    state.error = '';
    state.saveMessage = '';
    try {
      if (!(await client.saveIni())) {
        throw new Error('save_ini refused');
      }
      state.saveMessage = 'INI file saved successfully';
      await this.loadTunables();
    } catch (e: unknown) {
      state.error = e instanceof Error ? e.message : String(e);
    } finally {
      state.saving = false;
    }
  },

  setActiveTab(name: string) {
    state.activeTab = name;
  },
};
