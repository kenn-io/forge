import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../app/runtime.js";
import { makeGeneratedClient } from "../testing/generated-client.js";

const mocks = vi.hoisted(() => ({
  post: vi.fn(),
  repos: vi.fn(),
  navigate: vi.fn(),
  showFlash: vi.fn(),
}));

vi.mock("../api/runtime.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/runtime.js")>();
  return {
    ...actual,
    client: {
      POST: mocks.post,
    },
  };
});

vi.mock("../stores/router.svelte.js", () => ({
  navigate: mocks.navigate,
  buildItemRoute: vi.fn((ref) =>
    ref.itemType === "pr"
      ? `/pulls/${ref.provider}/${ref.repoPath}/${ref.number}`
      : `/issues/${ref.provider}/${ref.repoPath}/${ref.number}`,
  ),
}));

vi.mock("../stores/flash.svelte.js", () => ({
  showFlash: mocks.showFlash,
}));

async function clickItemRef(
  attributes: Record<string, string>,
  contexts: Array<{ provider: string; platformHost?: string }> = [],
): Promise<{ captured: boolean; href: string; target: string }> {
  const { initItemRefHandler } = await import("./itemRefHandler.js");
  const runtime = makeAppRuntime(
    makeGeneratedClient({
      RepositoriesService: {
        listRepos: mocks.repos,
        resolveRepoItem: mocks.post,
        resolveRepoItemOnHost: mocks.post,
      },
    }),
  );
  const cleanup = initItemRefHandler(runtime, undefined, document, () => contexts);
  const anchor = document.createElement("a");
  anchor.className = "item-ref";
  anchor.href = attributes.href ?? "/issues/github/acme/widgets/12";
  anchor.textContent = "#12";
  anchor.target = "_blank";
  for (const [name, value] of Object.entries(attributes)) {
    if (name === "href") continue;
    anchor.setAttribute(name, value);
  }
  document.body.appendChild(anchor);
  await vi.waitFor(() => expect(mocks.repos).toHaveBeenCalled());
  await new Promise((resolve) => setTimeout(resolve, 0));
  let captured = false;
  const observe = (event: Event) => {
    captured = event.defaultPrevented;
    event.preventDefault();
  };
  document.addEventListener("click", observe);
  anchor.dispatchEvent(
    new MouseEvent("click", {
      bubbles: true,
      cancelable: true,
      button: 0,
    }),
  );
  if (captured) await vi.waitFor(() => expect(mocks.post).toHaveBeenCalled());
  await new Promise((resolve) => setTimeout(resolve, 0));
  cleanup();
  document.removeEventListener("click", observe);
  await Effect.runPromise(runtime.disposeEffect);
  return { captured, href: anchor.href, target: anchor.target };
}

