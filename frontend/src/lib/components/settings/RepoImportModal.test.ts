import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS } from "../../api/types.js";
import type { RepoPreviewResponse, SettingsSnapshot } from "../../stores/settings-workflow.js";
import RepoImportModalRuntimeHarness from "./RepoImportModalRuntimeHarness.svelte";
import { makeStartupSnapshot } from "../../../test/startupSnapshot.js";
import { installOffsetParentStub, removeOffsetParentStub } from "../../../test/stubOffsetParent.js";

beforeAll(installOffsetParentStub);
afterAll(removeOffsetParentStub);

const { preview, bulk } = vi.hoisted(() => ({
  preview: vi.fn(),
  bulk: vi.fn(),
}));

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  const { ApiProblemError } = await import("../../api/effect-errors.js");
  const settingsFailure = (operation: string, cause: unknown) =>
    new ApiProblemError({
      operation,
      problem: {
        code: "upstreamError",
        detail: cause instanceof Error ? cause.message : String(cause),
        type: "about:blank",
      },
    });
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      previewRepos: (owner, pattern, options) =>
        Effect.tryPromise({
          try: () => preview(owner, pattern, options),
          catch: (cause) => settingsFailure("preview repositories", cause),
        }),
      bulkAddRepos: (repos) =>
        Effect.tryPromise({
          try: () => bulk(repos),
          catch: (cause) => settingsFailure("add repositories", cause),
        }),
    }),
  };
});

function renderRepoImportModal(props: {
  open: boolean;
  onClose: () => void;
  onImported: (settings: SettingsSnapshot) => void;
}): void {
  render(RepoImportModalRuntimeHarness, { props });
}

function settingsResponse() {
  return makeStartupSnapshot({
    issues: { hide_bots: false },
    notifications: { enabled: false },
    terminal: { ...DEFAULT_TERMINAL_SETTINGS, font_size: 14 },
  });
}

const rows = [
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "worker",
    repo_path: "acme/worker",
    description: "Background jobs",
    private: false,
    fork: false,
    pushed_at: "2026-04-20T00:00:00Z",
    already_configured: false,
  },
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "api",
    repo_path: "acme/api",
    description: "HTTP API",
    private: true,
    fork: false,
    pushed_at: "2026-04-22T00:00:00Z",
    already_configured: false,
  },
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widget",
    repo_path: "acme/widget",
    description: "Configured",
    private: false,
    fork: true,
    pushed_at: "2026-04-21T00:00:00Z",
    already_configured: true,
  },
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "empty",
    repo_path: "acme/empty",
    description: null,
    private: false,
    fork: false,
    pushed_at: null,
    already_configured: false,
  },
];

