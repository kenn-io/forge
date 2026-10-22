import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../app/runtime.js";
import { makeGeneratedClient } from "../testing/generated-client.js";

const mocks = vi.hoisted(() => ({
  post: vi.fn(),
  repos: vi.fn(),
  navigate: vi.fn(),
  navigateToURL: vi.fn(),
  showFlash: vi.fn(),
}));

vi.mock("./pageNavigation.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./pageNavigation.js")>();
  return { ...actual, navigateToURL: mocks.navigateToURL };
});

vi.mock("../api/runtime.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/runtime.js")>();
  return {
    ...actual,
    client: {
      POST: mocks.post,
    },
  };
});

vi.mock("../stores/router.svelte.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../stores/router.svelte.js")>();
  return {
    ...actual,
    navigate: mocks.navigate,
    buildItemRoute: vi.fn((ref) =>
      ref.itemType === "pr"
        ? `/pulls/${ref.provider}/${ref.repoPath}/${ref.number}`
        : `/issues/${ref.provider}/${ref.repoPath}/${ref.number}`,
    ),
  };
});

vi.mock("../stores/flash.svelte.js", () => ({
  showFlash: mocks.showFlash,
}));

async function clickItemRef(
  attributes: Record<string, string>,
  contexts: Array<{ provider: string; platformHost?: string }> = [],
  event: { type: string; init: MouseEventInit } = { type: "click", init: { button: 0 } },
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
  document.addEventListener(event.type, observe);
  anchor.dispatchEvent(new MouseEvent(event.type, { bubbles: true, cancelable: true, ...event.init }));
  if (captured) await vi.waitFor(() => expect(mocks.post).toHaveBeenCalled());
  await new Promise((resolve) => setTimeout(resolve, 0));
  cleanup();
  document.removeEventListener(event.type, observe);
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
    mocks.navigateToURL.mockReset();
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

  it.each(["_blank", "details"])("leaves explicit target %s to the browser", async (target) => {
    const result = await clickItemRef({ class: "", href: "https://github.com/acme/widgets/pull/12", target });
    expect(result.captured).toBe(false);
    expect(result.target).toBe(target);
    expect(mocks.post).not.toHaveBeenCalled();
    expect(mocks.navigate).not.toHaveBeenCalled();
  });

  it("routes explicit current-page links inside Forge", async () => {
    mocks.post.mockResolvedValue({ repo_tracked: true, item_type: "pr" });
    const result = await clickItemRef({ class: "", href: "https://github.com/acme/widgets/pull/12", target: "_self" });
    expect(result.captured).toBe(true);
    expect(mocks.navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12");
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

  it("routes from the data attributes so a base-path href is not prefixed twice", async () => {
    mocks.post.mockResolvedValue({ repo_tracked: true, item_type: "pr" });

    const result = await clickItemRef({
      href: "/forge/pulls/github/acme/widgets/12",
      "data-provider": "github",
      "data-platform-host": "github.com",
      "data-owner": "acme",
      "data-name": "widgets",
      "data-repo-path": "acme/widgets",
      "data-number": "12",
      "data-item-type": "pr",
    });

    expect(result.captured).toBe(true);
    expect(mocks.navigate).toHaveBeenCalledExactlyOnceWith("/pulls/github/acme/widgets/12");
  });

  it("passes GitLab PR item type hints", async () => {
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

  it("passes Bitbucket Cloud issue item type hints so a same-numbered PR does not win", async () => {
    mocks.post.mockResolvedValue({ repo_tracked: true, item_type: "issue" });

    await clickItemRef({ class: "", href: "https://bitbucket.org/acme/widgets/issues/5" });

    expect(mocks.post).toHaveBeenCalledWith(
      expect.objectContaining({ provider: "bitbucket", owner: "acme", name: "widgets", number: 5 }),
      { item_type: "issue" },
      expect.objectContaining({ signal: expect.anything() }),
    );
    expect(mocks.navigate).toHaveBeenCalledWith("/issues/bitbucket/acme/widgets/5");
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

  const untrackedReference = {
    "data-provider": "github",
    "data-platform-host": "github.com",
    "data-owner": "other",
    "data-name": "repo",
    "data-repo-path": "other/repo",
    "data-number": "77",
    "data-external-url": "https://github.com/other/repo/issues/77",
  };

  it.each([
    ["ctrl-click", "click", { button: 0, ctrlKey: true }],
    ["cmd-click", "click", { button: 0, metaKey: true }],
    ["shift-click", "click", { button: 0, shiftKey: true }],
    ["middle click", "auxclick", { button: 1 }],
    ["context menu", "contextmenu", { button: 2 }],
  ])("opens an untracked reference at the provider on %s", async (_label, type, init) => {
    const click = await clickItemRef(untrackedReference, [], { type, init });

    expect(click).toEqual({ captured: false, href: "https://github.com/other/repo/issues/77", target: "" });
    expect(mocks.post).not.toHaveBeenCalled();
    expect(mocks.navigate).not.toHaveBeenCalled();
  });

  it("keeps the Forge route of a tracked reference on ctrl-click", async () => {
    const click = await clickItemRef(
      { ...untrackedReference, "data-owner": "acme", "data-name": "widgets", "data-repo-path": "acme/widgets" },
      [],
      { type: "click", init: { button: 0, ctrlKey: true } },
    );

    expect(click).toEqual({ captured: false, href: "http://localhost:3000/issues/github/acme/widgets/12", target: "" });
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
  const unsyncedForgejoPull = {
    "data-provider": "forgejo",
    "data-platform-host": "forge.example.com",
    "data-owner": "acme",
    "data-name": "widgets",
    "data-repo-path": "acme/widgets",
    "data-number": "12",
  };

  async function problemResponse(status: number, code: "notFound" | "upstreamError"): Promise<never> {
    const { GeneratedProblemResponse } = await import("../api/runtime.js");
    const problem = { status, code, title: "Problem", detail: "problem" };
    throw new GeneratedProblemResponse(problem, Response.json(problem, { status }));
  }

  it("opens the provider page when a tracked repository has not synced the item", async () => {
    mocks.post.mockImplementation(() => problemResponse(404, "notFound"));

    const click = await clickItemRef({
      ...unsyncedForgejoPull,
      "data-external-url": "https://forge.example.com/acme/widgets/pulls/12",
    });

    expect(click.captured).toBe(true);
    expect(mocks.navigateToURL).toHaveBeenCalledWith("https://forge.example.com/acme/widgets/pulls/12");
    expect(mocks.showFlash).not.toHaveBeenCalled();
    expect(mocks.navigate).not.toHaveBeenCalled();
  });

  it.each([
    ["without a provider URL", {}],
    ["with a non-http provider URL", { "data-external-url": "javascript:alert(1)" }],
  ])("keeps the not-found flash for an unsynced item %s", async (_label, extra) => {
    mocks.post.mockImplementation(() => problemResponse(404, "notFound"));

    await clickItemRef({ ...unsyncedForgejoPull, ...extra });

    expect(mocks.showFlash).toHaveBeenCalledWith("Item acme/widgets#12 not found.", { tone: "danger" });
    expect(mocks.navigateToURL).not.toHaveBeenCalled();
  });

  it.each([
    ["the user stays on the page", []],
    ["the user moved to another page", ["/settings"]],
    ["the user selected another Activity item", ["/?selected=pr:13"]],
    ["the user closed the Activity panel", ["/"]],
    ["the user left and came back to the same page", ["/settings", "/?selected=pr:12"]],
  ])("opens the provider page for a late 404 only while %s", async (_label, laterURLs) => {
    const { initItemRefHandler } = await import("./itemRefHandler.js");
    const runtime = makeAppRuntime(
      makeGeneratedClient({ RepositoriesService: { listRepos: mocks.repos, resolveRepoItemOnHost: mocks.post } }),
    );
    const lookupStarted = Promise.withResolvers<void>();
    const releaseLookup = Promise.withResolvers<void>();
    let lookupSettled = false;
    mocks.post.mockImplementation(async () => {
      lookupStarted.resolve();
      await releaseLookup.promise;
      lookupSettled = true;
      return problemResponse(404, "notFound");
    });
    const originURL = window.location.pathname + window.location.search;
    window.history.pushState(null, "", "/?selected=pr:12");
    const cleanup = initItemRefHandler(runtime, undefined, document);
    await vi.waitFor(() => expect(mocks.repos).toHaveBeenCalled());
    const anchor = document.createElement("a");
    anchor.className = "item-ref";
    anchor.href = "/pulls/forgejo/acme/widgets/12";
    for (const [name, value] of Object.entries({
      ...unsyncedForgejoPull,
      "data-external-url": "https://forge.example.com/acme/widgets/pulls/12",
    })) {
      anchor.setAttribute(name, value);
    }
    document.body.appendChild(anchor);
    try {
      anchor.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
      await lookupStarted.promise;
      for (const url of laterURLs) window.history.replaceState(null, "", url);
      releaseLookup.resolve();
      await vi.waitFor(() => expect(lookupSettled).toBe(true));
      await new Promise((resolve) => setTimeout(resolve, 0));
      if (laterURLs.length > 0) expect(mocks.navigateToURL).not.toHaveBeenCalled();
      else expect(mocks.navigateToURL).toHaveBeenCalledWith("https://forge.example.com/acme/widgets/pulls/12");
      expect(mocks.showFlash).not.toHaveBeenCalled();
    } finally {
      window.history.pushState(null, "", originURL);
      cleanup();
      await Effect.runPromise(runtime.disposeEffect);
    }
  });

  it("keeps the failure flash when the resolver fails for another reason", async () => {
    mocks.post.mockImplementation(() => problemResponse(502, "upstreamError"));

    await clickItemRef({
      ...unsyncedForgejoPull,
      "data-external-url": "https://forge.example.com/acme/widgets/pulls/12",
    });

    expect(mocks.showFlash).toHaveBeenCalledWith("Failed to resolve acme/widgets#12. Try again later.", {
      tone: "danger",
    });
    expect(mocks.navigateToURL).not.toHaveBeenCalled();
  });
});
