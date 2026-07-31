<script setup lang="ts">
// The property editor — 2.9's editproperties_gtk.c.
//
// Without it the toolbar can place a contact but not say what it reads, which
// is most of the way to a program that does nothing. What each element needs is
// different, so this shows one form per element kind rather than a generic
// varType/varNum pair: nobody thinks of a timer as "varNum 3".
//
// Two write paths, matching 2.9: a block's preset and base are the block's, not
// the rung's, and go straight to the controller; an expression is staged with
// the rung and written on Apply, so Cancel undoes it.
import { computed, ref, watch } from 'vue';
import {
  BASE_MINS,
  BASE_SECS,
  BASE_100MS,
  ELE_INPUT,
  ELE_INPUT_NOT,
  ELE_RISING_INPUT,
  ELE_FALLING_INPUT,
  ELE_TIMER,
  ELE_MONOSTABLE,
  ELE_COUNTER,
  ELE_TIMER_IEC,
  ELE_COMPAR,
  ELE_OUTPUT,
  ELE_OUTPUT_NOT,
  ELE_OUTPUT_SET,
  ELE_OUTPUT_RESET,
  ELE_OUTPUT_JUMP,
  ELE_OUTPUT_CALL,
  ELE_OUTPUT_OPERATE,
  ELE_CONNECTION,
  ELE_FREE,
  ELE_UNUSABLE,
  SectionLanguage,
  TimerIECMode,
} from '../generated/classicladder_client';
import {
  ladderStore,
  selectedElement,
  parseVar,
  formatVar,
  varIsWritable,
  halLinkFor,
  expressionText,
  sectionRungs,
} from '../stores/ladder';

const state = ladderStore.state;
const el = computed(() => selectedElement());

// The form fields re-sync off this rather than off `el` itself. Placing an
// element mutates the cell object already in the draft, so the reference does
// not change and a watcher on `el` would not fire — leaving the variable field
// showing what the cell held before the tool overwrote it.
const elKey = computed(() => {
  const c = state.selectedCell;
  const e = el.value;
  if (!c || !e) return '';
  return `${c.rungIdx}:${c.row}:${c.col}:${e.type}:${e.varType}:${e.varNum}`;
});

const BASES = [
  { id: BASE_MINS, label: 'minutes' },
  { id: BASE_SECS, label: 'seconds' },
  { id: BASE_100MS, label: '100 ms' },
];

const IEC_MODES = [
  { id: TimerIECMode.TON, label: 'TON — on delay' },
  { id: TimerIECMode.TOF, label: 'TOF — off delay' },
  { id: TimerIECMode.TP, label: 'TP — pulse' },
];

function kindOf(type: number): string {
  switch (type) {
    case ELE_INPUT: return 'Contact';
    case ELE_INPUT_NOT: return 'Negated contact';
    case ELE_RISING_INPUT: return 'Rising edge contact';
    case ELE_FALLING_INPUT: return 'Falling edge contact';
    case ELE_OUTPUT: return 'Coil';
    case ELE_OUTPUT_NOT: return 'Negated coil';
    case ELE_OUTPUT_SET: return 'Set coil';
    case ELE_OUTPUT_RESET: return 'Reset coil';
    case ELE_OUTPUT_JUMP: return 'Jump coil';
    case ELE_OUTPUT_CALL: return 'Call coil';
    case ELE_TIMER: return 'Timer';
    case ELE_MONOSTABLE: return 'Monostable';
    case ELE_COUNTER: return 'Counter';
    case ELE_TIMER_IEC: return 'IEC timer';
    case ELE_COMPAR: return 'Comparison';
    case ELE_OUTPUT_OPERATE: return 'Assignment';
    case ELE_CONNECTION: return 'Connection';
    case ELE_UNUSABLE: return 'Block body';
    default: return 'Empty cell';
  }
}

const readsAVariable = computed(() => {
  const t = el.value?.type;
  return t === ELE_INPUT || t === ELE_INPUT_NOT || t === ELE_RISING_INPUT || t === ELE_FALLING_INPUT
    || t === ELE_OUTPUT || t === ELE_OUTPUT_NOT || t === ELE_OUTPUT_SET || t === ELE_OUTPUT_RESET;
});

const isCoil = computed(() => {
  const t = el.value?.type;
  return t === ELE_OUTPUT || t === ELE_OUTPUT_NOT || t === ELE_OUTPUT_SET || t === ELE_OUTPUT_RESET;
});

const hasNothingToEdit = computed(() => {
  const t = el.value?.type;
  return t === undefined || t === ELE_FREE || t === ELE_CONNECTION || t === ELE_UNUSABLE;
});

// --- The variable field ---
//
// Held as text while it is being typed, because half a name is not a variable.
// It is committed on blur or Enter, the way 2.9's entry is.

const varText = ref('');
const varError = ref('');

watch(elKey, () => {
  const e = el.value;
  varError.value = '';
  varText.value = e && readsAVariable.value ? formatVar(e.varType, e.varNum) : '';
}, { immediate: true });

