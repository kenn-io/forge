import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { GeneratedProblemResponse } from "../../api/runtime.js";
import { makeGeneratedClient } from "../../testing/generated-client.js";
import { navigate } from "../../stores/router.svelte.js";
import { getInlineWorkspaceController, resetWorkspaceHostForTest } from "../../stores/workspace-host.svelte.js";

const mocks = vi.hoisted(() => ({
  runtimeClient: {
    getWorkspace: vi.fn(),
    getWorkspaceRuntime: vi.fn(),
    launchWorkspaceRuntimeSession: vi.fn(),
    getFleetWorkspace: vi.fn(),
    getFleetWorkspaceRuntime: vi.fn(),
    launchFleetWorkspaceRuntimeSession: vi.fn(),
  },
}));
const runtimeState = vi.hoisted<{ appRuntime?: OwnedAppRuntime }>(() => ({}));
const events = vi.hoisted(() => ({ subscribeWorkspaceEvents: vi.fn() }));

vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtimeState.appRuntime }));

vi.mock("../../context.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../context.js")>();
  return {
    ...actual,
    getStores: () => ({
      events,
      settings: {
        getTerminalSettings: () => ({
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
        }),
      },
    }),
  };
});

import MobileWorkspaceTerminal from "./MobileWorkspaceTerminal.svelte";
import type { WorkspaceEventsNotification } from "../../stores/events.svelte.js";
import {
  markWorkspaceIdDeleted,
  queueWorkspaceLaunch,
  resetWorkspaceCreatePendingForTest,
} from "../../stores/workspace-create-pending.svelte.js";
import { resetSessionHostForTest } from "../../stores/session-host.svelte.js";

const workspace = {
  id: "ws-a",
  status: "ready",
  created_at: "2026-09-26T00:00:00Z",
  enrichment_status: "fresh",
  git_head_ref: "feature/test",
  item_number: 7,
  item_type: "pull_request",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widgets",
  tmux_session: "forge-test",
  worktree_path: "/tmp/test-worktree",
  repo: { provider: "github", platform_host: "github.com", owner: "acme", name: "widgets", repo_path: "acme/widgets" },
};
const runtime = {
  launch_targets: [{ key: "helper", label: "Helper", kind: "agent", available: true, source: "config" }],
  sessions: [],
};
const props = { workspaceId: "ws-a", onBack: vi.fn(), onMissing: vi.fn(), onOpenItem: vi.fn() };

const identity = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 7,
  itemType: "pull" as const,
};
const workspaceRef = { id: "ws-a", status: "ready" };

