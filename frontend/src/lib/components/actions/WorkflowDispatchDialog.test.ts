import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { expect, it, vi } from "vitest";
vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => ({
    runMicrotask: (work: () => void) => {
      queueMicrotask(work);
      return { interrupt: () => undefined };
    },
  }),
}));
import WorkflowDispatchDialog from "./WorkflowDispatchDialog.svelte";

const workflow = {
  available: true,
  definition_sha: "sha",
  id: "deploy",
  inputs: [],
  name: "Deploy",
  path: "deploy.yml",
  state: "active",
  web_url: "https://github.com/actions/deploy",
} as const;
function props() {
  return {
    open: true,
    workflow,
    environments: [],
    initialRef: "main",
    operation: { available: true },
    onsubmit: vi.fn(),
    onclose: vi.fn(),
    onreload: vi.fn(),
    onnewcycle: vi.fn(),
  };
}

it("restores trigger focus when canceled before admission", async () => {
  const trigger = document.createElement("button");
  document.body.append(trigger);
  trigger.focus();
  const base = props();
  render(WorkflowDispatchDialog, { ...base, state: { kind: "idle" }, trigger });
  await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(base.onclose).toHaveBeenCalledOnce();
  expect(base.onnewcycle).not.toHaveBeenCalled();
  await waitFor(() => expect(document.activeElement).toBe(trigger));
  trigger.remove();
});

it.each([
  { kind: "pending" } as const,
  { kind: "uncertain", message: "Outcome unknown" } as const,
  { kind: "conflict" } as const,
  { kind: "succeeded" } as const,
  { kind: "failed", message: "The provider rejected this workflow." } as const,
])("dismisses $kind without resetting or submitting the operation", async (state) => {
  const base = props();
  render(WorkflowDispatchDialog, { ...base, state });
  await fireEvent.keyDown(window, { key: "Escape" });
  expect(base.onclose).toHaveBeenCalledTimes(1);
  await fireEvent.pointerDown(screen.getByRole("dialog").parentElement as HTMLElement);
  expect(base.onclose).toHaveBeenCalledTimes(2);
  await fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(base.onclose).toHaveBeenCalledTimes(3);
  expect(base.onnewcycle).not.toHaveBeenCalled();
  expect(base.onsubmit).not.toHaveBeenCalled();
});

it("shows catalog loading and allows retry after a reload failure", async () => {
  const base = props();
  const view = render(WorkflowDispatchDialog, { ...base, state: { kind: "conflict" } });
  await fireEvent.click(screen.getByRole("button", { name: "Reload workflows" }));
  expect(base.onreload).toHaveBeenCalledOnce();
  await view.rerender({ ...base, state: { kind: "conflict" }, reloading: true });
  const reload = screen.getByRole("button", { name: "Reloading workflows…" }) as HTMLButtonElement;
  expect(reload.disabled).toBe(true);
  reload.click();
  expect(base.onreload).toHaveBeenCalledOnce();

  await view.rerender({
    ...base,
    state: { kind: "conflict", reloadError: "Workflow catalog is unavailable." },
    reloading: false,
  });
  expect(screen.getByRole("alert").textContent).toContain("Workflow catalog is unavailable.");
  await fireEvent.click(screen.getByRole("button", { name: "Reload workflows" }));
  expect(base.onreload).toHaveBeenCalledTimes(2);
  expect(base.onsubmit).not.toHaveBeenCalled();
});

it.each([
  { state: { kind: "failed", message: "The provider rejected this workflow." } as const, label: "Run again" },
  { state: { kind: "succeeded" } as const, label: "Run again" },
  { state: { kind: "uncertain", message: "Outcome unknown" } as const, label: "Dispatch again" },
])("starts a fresh confirmation for $state.kind only on explicit request", async ({ state, label }) => {
  const base = props();
  render(WorkflowDispatchDialog, { ...base, state });
  await fireEvent.click(screen.getByRole("button", { name: label }));
  expect(base.onnewcycle).toHaveBeenCalledOnce();
  expect(base.onsubmit).not.toHaveBeenCalled();
  expect(base.onclose).not.toHaveBeenCalled();
});
