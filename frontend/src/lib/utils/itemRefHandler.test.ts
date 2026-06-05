import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const mocks = vi.hoisted(() => ({
  post: vi.fn(),
  navigate: vi.fn(),
  showFlash: vi.fn(),
}));

vi.mock("../api/runtime.js", () => ({
  client: {
    POST: mocks.post,
  },
}));

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

async function clickItemRef(attributes: Record<string, string>): Promise<void> {
  const { initItemRefHandler } = await import("./itemRefHandler.js");
  const cleanup = initItemRefHandler();
  const anchor = document.createElement("a");
  anchor.className = "item-ref";
  anchor.href = attributes.href ?? "/issues/github/acme/widgets/12";
  anchor.textContent = "#12";
  for (const [name, value] of Object.entries(attributes)) {
    if (name === "href") continue;
    anchor.setAttribute(name, value);
  }
  document.body.appendChild(anchor);
  anchor.dispatchEvent(
    new MouseEvent("click", {
      bubbles: true,
      cancelable: true,
      button: 0,
    }),
  );
  await Promise.resolve();
  await Promise.resolve();
  cleanup();
}

describe("itemRefHandler", () => {
  afterEach(() => {
    document.body.innerHTML = "";
    vi.restoreAllMocks();
    mocks.post.mockReset();
    mocks.navigate.mockReset();
    mocks.showFlash.mockReset();
  });

  it("navigates internally when the referenced repo is tracked", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    mocks.post.mockResolvedValue({
      data: { repo_tracked: true, item_type: "pr" },
      error: undefined,
      response: { status: 200 },
    });

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

    expect(mocks.post).toHaveBeenCalledWith("/repo/{provider}/{owner}/{name}/resolve/{number}", {
      params: {
        path: {
          provider: "github",
          owner: "acme",
          name: "widgets",
          number: 12,
        },
      },
    });
    expect(mocks.navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12");
    expect(open).not.toHaveBeenCalled();
    expect(mocks.showFlash).not.toHaveBeenCalled();
  });

  it("passes item type hints only for GitLab references", async () => {
    mocks.post.mockResolvedValue({
      data: { repo_tracked: true, item_type: "pr" },
      error: undefined,
      response: { status: 200 },
    });

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

    expect(mocks.post).toHaveBeenCalledWith("/host/{platform_host}/repo/{provider}/{owner}/{name}/resolve/{number}", {
      params: {
        path: {
          platform_host: "gitlab.example.com",
          provider: "gitlab",
          owner: "group",
          name: "project",
          number: 12,
        },
        query: { item_type: "pr" },
      },
    });
  });

  it("passes GitLab issue item type hints", async () => {
    mocks.post.mockResolvedValue({
      data: { repo_tracked: true, item_type: "issue" },
      error: undefined,
      response: { status: 200 },
    });

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

    expect(mocks.post).toHaveBeenCalledWith("/host/{platform_host}/repo/{provider}/{owner}/{name}/resolve/{number}", {
      params: {
        path: {
          platform_host: "gitlab.example.com",
          provider: "gitlab",
          owner: "group",
          name: "project",
          number: 10,
        },
        query: { item_type: "issue" },
      },
    });
  });

  it("opens the provider URL when an untracked reference has an external fallback", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    mocks.post.mockResolvedValue({
      data: { repo_tracked: false, item_type: "issue" },
      error: undefined,
      response: { status: 200 },
    });

    await clickItemRef({
      "data-provider": "github",
      "data-platform-host": "github.com",
      "data-owner": "other",
      "data-name": "repo",
      "data-repo-path": "other/repo",
      "data-number": "77",
      "data-external-url": "https://github.com/other/repo/issues/77",
    });

    expect(open).toHaveBeenCalledWith("https://github.com/other/repo/issues/77", "_blank", "noopener,noreferrer");
    expect(mocks.navigate).not.toHaveBeenCalled();
    expect(mocks.showFlash).not.toHaveBeenCalled();
  });

  it("rejects non-http external fallbacks for untracked references", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    mocks.post.mockResolvedValue({
      data: { repo_tracked: false, item_type: "issue" },
      error: undefined,
      response: { status: 200 },
    });

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
    expect(mocks.showFlash).toHaveBeenCalledWith("other/repo is not tracked. Add it in Settings to navigate here.");
  });

  it("keeps the not-tracked flash for references without an external fallback", async () => {
    mocks.post.mockResolvedValue({
      data: { repo_tracked: false, item_type: "issue" },
      error: undefined,
      response: { status: 200 },
    });

    await clickItemRef({
      "data-provider": "github",
      "data-platform-host": "github.com",
      "data-owner": "other",
      "data-name": "repo",
      "data-repo-path": "other/repo",
      "data-number": "77",
    });

    expect(mocks.showFlash).toHaveBeenCalledWith("other/repo is not tracked. Add it in Settings to navigate here.");
    expect(mocks.navigate).not.toHaveBeenCalled();
  });
});
