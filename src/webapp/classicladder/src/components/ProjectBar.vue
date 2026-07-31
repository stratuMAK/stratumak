<script setup lang="ts">
// Load and save the .clp project.
//
// The app had neither: its "Save" wrote the program into the controller's RAM
// and stopped there, so a machine that was power-cycled came back with the
// ladder it booted with and none of the day's work. Writing the file is what
// "save" means to whoever is standing at the machine.
//
// Paths are resolved and contained by the controller (internal/pathres), so
// what is typed here is a request, not a filesystem operation.
import { ref, watch, computed } from 'vue';
import { ladderStore } from '../stores/ladder';
import { liveStore } from '../stores/live';

const state = ladderStore.state;
const open = ref(false);
const path = ref('');
const notice = ref('');

// The project the controller has open, which is the default target for a save.
const currentFile = computed(() => liveStore.state.status?.projectFile ?? '');

watch(currentFile, (f) => {
  if (path.value === '') path.value = f;
}, { immediate: true });

async function save() {
  notice.value = '';
  const target = path.value.trim() || currentFile.value;
  if (!target) {
    state.error = 'No file to save to — type a path.';
    return;
  }
  if (await ladderStore.saveProject(target)) {
    notice.value = `Saved to ${target}`;
  }
}

async function load() {
  notice.value = '';
  const target = path.value.trim();
  if (!target) {
    state.error = 'No file to load — type a path.';
    return;
  }
  if (!window.confirm(
    `Load ${target}? The program now in the controller is replaced, and anything unsaved is lost.`)) {
    return;
  }
  if (await ladderStore.loadProject(target)) {
    notice.value = `Loaded ${target}`;
  }
}
</script>

<template>
  <div class="project">
    <button class="project-toggle" @click="open = !open">
      <span class="caret">{{ open ? '▾' : '▸' }}</span>
      Project
      <span class="project-file" v-if="currentFile">{{ currentFile }}</span>
      <span class="project-file none" v-else>none loaded</span>
    </button>

    <div class="project-body" v-if="open">
      <label for="project-path">File</label>
      <div class="project-row">
        <input id="project-path" v-model="path" placeholder="demo_sim_cl.clp" @keyup.enter="save" />
        <button class="btn" :disabled="state.busy" @click="save">Save</button>
        <button class="btn btn-plain" :disabled="state.busy" @click="load">Load</button>
      </div>
      <p class="notice" v-if="notice">{{ notice }}</p>
      <p class="hint">
        Relative paths are taken from the configuration directory. Save writes the
        program the controller is running, including edits already applied.
      </p>
    </div>
  </div>
</template>

<style scoped>
.project {
  background: #1e1e2e;
  border: 1px solid #45475a;
  border-radius: 4px;
  margin-bottom: 12px;
}

.project-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  background: none;
  border: none;
  color: #cdd6f4;
  padding: 6px 10px;
  cursor: pointer;
  font-size: 12px;
  text-align: left;
}

.caret { color: #6c7086; }

.project-file {
  font-family: monospace;
  font-size: 11px;
  color: #89b4fa;
  margin-left: auto;
}
.project-file.none { color: #6c7086; font-family: inherit; font-style: italic; }

.project-body {
  padding: 4px 10px 10px;
  border-top: 1px solid #45475a;
}

.project-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.project-row input { flex: 1; }

.btn-plain { background: #313244; color: #cdd6f4; }
.btn-plain:hover { background: #45475a; }
.btn:disabled { opacity: 0.4; cursor: default; }

.notice { font-size: 11px; color: #a6e3a1; margin-top: 6px; }
.hint { font-size: 11px; color: #6c7086; margin-top: 6px; line-height: 1.4; }
</style>
