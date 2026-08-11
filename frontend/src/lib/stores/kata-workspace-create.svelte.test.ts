import { beforeEach, describe, expect, it } from "vite-plus/test";

import { markWorkspaceIdDeleted, resetWorkspaceCreatePendingForTest } from "./workspace-create-pending.svelte.js";
import {
  beginKataWorkspaceCreate,
  createdKataWorkspaceRef,
  reconcileKataWorkspaceCreated,
  recordKataWorkspaceCreated,
  resetKataWorkspaceCreateForTest,
  resolveKataWorkspaceRef,
} from "./kata-workspace-create.svelte.js";

const identity = { daemonID: "daemon-a", issueUID: "issue-a" };
const created = { id: "workspace-a", status: "provisioning" };

describe("Kata workspace creation state", () => {
  beforeEach(() => {
    resetKataWorkspaceCreateForTest();
    resetWorkspaceCreatePendingForTest();
  });

  it("keeps a confirmed creation over a stale pre-create association response", () => {
    beginKataWorkspaceCreate(identity);
    const staleResponseTick = 1;
    recordKataWorkspaceCreated(identity, created);

    reconcileKataWorkspaceCreated(identity, null, staleResponseTick);

    expect(createdKataWorkspaceRef(identity)).toEqual(created);
    expect(resolveKataWorkspaceRef(identity, { id: "workspace-old", status: "ready" })).toEqual(created);
  });

  it("reconciles a confirmed creation against a fresh authoritative response", () => {
    recordKataWorkspaceCreated(identity, created);

    reconcileKataWorkspaceCreated(identity, created, Number.MAX_SAFE_INTEGER);

    expect(createdKataWorkspaceRef(identity)).toBeNull();
    expect(resolveKataWorkspaceRef(identity, { ...created, status: "ready" })).toEqual({
      id: "workspace-a",
      status: "ready",
    });
  });

  it("forgets deleted workspaces and rejects a delayed create response", () => {
    recordKataWorkspaceCreated(identity, created);
    markWorkspaceIdDeleted(created.id);

    expect(createdKataWorkspaceRef(identity)).toBeNull();
    expect(resolveKataWorkspaceRef(identity, created)).toBeNull();
    expect(beginKataWorkspaceCreate(identity)).toBe(true);

    recordKataWorkspaceCreated(identity, created);
    expect(createdKataWorkspaceRef(identity)).toBeNull();
  });
});