describe("itemRefHandler", () => {
  beforeEach(() => {
    mocks.repos.mockResolvedValue(
      [
        ["github", "github.com", "acme", "widgets"],
        ["gitlab", "gitlab.com", "acme", "widgets"],
        ["gitlab", "gitlab.example.com", "group", "project"],
        ["forgejo", "forge.example.com", "acme", "widgets"],
        ["gitea", "git.example.com", "acme", "widgets"],
        ["bitbucket", "bitbucket.org", "acme", "widgets"],
      ].map(([Platform, PlatformHost, Owner, Name]) => ({
        Platform,
        PlatformHost,
        Owner,
        Name,
        ID: 1,
        PlatformRepoID: 1,
        AllowMergeCommit: true,
        AllowRebaseMerge: true,
        AllowSquashMerge: true,
        CreatedAt: "2026-01-01T00:00:00Z",
        LastSyncCompletedAt: null,
        LastSyncStartedAt: null,
        LastSyncError: "",
        ViewerCanMerge: true,
      })),
    );
  });
  afterEach(() => {
    document.body.innerHTML = "";
    vi.restoreAllMocks();
    mocks.post.mockReset();
    mocks.repos.mockReset();
    mocks.navigate.mockReset();
    mocks.showFlash.mockReset();
  });

  it.each([
    ["github", "github.com", "https://github.com/acme/widgets/pull/12", "pr"],
    ["github", "github.com", "https://github.com/acme/widgets/issues/12", "issue"],
    ["gitlab", "gitlab.com", "https://gitlab.com/acme/widgets/-/merge_requests/12", "pr"],
    ["forgejo", "forge.example.com", "https://forge.example.com/acme/widgets/pulls/12", "pr"],
    ["gitea", "git.example.com", "https://git.example.com/acme/widgets/issues/12", "issue"],
    ["bitbucket", "bitbucket.org", "https://bitbucket.org/acme/widgets/pull-requests/12", "pr"],
  ])("routes dynamically inserted %s links without item-ref markup", async (provider, platformHost, href, itemType) => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    mocks.post.mockResolvedValue({ repo_tracked: true, item_type: itemType });
    const contexts = platformHost.endsWith(".example.com") ? [{ provider, platformHost }] : [];

    await clickItemRef({ class: "", href }, contexts);

    expect(mocks.navigate).toHaveBeenCalledWith(
      `/${itemType === "pr" ? "pulls" : "issues"}/${provider}/acme/widgets/12`,
    );
    expect(open).not.toHaveBeenCalled();
  });

  it.each([
    ["https://github.com/other/repo/pull/12", {}],
    ["https://github.com/acme/widgets/pull/12", { ctrlKey: true }],
    ["https://github.com/acme/widgets/issues/12", { metaKey: true }],
    ["https://example.com/acme/widgets/pull/12", {}],
    ["https://github.com/acme/widgets/actions/runs/12", {}],
  ])("leaves external or modified clicks to the browser: %s", async (href, modifiers) => {
    const { initItemRefHandler } = await import("./itemRefHandler.js");
    const runtime = makeAppRuntime(makeGeneratedClient({ RepositoriesService: { listRepos: mocks.repos } }));
    const root = document.createElement("div");
    const cleanup = initItemRefHandler(runtime, undefined, root);
    const anchor = document.createElement("a");
    anchor.href = href;
    root.append(anchor);
    let captured = true;
    root.addEventListener("click", (event) => {
      captured = event.defaultPrevented;
      event.preventDefault();
    });
    anchor.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...modifiers }));
    expect(captured).toBe(false);
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("navigates internally when the referenced repo is tracked", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    mocks.post.mockResolvedValue({ repo_tracked: true, item_type: "pr" });

    await clickItemRef({
      "data-provider": "github",
      "data-platform-host": "github.com",
      "data-owner": "acme",
      "data-name": "widgets",
      "data-repo-path": "acme/widgets",
      "data-number": "12",
      "data-item-type": "pr",
      "data-external-url": "https://github.com/acme/widgets/pull/12",
    });

    expect(mocks.post).toHaveBeenCalledWith(
      { provider: "github", owner: "acme", name: "widgets", number: 12 },
      undefined,
      { signal: expect.any(AbortSignal) },
    );
    expect(mocks.navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12");
    expect(open).not.toHaveBeenCalled();
    expect(mocks.showFlash).not.toHaveBeenCalled();
  });

  it("passes item type hints only for GitLab references", async () => {
    mocks.post.mockResolvedValue({ repo_tracked: true, item_type: "pr" });

    await clickItemRef({
      "data-provider": "gitlab",
      "data-platform-host": "gitlab.example.com",
      "data-owner": "group",
      "data-name": "project",
      "data-repo-path": "group/project",
      "data-number": "12",
      "data-item-type": "pr",
      "data-external-url": "https://gitlab.example.com/group/project/-/merge_requests/12",
    });

    expect(mocks.post).toHaveBeenCalledWith(
      expect.objectContaining({
        platformHost: "gitlab.example.com",
        provider: "gitlab",
        owner: "group",
        name: "project",
        number: 12,
      }),
      { item_type: "pr" },
      expect.objectContaining({ signal: expect.anything() }),
    );
  });

  it("passes GitLab issue item type hints", async () => {
    mocks.post.mockResolvedValue({ repo_tracked: true, item_type: "issue" });

    await clickItemRef({
      "data-provider": "gitlab",
      "data-platform-host": "gitlab.example.com",
      "data-owner": "group",
      "data-name": "project",
      "data-repo-path": "group/project",
      "data-number": "10",
      "data-item-type": "issue",
      "data-external-url": "https://gitlab.example.com/group/project/-/issues/10",
    });

    expect(mocks.post).toHaveBeenCalledWith(
      expect.objectContaining({
        platformHost: "gitlab.example.com",
        provider: "gitlab",
        owner: "group",
        name: "project",
        number: 10,
      }),
      { item_type: "issue" },
      expect.objectContaining({ signal: expect.anything() }),
    );
  });

  it("preserves native navigation for an untracked reference with an external fallback", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    mocks.post.mockResolvedValue({ repo_tracked: false, item_type: "issue" });

    const click = await clickItemRef({
      "data-provider": "github",
      "data-platform-host": "github.com",
      "data-owner": "other",
      "data-name": "repo",
      "data-repo-path": "other/repo",
      "data-number": "77",
      "data-external-url": "https://github.com/other/repo/issues/77",
    });

    expect(click).toEqual({ captured: false, href: "https://github.com/other/repo/issues/77", target: "_blank" });
    expect(mocks.post).not.toHaveBeenCalled();
    expect(open).not.toHaveBeenCalled();
    expect(mocks.navigate).not.toHaveBeenCalled();
    expect(mocks.showFlash).not.toHaveBeenCalled();
  });

  it("rejects non-http external fallbacks for untracked references", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    mocks.post.mockResolvedValue({ repo_tracked: false, item_type: "issue" });

    await clickItemRef({
      "data-provider": "github",
      "data-platform-host": "github.com",
      "data-owner": "other",
      "data-name": "repo",
      "data-repo-path": "other/repo",
      "data-number": "77",
      "data-external-url": "javascript:alert(1)",
    });

    expect(open).not.toHaveBeenCalled();
    expect(mocks.navigate).not.toHaveBeenCalled();
    expect(mocks.showFlash).toHaveBeenCalledWith("other/repo is not tracked. Add it in Settings to navigate here.", {
      tone: "danger",
    });
  });

  it("keeps the not-tracked flash for references without an external fallback", async () => {
    mocks.post.mockResolvedValue({ repo_tracked: false, item_type: "issue" });

    await clickItemRef({
      "data-provider": "github",
      "data-platform-host": "github.com",
      "data-owner": "other",
      "data-name": "repo",
      "data-repo-path": "other/repo",
      "data-number": "77",
    });

    expect(mocks.showFlash).toHaveBeenCalledWith("other/repo is not tracked. Add it in Settings to navigate here.", {
      tone: "danger",
    });
    expect(mocks.navigate).not.toHaveBeenCalled();
  });
});
