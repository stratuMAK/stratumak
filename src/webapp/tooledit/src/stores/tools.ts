import { reactive } from 'vue';
import { ToolsClient, type ToolEntry } from '../generated/tools_client';

export interface ToolEditState {
  tools: ToolEntry[];
  loading: boolean;
  error: string | null;
  // true when a write succeeded but the follow-up reload failed, i.e. the
  // displayed table no longer matches the server; cleared by a successful load
  stale: boolean;
}

const instance = new URLSearchParams(window.location.search).get('instance') || 'milltask';
const client = new ToolsClient(window.location.origin, instance);

const state = reactive<ToolEditState>({
  tools: [],
  loading: false,
  error: null,
  stale: false,
});

async function loadTools() {
  state.loading = true;
  state.error = null;
  try {
    state.tools = await client.listTools();
    state.stale = false;
  } catch (e: unknown) {
    state.error = e instanceof Error ? e.message : String(e);
  } finally {
    state.loading = false;
  }
}

async function getTool(toolno: number): Promise<ToolEntry | null> {
  state.error = null;
  try {
    return await client.getTool(toolno);
  } catch (e: unknown) {
    state.error = e instanceof Error ? e.message : String(e);
    return null;
  }
}

async function saveTool(tool: ToolEntry): Promise<boolean> {
  state.error = null;
  try {
    await client.putTool(tool.toolno, tool);
  } catch (e: unknown) {
    state.error = e instanceof Error ? e.message : String(e);
    return false;
  }
  state.stale = true;
  await loadTools();
  return true;
}

async function deleteTool(toolno: number) {
  state.error = null;
  try {
    await client.deleteTool(toolno);
  } catch (e: unknown) {
    state.error = e instanceof Error ? e.message : String(e);
    return;
  }
  state.stale = true;
  await loadTools();
}

async function reloadTable() {
  state.error = null;
  try {
    await client.reloadTools();
  } catch (e: unknown) {
    state.error = e instanceof Error ? e.message : String(e);
    return;
  }
  state.stale = true;
  await loadTools();
}

export const toolStore = {
  state,
  loadTools,
  getTool,
  saveTool,
  deleteTool,
  reloadTable,
};
