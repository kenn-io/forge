import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";

const mockGet = vi.fn();
const mockNavigate = vi.fn();

vi.mock("../../api/runtime.js", () => ({
  client: {
    GET: (...args: unknown[]) => mockGet(...args),
  },
}));

vi.mock("../../stores/router.svelte.ts", () => ({
  navigate: (path: string) => mockNavigate(path),
}));

class MockEventSource {
  addEventListener = vi.fn();
  close = vi.fn();

  constructor(readonly url: string) {}
}

interface WorkspaceFixtureOptions {
  id: string;
  provider: string;
  platformHost: string;
  owner: string;
  name: string;
  number: number;
  title?: string;
  branch?: string;
  itemType?: "pull_request" | "issue";
  createdAt?: string;
  tmuxLastOutputAt?: string | null;
  itemLastActivityAt?: string | null;
}

function workspaceFixture({
  id,
  provider,
  platformHost,
  owner,
  name,
  number,
  title = `PR ${number}`,
  branch = `feature-${number}`,
  itemType = "pull_request",
  createdAt = "2026-05-12T12:00:00Z",
  tmuxLastOutputAt = null,
  itemLastActivityAt = null,
}: WorkspaceFixtureOptions) {
  return {
    id,
    repo: {
      provider,
      platform_host: platformHost,
      owner,
      name,
      repo_path: `${owner}/${name}`,
    },
    platform_host: platformHost,
    repo_owner: owner,
    repo_name: name,
    item_type: itemType,
    item_number: number,
    git_head_ref: branch,
    worktree_path: `/tmp/${id}`,
    tmux_session: id,
    status: "ready",
    created_at: createdAt,
    tmux_last_output_at: tmuxLastOutputAt,
    item_last_activity_at: itemLastActivityAt,
    mr_title: title,
    mr_state: "open",
  };
}

// Three workspaces across two repos with distinct creation and
// activity timestamps, listed in API order (created_at DESC).
function sortFixtures() {
  return [
    workspaceFixture({
      id: "ws-new",
      provider: "github",
      platformHost: "github.com",
      owner: "kenn-io",
      name: "middleman",
      number: 3,
      title: "Newest created",
      createdAt: "2026-05-12T12:00:00Z",
      tmuxLastOutputAt: "2026-05-12T13:00:00Z",
    }),
    workspaceFixture({
      id: "ws-mid",
      provider: "github",
      platformHost: "github.com",
      owner: "kenn-io",
      name: "agentsview",
      number: 2,
      title: "Most recently active",
      createdAt: "2026-05-11T12:00:00Z",
      tmuxLastOutputAt: "2026-05-14T09:00:00Z",
    }),
    workspaceFixture({
      id: "ws-old",
      provider: "github",
      platformHost: "github.com",
      owner: "kenn-io",
      name: "middleman",
      number: 1,
      title: "Oldest without activity",
      createdAt: "2026-05-10T12:00:00Z",
    }),
  ];
}

function rowTitles(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll(".ws-name")).map((el) => el.textContent?.trim() ?? "");
}