function commitVar() {
  const e = el.value;
  if (!e) return;
  const ref_ = parseVar(varText.value);
  if (!ref_) {
    varError.value = `"${varText.value}" is not a variable of this PLC.`;
    return;
  }
  if (isCoil.value && !varIsWritable(ref_.varType)) {
    varError.value = `${formatVar(ref_.varType, ref_.offset)} is read-only — a coil cannot drive it.`;
    return;
  }
  varError.value = '';
  e.varType = ref_.varType;
  e.varNum = ref_.offset;
  // Show it back in canonical form, so the operator sees what was understood —
  // a symbol becomes the variable it names.
  varText.value = formatVar(e.varType, e.varNum);
}

// What the variable is: its symbol, and the HAL signal it is wired to. The
// second is the annotation that makes someone else's ladder readable, and only
// the controller can answer it.
const varAnnotation = computed(() => {
  const e = el.value;
  if (!e || !readsAVariable.value) return null;
  const name = formatVar(e.varType, e.varNum);
  const symbol = state.symbolMap.get(name);
  const link = halLinkFor(e.varType, e.varNum);
  if (!symbol && !link) return null;
  return { symbol, pin: link?.pin, signal: link?.signal };
});

// --- Blocks ---

const blockCount = computed(() => {
  const prog = state.program;
  if (!prog) return 0;
  switch (el.value?.type) {
    case ELE_TIMER: return prog.timers.length;
    case ELE_MONOSTABLE: return prog.monostables.length;
    case ELE_COUNTER: return prog.counters.length;
    case ELE_TIMER_IEC: return prog.timersIec.length;
    default: return 0;
  }
});

// Clamped to the blocks this PLC was configured with: typing 99 on a ten-timer
// PLC would otherwise fire a preset write the controller refuses.
const blockNumber = computed({
  get: () => el.value?.varNum ?? 0,
  set: (v: number) => {
    if (!el.value) return;
    const max = Math.max(0, blockCount.value - 1);
    el.value.varNum = Math.min(max, Math.max(0, Math.trunc(v)));
  },
});

// Block parameters come from the program, and go back one block at a time.
const preset = ref(0);
const base = ref(BASE_SECS);
const iecMode = ref<TimerIECMode>(TimerIECMode.TON);

watch(elKey, () => {
  const prog = state.program;
  const e = el.value;
  if (!prog || !e) return;
  const n = e.varNum;
  switch (e.type) {
    case ELE_TIMER:
      preset.value = prog.timers[n]?.preset ?? 0;
      base.value = prog.timers[n]?.base ?? BASE_SECS;
      break;
    case ELE_MONOSTABLE:
      preset.value = prog.monostables[n]?.preset ?? 0;
      base.value = prog.monostables[n]?.base ?? BASE_SECS;
      break;
    case ELE_COUNTER:
      preset.value = prog.counters[n]?.preset ?? 0;
      break;
    case ELE_TIMER_IEC:
      preset.value = prog.timersIec[n]?.preset ?? 0;
      base.value = prog.timersIec[n]?.base ?? BASE_SECS;
      iecMode.value = prog.timersIec[n]?.mode ?? TimerIECMode.TON;
      break;
  }
}, { immediate: true });

async function commitBlock() {
  const e = el.value;
  if (!e) return;
  const n = e.varNum;
  switch (e.type) {
    case ELE_TIMER: await ladderStore.setTimer(n, preset.value, base.value); break;
    case ELE_MONOSTABLE: await ladderStore.setMonostable(n, preset.value, base.value); break;
    case ELE_COUNTER: await ladderStore.setCounter(n, preset.value); break;
    case ELE_TIMER_IEC: await ladderStore.setTimerIec(n, preset.value, base.value, iecMode.value); break;
  }
}

// --- Expressions ---

const exprText = ref('');

watch(elKey, () => {
  const e = el.value;
  if (e && (e.type === ELE_COMPAR || e.type === ELE_OUTPUT_OPERATE)) {
    exprText.value = expressionText(e.varNum);
  }
}, { immediate: true });

function commitExpr() {
  const e = el.value;
  if (!e) return;
  ladderStore.setDraftExpression(e.varNum, exprText.value);
}

// --- Jump and call targets ---

// A jump names a rung. Offering the section's rungs by number and label beats
// a bare number field: the label is what the author wrote it for.
const jumpTargets = computed(() =>
  sectionRungs(state.activeSection).map(r => ({
    index: r.index,
    label: r.rung.label ? `${r.index} — ${r.rung.label}` : String(r.index),
  })),
);

// A call names a sub-routine by its number, not by its section index.
const subRoutines = computed(() => {
  const prog = state.program;
  if (!prog) return [];
  return prog.sections
    .filter(s => s.used && s.subRoutineNumber >= 0 && s.language === SectionLanguage.LADDER)
    .map(s => ({ number: s.subRoutineNumber, label: `SR${s.subRoutineNumber} — ${s.name}` }));
});
</script>

