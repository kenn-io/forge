import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { STORES_KEY } from "../../context.js";
import { createDiffStore } from "../../stores/diff.svelte.js";
import { createAppStores } from "../../app-stores.svelte.js";
import { createMockApiFetch } from "../../../test/mockApiFetch.js";
import type { StoreInstances } from "../../types.js";
import WorkspaceRightSidebarTestHarness from "./WorkspaceRightSidebarTestHarness.svelte";
import type { WorkspaceDiffGitState } from "./workspace-diff-default.js";

let runtime: OwnedAppRuntime;

beforeEach(() => {
  runtime = makeAppRuntime();
});

vi.mock("../kata/KataLinksPanel.svelte", async () => ({
  default: (await import("../../views/KataLinksPanelTestDouble.svelte")).default,
}));

function makeStores(): Pick<StoreInstances, "diff"> & Partial<StoreInstances> {
  return {
    diff: createDiffStore({ runtime }),
    roborevDaemon: {
      isAvailable: () => false,
    } as StoreInstances["roborevDaemon"],
  };
}

function renderSidebar(refreshToken = 0, gitState: WorkspaceDiffGitState = {}) {
  const sidebarProps = {
    gitState,
    activeTab: "diff" as "diff" | "reviews",
    workspaceHostKey: undefined as string | undefined,
    workspaceID: "ws-1",
    worktreePath: "/tmp/worktrees/ws-1",
    provider: "github",
    platformHost: "github.com",
    repoOwner: "acme",
    repoName: "widgets",
    repoPath: "acme/widgets",
    ownerItemType: "pull_request" as const,
    ownerItemNumber: 7,
    associatedPRNumber: 7,
    branch: "feature/widgets",
    roborevBaseUrl: "http://localhost/api/roborev",
    refreshToken,
  };
  const rendered = render(WorkspaceRightSidebarTestHarness, {
    props: {
      runtime,
      sidebarProps,
    },
    context: new Map([[STORES_KEY, makeStores()]]),
  });
  return {
    ...rendered,
    rerender: (props: Partial<typeof sidebarProps>) =>
      rendered.rerender({ runtime, sidebarProps: { ...sidebarProps, ...props } }),
  };
}

function renderDisabledSidebar() {
  return render(WorkspaceRightSidebarTestHarness, {
    props: {
      runtime,
      sidebarProps: {
        activeTab: "diff",
        workspaceID: "ws-1",
        worktreePath: "/tmp/worktrees/ws-1",
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
        disabled: true,
      },
    },
    context: new Map([[STORES_KEY, makeStores()]]),
  });
}

function renderKataSidebarWithoutPR() {
  return render(WorkspaceRightSidebarTestHarness, {
    props: {
      runtime,
      sidebarProps: {
        activeTab: "diff",
        workspaceID: "ws-kata-1",
        worktreePath: "/tmp/worktrees/ws-kata-1",
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widgets",
        repoPath: "acme/widgets",
        ownerItemType: "kata_task",
        ownerItemNumber: 0,
        associatedPRNumber: null,
        branch: "kenn-forge/kata/task-123",
        roborevBaseUrl: "http://localhost/api/roborev",
      },
    },
    context: new Map([[STORES_KEY, makeStores()]]),
  });
}

function renderKataLinksSidebar(
  ownerItemType: "pull_request" | "issue" | "kata_task" | "adhoc",
  workspaceHostKey?: string,
) {
  return render(WorkspaceRightSidebarTestHarness, {
    props: {
      runtime,
      sidebarProps: {
        activeTab: "kata",
        workspaceID: "ws-linked-1",
        worktreePath: "/tmp/worktrees/ws-linked-1",
        workspaceHostKey,
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widgets",
        repoPath: "acme/widgets",
        ownerItemType,
        ownerItemNumber: ownerItemType === "adhoc" ? 0 : 7,
        associatedPRNumber: null,
        branch: "feature/widgets",
        roborevBaseUrl: "http://localhost/api/roborev",
      },
    },
    context: new Map([[STORES_KEY, makeStores()]]),
  });
}

