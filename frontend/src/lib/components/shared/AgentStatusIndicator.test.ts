import { cleanup, render, screen } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, expect, it } from "vite-plus/test";
import AgentStatusIndicator from "./AgentStatusIndicator.svelte";
import { createSettingsStore } from "../../stores/settings.svelte.js";
import { STORES_KEY } from "../../context.js";

let settings: ReturnType<typeof createSettingsStore>;
beforeEach(() => {
  settings = createSettingsStore();
});
function setVisibility(enabled: boolean): void {
  settings.setWorkspaceSettings({ ...settings.getWorkspaceSettings(), show_agent_status_in_lists: enabled });
}
function renderIndicator(state: string) {
  return render(AgentStatusIndicator, { props: { state }, context: new Map([[STORES_KEY, { settings }]]) });
}

afterEach(() => {
  cleanup();
  setVisibility(false);
  localStorage.clear();
});

it("updates existing list indicators when the display preference changes", async () => {
  renderIndicator("working");
  expect(screen.queryByText("Working")).toBeNull();
  setVisibility(true);
  await tick();
  expect(screen.getByText("Working")).toBeTruthy();
  setVisibility(false);
  await tick();
  expect(screen.queryByText("Working")).toBeNull();
});

it.each([
  ["working", "Working"],
  ["approval", "Approval"],
  ["input", "Input"],
  ["done", "Done"],
])("shows the linked agent's %s state", (agentState, label) => {
  setVisibility(true);
  renderIndicator(agentState);
  expect(screen.getByText(label)).toBeTruthy();
  expect(screen.getByLabelText(`Agent ${label.toLowerCase()}`)).toBeTruthy();
});

it("removes the label when the live agent state clears", async () => {
  setVisibility(true);
  const { rerender } = renderIndicator("done");
  await rerender({ state: undefined });
  expect(screen.queryByText("Done")).toBeNull();
});
