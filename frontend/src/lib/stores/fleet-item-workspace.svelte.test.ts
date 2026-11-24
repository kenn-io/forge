import { Effect } from "effect";
import { flushSync } from "svelte";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vite-plus/test";
import type { GeneratedClient } from "../api/generated-api.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import type { WorkspaceItemIdentity } from "../workspace-inline.js";
import { createFleetItemWorkspace, type FleetItemWorkspaceItem } from "./fleet-item-workspace.svelte.js";
import {
  clearCreatedWorkspaceById,
  discoveredWorkspaceRef,
  resetWorkspaceCreatePendingForTest,
} from "./workspace-create-pending.svelte.js";

afterEach(resetWorkspaceCreatePendingForTest);

const buildB = "b".repeat(32);
const buildC = "c".repeat(32);

const identity: WorkspaceItemIdentity = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widget",
  repoPath: "acme/widget",
  number: 1,
  itemType: "pull",
};

const item: FleetItemWorkspaceItem = {
  kind: "pull",
  repo: { provider: "github", platformHost: "github.com", owner: "acme", name: "widget", repoPath: "acme/widget" },
  number: 1,
};

function workspace(id: string, hostKey: string, status: string) {
  return {
    id,
    status,
    fleet_host_key: hostKey,
    item_type: "pull_request",
    item_number: 1,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widget",
    repo: { provider: "github", platform_host: "github.com", owner: "acme", name: "widget", repo_path: "acme/widget" },
  };
}

function host(configKey: string, name: string, reachable: boolean) {
  return { configKey, name, kind: "remote", reachable };
}

type Snapshot = { hosts: ReturnType<typeof host>[]; workspaces: ReturnType<typeof workspace>[] };

// Runs the lookup the way a detail page does; bump() stands in for a new
// detail load.
function startLookup(get: () => Promise<unknown>) {
  const runtime = makeTestAppRuntime({ GET: vi.fn(get), POST: vi.fn() } as unknown as GeneratedClient);
  let revision = $state(0);
  let lookup!: ReturnType<typeof createFleetItemWorkspace>;
  const cleanup = $effect.root(() => {
    lookup = createFleetItemWorkspace(
      runtime,
      () => item,
      () => identity,
      () => revision,
    );
  });
  onTestFinished(async () => {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
  });
  flushSync();
  return {
    lookup,
    bump: () => {
      revision += 1;
      flushSync();
    },
  };
}

describe("fleet item workspace lookup", () => {
  it("picks a usable workspace over one being deleted on another machine", async () => {
    const snapshot: Snapshot = {
      hosts: [host(buildB, "build-b", true), host(buildC, "build-c", true)],
      workspaces: [workspace("ws-deleting", buildB, "deleting"), workspace("ws-ready", buildC, "ready")],
    };
    startLookup(async () => ({ data: snapshot }));

    await vi.waitFor(() => expect(discoveredWorkspaceRef(identity)?.id).toBe("ws-ready"));
  });

  it("drops a workspace its machine stops listing, and keeps one whose machine did not answer", async () => {
    let snapshot: Snapshot = {
      hosts: [host(buildB, "build-b", true)],
      workspaces: [workspace("ws-remote", buildB, "ready")],
    };
    const { bump } = startLookup(async () => ({ data: snapshot }));
    await vi.waitFor(() => expect(discoveredWorkspaceRef(identity)?.id).toBe("ws-remote"));

    snapshot = { hosts: [host(buildB, "build-b", false)], workspaces: [] };
    bump();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(discoveredWorkspaceRef(identity)?.id).toBe("ws-remote");

    snapshot = { hosts: [host(buildB, "build-b", true)], workspaces: [] };
    bump();
    await vi.waitFor(() => expect(discoveredWorkspaceRef(identity)).toBeNull());
  });

  it("does not bring back a workspace its machine reported gone, but finds its replacement", async () => {
    let snapshot: Snapshot = {
      hosts: [host(buildB, "build-b", true)],
      workspaces: [workspace("ws-old", buildB, "ready")],
    };
    const { bump } = startLookup(async () => ({ data: snapshot }));
    await vi.waitFor(() => expect(discoveredWorkspaceRef(identity)?.id).toBe("ws-old"));

    // The machine answered 404 for the workspace, while the snapshot still lists it.
    clearCreatedWorkspaceById("ws-old", buildB);
    expect(discoveredWorkspaceRef(identity)).toBeNull();
    bump();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(discoveredWorkspaceRef(identity)).toBeNull();

    snapshot = {
      ...snapshot,
      workspaces: [workspace("ws-old", buildB, "ready"), workspace("ws-new", buildB, "ready")],
    };
    bump();
    await vi.waitFor(() => expect(discoveredWorkspaceRef(identity)?.id).toBe("ws-new"));
  });

  it("stays pending only until the first lookup for the item finishes", async () => {
    const first = Promise.withResolvers<unknown>();
    const second = Promise.withResolvers<unknown>();
    const responses = [first.promise, second.promise];
    const { lookup, bump } = startLookup(() => responses.shift()!);

    expect(lookup.pending(item)).toBe(true);
    first.resolve({ data: { hosts: [], workspaces: [] } });
    await vi.waitFor(() => expect(lookup.pending(item)).toBe(false));

    // A later detail load looks again without holding Create Workspace.
    bump();
    expect(lookup.pending(item)).toBe(false);
    second.resolve({ data: { hosts: [], workspaces: [] } });
  });

  it("stops pending when the lookup crashes", async () => {
    const { lookup } = startLookup(async () => undefined);

    await vi.waitFor(() => expect(lookup.pending(item)).toBe(false));
  });
});