<template>
  <div class="props">
    <div class="props-head">
      <span class="props-kind">{{ el ? kindOf(el.type) : 'Nothing selected' }}</span>
      <span class="props-pos" v-if="state.selectedCell">
        row {{ state.selectedCell.row }}, col {{ state.selectedCell.col }}
      </span>
    </div>

    <p class="hint" v-if="!el">
      Pick a cell in the rung to edit what it does.
    </p>
    <p class="hint" v-else-if="hasNothingToEdit">
      This cell has nothing to configure.
    </p>

    <!-- Contacts and coils: the variable they work on -->
    <template v-else-if="readsAVariable">
      <label for="prop-var">Variable</label>
      <input id="prop-var" v-model="varText" @blur="commitVar" @keyup.enter="commitVar"
             placeholder="%I0, %B3, %Q1 — or a symbol" />
      <p class="err" v-if="varError">{{ varError }}</p>
      <p class="annot" v-else-if="varAnnotation">
        <span v-if="varAnnotation.symbol" class="annot-sym">{{ varAnnotation.symbol }}</span>
        <span v-if="varAnnotation.pin" class="annot-hal">
          {{ varAnnotation.pin }}<template v-if="varAnnotation.signal"> → {{ varAnnotation.signal }}</template>
          <template v-else> (no signal)</template>
        </span>
      </p>
    </template>

    <!-- Timer, monostable, IEC timer, counter -->
    <template v-else-if="el.type === ELE_TIMER || el.type === ELE_MONOSTABLE
                         || el.type === ELE_COUNTER || el.type === ELE_TIMER_IEC">
      <label for="prop-blocknum">Block number (0 – {{ Math.max(0, blockCount - 1) }})</label>
      <input id="prop-blocknum" type="number" min="0" :max="Math.max(0, blockCount - 1)"
             v-model.number="blockNumber" />

      <template v-if="el.type !== ELE_COUNTER">
        <label for="prop-base">Time base</label>
        <select id="prop-base" v-model.number="base" @change="commitBlock">
          <option v-for="b in BASES" :key="b.id" :value="b.id">{{ b.label }}</option>
        </select>
      </template>

      <template v-if="el.type === ELE_TIMER_IEC">
        <label for="prop-mode">Mode</label>
        <select id="prop-mode" v-model.number="iecMode" @change="commitBlock">
          <option v-for="m in IEC_MODES" :key="m.id" :value="m.id">{{ m.label }}</option>
        </select>
      </template>

      <label for="prop-preset">
        Preset<template v-if="el.type !== ELE_COUNTER"> (in {{ BASES.find(b => b.id === base)?.label }})</template>
      </label>
      <input id="prop-preset" type="number" min="0" v-model.number="preset"
             @blur="commitBlock" @keyup.enter="commitBlock" />
      <p class="hint">
        The preset belongs to the block, so it takes effect at once — Cancel on
        the rung will not undo it.
      </p>
    </template>

    <!-- Comparison and assignment -->
    <template v-else-if="el.type === ELE_COMPAR || el.type === ELE_OUTPUT_OPERATE">
      <label for="prop-expr">
        {{ el.type === ELE_COMPAR ? 'Comparison' : 'Assignment' }} (expression {{ el.varNum }})
      </label>
      <input id="prop-expr" v-model="exprText" @blur="commitExpr" @keyup.enter="commitExpr"
             :placeholder="el.type === ELE_COMPAR ? '%W0>5' : '%W1:=%IW2+1'" />
      <p class="hint">
        Written in variable names; the controller parses and compiles it when the
        rung is applied, and refuses the rung if it does not compile.
      </p>
    </template>

    <!-- Jump -->
    <template v-else-if="el.type === ELE_OUTPUT_JUMP">
      <label for="prop-jump">Jump to rung</label>
      <select id="prop-jump" v-model.number="el.varNum">
        <option v-for="t in jumpTargets" :key="t.index" :value="t.index">{{ t.label }}</option>
      </select>
    </template>

    <!-- Call -->
    <template v-else-if="el.type === ELE_OUTPUT_CALL">
      <label for="prop-call">Call sub-routine</label>
      <select id="prop-call" v-model.number="el.varNum" v-if="subRoutines.length">
        <option v-for="s in subRoutines" :key="s.number" :value="s.number">{{ s.label }}</option>
      </select>
      <p class="hint" v-else>
        This program has no sub-routine sections. Add one from the Sections tab.
      </p>
    </template>
  </div>
</template>

<style scoped>
.props {
  background: #1e1e2e;
  border: 1px solid #45475a;
  border-radius: 4px;
  padding: 10px 12px;
}

.props-head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 8px;
  padding-bottom: 6px;
  border-bottom: 1px solid #45475a;
}

.props-kind { font-weight: 600; font-size: 13px; color: #cdd6f4; }
.props-pos { font-size: 11px; color: #6c7086; font-family: monospace; }

.props label { margin-top: 8px; }
.props input, .props select { margin-bottom: 2px; }

.hint { font-size: 11px; color: #6c7086; margin-top: 6px; line-height: 1.4; }
.err { font-size: 11px; color: #f38ba8; margin-top: 4px; }

.annot {
  font-size: 11px;
  margin-top: 6px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.annot-sym { color: #a6e3a1; font-weight: 600; }
.annot-hal { color: #89b4fa; font-family: monospace; }
</style>
