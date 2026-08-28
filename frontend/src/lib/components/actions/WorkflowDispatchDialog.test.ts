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
  render(WorkflowDispatchDialog, { open: true, workflow, environments: [], initialRef: "main", operation, state: { kind: "idle" }, trigger, onsubmit: vi.fn(), onclose });
  await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(onclose).toHaveBeenCalledOnce();
  await waitFor(() => expect(document.activeElement).toBe(trigger));
});

it("remains open while pending, uncertain, or conflicted and closes after acknowledgement", async () => {
  const onclose = vi.fn();
  const base = { open: true, workflow, environments: [], initialRef: "main", operation, trigger: null, onsubmit: vi.fn(), onclose };
  const view = render(WorkflowDispatchDialog, { ...base, state: { kind: "pending" } as const });
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();

  await view.rerender({ ...base, state: { kind: "uncertain", message: "Outcome unknown" } });
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Close" })).toBeNull();

  await view.rerender({ ...base, state: { kind: "conflict" } });
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Reload workflows" })).toBeTruthy();

  await view.rerender({ ...base, state: { kind: "succeeded" } });
  await fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(onclose).toHaveBeenCalledOnce();
});
