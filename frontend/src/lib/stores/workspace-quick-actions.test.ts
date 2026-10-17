import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import type { RuntimeSession } from "../api/types.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import {
  quickActionLaunches,
  quickActionWorkspaces,
  quickActionWorkspaceKey,
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
  const telemetry = vi.fn(async () => ({ status: "queued" }));

  beforeEach(() => {
    quickActionLaunches.clear();
    quickActionWorkspaces.clear();
    launchHandoff.mockReset();
    launchDevboxHandoff.mockReset();
    telemetry.mockClear();
    runtime = makeAppRuntime(
      makeGeneratedClient({
        WorkspacesService: { launchWorkspaceAgentHandoff: launchHandoff },
        DevboxesService: { launchDevboxHandoff },
        SystemService: { captureTelemetryEvent: telemetry },
      }),
    );
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
    quickActionLaunches.clear();
    quickActionWorkspaces.clear();
  });

  it("blocks another action until the acknowledged session is presented", async () => {
    const handoff = Promise.withResolvers<{ session: RuntimeSession; initial_message: { state: string } }>();
    launchHandoff.mockReturnValue(handoff.promise);
    const acknowledged = vi.fn();
    const firstAction = { label: "Review", agent: "chat", prompt: "Review this change" };
    const secondAction = { label: "Fix", agent: "helper", prompt: "Fix this change" };
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", firstAction, undefined, acknowledged);
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledOnce());
    const pendingLaunch = quickActionLaunches.get("ws-a");
    if (!pendingLaunch) throw new Error("Missing pending launch");
    pendingLaunch.explicitlyMovedSessionKeys = new Set([session.key]);
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", secondAction);
    expect(launchHandoff).toHaveBeenCalledOnce();
    expect(quickActionLaunches.get("ws-a")).toBe(pendingLaunch);

    handoff.resolve({ session, initial_message: { state: "delivered" } });
    await vi.waitFor(() => expect(acknowledged).toHaveBeenCalledWith(session));
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", secondAction);
    expect(launchHandoff).toHaveBeenCalledOnce();
    expect(quickActionLaunches.get("ws-a")?.session).toEqual(session);
    expect(quickActionLaunches.get("ws-a")?.explicitlyMovedSessionKeys?.has(session.key)).toBe(true);

    quickActionLaunches.delete("ws-a");
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", secondAction);
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledTimes(2));
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

  it("allows independent workspaces and hosts to launch concurrently", async () => {
    launchHandoff.mockReturnValue(new Promise(() => {}));
    launchDevboxHandoff.mockReturnValue(new Promise(() => {}));
    const action = { label: "Review", agent: "chat", prompt: "Review this change" };
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", action);
    runWorkspaceQuickAction(runtime, "ws-b", "pull_request", action);
    runWorkspaceQuickAction(runtime, "ws-a", "pull_request", action, "devbox:peer-a");
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledTimes(2));
    await vi.waitFor(() => expect(launchDevboxHandoff).toHaveBeenCalledOnce());
    expect(quickActionLaunches.has("ws-a")).toBe(true);
    expect(quickActionLaunches.has("ws-b")).toBe(true);
    expect(quickActionLaunches.has(quickActionWorkspaceKey("ws-a", "devbox:peer-a"))).toBe(true);
  });
});