function mockEmptyWorkspaceDiff(): string[] {
  const calls: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    calls.push(url);
    if (url.includes("/api/roborev/api/repos")) {
      return Response.json({ repos: [] });
    }
    if (url.includes("/api/v1/workspaces/ws-1/commits")) {
      return Response.json({ commits: [] });
    }
    if (url.includes("/api/v1/workspaces/ws-1/files") || url.includes("/api/v1/workspaces/ws-1/diff")) {
      return Response.json({ stale: false, whitespace_only_count: 0, files: [] });
    }
    return Response.json({}, { status: 404 });
  });
  return calls;
}

describe("WorkspaceRightSidebar", () => {
  afterEach(async () => {
    cleanup();
    vi.restoreAllMocks();
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("displays a selected PR without changing the workspace's linked PR", async () => {
    const api = createMockApiFetch();
    vi.spyOn(globalThis, "fetch").mockImplementation(api.fetch);
    const { stores } = createAppStores({ runtime });
    const sidebarProps = {
      activeTab: "pr" as const,
      workspaceID: "ws-1",
      worktreePath: "/tmp/worktrees/ws-1",
      provider: "github",
      platformHost: "github.com",
      repoOwner: "acme",
      repoName: "widgets",
      repoPath: "acme/widgets",
      ownerItemType: "pull_request" as const,
      ownerItemNumber: 42,
      associatedPRNumber: 42,
      viewedPR: {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
        number: 55,
      },
      branch: "feature/widgets",
      roborevBaseUrl: "/api/roborev",
    };
    const view = render(WorkspaceRightSidebarTestHarness, {
      props: { runtime, sidebarProps },
      context: new Map([[STORES_KEY, stores]]),
    });
    await screen.findByRole("heading", { name: "Refactor theme system" });
    await view.rerender({ runtime, sidebarProps: { ...sidebarProps, viewedPR: null } });
    await screen.findByRole("heading", { name: "Add browser regression coverage" });
    expect(api.requests.filter(({ method }) => method === "PATCH" || method === "PUT")).toEqual([]);
  });

  it.each(["pull_request", "issue", "kata_task", "adhoc"] as const)(
    "shows Forge-owned Kata links for a %s workspace",
    (ownerItemType) => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue(Response.json({ repos: [] }));
      renderKataLinksSidebar(ownerItemType);

      const panel = screen.getByTestId("kata-links-panel");
      expect(panel.getAttribute("data-active")).toBe("true");
      expect(JSON.parse(panel.getAttribute("data-subject") ?? "null")).toEqual({
        kind: "workspace",
        workspaceID: "ws-linked-1",
      });
    },
  );

  it("does not send remote workspace identities to local Kata routes", () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(Response.json({ repos: [] }));
    renderKataLinksSidebar("pull_request", "member-a");

    expect(screen.queryByTestId("kata-links-panel")).toBeNull();
    expect(screen.getByText("Kata links are unavailable for remote workspaces")).toBeTruthy();
  });

  it("does not read local reviews for a remote workspace", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(Response.json({ files: [], commits: [] }));
    const { rerender } = renderSidebar();
    await rerender({ activeTab: "reviews", workspaceHostKey: "member" });

    expect(screen.getByText("Local reviews are unavailable for remote workspaces")).toBeTruthy();
    expect(
      fetch.mock.calls.filter(([input]) => (input instanceof Request ? input.url : String(input)).includes("roborev")),
    ).toHaveLength(0);
  });

  it.each([
    ["a dirty worktree", { worktreeDirty: true, commitsAhead: 2 }, "head", "Compare with HEAD"],
    ["unpushed commits", { worktreeDirty: false, commitsAhead: 2 }, "pushed", "Compare with pushed branch"],
    ["a fully pushed branch", { worktreeDirty: false, commitsAhead: 0 }, "merge-target", "Compare with merge target"],
    ["a never-pushed branch", { worktreeDirty: false }, "merge-target", "Compare with merge target"],
    [
      "commits counted against a fork PR head",
      { worktreeDirty: false, commitsAhead: 2, commitsVsPRHead: true },
      "merge-target",
      "Compare with merge target",
    ],
  ] as const)("opens the diff for %s on the matching base", async (_label, gitState, base, buttonName) => {
    const calls = mockEmptyWorkspaceDiff();

    renderSidebar(0, gitState);

    await waitFor(() => {
      expect(calls.some((url) => url.endsWith(`/api/v1/workspaces/ws-1/diff?base=${base}`))).toBe(true);
    });
    expect(calls.filter((url) => url.includes("/api/v1/workspaces/ws-1/diff?")).length).toBe(1);
    expect(screen.getByRole("button", { name: buttonName }).getAttribute("aria-pressed")).toBe("true");
  });

  it("keeps the user's diff base when the workspace git state changes", async () => {
    const calls = mockEmptyWorkspaceDiff();

    const { rerender } = renderSidebar(0, { worktreeDirty: true });
    await waitFor(() => {
      expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/diff?base=head"))).toBe(true);
    });

    await rerender({ gitState: { worktreeDirty: false, commitsAhead: 1 } });
    await waitFor(() => {
      expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/diff?base=pushed"))).toBe(true);
    });

    await fireEvent.click(screen.getByRole("button", { name: "Compare with HEAD" }));
    calls.length = 0;
    await rerender({ gitState: { worktreeDirty: false, commitsAhead: 0 } });

    expect(screen.getByRole("button", { name: "Compare with HEAD" }).getAttribute("aria-pressed")).toBe("true");
    expect(calls.some((url) => url.includes("base=merge-target"))).toBe(false);
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

    calls.length = 0;
    await rerender({ diffRefreshToken: 1 });
    await waitFor(() => {
      expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/diff?base=merge-target&commit=sha2"))).toBe(
        true,
      );
    });
    expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-1/commits"))).toBe(false);
  });

  it("disables diff controls while the workspace is deleting", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;

      if (url.includes("/api/roborev/api/repos")) {
        return Response.json({ repos: [] });
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

    renderDisabledSidebar();

    expect(screen.getByRole("button", { name: "Compare with HEAD" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Compare with pushed branch" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Compare with merge target" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: /Select commit range/ }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "More diff filters" }).hasAttribute("disabled")).toBe(true);
  });

  it("does not reserve an empty stale-warning row in workspace diffs", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;

      if (url.includes("/api/roborev/api/repos")) {
        return Response.json({ repos: [] });
      }
      if (url.includes("/api/v1/workspaces/ws-1/files")) {
        return Response.json({ stale: false, whitespace_only_count: 0, files: [] });
      }
      if (url.includes("/api/v1/workspaces/ws-1/diff")) {
        return Response.json({ stale: false, whitespace_only_count: 0, files: [] });
      }
      return Response.json({}, { status: 404 });
    });

    renderSidebar();

    await waitFor(() => expect(screen.getByText("No changed files match this category.")).toBeTruthy());
    expect(
      screen.queryByText("Diff may be outdated -- showing changes as of an earlier version of this PR."),
    ).toBeNull();
  });

  it("omits merge target diffs for kata workspaces without an associated PR", async () => {
    const calls: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      calls.push(url);

      if (url.includes("/api/roborev/api/repos")) {
        return Response.json({ repos: [] });
      }
      if (url.includes("/api/v1/workspaces/ws-kata-1/files")) {
        return Response.json({
          stale: false,
          whitespace_only_count: 0,
          files: [],
        });
      }
      if (url.includes("/api/v1/workspaces/ws-kata-1/diff")) {
        return Response.json({
          stale: false,
          whitespace_only_count: 0,
          files: [],
        });
      }
      return Response.json({}, { status: 404 });
    });

    renderKataSidebarWithoutPR();

    expect(screen.getByRole("button", { name: "Compare with HEAD" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Compare with pushed branch" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Compare with merge target" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Select commit range/ })).toBeNull();
    await waitFor(() => {
      expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-kata-1/diff?base=head"))).toBe(true);
    });
    expect(calls.some((url) => url.includes("base=merge-target"))).toBe(false);
    expect(calls.some((url) => url.endsWith("/api/v1/workspaces/ws-kata-1/commits"))).toBe(false);
  });
});
