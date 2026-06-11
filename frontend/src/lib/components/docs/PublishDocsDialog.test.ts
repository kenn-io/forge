import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import PublishDocsDialog from "./PublishDocsDialog.svelte";
import type { DocsAPI } from "../../api/docs/api";
import type { GitPublishResponse } from "../../api/docs/types";

function fakeApi(overrides: Partial<DocsAPI> = {}): DocsAPI {
  const base: Partial<DocsAPI> = {
    gitChanges: async () => ({
      is_repo: true,
      branch: "main",
      upstream: "origin/main",
      changes: [{ path: "new.md", status: "untracked" }],
      ignored_non_markdown_count: 0,
      suggested_message: "docs: update new.md\n\n- new.md\n",
    }),
    gitPublish: async () => ({
      commit: "abcdef1234567890abcdef1234567890abcdef12",
      short_commit: "abcdef1",
      branch: "main",
      upstream: "origin/main",
      pushed: true,
      files: [{ path: "new.md", status: "untracked" }],
    }),
  };
  return { ...base, ...overrides } as DocsAPI;
}

describe("PublishDocsDialog", () => {
  afterEach(() => cleanup());

  test("renders the suggested message and the file list once preview loads", async () => {
    render(PublishDocsDialog, {
      props: {
        open: true,
        folderID: "notes",
        api: fakeApi(),
        onClose: () => {},
        onPublished: () => {},
      },
    });
    const textarea = await screen.findByRole("textbox", { name: /commit message/i });
    expect((textarea as HTMLTextAreaElement).value).toContain("docs: update new.md");
    expect(screen.getByText("new.md")).toBeTruthy();
  });

  test("not-a-repo state hides the form and shows the explanation", async () => {
    render(PublishDocsDialog, {
      props: {
        open: true,
        folderID: "notes",
        api: fakeApi({
          gitChanges: async () => ({
            is_repo: false,
            changes: [],
            ignored_non_markdown_count: 0,
          }),
        }),
        onClose: () => {},
        onPublished: () => {},
      },
    });
    expect(await screen.findByText(/not a git repository/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /commit & push/i })).toBeNull();
  });

  test("no-changes state disables the Commit & Push button", async () => {
    render(PublishDocsDialog, {
      props: {
        open: true,
        folderID: "notes",
        api: fakeApi({
          gitChanges: async () => ({
            is_repo: true,
            branch: "main",
            upstream: "origin/main",
            changes: [],
            ignored_non_markdown_count: 0,
          }),
        }),
        onClose: () => {},
        onPublished: () => {},
      },
    });
    const button = await screen.findByRole("button", { name: /commit & push/i });
    expect((button as HTMLButtonElement).disabled).toBe(true);
  });

  test("asset limitation note is visible when there are changes", async () => {
    render(PublishDocsDialog, {
      props: {
        open: true,
        folderID: "notes",
        api: fakeApi(),
        onClose: () => {},
        onPublished: () => {},
      },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    expect(screen.getByText(/Only Markdown files will be committed/i)).toBeTruthy();
  });

  test("successful publish calls onPublished with the final file count and short SHA", async () => {
    const onPublished = vi.fn();
    render(PublishDocsDialog, {
      props: {
        open: true,
        folderID: "notes",
        api: fakeApi(),
        onClose: () => {},
        onPublished,
      },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    await fireEvent.click(screen.getByRole("button", { name: /commit & push/i }));
    await waitFor(() => expect(onPublished).toHaveBeenCalledTimes(1));
    const arg = onPublished.mock.calls[0]![0];
    expect(arg.short_commit).toBe("abcdef1");
    expect(arg.files).toHaveLength(1);
  });

  test("push_failed_after_commit keeps the dialog open with a recovery message", async () => {
    const api = fakeApi({
      gitPublish: async () => {
        const err = new Error("push failed: timeout") as Error & {
          status?: number;
          code?: string;
          commit?: string;
        };
        err.status = 500;
        err.code = "push_failed_after_commit";
        err.commit = "abcdef1234567890abcdef1234567890abcdef12";
        throw err;
      },
    });
    const onPublished = vi.fn();
    const onClose = vi.fn();
    render(PublishDocsDialog, {
      props: { open: true, folderID: "notes", api, onClose, onPublished },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    await fireEvent.click(screen.getByRole("button", { name: /commit & push/i }));
    await waitFor(() => expect(screen.getByText(/Committed abcdef1 locally, but push failed/i)).toBeTruthy());
    expect(onClose).not.toHaveBeenCalled();
    expect(onPublished).not.toHaveBeenCalled();
  });

  test("index_not_clean renders actionable guidance", async () => {
    const api = fakeApi({
      gitPublish: async () => {
        const err = new Error("partial.md has partially staged hunks") as Error & {
          status?: number;
          code?: string;
        };
        err.status = 409;
        err.code = "index_not_clean";
        throw err;
      },
    });
    render(PublishDocsDialog, {
      props: {
        open: true,
        folderID: "notes",
        api,
        onClose: () => {},
        onPublished: () => {},
      },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    await fireEvent.click(screen.getByRole("button", { name: /commit & push/i }));
    await waitFor(() => expect(screen.getByText(/Finish or reset/i)).toBeTruthy());
  });

  test("unsafe_git_config renders a blocked-publish explanation", async () => {
    const api = fakeApi({
      gitPublish: async () => {
        const err = new Error("docs publish refuses repositories with command-bearing git config") as Error & {
          status?: number;
          code?: string;
        };
        err.status = 400;
        err.code = "unsafe_git_config";
        throw err;
      },
    });
    render(PublishDocsDialog, {
      props: {
        open: true,
        folderID: "notes",
        api,
        onClose: () => {},
        onPublished: () => {},
      },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    await fireEvent.click(screen.getByRole("button", { name: /commit & push/i }));
    await waitFor(() => expect(screen.getByText(/command-bearing config or attributes/i)).toBeTruthy());
  });

  test("unsafe_git_config preview failure shows the blocked-publish explanation", async () => {
    const api = fakeApi({
      gitChanges: async () => {
        const err = new Error("docs publish refuses repositories with command-bearing git config") as Error & {
          status?: number;
          code?: string;
        };
        err.status = 400;
        err.code = "unsafe_git_config";
        throw err;
      },
    });
    render(PublishDocsDialog, {
      props: {
        open: true,
        folderID: "notes",
        api,
        onClose: () => {},
        onPublished: () => {},
      },
    });
    await waitFor(() => expect(screen.getByText(/command-bearing config or attributes/i)).toBeTruthy());
    expect(screen.queryByRole("button", { name: /commit & push/i })).toBeNull();
  });

  test("dialog cannot be closed while publishing", async () => {
    let resolvePublish: (v: GitPublishResponse) => void;
    const pendingPublish = new Promise<GitPublishResponse>((r) => {
      resolvePublish = r;
    });
    const onClose = vi.fn();
    render(PublishDocsDialog, {
      props: {
        open: true,
        folderID: "notes",
        api: fakeApi({ gitPublish: () => pendingPublish }),
        onClose,
        onPublished: () => {},
      },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    await fireEvent.click(screen.getByRole("button", { name: /commit & push/i }));
    // While publishing is in flight, the Cancel button must be disabled.
    expect((screen.getByRole("button", { name: /cancel/i }) as HTMLButtonElement).disabled).toBe(true);
    // Exercise the close paths the dialog exposes — the header X button
    // and the Escape key — and confirm each is a no-op while a publish
    // is in flight. Without the guard these used to bypass `disabled`
    // on Cancel and discard publish_failed errors mid-flight.
    await fireEvent.click(screen.getByRole("button", { name: /^close$/i }));
    expect(onClose).not.toHaveBeenCalled();
    await fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    // Now resolve the publish and confirm normal flow resumes.
    resolvePublish!({
      commit: "abcdef1234567890abcdef1234567890abcdef12",
      short_commit: "abcdef1",
      branch: "main",
      upstream: "origin/main",
      pushed: true,
      files: [{ path: "new.md", status: "untracked" }],
    });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });
});
