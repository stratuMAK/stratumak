<script setup lang="ts">
// What the selected chart element holds — 2.9's LoadSeqElementProperties.
//
// There are only three things to say: a step has a number, a transition has the
// variable it waits on, and a comment has its text. Everything else about an
// element — where it is, what it links to, whether it is the initial step — is
// said by placing it, which is why 2.9 offers no field for any of them.
//
// The one addition is the initial-step tick. 2.9 can only make a step initial by
// deleting it and placing it again with the other tool, which loses its number
// and its links; that is a gap, not a decision.
import { computed, ref, watch } from 'vue';
import { SEQ_COMMENT_LGT, MAX_STEPS } from '../generated/classicladder_client';
import { formatVar, halLinkFor, ladderStore } from '../stores/ladder';
import { sfcStore, selectedStep, selectedTransition, selectedComment } from '../stores/sfc';

const state = sfcStore.state;

const step = computed(() => selectedStep());
const transition = computed(() => selectedTransition());
const comment = computed(() => selectedComment());

// The form re-syncs off this rather than off the element object: a tool that
// mutates the selected element leaves the object identity alone, so a watcher
// on the element itself would not fire and the fields would keep showing what
// the element held before.
const key = computed(() => {
  const sel = state.selected;
  if (!sel) return '';
  const s = step.value, t = transition.value, c = comment.value;
  if (s) return `s:${sel.offset}:${s.stepNumber}:${s.initStep}`;
  if (t) return `t:${sel.offset}:${t.varTypeCondi}:${t.varNumCondi}`;
  if (c) return `c:${sel.offset}:${c.comment}`;
  return '';
});

const kind = computed(() => {
  if (step.value) return step.value.initStep ? 'Initial step' : 'Step';
  if (transition.value) return 'Transition';
  if (comment.value) return 'Comment';
  return 'Nothing selected';
});

const stepNumber = ref(0);
const condText = ref('');
const commentText = ref('');

watch(key, () => {
  stepNumber.value = step.value?.stepNumber ?? 0;
  condText.value = transition.value
    ? formatVar(transition.value.varTypeCondi, transition.value.varNumCondi)
    : '';
  commentText.value = comment.value?.comment ?? '';
}, { immediate: true });

function commitStepNumber() {
  if (!sfcStore.setStepNumber(stepNumber.value)) return;
  stepNumber.value = step.value?.stepNumber ?? stepNumber.value;
}

function commitCondition() {
  if (!sfcStore.setTransitionCondition(condText.value)) return;
  // Show it back the way the chart writes it, so a symbol becomes the variable
  // it names and the operator sees what was understood.
  const t = transition.value;
  if (t) condText.value = formatVar(t.varTypeCondi, t.varNumCondi);
}

// What the condition is, beyond its number: its symbol, and the HAL signal it
// is wired to if it is a physical bit.
const condAnnotation = computed(() => {
  const t = transition.value;
  if (!t) return null;
  const name = formatVar(t.varTypeCondi, t.varNumCondi);
  const symbol = ladderStore.state.symbolMap.get(name);
  const link = halLinkFor(t.varTypeCondi, t.varNumCondi);
  if (!symbol && !link) return null;
  return { symbol, pin: link?.pin, signal: link?.signal };
});
</script>

<template>
  <div class="props">
    <div class="props-head">
      <span class="props-kind">{{ kind }}</span>
      <span class="props-pos" v-if="state.selected">slot {{ state.selected.offset }}</span>
    </div>

    <p class="hint" v-if="!state.selected">
      Pick a step, a transition or a comment to edit what it holds.
    </p>

    <template v-else-if="step">
      <label for="sfc-stepnum">Step number</label>
      <input id="sfc-stepnum" type="number" min="0" :max="MAX_STEPS - 1"
             v-model.number="stepNumber" @blur="commitStepNumber" @keyup.enter="commitStepNumber" />
      <p class="hint">
        A rung reads this step as %X{{ step.stepNumber }}, and how long it has
        been active as %X{{ step.stepNumber }}.V.
      </p>

      <label class="check">
        <input type="checkbox" :checked="step.initStep"
               @change="sfcStore.setStepInit(($event.target as HTMLInputElement).checked)" />
        Chart starts in this step
      </label>
    </template>

    <template v-else-if="transition">
      <label for="sfc-cond">Condition</label>
      <input id="sfc-cond" v-model="condText" @blur="commitCondition" @keyup.enter="commitCondition"
             placeholder="%I0, %B3 — or a symbol" />
      <p class="annot" v-if="condAnnotation">
        <span v-if="condAnnotation.symbol" class="annot-sym">{{ condAnnotation.symbol }}</span>
        <span v-if="condAnnotation.pin" class="annot-hal">
          {{ condAnnotation.pin }}<template v-if="condAnnotation.signal"> → {{ condAnnotation.signal }}</template>
          <template v-else> (no signal)</template>
        </span>
      </p>
      <p class="hint">
        The transition is taken when this is true and every step above it is
        active.
      </p>
    </template>

    <template v-else-if="comment">
      <label for="sfc-comment">Text</label>
      <input id="sfc-comment" v-model="commentText" :maxlength="SEQ_COMMENT_LGT - 1"
             @input="sfcStore.setCommentText(commentText)" />
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
.props input { margin-bottom: 2px; }

.check {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: #cdd6f4;
}
.check input { margin: 0; width: auto; }

.hint { font-size: 11px; color: #6c7086; margin-top: 6px; line-height: 1.4; }

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
