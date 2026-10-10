import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import type { RuntimeSession } from "../api/types.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import { quickActionLaunches, quickActionWorkspaces, runWorkspaceQuickAction } from "./workspace-quick-actions.js";

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

describe("quick-action launch ownership", () => {
  let runtime: OwnedAppRuntime;
  const launchHandoff = vi.fn();

  beforeEach(() => {
    quickActionLaunches.clear();
    quickActionWorkspaces.clear();
    launchHandoff.mockReset();
    runtime = makeAppRuntime(
      makeGeneratedClient({
        WorkspacesService: { launchWorkspaceAgentHandoff: launchHandoff },
      }),
    );
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
    quickActionLaunches.clear();
    quickActionWorkspaces.clear();
  });

  it.each(["success", "failure"])("keeps a newer launch when an older request returns %s", async (outcome) => {
    const older = Promise.withResolvers<{ session: RuntimeSession; initial_message: { state: string } }>();
    const newer = Promise.withResolvers<{ session: RuntimeSession; initial_message: { state: string } }>();
    launchHandoff.mockReturnValueOnce(older.promise).mockReturnValueOnce(newer.promise);
    const commands = vi.spyOn(runtime, "runCommand");
    const olderAcknowledged = vi.fn();
    const newerAcknowledged = vi.fn();
    runWorkspaceQuickAction(
      runtime,
      "ws-a",
      { label: "Old action", agent: "old", prompt: "Old request" },
      undefined,
      olderAcknowledged,
    );
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledOnce());
    runWorkspaceQuickAction(
      runtime,
      "ws-a",
      { label: "New action", agent: "chat", prompt: "New request" },
      undefined,
      newerAcknowledged,
    );
    await vi.waitFor(() => expect(launchHandoff).toHaveBeenCalledTimes(2));
    newer.resolve({ session, initial_message: { state: "delivered" } });
    await vi.waitFor(() => expect(newerAcknowledged).toHaveBeenCalledWith(session));
    const olderExecution = commands.mock.results[0]?.value;
    if (!olderExecution) throw new Error("Missing older command execution");
    const currentLaunch = quickActionLaunches.get("ws-a");
    const olderSession = { ...session, key: "chat-older", target_key: "old" };
    if (outcome === "success") {
      older.resolve({ session: olderSession, initial_message: { state: "delivered" } });
      await vi.waitFor(() => expect(olderAcknowledged).toHaveBeenCalledWith(olderSession));
    } else {
      older.reject(new Error("Handoff unavailable"));
      await olderExecution.exit;
      expect(olderAcknowledged).not.toHaveBeenCalled();
    }
    expect(quickActionLaunches.get("ws-a")).toBe(currentLaunch);
    expect(currentLaunch?.session).toEqual(session);
  });
});
