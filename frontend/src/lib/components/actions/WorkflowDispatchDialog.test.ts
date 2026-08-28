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
  render(WorkflowDispatchDialog, { open: true, workflow, environments: [], initialRef: "main", operation, state: { kind: "idle" }, trigger, onsubmit: vi.fn(), onclose, onreload: vi.fn(), onnewcycle: vi.fn() });
  await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(onclose).toHaveBeenCalledOnce();
  await waitFor(() => expect(document.activeElement).toBe(trigger));
});

it.each([
  [{ kind: "pending" } as const],
  [{ kind: "uncertain", message: "Outcome unknown", candidates: [] } as const],
  [{ kind: "conflict" } as const],
])("refuses Escape and overlay dismissal while state is %s", async (state) => {
  const onclose = vi.fn();
  render(WorkflowDispatchDialog, { open: true, workflow, environments: [], initialRef: "main", operation, state, trigger: null, onsubmit: vi.fn(), onclose, onreload: vi.fn(), onnewcycle: vi.fn() });
  const dialog = screen.getByRole("dialog");
  await fireEvent.keyDown(window, { key: "Escape" });
  await fireEvent.click(dialog.parentElement as HTMLElement);
  expect(onclose).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog")).toBeTruthy();
});

it("reloads a conflict exactly once and closes only after acknowledgement", async () => {
  const onclose = vi.fn();
  const onreload = vi.fn();
  const base = { open: true, workflow, environments: [], initialRef: "main", operation, trigger: null, onsubmit: vi.fn(), onclose, onreload, onnewcycle: vi.fn() };
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
  const base = { open: true, workflow, environments: [], initialRef: "main", operation, trigger: null, onsubmit: vi.fn(), onclose: vi.fn(), onreload, onnewcycle: vi.fn() };
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

it("presents definite rejection as dismissible and requires a fresh explicit cycle", async () => {
  const onclose = vi.fn();
  const onsubmit = vi.fn();
  const onnewcycle = vi.fn();
  render(WorkflowDispatchDialog, {
    open: true,
    workflow,
    environments: [],
    initialRef: "main",
    operation,
    state: { kind: "failed", message: "The provider rejected this workflow." },
    trigger: null,
    onsubmit,
    onclose,
    onreload: vi.fn(),
    onnewcycle,
  });

  expect(screen.getByRole("alert").textContent).toContain("rejected");
  await fireEvent.click(screen.getByRole("button", { name: "Run again" }));
  expect(onnewcycle).toHaveBeenCalledTimes(1);
  expect(onsubmit).not.toHaveBeenCalled();
  expect(onclose).not.toHaveBeenCalled();

  await fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(onnewcycle).toHaveBeenCalledTimes(2);
  expect(onclose).toHaveBeenCalledOnce();
});

it("renders ambiguous candidates and makes Dispatch again only begin a fresh confirmation cycle", async () => {
  const onclose = vi.fn();
  const onsubmit = vi.fn();
  const onnewcycle = vi.fn();
  render(WorkflowDispatchDialog, {
    open: true,
    workflow,
    environments: [],
    initialRef: "main",
    operation,
    state: {
      kind: "uncertain",
      message: "The provider could not confirm the dispatch.",
      candidates: [{
        actor: "maintainer",
        conclusion: "",
        event: "workflow_dispatch",
        head_sha: "candidate-head",
        id: "candidate-9",
        name: "Deploy",
        ref: "main",
        run_number: 9,
        status: "queued",
        web_url: "https://github.com/acme/app/actions/runs/9",
        workflow_id: "deploy",
      }],
    },
    trigger: null,
    onsubmit,
    onclose,
    onreload: vi.fn(),
    onnewcycle,
  });

  expect(screen.getByText("candidate-9")).toBeTruthy();
  expect(screen.getByRole("link", { name: "Open candidate run on provider" }).getAttribute("href")).toContain(
    "/actions/runs/9",
  );
  await fireEvent.keyDown(window, { key: "Escape" });
  expect(onclose).not.toHaveBeenCalled();

  await fireEvent.click(screen.getByRole("button", { name: "Dispatch again" }));
  expect(onnewcycle).toHaveBeenCalledOnce();
  expect(onsubmit).not.toHaveBeenCalled();
});

it("acknowledges success on Close and offers Run again without posting", async () => {
  const onclose = vi.fn();
  const onsubmit = vi.fn();
  const onnewcycle = vi.fn();
  render(WorkflowDispatchDialog, {
    open: true,
    workflow,
    environments: [],
    initialRef: "main",
    operation,
    state: { kind: "succeeded" },
    trigger: null,
    onsubmit,
    onclose,
    onreload: vi.fn(),
    onnewcycle,
  });

  await fireEvent.click(screen.getByRole("button", { name: "Run again" }));
  expect(onnewcycle).toHaveBeenCalledTimes(1);
  expect(onclose).not.toHaveBeenCalled();
  expect(onsubmit).not.toHaveBeenCalled();

  await fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(onnewcycle).toHaveBeenCalledTimes(2);
  expect(onclose).toHaveBeenCalledOnce();
});