describe("RepoImportModal", () => {
  afterEach(() => {
    cleanup();
    preview.mockReset();
    bulk.mockReset();
  });

  it("previews rows and defaults selectable rows to selected", async () => {
    preview.mockResolvedValue({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: rows,
    });
    renderRepoImportModal({ open: true, onClose: vi.fn(), onImported: vi.fn() });

    await fireEvent.input(screen.getByLabelText("Repository pattern"), {
      target: { value: "acme/*" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));

    await screen.findByText("acme/api");
    expect(screen.getByText("Selected 3 of 3")).toBeTruthy();
    expect(
      (
        screen.getByRole("checkbox", {
          name: "Select acme/widget",
        }) as HTMLInputElement
      ).disabled,
    ).toBe(true);
    expect(screen.getByText("Never pushed")).toBeTruthy();
  });

  it("filters, deselects visible rows, and submits remaining selected rows", async () => {
    const onImported = vi.fn();
    preview.mockResolvedValue({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: rows,
    });
    bulk.mockResolvedValue(settingsResponse());
    renderRepoImportModal({ open: true, onClose: vi.fn(), onImported });

    await fireEvent.input(screen.getByLabelText("Repository pattern"), {
      target: { value: "acme/*" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByText("acme/api");

    await fireEvent.input(screen.getByLabelText("Filter repositories"), {
      target: { value: "worker" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "None" }));
    await fireEvent.input(screen.getByLabelText("Filter repositories"), {
      target: { value: "" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add selected repositories" }));

    await waitFor(() =>
      expect(bulk).toHaveBeenCalledWith([
        {
          provider: "github",
          host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
        },
        {
          provider: "github",
          host: "github.com",
          owner: "acme",
          name: "empty",
          repo_path: "acme/empty",
        },
      ]),
    );
    expect(onImported).toHaveBeenCalled();
  });

  it("hides private repositories and forks from preview selection", async () => {
    preview.mockResolvedValue({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: rows,
    });
    bulk.mockResolvedValue(settingsResponse());
    renderRepoImportModal({ open: true, onClose: vi.fn(), onImported: vi.fn() });

    await fireEvent.input(screen.getByLabelText("Repository pattern"), {
      target: { value: "acme/*" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByText("acme/api");

    await fireEvent.click(screen.getByLabelText("Hide private"));
    await fireEvent.click(screen.getByLabelText("Hide forks"));
    expect(screen.queryByText("acme/api")).toBeNull();
    expect(screen.queryByText("acme/widget")).toBeNull();
    expect(screen.getByText("Selected 2 of 2")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Add selected repositories" }));

    await waitFor(() =>
      expect(bulk).toHaveBeenCalledWith([
        {
          provider: "github",
          host: "github.com",
          owner: "acme",
          name: "worker",
          repo_path: "acme/worker",
        },
        {
          provider: "github",
          host: "github.com",
          owner: "acme",
          name: "empty",
          repo_path: "acme/empty",
        },
      ]),
    );
  });

  it("does not start duplicate previews while loading", async () => {
    preview.mockReturnValue(new Promise(() => {}));
    renderRepoImportModal({ open: true, onClose: vi.fn(), onImported: vi.fn() });

    const input = screen.getByLabelText("Repository pattern");
    await fireEvent.input(input, { target: { value: "acme/*" } });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await fireEvent.keyDown(input, { key: "Enter" });

    expect(preview).toHaveBeenCalledTimes(1);
  });

  it("sets Forgejo and Gitea default hosts and keeps owner patterns non-nested", async () => {
    renderRepoImportModal({ open: true, onClose: vi.fn(), onImported: vi.fn() });

    const host = screen.getByLabelText("Host") as HTMLInputElement;
    const pattern = screen.getByLabelText("Repository pattern");

    await fireEvent.click(screen.getByRole("combobox", { name: /Provider/ }));
    await fireEvent.click(screen.getByRole("option", { name: "Forgejo" }));
    expect(host.value).toBe("codeberg.org");
    await fireEvent.input(pattern, {
      target: { value: "team/subgroup/project-*" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Format: owner/pattern");
    expect(preview).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole("combobox", { name: /Provider/ }));
    await fireEvent.click(screen.getByRole("option", { name: "Gitea" }));
    expect(host.value).toBe("gitea.com");
    await fireEvent.input(pattern, {
      target: { value: "team/service-*" },
    });
    preview.mockResolvedValueOnce({
      provider: "gitea",
      platform_host: "gitea.com",
      owner: "team",
      pattern: "service-*",
      repos: [],
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));

    await waitFor(() =>
      expect(preview).toHaveBeenCalledWith("team", "service-*", {
        provider: "gitea",
        host: "gitea.com",
      }),
    );
  });

  it("keeps tab focus inside the modal", async () => {
    renderRepoImportModal({ open: true, onClose: vi.fn(), onImported: vi.fn() });

    const input = screen.getByLabelText("Repository pattern");
    await waitFor(() => expect(document.activeElement).toBe(input));
    const close = screen.getByRole("button", { name: "Close" });
    close.focus();
    await fireEvent.keyDown(close, { key: "Tab", shiftKey: true });

    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Cancel" }));
  });

  it("ignores stale preview responses after input changes", async () => {
    let resolveFirst: (value: RepoPreviewResponse) => void = () => {};
    preview.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveFirst = resolve;
      }),
    );
    renderRepoImportModal({ open: true, onClose: vi.fn(), onImported: vi.fn() });

    await fireEvent.input(screen.getByLabelText("Repository pattern"), {
      target: { value: "acme/*" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await fireEvent.input(screen.getByLabelText("Repository pattern"), {
      target: { value: "acme/api-*" },
    });
    resolveFirst({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: rows,
    });

    await waitFor(() => expect(screen.queryByText("acme/api")).toBeNull());
    expect(screen.getByText("Selected 0 of 0")).toBeTruthy();
  });

  it("clears stale rows on failed preview", async () => {
    preview.mockResolvedValueOnce({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: rows,
    });
    preview.mockRejectedValueOnce(new Error("GitHub API error: boom"));
    renderRepoImportModal({ open: true, onClose: vi.fn(), onImported: vi.fn() });

    await fireEvent.input(screen.getByLabelText("Repository pattern"), {
      target: { value: "acme/*" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByText("acme/api");
    await fireEvent.input(screen.getByLabelText("Repository pattern"), {
      target: { value: "acme/worker*" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));

    expect((await screen.findByRole("alert")).textContent).toContain("GitHub API error: boom");
    expect(screen.queryByText("acme/api")).toBeNull();
  });
});
