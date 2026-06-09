import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { STORES_KEY } from "../../context.js";
import { createDiffStore } from "../../stores/diff.svelte.js";
import type { StoreInstances } from "../../types.js";
import WorkspaceRightSidebar from "./WorkspaceRightSidebar.svelte";

function makeStores(): Pick<StoreInstances, "diff"> & Partial<StoreInstances> {
  return {
    diff: createDiffStore(),
    roborevDaemon: {
      isAvailable: () => false,
    } as StoreInstances["roborevDaemon"],
  };
}

function renderSidebar(refreshToken = 0) {
  return render(WorkspaceRightSidebar, {
    props: {
      activeTab: "diff",
      workspaceID: "ws-1",
      provider: "github",
      platformHost: "github.com",
      repoOwner: "acme",
      repoName: "widgets",
      repoPath: "acme/widgets",
      ownerItemType: "pull_request",
      ownerItemNumber: 7,
      associatedPRNumber: 7,
      branch: "feature/widgets",
      roborevBaseUrl: "http://localhost/api/roborev",
      refreshToken,
    },
    context: new Map([[STORES_KEY, makeStores()]]),
  });
}

describe("WorkspaceRightSidebar", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("preserves the workspace diff base and selected commit across refreshes", async () => {
    const calls: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      calls.push(url);

      if (url.includes("/api/roborev/api/repos")) {
        return Response.json({ repos: [] });
      }
      if (url.includes("/api/v1/workspaces/ws-1/commits")) {
        return Response.json({
          commits: [
            {
              sha: "sha2",
              message: "second commit",
              author_name: "Alice",
              authored_at: "2026-01-01T00:00:00Z",
            },
            {
              sha: "sha1",
              message: "first commit",
              author_name: "Alice",
              authored_at: "2026-01-01T00:00:00Z",
            },
          ],
        });
      }
      if (url.includes("/api/v1/workspaces/ws-1/files")) {
        return Response.json({
          stale: false,
          whitespace_only_count: 0,
          files: [],
        });
      }
      if (url.includes("/api/v1/workspaces/ws-1/diff")) {
        return Response.json({
          stale: false,
          whitespace_only_count: 0,
          files: [],
        });
      }
      return Response.json({}, { status: 404 });
    });

    const { rerender } = renderSidebar();

    await waitFor(() => {
      expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/diff?base=head"))).toBe(true);
    });

    await fireEvent.click(screen.getByRole("button", { name: "Compare with merge target" }));
    await waitFor(() => {
      expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/diff?base=merge-target"))).toBe(true);
    });

    await fireEvent.click(screen.getByRole("button", { name: /Select commit range/ }));
    await fireEvent.click(await screen.findByRole("button", { name: /second commit/ }));
    await waitFor(() => {
      expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/diff?base=merge-target&commit=sha2"))).toBe(
        true,
      );
    });

    calls.length = 0;
    await rerender({ refreshToken: 1 });

    await waitFor(() => {
      expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/diff?base=merge-target&commit=sha2"))).toBe(
        true,
      );
    });
    expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/commits"))).toBe(true);
    expect(screen.getByRole("button", { name: "Compare with merge target" }).getAttribute("aria-pressed")).toBe("true");
    expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/diff?base=head"))).toBe(false);
  });
});