describe("MobileWorkspaceTerminal", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks.runtimeClient)) mock.mockReset();
    mocks.runtimeClient.getWorkspace.mockResolvedValue(workspace);
    mocks.runtimeClient.getWorkspaceRuntime.mockResolvedValue(runtime);
    mocks.runtimeClient.getFleetWorkspace.mockResolvedValue(workspace);
    mocks.runtimeClient.getFleetWorkspaceRuntime.mockResolvedValue(runtime);
    events.subscribeWorkspaceEvents.mockReset().mockReturnValue(() => {});
    runtimeState.appRuntime = makeAppRuntime(
      makeGeneratedClient({
        WorkspacesService: mocks.runtimeClient,
        FleetService: mocks.runtimeClient,
      }),
    );
    localStorage.clear();
    resetWorkspaceHostForTest();
    resetWorkspaceCreatePendingForTest();
    resetSessionHostForTest();
    navigate("/pulls");
  });

  afterEach(async () => {
    cleanup();
    if (runtimeState.appRuntime) await Effect.runPromise(runtimeState.appRuntime.disposeEffect);
    resetWorkspaceHostForTest();
    resetWorkspaceCreatePendingForTest();
    resetSessionHostForTest();
  });

  it.each([true, false])("shows only available launch choices (available: %s)", async (available) => {
    mocks.runtimeClient.getWorkspaceRuntime.mockResolvedValue({
      ...runtime,
      launch_targets: [
        { key: "helper", label: "Helper", kind: "agent", available, source: "config" },
        {
          key: "disabled",
          label: "Disabled agent",
          kind: "agent",
          available: false,
          disabled_reason: "disabled by config",
        },
        {
          key: "missing",
          label: "Missing agent",
          kind: "acp",
          available: false,
          disabled_reason: "executable not found",
        },
      ],
    });
    render(MobileWorkspaceTerminal, { props });
    await fireEvent.click(await screen.findByRole("button", { name: "Terminal options" }));
    await fireEvent.click(await screen.findByRole("button", { name: /New terminal/ }));
    const sheet = within(await screen.findByRole("dialog", { name: "Launch workspace session" }));
    expect(sheet.queryByText("Disabled agent")).toBeNull();
    expect(sheet.queryByText("Missing agent")).toBeNull();
    if (available) {
      expect(sheet.getByRole("button", { name: /Helper/ }).hasAttribute("disabled")).toBe(false);
    } else {
      expect(sheet.queryByRole("button", { name: /Helper/ })).toBeNull();
      expect(sheet.getByText("No launch targets are available for this workspace.")).toBeTruthy();
    }
  });

  it("starts runtime discovery while workspace details are held", async () => {
    const detail = Promise.withResolvers<typeof workspace>();
    mocks.runtimeClient.getWorkspace.mockReturnValue(detail.promise);
    render(MobileWorkspaceTerminal, { props });
    await waitFor(() => expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce());
    expect(screen.getByText("Loading workspace…")).toBeTruthy();
    detail.resolve(workspace);
    expect(await screen.findByRole("button", { name: "Terminal options" })).toBeTruthy();
  });

  it.each(["synchronous", "asynchronous"])(
    "loads once when Open arrives %s and refreshes a stale reconnect",
    async (delivery) => {
      const detail = Promise.withResolvers<typeof workspace>();
      const runtimeRead = Promise.withResolvers<typeof runtime>();
      let notify: ((event: WorkspaceEventsNotification) => void) | undefined;
      events.subscribeWorkspaceEvents.mockImplementation((subscriber: (event: WorkspaceEventsNotification) => void) => {
        notify = subscriber;
        if (delivery === "synchronous") subscriber({ type: "open" });
        else queueMicrotask(() => subscriber({ type: "open" }));
        return () => {};
      });
      mocks.runtimeClient.getWorkspace.mockReturnValue(detail.promise);
      mocks.runtimeClient.getWorkspaceRuntime.mockReturnValue(runtimeRead.promise);
      render(MobileWorkspaceTerminal, { props });
      await waitFor(() => expect(notify).toBeTypeOf("function"));
      detail.resolve(workspace);
      runtimeRead.resolve(runtime);
      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Terminal options" }).hasAttribute("disabled")).toBe(false),
      );
      expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledOnce();
      expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce();

      notify?.({ type: "reconnect.stale", payload: {} });
      await waitFor(() => expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledTimes(2));
      await waitFor(() => expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledTimes(2));
    },
  );

  it("accepts a ready status while reconnect detail refresh is held", async () => {
    const reconnectDetail = Promise.withResolvers<typeof workspace>();
    const statusDetail = Promise.withResolvers<typeof workspace>();
    let notify: ((event: WorkspaceEventsNotification) => void) | undefined;
    events.subscribeWorkspaceEvents.mockImplementation((subscriber: (event: WorkspaceEventsNotification) => void) => {
      notify = subscriber;
      subscriber({ type: "open" });
      return () => {};
    });
    mocks.runtimeClient.getWorkspace
      .mockResolvedValueOnce({ ...workspace, status: "creating" })
      .mockReturnValueOnce(reconnectDetail.promise)
      .mockReturnValue(statusDetail.promise);
    mocks.runtimeClient.launchWorkspaceRuntimeSession.mockReturnValue(new Promise(() => {}));
    queueWorkspaceLaunch("ws-a", "helper", undefined);
    render(MobileWorkspaceTerminal, { props });
    await screen.findByText("Setting up workspace…");
    await waitFor(() => expect(notify).toBeTypeOf("function"));
    notify?.({ type: "reconnect.stale", payload: {} });
    await waitFor(() => expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledTimes(2));
    notify?.({ type: "workspace_status", payload: { id: "ws-a", status: "ready" } });
    await waitFor(() => expect(mocks.runtimeClient.launchWorkspaceRuntimeSession).toHaveBeenCalledOnce());
    expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce();
  });

  it("launches without another setup poll when older details arrive after readiness", async () => {
    const reconnectDetail = Promise.withResolvers<typeof workspace>();
    const admission = Promise.withResolvers<typeof runtime>();
    let notify: ((event: WorkspaceEventsNotification) => void) | undefined;
    events.subscribeWorkspaceEvents.mockImplementation((subscriber: typeof notify) => {
      notify = subscriber;
      return () => {};
    });
    mocks.runtimeClient.getWorkspace
      .mockResolvedValueOnce({ ...workspace, status: "creating" })
      .mockReturnValueOnce(reconnectDetail.promise)
      .mockResolvedValue({ ...workspace, git_head_ref: "feature/fresh-response" });
    mocks.runtimeClient.getWorkspaceRuntime.mockReturnValueOnce(admission.promise).mockResolvedValue(runtime);
    mocks.runtimeClient.launchWorkspaceRuntimeSession.mockReturnValue(new Promise(() => {}));
    queueWorkspaceLaunch("ws-a", "helper", undefined);
    render(MobileWorkspaceTerminal, { props });
    await screen.findByText("Setting up workspace…");
    await waitFor(() => expect(notify).toBeTypeOf("function"));
    notify?.({ type: "reconnect.stale", payload: {} });
    await waitFor(() => expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledTimes(2));
    notify?.({ type: "workspace_status", payload: { id: "ws-a", status: "ready" } });
    await waitFor(() => expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledTimes(3));
    await screen.findByText(/feature\/fresh-response/);
    await waitFor(() => expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce());
    reconnectDetail.resolve({ ...workspace, status: "creating" });
    await new Promise((resolve) => setTimeout(resolve, 0));
    admission.resolve(runtime);
    await waitFor(() => expect(mocks.runtimeClient.launchWorkspaceRuntimeSession).toHaveBeenCalledOnce());
    expect(screen.queryByText("Setting up workspace…")).toBeNull();
    expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledTimes(3);
  });

  it("rechecks pending enrichment after initial loading without rereading runtime", async () => {
    const initial = Promise.withResolvers<typeof workspace>();
    events.subscribeWorkspaceEvents.mockImplementation((subscriber: (event: WorkspaceEventsNotification) => void) => {
      queueMicrotask(() => subscriber({ type: "open" }));
      return () => {};
    });
    mocks.runtimeClient.getWorkspace.mockReturnValueOnce(initial.promise).mockResolvedValue(workspace);
    render(MobileWorkspaceTerminal, { props });
    await waitFor(() => expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce());
    expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledOnce();
    initial.resolve({ ...workspace, enrichment_status: "pending" });
    await waitFor(() => expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledTimes(2));
    expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce();
  });

  it("polls only details during setup and discovers runtime immediately when setup becomes ready", async () => {
    const timeouts = vi.spyOn(globalThis, "setTimeout");
    mocks.runtimeClient.getWorkspace.mockResolvedValue({ ...workspace, status: "creating" });
    render(MobileWorkspaceTerminal, { props });
    await screen.findByText("Setting up workspace…");
    await waitFor(() => expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce());
    await waitFor(() => expect(timeouts.mock.calls.some(([, delay]) => delay === 5000)).toBe(true));
    const poll = timeouts.mock.calls.find(([, delay]) => delay === 5000)?.[0];
    if (typeof poll !== "function") throw new Error("missing readiness poll");
    poll();
    await waitFor(() => expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledTimes(2));
    expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce();
    mocks.runtimeClient.getWorkspace.mockResolvedValue(workspace);
    await waitFor(() => expect(timeouts.mock.calls.filter(([, delay]) => delay === 5000)).toHaveLength(2));
    const nextPoll = timeouts.mock.calls.filter(([, delay]) => delay === 5000)[1]?.[0];
    if (typeof nextPoll !== "function") throw new Error("missing second readiness poll");
    nextPoll();
    await waitFor(() => expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("button", { name: "Terminal options" }).hasAttribute("disabled")).toBe(false);
    timeouts.mockRestore();
  });

  it("uses one fresh queued-admission read after ready events while detail refresh is held", async () => {
    const detail = Promise.withResolvers<typeof workspace>();
    const admission = Promise.withResolvers<typeof runtime>();
    let notify: ((event: WorkspaceEventsNotification) => void) | undefined;
    events.subscribeWorkspaceEvents.mockImplementation((subscriber: typeof notify) => {
      notify = subscriber;
      return () => {};
    });
    mocks.runtimeClient.getWorkspace
      .mockResolvedValueOnce({ ...workspace, status: "creating" })
      .mockReturnValue(detail.promise);
    mocks.runtimeClient.getWorkspaceRuntime
      .mockReturnValueOnce(admission.promise)
      .mockReturnValue(new Promise(() => {}));
    mocks.runtimeClient.launchWorkspaceRuntimeSession.mockReturnValue(new Promise(() => {}));
    queueWorkspaceLaunch("ws-a", "helper", undefined);
    render(MobileWorkspaceTerminal, { props });
    await screen.findByText("Setting up workspace…");
    expect(mocks.runtimeClient.launchWorkspaceRuntimeSession).not.toHaveBeenCalled();
    await waitFor(() => expect(notify).toBeTypeOf("function"));
    notify?.({ type: "workspace_status", payload: { id: "ws-a", status: "ready" } });
    await waitFor(() => expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce());
    admission.resolve(runtime);
    await waitFor(() => expect(mocks.runtimeClient.launchWorkspaceRuntimeSession).toHaveBeenCalledOnce());
    expect(mocks.runtimeClient.launchWorkspaceRuntimeSession).toHaveBeenCalledWith(
      { id: "ws-a" },
      { target_key: "helper", display_region: "workflow" },
      expect.anything(),
    );
    expect(mocks.runtimeClient.getWorkspaceRuntime).toHaveBeenCalledOnce();
  });

  it("waits for the owning Fleet host's readiness before queued launch", async () => {
    const detail = Promise.withResolvers<typeof workspace>();
    let notify: ((event: WorkspaceEventsNotification) => void) | undefined;
    events.subscribeWorkspaceEvents.mockImplementation((subscriber: typeof notify) => {
      notify = subscriber;
      return () => {};
    });
    mocks.runtimeClient.getFleetWorkspace
      .mockResolvedValueOnce({ ...workspace, status: "creating" })
      .mockReturnValue(detail.promise);
    mocks.runtimeClient.launchFleetWorkspaceRuntimeSession.mockReturnValue(new Promise(() => {}));
    markWorkspaceIdDeleted("ws-a");
    queueWorkspaceLaunch("ws-a", "helper", "peer-one");
    render(MobileWorkspaceTerminal, { props: { ...props, hostKey: "peer-one" } });
    await screen.findByText("Setting up workspace…");
    await waitFor(() => expect(notify).toBeTypeOf("function"));
    notify?.({ type: "workspace_status", payload: { id: "ws-a", status: "ready" } });
    await waitFor(() => expect(mocks.runtimeClient.getFleetWorkspace).toHaveBeenCalledTimes(2));
    expect(mocks.runtimeClient.launchFleetWorkspaceRuntimeSession).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Terminal options" }).hasAttribute("disabled")).toBe(true);
    detail.resolve(workspace);
    await waitFor(() => expect(mocks.runtimeClient.launchFleetWorkspaceRuntimeSession).toHaveBeenCalledOnce());
    expect(mocks.runtimeClient.getFleetWorkspaceRuntime).toHaveBeenCalledOnce();
    expect(mocks.runtimeClient.launchFleetWorkspaceRuntimeSession).toHaveBeenCalledWith(
      { id: "ws-a", hostKey: "peer-one" },
      { target_key: "helper", display_region: "workflow" },
      expect.anything(),
    );
  });

  it.each(["deleting", "deletion_failed"])("does not replace %s with a late ready event", async (status) => {
    let notify: ((event: WorkspaceEventsNotification) => void) | undefined;
    events.subscribeWorkspaceEvents.mockImplementation((subscriber: typeof notify) => {
      notify = subscriber;
      return () => {};
    });
    mocks.runtimeClient.getWorkspace
      .mockResolvedValueOnce({ ...workspace, status })
      .mockReturnValue(new Promise(() => {}));
    queueWorkspaceLaunch("ws-a", "helper", undefined);
    render(MobileWorkspaceTerminal, { props });
    await screen.findByText(status === "deleting" ? "Deleting workspace…" : "Workspace deletion failed");
    await waitFor(() => expect(notify).toBeTypeOf("function"));
    notify?.({ type: "workspace_status", payload: { id: "ws-a", status: "ready" } });
    await waitFor(() => expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledTimes(2));
    expect(mocks.runtimeClient.launchWorkspaceRuntimeSession).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Terminal options" }).hasAttribute("disabled")).toBe(true);
  });

  it("rechecks runtime for queued intent after returning to the same workspace", async () => {
    const view = render(MobileWorkspaceTerminal, { props });
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Terminal options" }).hasAttribute("disabled")).toBe(false),
    );
    const held = Promise.withResolvers<typeof runtime>();
    mocks.runtimeClient.getWorkspace.mockResolvedValue({ ...workspace, id: "ws-b" });
    await view.rerender({ ...props, workspaceId: "ws-b" });
    await waitFor(() =>
      expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledWith({ id: "ws-b" }, expect.anything()),
    );
    mocks.runtimeClient.getWorkspace.mockResolvedValue(workspace);
    mocks.runtimeClient.getWorkspaceRuntime.mockReturnValue(held.promise);
    mocks.runtimeClient.launchWorkspaceRuntimeSession.mockReturnValue(new Promise(() => {}));
    queueWorkspaceLaunch("ws-a", "helper", undefined);
    await view.rerender(props);
    await waitFor(() => expect(mocks.runtimeClient.getWorkspace).toHaveBeenCalledTimes(3));
    expect(mocks.runtimeClient.launchWorkspaceRuntimeSession).not.toHaveBeenCalled();
    held.resolve(runtime);
    await waitFor(() => expect(mocks.runtimeClient.launchWorkspaceRuntimeSession).toHaveBeenCalledOnce());
  });

  it("returns to the workspace list without treating a lookup miss as deletion", async () => {
    mocks.runtimeClient.getWorkspace.mockRejectedValue(
      new GeneratedProblemResponse(
        {
          type: "about:blank",
          title: "Not found",
          status: 404,
          detail: "workspace not found",
          code: "workspaceNotFound",
        },
        new Response(null, { status: 404 }),
      ),
    );
    const controller = getInlineWorkspaceController("prs");
    controller.claim(identity, workspaceRef);
    const onMissing = vi.fn();

    render(MobileWorkspaceTerminal, {
      props: {
        workspaceId: "ws-a",
        visible: false,
        onBack: vi.fn(),
        onMissing,
        onOpenItem: vi.fn(),
      },
    });

    await waitFor(() => expect(onMissing).toHaveBeenCalledOnce());
    expect(controller.effectiveWorkspaceRef(identity, workspaceRef)).toEqual(workspaceRef);
  });
});
