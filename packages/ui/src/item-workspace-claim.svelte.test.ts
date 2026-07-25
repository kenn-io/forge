import { flushSync } from "svelte";
import { describe, expect, it, vi } from "vitest";
import { useItemWorkspaceClaim } from "./item-workspace-claim.svelte";
import type { InlineWorkspaceController, WorkspaceItemIdentity, WorkspaceRefLite } from "./workspace-inline";

function identity(number = 7, itemType = "pull"): WorkspaceItemIdentity {
  return {
    provider: "github",
    platformHost: "github.com",
    owner: "acme",
    name: "widgets",
    repoPath: "acme/widgets",
    number,
    itemType,
  };
}

const REF: WorkspaceRefLite = { id: "ws-1", status: "running" };

function fakeController(): {
  controller: InlineWorkspaceController;
  claim: ReturnType<typeof vi.fn>;
  release: ReturnType<typeof vi.fn>;
  invalidate: (id: WorkspaceItemIdentity) => void;
} {
  const claim = vi.fn();
  const release = vi.fn();
  const listeners = new Set<(id: WorkspaceItemIdentity) => void>();
  const controller = {
    surface: "prs",
    effectiveWorkspaceRef: (_id: WorkspaceItemIdentity, envelope: WorkspaceRefLite | null | undefined) =>
      envelope ?? null,
    claim,
    release,
    isClaimedFor: () => false,
    recordCreated: vi.fn(),
    recordDeleted: vi.fn(),
    reconcile: vi.fn(),
    getDockMode: () => "split" as const,
    setDockMode: vi.fn(),
    focusTerminal: vi.fn(),
    openInWorkspaces: vi.fn(),
    onIdentityInvalidated: (cb: (id: WorkspaceItemIdentity) => void) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    slotAttachment: () => undefined,
  } as unknown as InlineWorkspaceController;
  return {
    controller,
    claim,
    release,
    invalidate: (id) => {
      for (const cb of listeners) cb(id);
    },
  };
}

describe("item workspace claim lifecycle", () => {
  it("claims while the loaded detail matches the selection", () => {
    const { controller, claim } = fakeController();
    const cleanup = $effect.root(() => {
      useItemWorkspaceClaim({
        controller: () => controller,
        identity: () => identity(),
        detailMatches: () => true,
        envelopeRef: () => REF,
        refresh: () => {},
      });
    });
    flushSync();

    expect(claim).toHaveBeenCalledWith(identity(), REF);
    cleanup();
  });

  it("releases rather than claiming when the loaded detail is for another item", () => {
    // A stale envelope would otherwise attach another item's workspace.
    const { controller, claim, release } = fakeController();
    const cleanup = $effect.root(() => {
      useItemWorkspaceClaim({
        controller: () => controller,
        identity: () => identity(),
        detailMatches: () => false,
        envelopeRef: () => REF,
        refresh: () => {},
      });
    });
    flushSync();

    expect(claim).not.toHaveBeenCalled();
    expect(release).toHaveBeenCalled();
    cleanup();
  });

  it("releases when the detail carries no workspace", () => {
    const { controller, claim, release } = fakeController();
    const cleanup = $effect.root(() => {
      useItemWorkspaceClaim({
        controller: () => controller,
        identity: () => identity(),
        detailMatches: () => true,
        envelopeRef: () => null,
        refresh: () => {},
      });
    });
    flushSync();

    expect(claim).not.toHaveBeenCalled();
    expect(release).toHaveBeenCalled();
    cleanup();
  });

  it("releases on teardown so a claim never outlives its view", () => {
    const { controller, release } = fakeController();
    const cleanup = $effect.root(() => {
      useItemWorkspaceClaim({
        controller: () => controller,
        identity: () => identity(),
        detailMatches: () => true,
        envelopeRef: () => REF,
        refresh: () => {},
      });
    });
    flushSync();
    release.mockClear();

    cleanup();
    expect(release).toHaveBeenCalled();
  });

  it("refreshes only when the invalidated identity is the current one", () => {
    const { controller, invalidate } = fakeController();
    const refresh = vi.fn();
    const cleanup = $effect.root(() => {
      useItemWorkspaceClaim({
        controller: () => controller,
        identity: () => identity(7, "pull"),
        detailMatches: () => true,
        envelopeRef: () => REF,
        refresh,
      });
    });
    flushSync();

    invalidate(identity(8, "pull"));
    expect(refresh).not.toHaveBeenCalled();

    // Same repo and number but an issue, not a pull: a different item that
    // happens to share both.
    invalidate(identity(7, "issue"));
    expect(refresh).not.toHaveBeenCalled();

    invalidate(identity(7, "pull"));
    expect(refresh).toHaveBeenCalledTimes(1);
    cleanup();
  });

  it("does nothing at all without a controller", () => {
    const cleanup = $effect.root(() => {
      useItemWorkspaceClaim({
        controller: () => null,
        identity: () => identity(),
        detailMatches: () => true,
        envelopeRef: () => REF,
        refresh: () => {},
      });
    });
    expect(() => flushSync()).not.toThrow();
    cleanup();
  });
});
