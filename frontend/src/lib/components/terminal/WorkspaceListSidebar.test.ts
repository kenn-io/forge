import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";

const mockGet = vi.fn();
const mockPost = vi.fn();
const mockDelete = vi.fn();
const mockNavigate = vi.fn();

vi.mock("../../api/runtime.js", () => ({
  client: {
    DELETE: (...args: unknown[]) => mockDelete(...args),
    GET: (...args: unknown[]) => mockGet(...args),
    POST: (...args: unknown[]) => mockPost(...args),
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
  itemType?: "pull_request" | "issue" | "kata_task";
  itemKey?: string;
  isDraft?: boolean;
  kata?: {
    daemon_id: string;
    project_uid: string;
    project_name?: string;
    issue_uid: string;
    short_id?: string;
    qualified_id?: string;
    title?: string;
  };
  createdAt?: string;
  tmuxLastOutputAt?: string | null;
  itemLastActivityAt?: string | null;
  additions?: number | null;
  deletions?: number | null;
  commitsAhead?: number | null;
  commitsBehind?: number | null;
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
  itemKey = undefined,
  isDraft = false,
  kata = undefined,
  createdAt = "2026-05-12T12:00:00Z",
  tmuxLastOutputAt = null,
  itemLastActivityAt = null,
  additions = null,
  deletions = null,
  commitsAhead = null,
  commitsBehind = null,
}: WorkspaceFixtureOptions) {
  const isKata = itemType === "kata_task";
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
    item_key: itemKey,
    kata,
    git_head_ref: branch,
    worktree_path: `/tmp/${id}`,
    tmux_session: id,
    status: "ready",
    created_at: createdAt,
    tmux_last_output_at: tmuxLastOutputAt,
    item_last_activity_at: itemLastActivityAt,
    mr_title: isKata ? null : title,
    mr_state: isKata ? null : "open",
    mr_is_draft: isKata ? null : isDraft,
    mr_additions: additions,
    mr_deletions: deletions,
    commits_ahead: commitsAhead,
    commits_behind: commitsBehind,
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

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, reject, resolve };
}

describe("WorkspaceListSidebar", () => {
  beforeEach(() => {
    mockGet.mockReset();
    mockPost.mockReset();
    mockDelete.mockReset();
    mockNavigate.mockReset();
    localStorage.clear();
    vi.stubGlobal("EventSource", MockEventSource);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("shows fleet hosts when peers are present", async () => {
    mockGet.mockImplementation((path: string) => {
      if (path === "/snapshot") {
        return Promise.resolve({
          data: {
            hosts: [
              {
                configKey: "hub",
                diagnostics: [],
                id: "hub",
                kind: "self",
                name: "hub",
                operationAvailability: {},
                platform: "darwin",
                preferredTransport: "local",
                reachable: true,
                tmuxSessions: [],
              },
              {
                configKey: "member",
                diagnostics: [],
                id: "member",
                kind: "remote",
                name: "member",
                operationAvailability: {},
                platform: "linux",
                preferredTransport: "http",
                reachable: true,
                tmuxSessions: [],
              },
            ],
          },
        });
      }
      return Promise.resolve({ data: { workspaces: [] } });
    });

    render(WorkspaceListSidebar, {
      props: { selectedId: "" },
    });

    await screen.findByText("Fleet");
    expect(screen.getByText("2/2")).toBeTruthy();
    expect(screen.getByText("hub")).toBeTruthy();
    expect(screen.getByText("self")).toBeTruthy();
    expect(screen.getByText("local")).toBeTruthy();
    expect(screen.getByText("member")).toBeTruthy();
    expect(screen.getByText("remote")).toBeTruthy();
    expect(screen.getByText("http")).toBeTruthy();
  });

  it("hides the fleet status block when only the local host is present", async () => {
    mockGet.mockImplementation((path: string) => {
      if (path === "/snapshot") {
        return Promise.resolve({
          data: {
            hosts: [
              {
                configKey: "member",
                diagnostics: [],
                id: "member",
                kind: "self",
                name: "member",
                operationAvailability: {},
                platform: "linux",
                preferredTransport: "local",
                reachable: true,
                tmuxSessions: [],
              },
            ],
          },
        });
      }
      return Promise.resolve({ data: { workspaces: [] } });
    });

    render(WorkspaceListSidebar, {
      props: { selectedId: "" },
    });

    await waitFor(() => {
      expect(mockGet).toHaveBeenCalledWith(
        "/snapshot",
        expect.objectContaining({
          params: { query: { include_peers: true } },
        }),
      );
    });
    expect(screen.queryByText("Fleet")).toBeNull();
    expect(screen.queryByText("1/1")).toBeNull();
    expect(screen.queryByText("member")).toBeNull();
    expect(screen.queryByText("self")).toBeNull();
    expect(screen.queryByText("local")).toBeNull();
  });

  it("reports when no workspaces exist", async () => {
    const onWorkspaceListStateChange = vi.fn();
    mockGet.mockImplementation((path: string) => {
      if (path === "/snapshot") {
        return Promise.resolve({
          data: {
            hosts: [
              {
                configKey: "member",
                diagnostics: [],
                id: "member",
                kind: "self",
                name: "member",
                operationAvailability: {},
                platform: "linux",
                preferredTransport: "local",
                reachable: true,
                tmuxSessions: [],
              },
            ],
          },
        });
      }
      return Promise.resolve({ data: { workspaces: [] } });
    });

    render(WorkspaceListSidebar, {
      props: { selectedId: "", onWorkspaceListStateChange },
    });

    expect(await screen.findByText("No workspaces yet.")).toBeTruthy();
    await waitFor(() => {
      expect(onWorkspaceListStateChange).toHaveBeenLastCalledWith({
        status: "loaded",
        total: 0,
      });
    });
  });

  it("loads workspaces from reachable ssh fleet hosts", async () => {
    mockGet.mockImplementation((path: string, options?: { params?: { path?: { host_key?: string } } }) => {
      if (path === "/snapshot") {
        return Promise.resolve({
          data: {
            hosts: [
              {
                configKey: "hub",
                diagnostics: [],
                id: "hub",
                kind: "self",
                name: "hub",
                operationAvailability: {},
                platform: "darwin",
                preferredTransport: "local",
                reachable: true,
                tmuxSessions: [],
              },
              {
                configKey: "epyc",
                diagnostics: [],
                id: "epyc",
                kind: "remote",
                name: "epyc",
                operationAvailability: {},
                platform: "linux",
                preferredTransport: "ssh",
                reachable: true,
                tmuxSessions: [],
              },
            ],
          },
        });
      }
      if (path === "/fleet/hosts/{host_key}/workspaces") {
        expect(options?.params?.path?.host_key).toBe("epyc");
        return Promise.resolve({
          data: {
            workspaces: [
              workspaceFixture({
                id: "remote-ws",
                provider: "github",
                platformHost: "github.com",
                owner: "remote",
                name: "service",
                number: 12,
                title: "Remote SSH workspace",
              }),
            ],
          },
        });
      }
      return Promise.resolve({ data: { workspaces: [] } });
    });

    render(WorkspaceListSidebar, {
      props: { selectedId: "" },
    });

    await waitFor(() => {
      expect(mockGet).toHaveBeenCalledWith(
        "/fleet/hosts/{host_key}/workspaces",
        expect.objectContaining({
          params: { path: { host_key: "epyc" } },
        }),
      );
    });
    expect(await screen.findByText("Remote SSH workspace")).toBeTruthy();
  });

  it("removes remote workspaces when the fleet snapshot becomes local-only", async () => {
    vi.useFakeTimers();
    const stalledLocalRefresh = deferred<{ data: { workspaces: unknown[] } }>();
    let localWorkspaceCalls = 0;
    let snapshotCalls = 0;

    mockGet.mockImplementation((path: string, options?: { params?: { path?: { host_key?: string } } }) => {
      if (path === "/snapshot") {
        snapshotCalls += 1;
        return Promise.resolve({
          data: {
            hosts:
              snapshotCalls === 1
                ? [
                    {
                      configKey: "hub",
                      diagnostics: [],
                      id: "hub",
                      kind: "self",
                      name: "hub",
                      operationAvailability: {},
                      platform: "darwin",
                      preferredTransport: "local",
                      reachable: true,
                      tmuxSessions: [],
                    },
                    {
                      configKey: "epyc",
                      diagnostics: [],
                      id: "epyc",
                      kind: "remote",
                      name: "epyc",
                      operationAvailability: {},
                      platform: "linux",
                      preferredTransport: "ssh",
                      reachable: true,
                      tmuxSessions: [],
                    },
                  ]
                : [
                    {
                      configKey: "hub",
                      diagnostics: [],
                      id: "hub",
                      kind: "self",
                      name: "hub",
                      operationAvailability: {},
                      platform: "darwin",
                      preferredTransport: "local",
                      reachable: true,
                      tmuxSessions: [],
                    },
                  ],
          },
        });
      }
      if (path === "/fleet/hosts/{host_key}/workspaces") {
        expect(options?.params?.path?.host_key).toBe("epyc");
        return Promise.resolve({
          data: {
            workspaces: [
              workspaceFixture({
                id: "remote-ws",
                provider: "github",
                platformHost: "github.com",
                owner: "remote",
                name: "service",
                number: 12,
                title: "Remote SSH workspace",
              }),
            ],
          },
        });
      }
      if (path === "/workspaces") {
        localWorkspaceCalls += 1;
        if (localWorkspaceCalls >= 3) {
          return stalledLocalRefresh.promise;
        }
        return Promise.resolve({
          data: {
            workspaces: [
              workspaceFixture({
                id: "local-ws",
                provider: "github",
                platformHost: "github.com",
                owner: "local",
                name: "service",
                number: 1,
                title: "Local workspace",
              }),
            ],
          },
        });
      }
      return Promise.resolve({ data: { workspaces: [] } });
    });

    render(WorkspaceListSidebar, {
      props: { selectedId: "" },
    });

    await vi.advanceTimersByTimeAsync(5_000);
    expect(await screen.findByText("Remote SSH workspace")).toBeTruthy();

    await vi.advanceTimersByTimeAsync(10_000);
    await tick();

    await waitFor(() => {
      expect(screen.queryByText("Remote SSH workspace")).toBeNull();
    });
    expect(screen.getByText("Local workspace")).toBeTruthy();
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

  it("keeps same-host repos from different providers in separate groups", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-gitea",
            provider: "gitea",
            platformHost: "code.example.com",
            owner: "acme",
            name: "widgets",
            number: 1,
            title: "Gitea widgets",
          }),
          workspaceFixture({
            id: "ws-forgejo",
            provider: "forgejo",
            platformHost: "code.example.com",
            owner: "acme",
            name: "widgets",
            number: 2,
            title: "Forgejo widgets",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-gitea" },
    });
    await screen.findByText("Gitea widgets");

    // Identical host and repo path, different providers: the rows must
    // not collapse into a single group whose label and icon come from
    // only the first item. Each provider keeps its own group + icon.
    expect(container.querySelectorAll(".group-header")).toHaveLength(2);
    expect(screen.getByRole("img", { name: "Gitea" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "Forgejo" })).toBeTruthy();
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

    await fireEvent.click(screen.getByTitle("View workspace options"));
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

    await fireEvent.click(screen.getByTitle("View workspace options"));
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

    await fireEvent.click(screen.getByTitle("View workspace options"));
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

    await fireEvent.click(screen.getByTitle("View workspace options"));
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

    await fireEvent.click(screen.getByTitle("View workspace options"));
    await fireEvent.click(screen.getByRole("button", { name: "Activity" }));
    first.unmount();

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-new" },
    });
    await screen.findByText("Newest created");

    expect(rowTitles(container)).toEqual(["Most recently active", "Newest created", "Oldest without activity"]);
    expect(container.querySelectorAll(".group-header")).toHaveLength(0);
  });

  it("folds sort choices into the workspace view menu", async () => {
    mockGet.mockResolvedValue({
      data: { workspaces: sortFixtures() },
    });

    render(WorkspaceListSidebar, {
      props: { selectedId: "ws-new" },
    });
    await screen.findByText("Newest created");

    await fireEvent.click(screen.getByRole("button", { name: "View" }));

    expect(screen.getByText("Sorting")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Org / repo" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Created" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Activity" })).toBeTruthy();
    expect(screen.getByText("Visibility")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Show org names" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Show PR diff stats" })).toBeTruthy();
  });

  it("can hide org names in grouped and flat workspace labels", async () => {
    mockGet.mockResolvedValue({
      data: { workspaces: sortFixtures() },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-new" },
    });
    await screen.findByText("kenn-io/middleman");

    await fireEvent.click(screen.getByRole("button", { name: "View" }));
    await fireEvent.click(screen.getByRole("button", { name: "Show org names" }));

    expect(screen.queryByText("kenn-io/middleman")).toBeNull();
    expect(screen.getByText("middleman")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Created" }));

    expect(container.querySelectorAll(".repo-context-name")[0]?.textContent?.trim()).toBe("middleman");
    expect(container.querySelectorAll(".repo-context-name")[1]?.textContent?.trim()).toBe("agentsview");
  });

  it("keeps hidden-org workspace repo labels distinguishable", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-github-acme",
            provider: "github",
            platformHost: "github.com",
            owner: "acme",
            name: "widgets",
            number: 1,
            title: "GitHub acme widgets",
            createdAt: "2026-05-12T12:00:00Z",
          }),
          workspaceFixture({
            id: "ws-ghe-acme",
            provider: "github",
            platformHost: "ghe.example.com",
            owner: "acme",
            name: "widgets",
            number: 2,
            title: "GHE acme widgets",
            createdAt: "2026-05-11T12:00:00Z",
          }),
          workspaceFixture({
            id: "ws-platform",
            provider: "gitlab",
            platformHost: "gitlab.example.com",
            owner: "platform",
            name: "widgets",
            number: 3,
            title: "Platform widgets",
            createdAt: "2026-05-10T12:00:00Z",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-github-acme" },
    });
    await screen.findByText("GitHub acme widgets");

    await fireEvent.click(screen.getByRole("button", { name: "View" }));
    await fireEvent.click(screen.getByRole("button", { name: "Show org names" }));

    expect(Array.from(container.querySelectorAll(".group-label")).map((el) => el.textContent?.trim())).toEqual([
      "github.com/acme/widgets",
      "ghe.example.com/acme/widgets",
      "platform/widgets",
    ]);

    await fireEvent.click(screen.getByRole("button", { name: "Created" }));

    expect(Array.from(container.querySelectorAll(".repo-context-name")).map((el) => el.textContent?.trim())).toEqual([
      "github.com/acme/widgets",
      "ghe.example.com/acme/widgets",
      "platform/widgets",
    ]);
  });

  it("keeps same-host different-provider repo groups separate", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-github-acme",
            provider: "github",
            platformHost: "github.com",
            owner: "acme",
            name: "widgets",
            number: 1,
            title: "GitHub acme widgets",
          }),
          workspaceFixture({
            id: "ws-gitea-acme",
            provider: "gitea",
            platformHost: "github.com",
            owner: "acme",
            name: "widgets",
            number: 2,
            title: "Gitea acme widgets",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-github-acme" },
    });
    await screen.findByText("GitHub acme widgets");

    expect(Array.from(container.querySelectorAll(".group-label")).map((el) => el.textContent?.trim())).toEqual([
      "github/github.com/acme/widgets",
      "gitea/github.com/acme/widgets",
    ]);
    expect(Array.from(container.querySelectorAll(".group-count")).map((el) => el.textContent?.trim())).toEqual([
      "1",
      "1",
    ]);
  });

  it("can hide PR diff stats while keeping branch metadata visible", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-diff",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 9,
            title: "Diff-heavy workspace",
            additions: 42,
            deletions: 7,
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-diff" },
    });
    await screen.findByText("Diff-heavy workspace");

    expect(container.querySelector(".workspace-diff-stats")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "View" }));
    await fireEvent.click(screen.getByRole("button", { name: "Show PR diff stats" }));

    expect(container.querySelector(".workspace-diff-stats")).toBeNull();
    expect(container.querySelector(".branch-chip")).toBeTruthy();
  });

  it("opens a host-aware context menu for local macOS workspaces", async () => {
    mockGet.mockImplementation((path: string) => {
      if (path === "/snapshot") {
        return Promise.resolve({
          data: {
            hosts: [
              {
                configKey: "hub",
                diagnostics: [],
                id: "hub",
                kind: "self",
                name: "hub",
                operationAvailability: {},
                platform: "darwin",
                preferredTransport: "local",
                reachable: true,
                tmuxSessions: [],
              },
            ],
          },
        });
      }
      return Promise.resolve({
        data: {
          workspaces: [
            workspaceFixture({
              id: "ws-local",
              provider: "github",
              platformHost: "github.com",
              owner: "kenn-io",
              name: "middleman",
              number: 555,
              title: "Local mac workspace",
            }),
          ],
        },
      });
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-local" },
    });
    await screen.findByText("Local mac workspace");

    await fireEvent.contextMenu(container.querySelector(".ws-row")!);

    expect(screen.getByRole("menu", { name: "Workspace actions" })).toBeTruthy();
    expect(screen.queryByText("Copy")).toBeNull();
    expect(screen.queryByText("Provider")).toBeNull();
    expect(screen.getByRole("menuitem", { name: "Copy worktree path" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "Reveal in Finder" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "Refresh git status" })).toBeTruthy();
  });

  function kataWorkspaceFixture(overrides: Partial<WorkspaceFixtureOptions> = {}) {
    return workspaceFixture({
      id: "ws-kata",
      provider: "github",
      platformHost: "github.com",
      owner: "kenn-io",
      name: "middleman",
      number: 0,
      branch: "middleman/kata/task-123-abcd1234",
      itemType: "kata_task",
      itemKey: "kata:ZGVza3RvcA:cHJvamVjdC1rYXRh:aXNzdWUta2F0YS0x",
      kata: {
        daemon_id: "desktop",
        project_uid: "project-kata",
        project_name: "Middleman",
        issue_uid: "issue-kata-1",
        short_id: "task-123",
        qualified_id: "Kata#task-123",
        title: "Wire kata workspace sidebar",
      },
      ...overrides,
    });
  }

  it("renders Kata task identity and opens the kata sidebar tab", async () => {
    mockGet.mockResolvedValue({
      data: { workspaces: [kataWorkspaceFixture()] },
    });
    const onOpenItemSidebar = vi.fn();

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-kata", onOpenItemSidebar },
    });
    await screen.findByText("Wire kata workspace sidebar");

    const bubble = container.querySelector(".item-bubble");
    expect(bubble).not.toBeNull();
    expect(bubble!.classList.contains("kata")).toBe(true);
    expect(bubble!.textContent?.trim()).toBe("task-123");
    // A Kata task has no provider item number, so the row must not show #0.
    expect(container.textContent).not.toContain("#0");

    await fireEvent.click(bubble!);
    expect(onOpenItemSidebar).toHaveBeenCalledWith("ws-kata", "kata_task", undefined);
  });

  it("gives a draft pull request bubble the draft state class, not open", async () => {
    // A draft PR must read as draft in the sidebar bubble (amber draft
    // styling) instead of falling through to the open/green treatment, so
    // the chip reflects the same draft status shown in the PR detail view.
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-draft",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 241,
            title: "Plan ACP agent chat integration",
            isDraft: true,
          }),
          workspaceFixture({
            id: "ws-open",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 242,
            title: "Ready for review PR",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-draft" },
    });
    await screen.findByText("Plan ACP agent chat integration");

    const bubbles = Array.from(container.querySelectorAll(".item-bubble"));
    const draftBubble = bubbles.find((b) => b.textContent?.trim() === "#241");
    const openBubble = bubbles.find((b) => b.textContent?.trim() === "#242");

    expect(draftBubble?.classList.contains("draft")).toBe(true);
    expect(draftBubble?.classList.contains("open")).toBe(false);
    expect(openBubble?.classList.contains("open")).toBe(true);
    expect(openBubble?.classList.contains("draft")).toBe(false);
  });

  it("omits provider item actions in the Kata workspace context menu", async () => {
    mockGet.mockResolvedValue({
      data: { workspaces: [kataWorkspaceFixture()] },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-kata" },
    });
    await screen.findByText("Wire kata workspace sidebar");

    await fireEvent.contextMenu(container.querySelector(".ws-row")!);

    expect(screen.getByRole("menu", { name: "Workspace actions" })).toBeTruthy();
    expect(screen.queryByRole("menuitem", { name: /Open item on/ })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Copy item URL" })).toBeNull();
  });

  it("filters a Kata workspace by its task identity", async () => {
    mockGet.mockResolvedValue({
      data: { workspaces: [kataWorkspaceFixture()] },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-kata" },
    });
    const filter = await screen.findByLabelText("Filter workspaces");

    await fireEvent.input(filter, { target: { value: "task-123" } });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(1);

    await fireEvent.input(filter, { target: { value: "no-such-task" } });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(0);
  });

  it("finds a Kata workspace by its task UID when it has no short ID", async () => {
    // A Kata task without a short/qualified ID renders the generic "Kata"
    // bubble, so it must stay findable by its durable identifiers.
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          kataWorkspaceFixture({
            kata: {
              daemon_id: "desktop",
              project_uid: "project-kata",
              project_name: "Middleman",
              issue_uid: "issue-kata-1",
            },
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-kata" },
    });
    const filter = await screen.findByLabelText("Filter workspaces");

    const bubble = container.querySelector(".item-bubble");
    expect(bubble!.textContent?.trim()).toBe("Kata");

    await fireEvent.input(filter, { target: { value: "issue-kata-1" } });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(1);

    await fireEvent.input(filter, { target: { value: "project-kata" } });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(1);
  });

  it("pushes an ahead workspace branch and shows a busy state while pending", async () => {
    const push = deferred<{
      error?: unknown;
      response: { ok: boolean; status: number };
    }>();
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-ahead",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 9,
            title: "Ahead workspace",
            commitsAhead: 2,
          }),
        ],
      },
    });
    mockPost.mockImplementation((path: string) => {
      if (path === "/workspaces/{id}/push") return push.promise;
      return Promise.resolve({ data: {}, response: { ok: true, status: 200 } });
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-ahead" },
    });
    await screen.findByText("Ahead workspace");

    await fireEvent.contextMenu(container.querySelector(".ws-row")!);

    await fireEvent.click(screen.getByRole("menuitem", { name: /Push branch/ }));

    expect(mockPost).toHaveBeenCalledWith("/workspaces/{id}/push", {
      params: { path: { id: "ws-ahead" } },
    });
    expect((screen.getByRole("menuitem", { name: /Pushing\.\.\./ }) as HTMLButtonElement).disabled).toBe(true);

    push.resolve({ response: { ok: true, status: 200 } });
    await waitFor(() => {
      expect(screen.queryByRole("menuitem", { name: /Pushing\.\.\./ })).toBeNull();
    });
  });

  it("pulls a behind workspace branch and shows a busy state while pending", async () => {
    const pull = deferred<{
      error?: unknown;
      response: { ok: boolean; status: number };
    }>();
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-behind",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 9,
            title: "Behind workspace",
            commitsBehind: 1,
          }),
        ],
      },
    });
    mockPost.mockImplementation((path: string) => {
      if (path === "/workspaces/{id}/pull") return pull.promise;
      return Promise.resolve({ data: {}, response: { ok: true, status: 200 } });
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-behind" },
    });
    await screen.findByText("Behind workspace");

    await fireEvent.contextMenu(container.querySelector(".ws-row")!);
    await fireEvent.click(screen.getByRole("menuitem", { name: /Pull remote changes/ }));

    expect(mockPost).toHaveBeenCalledWith("/workspaces/{id}/pull", {
      params: { path: { id: "ws-behind" } },
    });
    expect((screen.getByRole("menuitem", { name: /Pulling\.\.\./ }) as HTMLButtonElement).disabled).toBe(true);

    pull.resolve({ response: { ok: true, status: 200 } });
    await waitFor(() => {
      expect(screen.queryByRole("menuitem", { name: /Pulling\.\.\./ })).toBeNull();
    });
  });

  it("opens a local workspace path from the context menu", async () => {
    mockGet.mockImplementation((path: string) => {
      if (path === "/snapshot") {
        return Promise.resolve({
          data: {
            hosts: [
              {
                configKey: "hub",
                diagnostics: [],
                id: "hub",
                kind: "self",
                name: "hub",
                operationAvailability: {},
                platform: "darwin",
                preferredTransport: "local",
                reachable: true,
                tmuxSessions: [],
              },
            ],
          },
        });
      }
      return Promise.resolve({
        data: {
          workspaces: [
            workspaceFixture({
              id: "ws-reveal",
              provider: "github",
              platformHost: "github.com",
              owner: "kenn-io",
              name: "middleman",
              number: 12,
              title: "Reveal me",
            }),
          ],
        },
      });
    });
    mockPost.mockResolvedValue({
      error: undefined,
      response: { ok: true, status: 204 },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-reveal" },
    });
    await screen.findByText("Reveal me");

    await fireEvent.contextMenu(container.querySelector(".ws-row")!);
    await fireEvent.click(screen.getByRole("menuitem", { name: "Reveal in Finder" }));

    await waitFor(() => {
      expect(mockPost).toHaveBeenCalledWith("/workspaces/{id}/reveal", {
        params: { path: { id: "ws-reveal" } },
      });
    });
  });

  it("deletes a workspace from the context menu after in-app confirmation", async () => {
    vi.stubGlobal(
      "confirm",
      vi.fn(() => {
        throw new Error("native confirm should not be used");
      }),
    );
    mockGet.mockImplementation((path: string) => {
      if (path === "/snapshot") {
        return Promise.resolve({
          data: {
            hosts: [
              {
                configKey: "hub",
                diagnostics: [],
                id: "hub",
                kind: "self",
                name: "hub",
                operationAvailability: {},
                platform: "darwin",
                preferredTransport: "local",
                reachable: true,
                tmuxSessions: [],
              },
            ],
          },
        });
      }
      return Promise.resolve({
        data: {
          workspaces: [
            workspaceFixture({
              id: "ws-delete",
              provider: "github",
              platformHost: "github.com",
              owner: "kenn-io",
              name: "middleman",
              number: 10,
              title: "Delete me",
            }),
          ],
        },
      });
    });
    mockDelete.mockResolvedValue({
      error: undefined,
      response: { ok: true, status: 204 },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-delete" },
    });
    await screen.findByText("Delete me");

    await fireEvent.contextMenu(container.querySelector(".ws-row")!);
    await fireEvent.click(screen.getByRole("menuitem", { name: "Delete workspace..." }));

    const dialog = await screen.findByRole("dialog", { name: "Delete workspace?" });
    expect(dialog.textContent).toContain('Delete workspace "Delete me"?');
    expect(dialog.textContent).toContain("This removes its managed worktree and runtime sessions.");
    expect(window.confirm).not.toHaveBeenCalled();
    await fireEvent.click(within(dialog).getByRole("button", { name: "Delete workspace" }));

    await waitFor(() => {
      expect(mockDelete).toHaveBeenCalledWith("/workspaces/{id}", {
        params: { path: { id: "ws-delete" } },
      });
    });
    expect(mockNavigate).toHaveBeenCalledWith("/workspaces");
  });

  it("keeps a workspace when context menu deletion is cancelled in-app", async () => {
    vi.stubGlobal(
      "confirm",
      vi.fn(() => {
        throw new Error("native confirm should not be used");
      }),
    );
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-keep",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 11,
            title: "Keep me",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-keep" },
    });
    await screen.findByText("Keep me");

    await fireEvent.contextMenu(container.querySelector(".ws-row")!);
    await fireEvent.click(screen.getByRole("menuitem", { name: "Delete workspace..." }));
    const dialog = await screen.findByRole("dialog", { name: "Delete workspace?" });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("dialog", { name: "Delete workspace?" })).toBeNull();
    expect(window.confirm).not.toHaveBeenCalled();
    expect(mockDelete).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("omits local filesystem actions for remote workspace context menus", async () => {
    mockGet.mockImplementation((path: string, options?: { params?: { path?: { host_key?: string } } }) => {
      if (path === "/snapshot") {
        return Promise.resolve({
          data: {
            hosts: [
              {
                configKey: "hub",
                diagnostics: [],
                id: "hub",
                kind: "self",
                name: "hub",
                operationAvailability: {},
                platform: "darwin",
                preferredTransport: "local",
                reachable: true,
                tmuxSessions: [],
              },
              {
                configKey: "epyc",
                diagnostics: [],
                id: "epyc",
                kind: "remote",
                name: "epyc",
                operationAvailability: {},
                platform: "linux",
                preferredTransport: "ssh",
                reachable: true,
                tmuxSessions: [],
              },
            ],
          },
        });
      }
      if (path === "/fleet/hosts/{host_key}/workspaces") {
        expect(options?.params?.path?.host_key).toBe("epyc");
        return Promise.resolve({
          data: {
            workspaces: [
              workspaceFixture({
                id: "ws-remote",
                provider: "github",
                platformHost: "github.com",
                owner: "remote",
                name: "service",
                number: 12,
                title: "Remote workspace",
              }),
            ],
          },
        });
      }
      return Promise.resolve({ data: { workspaces: [] } });
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-remote", selectedHostKey: "epyc" },
    });
    await screen.findByText("Remote workspace");

    await fireEvent.contextMenu(container.querySelector(".ws-row")!);

    expect(screen.getByRole("menu", { name: "Workspace actions" })).toBeTruthy();
    expect(screen.queryByRole("menuitem", { name: "Copy worktree path" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Reveal in Finder" })).toBeNull();
    expect(screen.getByRole("menuitem", { name: "Refresh git status" })).toBeTruthy();
  });

  it("does not show push or pull commands for diverged workspace branches", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-diverged",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 9,
            title: "Diverged workspace",
            commitsAhead: 1,
            commitsBehind: 2,
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-diverged" },
    });
    await screen.findByText("Diverged workspace");

    await fireEvent.contextMenu(container.querySelector(".ws-row")!);

    expect(screen.queryByRole("menuitem", { name: "Push branch" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Pull remote changes" })).toBeNull();
    expect(screen.getByRole("menuitem", { name: "Refresh git status" })).toBeTruthy();
  });
});
