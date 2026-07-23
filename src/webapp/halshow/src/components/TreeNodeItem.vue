<script setup lang="ts">
import { halshowStore, type TreeNode, type WatchKind } from '../stores/halshow';

const props = defineProps<{
  node: TreeNode;
  depth: number;
}>();

// H-6: a leaf may also have children when an item's name collides with an
// interior node (a.b vs a.b.c). The main row then acts as the group and an
// extra self-row (shown when expanded) represents the leaf item itself.
function isDual(): boolean {
  return props.node.isLeaf && props.node.children.length > 0;
}

function onToggle(e: Event) {
  e.stopPropagation();
  halshowStore.toggleNode(props.node);
}

function onClick() {
  if (isDual()) {
    // Group behavior for the main row of a dual node
    halshowStore.selectNode({ ...props.node, isLeaf: false, kind: undefined });
  } else {
    halshowStore.selectNode(props.node);
  }
}

// H-9: watch entries are (name, kind) tuples — only watchable node kinds
// (pin/param/signal) can be added; components/functions/threads cannot.
function watchKind(): WatchKind | null {
  const k = props.node.kind;
  return k === 'pin' || k === 'param' || k === 'signal' ? k : null;
}

function onDblClick() {
  const kind = watchKind();
  if (props.node.isLeaf && !isDual() && kind) {
    halshowStore.addToWatch(props.node.fullPath, kind);
  }
}

function onSelfClick() {
  halshowStore.selectNode({ ...props.node, children: [], expanded: false });
}

function onSelfDblClick() {
  const kind = watchKind();
  if (kind) halshowStore.addToWatch(props.node.fullPath, kind);
}

function mainSelected(): boolean {
  const sel = halshowStore.state.selectedNode;
  if (!sel || sel.fullPath !== props.node.fullPath) return false;
  return isDual() ? !sel.isLeaf : true;
}

function selfSelected(): boolean {
  const sel = halshowStore.state.selectedNode;
  return !!sel && sel.fullPath === props.node.fullPath && sel.isLeaf;
}
</script>

<template>
  <div class="tree-node">
    <div
      class="node-row"
      :class="{
        selected: mainSelected(),
        leaf: node.isLeaf && node.children.length === 0,
      }"
      :style="{ paddingLeft: (depth * 16 + 6) + 'px' }"
      @click="onClick"
      @dblclick="onDblClick"
    >
      <span v-if="node.children.length > 0" class="expand-icon" @click="onToggle">{{ node.expanded ? '▾' : '▸' }}</span>
      <span v-else class="leaf-icon">•</span>
      <span class="node-name">{{ node.name }}</span>
    </div>
    <template v-if="node.children.length > 0 && node.expanded">
      <!-- H-6: self-row for a leaf that also has children -->
      <div
        v-if="node.isLeaf"
        class="node-row leaf"
        :class="{ selected: selfSelected() }"
        :style="{ paddingLeft: ((depth + 1) * 16 + 6) + 'px' }"
        @click="onSelfClick"
        @dblclick="onSelfDblClick"
      >
        <span class="leaf-icon">•</span>
        <span class="node-name">{{ node.name }}</span>
      </div>
      <TreeNodeItem
        v-for="child in node.children"
        :key="child.fullPath"
        :node="child"
        :depth="depth + 1"
      />
    </template>
  </div>
</template>

<script lang="ts">
export default { name: 'TreeNodeItem' };
</script>

<style scoped>
.node-row {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 2px 6px;
  cursor: pointer;
  white-space: nowrap;
}

.node-row:hover {
  background: #1a2a3a;
}

.node-row.selected {
  background: #2a4a6a;
  color: #fff;
}

.expand-icon {
  width: 16px;
  font-size: 10px;
  color: #888;
  flex-shrink: 0;
  cursor: pointer;
  text-align: center;
  padding: 2px;
}

.expand-icon:hover {
  color: #fff;
}

.leaf-icon {
  width: 12px;
  font-size: 8px;
  color: #555;
  flex-shrink: 0;
}

.node-name {
  font-family: monospace;
  font-size: 12px;
}
</style>
