import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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
    created_at: "2026-05-12T12:00:00Z",
    mr_title: title,
    mr_state: "open",
  };
}

describe("WorkspaceListSidebar", () => {
  beforeEach(() => {
    mockGet.mockReset();
    mockNavigate.mockReset();
    vi.stubGlobal("EventSource", MockEventSource);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
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

    render(WorkspaceListSidebar, { props: { selectedId: "ws-github" } });

    await screen.findByText("acme/widgets");
    expect(screen.getByRole("img", { name: "GitHub" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "GitLab" })).toBeTruthy();
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

    render(WorkspaceListSidebar, { props: { selectedId: "ws-github" } });

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
            name: "kataflow",
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
    expect(
      screen.getByText("Migrate native HTTP surface to Huma v2"),
    ).toBeTruthy();

    await fireEvent.input(filter, {
      target: { value: "kenn-platform" },
    });
    expect(container.querySelectorAll(".ws-row")).toHaveLength(1);
    expect(
      screen.getByText("Hosted code fetch and caching strategy"),
    ).toBeTruthy();

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
});
