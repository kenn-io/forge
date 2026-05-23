import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

const mockRefreshSyncStatus = vi.fn();

vi.mock("@middleman/ui", () => ({
  getStores: () => ({
    sync: {
      refreshSyncStatus: mockRefreshSyncStatus,
    },
  }),
}));

vi.mock("../../api/settings.js", () => ({
  addRepo: vi.fn(),
  removeRepo: vi.fn(),
  getSettings: vi.fn(),
  refreshRepo: vi.fn(),
  previewRepos: vi.fn(),
  bulkAddRepos: vi.fn(),
}));

import {
  addRepo,
  bulkAddRepos,
  previewRepos,
  refreshRepo,
} from "../../api/settings.js";
import RepoSettings from "./RepoSettings.svelte";

const mockAddRepo = vi.mocked(addRepo);
const mockRefreshRepo = vi.mocked(refreshRepo);
const mockPreviewRepos = vi.mocked(previewRepos);
const mockBulkAddRepos = vi.mocked(bulkAddRepos);

describe("RepoSettings", () => {
  afterEach(() => {
    cleanup();
    mockRefreshSyncStatus.mockReset();
    mockAddRepo.mockReset();
    mockRefreshRepo.mockReset();
    mockPreviewRepos.mockReset();
    mockBulkAddRepos.mockReset();
  });

  it("renders the glob count and refresh action", () => {
    render(RepoSettings, {
      props: {
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "roborev-dev",
            name: "*",
            repo_path: "roborev-dev/*",
            is_glob: true,
            matched_repo_count: 2,
          },
        ],
        onUpdate: vi.fn(),
      },
    });

    expect(
      screen.getByText(
        (_, element) => element?.textContent === "roborev-dev/* (2)",
      ),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy();
  });

  it("shows provider icons when configured repos use multiple providers", () => {
    render(RepoSettings, {
      props: {
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "widgets",
            repo_path: "acme/widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
          {
            provider: "forgejo",
            platform_host: "codeberg.org",
            owner: "forge",
            name: "service",
            repo_path: "forge/service",
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        onUpdate: vi.fn(),
      },
    });

    expect(screen.getByRole("img", { name: "GitHub" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "Forgejo" })).toBeTruthy();
  });

  it("hides provider icons when configured repos use one provider", () => {
    render(RepoSettings, {
      props: {
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "widgets",
            repo_path: "acme/widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
          {
            provider: "github",
            platform_host: "ghe.example.com",
            owner: "enterprise",
            name: "service",
            repo_path: "enterprise/service",
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        onUpdate: vi.fn(),
      },
    });

    expect(screen.queryByRole("img", { name: "GitHub" })).toBeNull();
  });

  it("opens the repository import modal and restores focus on close", async () => {
    render(RepoSettings, {
      props: {
        repos: [],
        onUpdate: vi.fn(),
      },
    });

    const trigger = screen.getByRole("button", { name: "Add repositories…" });
    await fireEvent.click(trigger);

    expect(
      screen.getByRole("dialog", { name: "Add repositories" }),
    ).toBeTruthy();
    expect(screen.getByLabelText("Repository pattern")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it("keeps direct glob add in an advanced section", () => {
    render(RepoSettings, {
      props: {
        repos: [],
        onUpdate: vi.fn(),
      },
    });

    const summary = screen.getByText(
      "Advanced: add provider-scoped repo or tracking glob directly",
    );
    expect(summary).toBeTruthy();
    expect(summary.closest("details")?.hasAttribute("open")).toBe(false);
  });

  it("forwards add/refresh through the settings API", async () => {
    mockAddRepo.mockResolvedValue({
      repos: [],
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        renderer: "xterm",
      },
      agents: [],
    });
    mockRefreshRepo.mockResolvedValue({
      repos: [],
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        renderer: "xterm",
      },
      agents: [],
    });

    render(RepoSettings, {
      props: {
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "*",
            repo_path: "acme/*",
            is_glob: true,
            matched_repo_count: 1,
          },
        ],
        onUpdate: vi.fn(),
      },
    });

    const input = screen.getByPlaceholderText("provider/owner/name");
    await fireEvent.input(input, { target: { value: "github/acme/widget" } });
    await fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(mockAddRepo).toHaveBeenCalledWith("acme", "widget", {
      provider: "github",
    });

    await fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(mockRefreshRepo).toHaveBeenCalledWith("acme", "*", {
      provider: "github",
      host: "github.com",
    });
  });

  it("updates repos and refreshes sync status after import", async () => {
    const importedRepos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "api",
        repo_path: "acme/api",
        is_glob: false,
        matched_repo_count: 1,
      },
    ];
    const onUpdate = vi.fn();
    mockPreviewRepos.mockResolvedValue({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
          description: "HTTP API",
          private: false,
          fork: false,
          pushed_at: null,
          already_configured: false,
        },
      ],
    });
    mockBulkAddRepos.mockResolvedValue({
      repos: importedRepos,
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        renderer: "xterm",
      },
      agents: [],
    });
    render(RepoSettings, {
      props: {
        repos: [],
        onUpdate,
      },
    });

    await fireEvent.click(
      screen.getByRole("button", { name: "Add repositories…" }),
    );
    await fireEvent.input(screen.getByLabelText("Repository pattern"), {
      target: { value: "acme/*" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByText("acme/api");
    await fireEvent.click(
      screen.getByRole("button", { name: "Add selected repositories" }),
    );

    await waitFor(() => expect(onUpdate).toHaveBeenCalledWith(importedRepos));
    expect(mockRefreshSyncStatus).toHaveBeenCalled();
  });
});
