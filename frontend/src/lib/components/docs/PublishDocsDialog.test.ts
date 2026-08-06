import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import PublishDocsDialogTestHarness from "./PublishDocsDialogTestHarness.svelte";
import type { DocsAPI } from "../../api/docs/api";
import type { GitPublishResponse } from "../../api/docs/types";
import { DocsWorkflow } from "../../stores/docs-workflow.js";

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
  let runtime: OwnedAppRuntime;

  beforeEach(() => {
    runtime = makeAppRuntime();
  });

  afterEach(async () => {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
  });

  test("renders the suggested message and the file list once preview loads", async () => {
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
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
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
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
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
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
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
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
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
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

  test("queues the folder and message captured by the submit click", async () => {
    const blocker = Promise.withResolvers<void>();
    let blockerStarted = false;
    const blockerExecution = runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* DocsWorkflow;
        yield* workflow.mutate(
          Effect.promise(() => {
            blockerStarted = true;
            return blocker.promise;
          }),
        );
      }),
      { operation: "block Docs publish queue", safeContext: {}, onFailure: () => {} },
    );
    await waitFor(() => expect(blockerStarted).toBe(true));
    const gitPublish = vi.fn(
      async (): Promise<GitPublishResponse> => ({
        commit: "abcdef1234567890abcdef1234567890abcdef12",
        short_commit: "abcdef1",
        branch: "main",
        pushed: true,
        files: [{ path: "new.md", status: "untracked" }],
      }),
    );
    const api = fakeApi({
      gitChanges: async (requestedFolderID) => ({
        is_repo: true,
        branch: "main",
        upstream: "origin/main",
        changes: [{ path: "new.md", status: "untracked" }],
        ignored_non_markdown_count: 0,
        suggested_message: `docs: publish ${requestedFolderID}`,
      }),
      gitPublish,
    });
    const rendered = render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
        open: true,
        folderID: "notes",
        api,
        onClose: () => {},
        onPublished: () => {},
      },
    });
    const textarea = await screen.findByRole("textbox", { name: /commit message/i });
    await fireEvent.input(textarea, { target: { value: "docs: submitted message" } });
    await fireEvent.click(screen.getByRole("button", { name: /commit & push/i }));

    await rendered.rerender({
      runtime,
      open: true,
      folderID: "other",
      api,
      onClose: () => {},
      onPublished: () => {},
    });
    blocker.resolve();
    await Effect.runPromise(blockerExecution.await);

    await waitFor(() => expect(gitPublish).toHaveBeenCalledOnce());
    expect(gitPublish).toHaveBeenCalledWith("notes", "docs: submitted message", expect.any(AbortSignal));
  });

  test("remounted dialog adopts an unfinished publish failure for its folder", async () => {
    const publish = Promise.withResolvers<GitPublishResponse>();
    const gitPublish = vi.fn(() => publish.promise);
    const api = fakeApi({ gitPublish });
    const onClose = vi.fn();
    const first = render(PublishDocsDialogTestHarness, {
      props: { runtime, open: true, folderID: "notes", api, onClose, onPublished: () => {} },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    await fireEvent.click(screen.getByRole("button", { name: /commit & push/i }));
    await waitFor(() => expect(gitPublish).toHaveBeenCalledOnce());
    first.unmount();

    render(PublishDocsDialogTestHarness, {
      props: { runtime, open: true, folderID: "notes", api, onClose, onPublished: () => {} },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    const failure = Object.assign(new Error("push failed: timeout"), {
      status: 500,
      code: "push_failed_after_commit",
      commit: "abcdef1234567890abcdef1234567890abcdef12",
    });
    publish.reject(failure);

    await waitFor(() => expect(screen.getByText(/Committed abcdef1 locally, but push failed/i)).toBeTruthy());
    expect(onClose).not.toHaveBeenCalled();
  });

  test("retained publish failure remains visible when the replacement preview fails", async () => {
    const publish = Promise.withResolvers<GitPublishResponse>();
    const gitPublish = vi.fn(() => publish.promise);
    const firstApi = fakeApi({ gitPublish });
    const first = render(PublishDocsDialogTestHarness, {
      props: { runtime, open: true, folderID: "notes", api: firstApi, onClose: () => {}, onPublished: () => {} },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    await fireEvent.click(screen.getByRole("button", { name: /commit & push/i }));
    await waitFor(() => expect(gitPublish).toHaveBeenCalledOnce());
    first.unmount();

    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
        open: true,
        folderID: "notes",
        api: fakeApi({ gitChanges: async () => Promise.reject(new Error("preview unavailable")) }),
        onClose: () => {},
        onPublished: () => {},
      },
    });
    publish.reject(
      Object.assign(new Error("push failed: timeout"), {
        status: 500,
        code: "push_failed_after_commit",
        commit: "abcdef1234567890abcdef1234567890abcdef12",
      }),
    );

    expect(await screen.findByText(/Committed abcdef1 locally, but push failed/i)).toBeTruthy();
    expect(screen.getByText(/preview unavailable/i)).toBeTruthy();
  });

  test("closing an adopted publish failure acknowledges it before a later session opens", async () => {
    const failure = Object.assign(new Error("push failed: timeout"), {
      status: 500,
      code: "push_failed_after_commit",
      commit: "abcdef1234567890abcdef1234567890abcdef12",
    });
    const api = fakeApi({ gitPublish: async () => Promise.reject(failure) });
    const onClose = vi.fn();
    const rendered = render(PublishDocsDialogTestHarness, {
      props: { runtime, open: true, folderID: "notes", api, onClose, onPublished: () => {} },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    await fireEvent.click(screen.getByRole("button", { name: /commit & push/i }));
    await screen.findByText(/Committed abcdef1 locally, but push failed/i);

    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    await rendered.rerender({ runtime, open: false, folderID: "notes", api, onClose, onPublished: () => {} });
    await rendered.rerender({ runtime, open: true, folderID: "notes", api, onClose, onPublished: () => {} });

    await screen.findByRole("textbox", { name: /commit message/i });
    expect(screen.queryByText(/Committed abcdef1 locally, but push failed/i)).toBeNull();
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
    render(PublishDocsDialogTestHarness, {
      props: { runtime, open: true, folderID: "notes", api, onClose, onPublished },
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
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
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
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
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
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
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

  test("Escape closes the dialog when no publish is in flight", async () => {
    const onClose = vi.fn();
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
        open: true,
        folderID: "notes",
        api: fakeApi(),
        onClose,
        onPublished: () => {},
      },
    });
    await screen.findByRole("textbox", { name: /commit message/i });
    // Same event path as the in-flight test below: the shared Modal shell
    // listens on window, and with no header X this is the shell-owned
    // dismissal that must keep working.
    await fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  test("dialog cannot be closed while publishing", async () => {
    let resolvePublish: (v: GitPublishResponse) => void;
    const pendingPublish = new Promise<GitPublishResponse>((r) => {
      resolvePublish = r;
    });
    const onClose = vi.fn();
    render(PublishDocsDialogTestHarness, {
      props: {
        runtime,
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
    // The shared Modal shell omits the header X when the footer already has
    // Cancel, so Escape is the remaining shell-owned close path. Confirm it
    // is a no-op while a publish is in flight — without the guard it used to
    // bypass `disabled` on Cancel and discard publish_failed errors
    // mid-flight.
    expect(screen.queryByRole("button", { name: /^close$/i })).toBeNull();
    await fireEvent.keyDown(window, { key: "Escape" });
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
