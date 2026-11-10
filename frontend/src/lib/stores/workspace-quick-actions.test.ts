import { tick } from "svelte";
import { getFlashes, dismissFlash } from "./flash.svelte.js";
import { GeneratedProblemResponse } from "../api/runtime.js";
import type { VisitEvent } from "@kenn-io/kit-ui/utils/browser-telemetry";
import { startTestTelemetry } from "../testing/telemetry.js";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import type { RuntimeSession } from "../api/types.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import {
  quickActionLaunches,
  quickActionWorkspaces,
  quickActionWorkspaceKey,
  runReturnedWorkspaceQuickAction,
  runWorkspaceQuickAction,
} from "./workspace-quick-actions.js";

const session: RuntimeSession = {
  key: "chat-newer",
  workspace_id: "ws-a",
  target_key: "chat",
  label: "Chat agent",
  kind: "acp",
  display_region: "workflow",
  status: "running",
  created_at: "2026-09-26T00:00:00Z",
};

describe("quick-action launch serialization", () => {
  let runtime: OwnedAppRuntime;
  const launchHandoff = vi.fn();
  const launchDevboxHandoff = vi.fn();
  const launchFleetHandoff = vi.fn();
  const retryFleetWorkspace = vi.fn();
  const telemetry = vi.fn(async (_event: VisitEvent, _options?: unknown) => ({ status: "queued" }));

  beforeEach(async () => {
    quickActionLaunches.clear();
    quickActionWorkspaces.clear();
    launchHandoff.mockReset();
    launchDevboxHandoff.mockReset();
    launchFleetHandoff.mockReset();
    retryFleetWorkspace.mockReset();
    telemetry.mockClear();
    runtime = makeAppRuntime(
      makeGeneratedClient({
        WorkspacesService: { launchWorkspaceAgentHandoff: launchHandoff },
        DevboxesService: { launchDevboxHandoff },
        FleetService: { launchFleetWorkspaceAgentHandoff: launchFleetHandoff, retryFleetWorkspace },
      }),
    );
    await startTestTelemetry(runtime, telemetry).ready;
  });

  afterEach(async () => {
    dismissFlash();
    await Effect.runPromise(runtime.disposeEffect);
    quickActionLaunches.clear();
    quickActionWorkspaces.clear();
  });

  it("queues another action for the same workspace until the previous launch finishes", async () => {
    const handoff = Promise.withResolvers<{ session: RuntimeSession; initial_message: { state: string } }>();
    launchHandoff
      .mockReturnValueOnce(handoff.promise)
      .mockResolvedValue({ session: { ...session, key: "helper-1" }, initial_message: { state: "delivered" } });
    const acknowledged = vi.fn();
    const firstAction = { label: "Review", agent: "chat", prompt: "Review this change" };
    const secondAction = { label: "Fix", agent: "helper", prompt: "Fix this change" };
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", firstAction, undefined, acknowledged);
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", secondAction);
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledOnce());
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(launchHandoff).toHaveBeenCalledOnce();

    handoff.resolve({ session, initial_message: { state: "delivered" } });
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledTimes(2));
    expect(acknowledged).toHaveBeenCalledWith(session);
    expect(launchHandoff.mock.calls.map(([, body]) => body)).toEqual([
      { target_key: "chat", message: "Review this change" },
      { target_key: "helper", message: "Fix this change" },
    ]);
    await vi.waitFor(() => expect(quickActionLaunches.get("ws-a")?.session?.key).toBe("helper-1"));

    // A finished launch never blocks a later one.
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", firstAction);
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledTimes(3));
  });

  it("drops launch failures shown while the document is hidden", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    launchHandoff.mockRejectedValue(
      new GeneratedProblemResponse(
        { code: "forbidden", title: "Launch denied", status: 403 },
        new Response(null, { status: 403 }),
      ),
    );
    try {
      runWorkspaceQuickAction(runtime, "ws-a", "pull_request", { label: "Review", agent: "chat", prompt: "Review" });
      await vi.waitFor(() => expect(getFlashes().some((flash) => flash.message.includes("Launch denied"))).toBe(true));
      expect(telemetry.mock.calls.filter(([event]) => event.event === "error_shown")).toEqual([]);
      visibility.mockReturnValue("visible");
      document.dispatchEvent(new Event("visibilitychange"));
      await tick();
      expect(telemetry.mock.calls.filter(([event]) => event.event === "error_shown")).toEqual([]);
    } finally {
      visibility.mockRestore();
    }
  });

  it("reports each rejected quick-action launch as a failed workspace open", async () => {
    launchHandoff.mockRejectedValue(
      new GeneratedProblemResponse(
        { code: "forbidden", title: "Launch denied", status: 403 },
        new Response(null, { status: 403 }),
      ),
    );
    const action = { label: "Review", agent: "chat", prompt: "Review this change" };
    for (let attempt = 1; attempt <= 2; attempt++) {
      runWorkspaceQuickAction(runtime, "ws-a", "pull_request", action);
      await vi.waitFor(() =>
        expect(telemetry.mock.calls.filter(([event]) => event.event === "error_shown")).toHaveLength(attempt),
      );
    }
    expect(telemetry).toHaveBeenCalledWith(
      { event: "error_shown", properties: { surface: "web", kind: "workspace_opened" } },
      {},
    );
  });

  it("reports a lost link to another machine as unconfirmed, and the same error here as a failure", async () => {
    const relayError = () =>
      new GeneratedProblemResponse(
        { code: "upstreamError", title: "Bad Gateway", status: 502, detail: "fleet host connection closed" },
        new Response(null, { status: 502 }),
      );
    launchFleetHandoff.mockRejectedValue(relayError());
    launchHandoff.mockRejectedValue(relayError());
    const action = { label: "Review", agent: "chat", prompt: "Review this change" };

    runWorkspaceQuickAction(runtime, "ws-remote", "pull_request", action, "b".repeat(32));
    await vi.waitFor(() => expect(getFlashes()).toHaveLength(1));
    expect(getFlashes()[0]).toMatchObject({
      tone: "warning",
      message: '"Review": lost contact with the workspace\'s machine; check the workspace for the agent.',
    });
    dismissFlash();

    runWorkspaceQuickAction(runtime, "ws-local", "pull_request", action);
    await vi.waitFor(() => expect(getFlashes()).toHaveLength(1));
    expect(getFlashes()[0]).toMatchObject({
      tone: "danger",
      message: '"Review" could not start: fleet host connection closed',
    });
  });

  it("sets up a failed workspace on another machine again before running the action there", async () => {
    const hostKey = "b".repeat(32);
    retryFleetWorkspace.mockResolvedValue({ id: "ws-failed", status: "provisioning" });
    launchFleetHandoff.mockResolvedValue({ session, initial_message: { state: "delivered" } });
    const action = { label: "Review", agent: "chat", prompt: "Review this change" };

    runReturnedWorkspaceQuickAction(runtime, { id: "ws-failed", status: "error" }, "pull_request", action, hostKey);
    await vi.waitFor(() => expect(launchFleetHandoff).toHaveBeenCalledOnce());
    expect(retryFleetWorkspace).toHaveBeenCalledExactlyOnceWith({ hostKey, id: "ws-failed" }, expect.anything());
    expect(retryFleetWorkspace.mock.invocationCallOrder[0]).toBeLessThan(
      launchFleetHandoff.mock.invocationCallOrder[0]!,
    );

    runReturnedWorkspaceQuickAction(runtime, { id: "ws-ready", status: "ready" }, "pull_request", action, hostKey);
    await vi.waitFor(() => expect(launchFleetHandoff).toHaveBeenCalledTimes(2));
    expect(retryFleetWorkspace).toHaveBeenCalledOnce();
  });

  it("refuses a workspace on another machine that is being deleted without asking it to launch", async () => {
    const hostKey = "b".repeat(32);
    const action = { label: "Review", agent: "chat", prompt: "Review this change" };

    for (const status of ["deleting", "deletion_failed"]) {
      runReturnedWorkspaceQuickAction(runtime, { id: "ws-going", status }, "pull_request", action, hostKey);
      await vi.waitFor(() => expect(getFlashes()).toHaveLength(1));
      expect(getFlashes()[0]).toMatchObject({
        tone: "danger",
        message: '"Review" could not start: its workspace is being deleted',
      });
      dismissFlash();
    }
    expect(launchFleetHandoff).not.toHaveBeenCalled();
    expect(retryFleetWorkspace).not.toHaveBeenCalled();
  });

  it("allows another action after the previous request fails", async () => {
    const handoff = Promise.withResolvers<{ session: RuntimeSession; initial_message: { state: string } }>();
    launchHandoff.mockReturnValueOnce(handoff.promise).mockReturnValue(new Promise(() => {}));
    const commands = vi.spyOn(runtime, "runCommand");
    const action = { label: "Review", agent: "chat", prompt: "Review this change" };
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", action);
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledOnce());
    const execution = commands.mock.results.at(-1)?.value;
    if (!execution) throw new Error("Missing command execution");
    handoff.reject(new Error("Handoff unavailable"));
    await execution.exit;
    expect(quickActionLaunches.has("ws-a")).toBe(false);
    expect(quickActionWorkspaces.has("ws-a")).toBe(true);
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", action);
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledTimes(2));
  });

  it("sends an action's ACP options with its handoff on every host", async () => {
    launchHandoff.mockReturnValue(new Promise(() => {}));
    launchDevboxHandoff.mockReturnValue(new Promise(() => {}));
    const action = {
      label: "Review",
      agent: "chat",
      prompt: "Review this change",
      acp_options: { model: "deep", effort: "high" },
    };
    runWorkspaceQuickAction(runtime, "ws-a", undefined, action);
    runWorkspaceQuickAction(runtime, "ws-a", undefined, action, "devbox:peer-a");
    const body = { target_key: "chat", message: "Review this change", acp_options: { model: "deep", effort: "high" } };
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledWith({ id: "ws-a" }, body, expect.anything()));
    await vi.waitFor(() =>
      expect(launchDevboxHandoff).toHaveBeenCalledWith({ connectionId: "peer-a", id: "ws-a" }, body, expect.anything()),
    );
  });

  it.each([undefined, "pull_request"] as const)(
    "launches independent workspaces and hosts with item type %s",
    async (itemType) => {
      launchHandoff.mockReturnValue(new Promise(() => {}));
      launchDevboxHandoff.mockReturnValue(new Promise(() => {}));
      const action = { label: "Review", agent: "chat", prompt: "Review this change" };
      runWorkspaceQuickAction(runtime, "ws-a", itemType, action);
      runWorkspaceQuickAction(runtime, "ws-b", itemType, action);
      runWorkspaceQuickAction(runtime, "ws-a", itemType, action, "devbox:peer-a");
      await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledTimes(2));
      await vi.waitFor(() => expect(launchDevboxHandoff).toHaveBeenCalledOnce());
      if (itemType === undefined) {
        expect(telemetry).not.toHaveBeenCalled();
      } else {
        await vi.waitFor(() =>
          expect(telemetry).toHaveBeenCalledWith(
            { event: "workspace_opened", properties: { surface: "web", item_type: "pull_request" } },
            expect.anything(),
          ),
        );
      }
      expect(quickActionLaunches.has("ws-a")).toBe(true);
      expect(quickActionLaunches.has("ws-b")).toBe(true);
      expect(quickActionLaunches.has(quickActionWorkspaceKey("ws-a", "devbox:peer-a"))).toBe(true);
    },
  );
});