describe("WorkspaceListSidebar", () => {
  beforeEach(() => {
    mockGet.mockReset();
    mockNavigate.mockReset();
    localStorage.clear();
    vi.stubGlobal("EventSource", MockEventSource);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("shows provider icons in repo groups when multiple providers are present", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-github",
            provider: "github",
            platformHost: "github.com",
            owner: "acme",
            name: "widgets",
            number: 42,
          }),
          workspaceFixture({
            id: "ws-gitlab",
            provider: "gitlab",
            platformHost: "gitlab.com",
            owner: "platform",
            name: "api",
            number: 7,
          }),
        ],
      },
    });

    render(WorkspaceListSidebar, {
      props: { selectedId: "ws-github" },
    });

    await screen.findByText("acme/widgets");
    expect(screen.getByRole("img", { name: "GitHub" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "GitLab" })).toBeTruthy();
  });

  it("does not render a blank rail while the workspace list is loading", async () => {
    mockGet.mockReturnValue(new Promise(() => {}));

    render(WorkspaceListSidebar, {
      props: { selectedId: "" },
    });

    expect(screen.getByText("Loading workspaces...")).toBeTruthy();
  });

  it("shows a retrying state when the initial workspace list hangs", async () => {
    vi.useFakeTimers();
    let aborted = false;
    mockGet.mockImplementation(
      (_path: string, opts?: { signal?: AbortSignal }) =>
        new Promise((_resolve, reject) => {
          opts?.signal?.addEventListener("abort", () => {
            aborted = true;
            reject(new DOMException("Aborted", "AbortError"));
          });
        }),
    );

    render(WorkspaceListSidebar, {
      props: { selectedId: "" },
    });

    expect(screen.getByText("Loading workspaces...")).toBeTruthy();
    await vi.advanceTimersByTimeAsync(10_000);
    await tick();

    expect(aborted).toBe(true);
    expect(screen.getByText("Still loading workspaces. Retrying...")).toBeTruthy();
  });

  it("hides provider icons in repo groups when one provider is present", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-github",
            provider: "github",
            platformHost: "github.com",
            owner: "acme",
            name: "widgets",
            number: 42,
          }),
          workspaceFixture({
            id: "ws-ghe",
            provider: "github",
            platformHost: "ghe.example.com",
            owner: "enterprise",
            name: "service",
            number: 9,
          }),
        ],
      },
    });

    render(WorkspaceListSidebar, {
      props: { selectedId: "ws-github" },
    });

    await screen.findByText("acme/widgets");
    expect(screen.queryByRole("img", { name: "GitHub" })).toBeNull();
  });

  it("filters workspaces by title, repo, and item number", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-title",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "taskboard",
            number: 9,
            title: "Migrate native HTTP surface to Huma v2",
            branch: "feat/huma-adoption",
          }),
          workspaceFixture({
            id: "ws-repo",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "kenn-platform",
            number: 2,
            title: "Hosted code fetch and caching strategy",
          }),
          workspaceFixture({
            id: "ws-number",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 224,
            title: "Add notification inbox triage",
            itemType: "issue",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-title" },
    });
    const filter = await screen.findByLabelText("Filter workspaces");

    await fireEvent.input(filter, {
      target: { value: "huma" },
    });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(1);
    expect(screen.getByText("Migrate native HTTP surface to Huma v2")).toBeTruthy();

    await fireEvent.input(filter, {
      target: { value: "kenn-platform" },
    });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(1);
    expect(screen.getByText("Hosted code fetch and caching strategy")).toBeTruthy();

    await fireEvent.input(filter, {
      target: { value: "#224" },
    });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(1);
    expect(screen.getByText("Add notification inbox triage")).toBeTruthy();
  });

  it("shows matching workspaces in collapsed groups while filtering", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-hidden",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 224,
            title: "Add notification inbox triage",
            itemType: "issue",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-hidden" },
    });
    const groupHeader = await screen.findByRole("button", {
      name: /kenn-io\/middleman/,
    });
    const filter = screen.getByLabelText("Filter workspaces");

    expect(container.querySelectorAll(".ws-row")).toHaveLength(1);
    await fireEvent.click(groupHeader);
    expect(container.querySelectorAll(".ws-row")).toHaveLength(0);

    await fireEvent.input(filter, {
      target: { value: "#224" },
    });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(1);
    expect(screen.getByText("Add notification inbox triage")).toBeTruthy();

    await fireEvent.input(filter, {
      target: { value: "" },
    });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(0);
  });

  it("sorts flat by creation time and drops group headers", async () => {
    mockGet.mockResolvedValue({
      data: { workspaces: sortFixtures() },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-new" },
    });
    await screen.findByText("Newest created");

    // Default org/repo mode groups rows under repo headers.
    expect(screen.getByText("kenn-io/middleman")).toBeTruthy();
    expect(container.querySelectorAll(".repo-context")).toHaveLength(0);

    await fireEvent.click(screen.getByTitle("Sort workspaces"));
    await fireEvent.click(screen.getByRole("button", { name: "Created" }));

    expect(rowTitles(container)).toEqual(["Newest created", "Most recently active", "Oldest without activity"]);
    expect(container.querySelectorAll(".group-header")).toHaveLength(0);
    // Flat rows carry their own repo context instead of a header.
    expect(container.querySelectorAll(".repo-context")).toHaveLength(3);
  });

  it("keeps provider and host identity visible in flat rows", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-github",
            provider: "github",
            platformHost: "github.com",
            owner: "acme",
            name: "widgets",
            number: 1,
            title: "GitHub workspace",
            createdAt: "2026-05-12T12:00:00Z",
          }),
          workspaceFixture({
            id: "ws-gitlab",
            provider: "gitlab",
            platformHost: "gitlab.example.com",
            owner: "acme",
            name: "widgets",
            number: 2,
            title: "GitLab workspace",
            createdAt: "2026-05-11T12:00:00Z",
          }),
          workspaceFixture({
            id: "ws-other",
            provider: "gitlab",
            platformHost: "gitlab.example.com",
            owner: "platform",
            name: "api",
            number: 3,
            title: "Unambiguous workspace",
            createdAt: "2026-05-10T12:00:00Z",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-github" },
    });
    await screen.findByText("GitHub workspace");

    await fireEvent.click(screen.getByTitle("Sort workspaces"));
    await fireEvent.click(screen.getByRole("button", { name: "Created" }));

    // Provider icons survive the loss of group headers.
    expect(container.querySelectorAll(".repo-context")).toHaveLength(3);
    expect(screen.getByRole("img", { name: "GitHub" })).toBeTruthy();
    expect(screen.getAllByRole("img", { name: "GitLab" })).toHaveLength(2);

    // acme/widgets exists on two hosts, so its rows carry the host;
    // platform/api is unique and stays short.
    const contextNames = container.querySelectorAll(".repo-context-name");
    expect(contextNames[0]?.textContent?.trim()).toBe("github.com/acme/widgets");
    expect(contextNames[1]?.textContent?.trim()).toBe("gitlab.example.com/acme/widgets");
    expect(contextNames[2]?.textContent?.trim()).toBe("platform/api");
  });

  it("sorts flat by last activity with creation time as fallback", async () => {
    mockGet.mockResolvedValue({
      data: { workspaces: sortFixtures() },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-new" },
    });
    await screen.findByText("Newest created");

    await fireEvent.click(screen.getByTitle("Sort workspaces"));
    await fireEvent.click(screen.getByRole("button", { name: "Activity" }));

    // ws-old has no tmux output, so it sorts by its creation time.
    expect(rowTitles(container)).toEqual(["Most recently active", "Newest created", "Oldest without activity"]);
  });

  it("sorts flat by item activity with creation time as fallback", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-created-newest",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 1,
            title: "Newest created fallback",
            createdAt: "2026-05-15T12:00:00Z",
          }),
          workspaceFixture({
            id: "ws-pr-active",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 2,
            title: "PR recently changed",
            createdAt: "2026-05-10T12:00:00Z",
            itemLastActivityAt: "2026-05-16T12:00:00Z",
          }),
          workspaceFixture({
            id: "ws-issue-active",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "agentsview",
            number: 3,
            title: "Issue recently changed",
            itemType: "issue",
            createdAt: "2026-05-09T12:00:00Z",
            itemLastActivityAt: "2026-05-17T12:00:00Z",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-created-newest" },
    });
    await screen.findByText("Newest created fallback");

    await fireEvent.click(screen.getByTitle("Sort workspaces"));
    const itemActivitySort = screen.getByRole("button", { name: "Item activity" });
    expect(itemActivitySort.getAttribute("title")).toBe(
      "Sort by latest linked PR or issue activity, falling back to workspace creation.",
    );
    await fireEvent.click(itemActivitySort);

    expect(rowTitles(container)).toEqual(["Issue recently changed", "PR recently changed", "Newest created fallback"]);
    expect(container.querySelectorAll(".group-header")).toHaveLength(0);
  });

  it("persists the selected sort across mounts", async () => {
    mockGet.mockResolvedValue({
      data: { workspaces: sortFixtures() },
    });

    const first = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-new" },
    });
    await screen.findByText("Newest created");

    await fireEvent.click(screen.getByTitle("Sort workspaces"));
    await fireEvent.click(screen.getByRole("button", { name: "Activity" }));
    first.unmount();

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-new" },
    });
    await screen.findByText("Newest created");

    expect(rowTitles(container)).toEqual(["Most recently active", "Newest created", "Oldest without activity"]);
    expect(container.querySelectorAll(".group-header")).toHaveLength(0);
  });
});
