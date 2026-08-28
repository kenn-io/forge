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

const workflow = { available: true, definition_sha: "sha", id: "deploy", inputs: [], name: "Deploy", path: "deploy.yml", state: "active", web_url: "https://github.com/actions/deploy" } as const;
const operation = { available: true } as const;

it("restores trigger focus when canceled before admission", async () => {
  const trigger = document.createElement("button");
  document.body.append(trigger);
  trigger.focus();
  const onclose = vi.fn();
  render(WorkflowDispatchDialog, { open: true, workflow, environments: [], initialRef: "main", operation, state: { kind: "idle" }, trigger, onsubmit: vi.fn(), onclose, onreload: vi.fn() });
  await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(onclose).toHaveBeenCalledOnce();
  await waitFor(() => expect(document.activeElement).toBe(trigger));
});

it.each([
  [{ kind: "pending" } as const],
  [{ kind: "uncertain", message: "Outcome unknown" } as const],
  [{ kind: "conflict" } as const],
])("refuses Escape and overlay dismissal while state is %s", async (state) => {
  const onclose = vi.fn();
  render(WorkflowDispatchDialog, { open: true, workflow, environments: [], initialRef: "main", operation, state, trigger: null, onsubmit: vi.fn(), onclose, onreload: vi.fn() });
  const dialog = screen.getByRole("dialog");
  await fireEvent.keyDown(window, { key: "Escape" });
  await fireEvent.click(dialog.parentElement as HTMLElement);
  expect(onclose).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog")).toBeTruthy();
});

it("reloads a conflict exactly once and closes only after acknowledgement", async () => {
  const onclose = vi.fn();
  const onreload = vi.fn();
  const base = { open: true, workflow, environments: [], initialRef: "main", operation, trigger: null, onsubmit: vi.fn(), onclose, onreload };
  const view = render(WorkflowDispatchDialog, { ...base, state: { kind: "conflict" } as const });
  const reload = screen.getByRole("button", { name: "Reload workflows" });
  await Promise.all([fireEvent.click(reload), fireEvent.click(reload)]);
  expect(onreload).toHaveBeenCalledOnce();
  expect(screen.getByRole("dialog")).toBeTruthy();

  await view.rerender({ ...base, state: { kind: "succeeded" } });
  await fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(onclose).toHaveBeenCalledOnce();
});

it("allows one reload in each distinct conflict cycle", async () => {
  const onreload = vi.fn();
  const base = { open: true, workflow, environments: [], initialRef: "main", operation, trigger: null, onsubmit: vi.fn(), onclose: vi.fn(), onreload };
  const view = render(WorkflowDispatchDialog, { ...base, state: { kind: "conflict" } as const });
  await fireEvent.click(screen.getByRole("button", { name: "Reload workflows" }));
  expect(onreload).toHaveBeenCalledTimes(1);
  await view.rerender({ ...base, state: { kind: "conflict" } });
  expect((screen.getByRole("button", { name: "Reload workflows" }) as HTMLButtonElement).disabled).toBe(true);
  await fireEvent.click(screen.getByRole("button", { name: "Reload workflows" }));
  expect(onreload).toHaveBeenCalledTimes(1);
  await view.rerender({ ...base, state: { kind: "idle" } });
  await view.rerender({ ...base, state: { kind: "conflict" } });
  await Promise.all([
    fireEvent.click(screen.getByRole("button", { name: "Reload workflows" })),
    fireEvent.click(screen.getByRole("button", { name: "Reload workflows" })),
  ]);
  expect(onreload).toHaveBeenCalledTimes(2);
  await view.rerender({ ...base, open: false, state: { kind: "conflict" } });
  await view.rerender({ ...base, state: { kind: "conflict" } });
  await fireEvent.click(screen.getByRole("button", { name: "Reload workflows" }));
  expect(onreload).toHaveBeenCalledTimes(3);
});
