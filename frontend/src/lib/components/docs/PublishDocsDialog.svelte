<script lang="ts">
  import { Effect } from "effect";
  import Modal from "../shared/Modal.svelte";
  import { DocsRequestError, executeDocsRequest, type DocsAPI } from "../../api/docs/api";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { DocsWorkflow, makeDocsOwner, type DocsPublishState } from "../../stores/docs-workflow.js";
  import type { GitChangesResponse, GitPublishResponse } from "../../api/docs/types";

  interface Props {
    open: boolean;
    folderID: string;
    api: DocsAPI;
    onClose: () => void;
    onPublished: (result: GitPublishResponse) => void;
  }

  let { open, folderID, api, onClose, onPublished }: Props = $props();
  const runtime = getAppRuntime();
  let activePublisherSessionID: string | undefined;
  let retainedPublishMessage: string | undefined;

  // Discriminated state machine — each variant maps to a distinct dialog body.
  // Errors keep the dialog open and live alongside `ready` so the form stays
  // visible for retry.
  type Status =
    | { kind: "loading" }
    | { kind: "not_a_repo" }
    | { kind: "no_changes"; preview: GitChangesResponse }
    | { kind: "ready"; preview: GitChangesResponse }
    | { kind: "load_error"; message: string };

  let status: Status = $state({ kind: "loading" });
  let message = $state("");
  let publishing = $state(false);
  let acknowledgingFailure = $state(false);
  let publishError: { message: string; commit?: string } | null = $state(null);

  const loadPreviewEffect = Effect.fn("DocsPublishDialog.loadPreview")(function* (
    requestedFolderID: string,
    sessionID: string,
    requestedAPI: DocsAPI,
  ) {
      const workflow = yield* DocsWorkflow;
      const preview = yield* workflow.read(
        sessionID,
        { lane: "preview", resource: JSON.stringify(["git-changes", requestedFolderID]) },
        executeDocsRequest("load Docs publish preview", (signal) =>
          requestedAPI.gitChanges(requestedFolderID, signal),
        ),
      );
      yield* Effect.sync(() => {
        if (activePublisherSessionID !== sessionID) return;
        if (!preview.is_repo) {
          status = { kind: "not_a_repo" };
          return;
        }
        if (retainedPublishMessage === undefined) message = preview.suggested_message ?? "";
        status = preview.changes.length === 0 ? { kind: "no_changes", preview } : { kind: "ready", preview };
      });
    });

  const observePublishState =
    (sessionID: string) =>
    (state: DocsPublishState): Effect.Effect<void> =>
      Effect.sync(() => {
        if (activePublisherSessionID !== sessionID) return;
        switch (state.kind) {
          case "pending":
            retainedPublishMessage = state.request.message;
            message = state.request.message;
            publishing = true;
            publishError = null;
            return;
          case "failed":
            retainedPublishMessage = state.request.message;
            message = state.request.message;
            publishing = false;
            publishError = translateError(state.error);
            return;
          case "succeeded":
            retainedPublishMessage = undefined;
            publishing = false;
            onPublished(state.result);
            onClose();
        }
      });

  // Each open is a fresh session. The owner-keyed workflow physically
  // interrupts an older preview before accepting its replacement.
  $effect(() => {
    if (!open) {
      activePublisherSessionID = undefined;
      retainedPublishMessage = undefined;
      status = { kind: "loading" };
      message = "";
      publishing = false;
      acknowledgingFailure = false;
      publishError = null;
      return;
    }
    const requestedFolderID = folderID;
    const requestedAPI = api;
    const sessionID = makeDocsOwner("docs-publish-dialog");
    activePublisherSessionID = sessionID;
    retainedPublishMessage = undefined;
    status = { kind: "loading" };
    message = "";
    publishing = false;
    acknowledgingFailure = false;
    publishError = null;
    const execution = runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* DocsWorkflow;
        yield* workflow.claimPublisher(requestedFolderID, sessionID, observePublishState(sessionID));
        yield* loadPreviewEffect(requestedFolderID, sessionID, requestedAPI);
      }),
      {
      operation: "load Docs publish preview",
      safeContext: { owner: sessionID },
      onFailure: (error) => {
        if (activePublisherSessionID !== sessionID) return;
        status = { kind: "load_error", message: translateError(error).message };
      },
      },
    );
    return () => {
      if (activePublisherSessionID === sessionID) activePublisherSessionID = undefined;
      execution.interrupt();
      runtime.runCommand(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          yield* workflow.releasePublisher(sessionID);
          yield* workflow.stop(sessionID);
        }),
        { operation: "release Docs publish dialog", safeContext: { owner: sessionID }, onFailure: () => {} },
      );
    };
  });

  function submit(): void {
    const submittedSessionID = activePublisherSessionID;
    if (status.kind !== "ready" || publishing || submittedSessionID === undefined) return;
    const submittedFolderID = folderID;
    const submittedMessage = message;
    const submittedAPI = api;
    publishing = true;
    publishError = null;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* DocsWorkflow;
        yield* workflow.publish({
          folderID: submittedFolderID,
          sessionID: submittedSessionID,
          message: submittedMessage,
          request: executeDocsRequest("publish Docs", (signal) =>
            submittedAPI.gitPublish(submittedFolderID, submittedMessage, signal),
          ),
        });
      }),
      {
        operation: "publish Docs",
        safeContext: { owner: submittedSessionID },
        onFailure: (error) => {
          if (activePublisherSessionID !== submittedSessionID) return;
          publishing = false;
          publishError = translateError(error);
        },
      },
    );
  }

  // guardedClose is a no-op while a publish is in flight so that Escape,
  // backdrop click, and the X button cannot reset state or discard a
  // push_failed_after_commit error before the user has read it.
  function guardedClose() {
    if (publishing || acknowledgingFailure) return;
    const sessionID = activePublisherSessionID;
    if (publishError && sessionID !== undefined) {
      acknowledgingFailure = true;
      runtime.runCommand(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          yield* workflow.acknowledgePublisher(sessionID);
          yield* Effect.sync(onClose);
        }),
        {
          operation: "acknowledge Docs publish failure",
          safeContext: { owner: sessionID },
          onFailure: () => {
            acknowledgingFailure = false;
            onClose();
          },
        },
      );
      return;
    }
    onClose();
  }

  // translateError maps server error envelopes to actionable copy. The
  // push_failed_after_commit case carries an extra `commit` field on the
  // thrown error so we can show the short SHA in the recovery message.
  function translateError(err: unknown): { message: string; commit?: string } {
    if (err instanceof DocsRequestError) {
      const e = err;
      switch (e.code) {
        case "push_failed_after_commit":
          {
            const translated = {
              message:
                `Committed ${e.commit?.slice(0, 7) ?? "locally"} locally, but push failed:\n${e.message}`,
            };
            if (e.commit) return { ...translated, commit: e.commit };
            return translated;
          }
        case "index_not_clean":
          return {
            message:
              `${e.message}\n\nFinish or reset the partial/unrelated staged changes in your terminal first.`,
          };
        case "no_upstream":
          return { message: e.message };
        case "unsafe_git_config":
          return {
            message:
              "Publish is blocked because this folder's git repository has command-bearing config or attributes " +
              "(for example git-lfs filters or a signing/credential program). Remove the repo-local config or " +
              "attributes, or publish from your terminal.",
          };
        case "conflict":
          return {
            message:
              "Markdown files have unresolved merge conflicts. Resolve them in your terminal first.",
          };
        default:
          return { message: e.message };
      }
    }
    if (err instanceof Error) return { message: err.message };
    return { message: "Unexpected error" };
  }
</script>

<Modal {open} title="Commit & Push Docs" onClose={guardedClose} width={520}>
  {#if status.kind === "loading"}
    <p class="muted">Loading…</p>
  {:else if status.kind === "load_error"}
    <p class="error">{status.message}</p>
  {:else if status.kind === "not_a_repo"}
    <p class="muted">This folder is not a git repository. There's nothing to publish.</p>
  {:else}
    {#if status.preview.changes.length === 0}
      <p class="muted">No changed Markdown files to publish.</p>
    {:else}
      <section class="changes">
        <h3 class="section-title">Files included</h3>
        <ul class="file-list">
          {#each status.preview.changes as change (`${change.status}:${change.old_path ?? ""}:${change.path}`)}
            <li class="file-row">
              <span class={`file-status file-status--${change.status}`}>{change.status}</span>
              <span class="file-path">{change.path}</span>
              {#if change.old_path}
                <span class="file-old">← {change.old_path}</span>
              {/if}
            </li>
          {/each}
        </ul>
        <p class="note">
          Only Markdown files will be committed. Referenced images and other assets are not included.
        </p>
      </section>
    {/if}
    <label class="field">
      <span class="field-label">Commit message</span>
      <textarea
        class="message"
        rows="6"
        bind:value={message}
        disabled={publishing}
        aria-label="Commit message"
      ></textarea>
    </label>
  {/if}
  {#if publishError}
    <p class="error">{publishError.message}</p>
  {/if}

  {#snippet footer()}
    <button type="button" class="secondary" onclick={guardedClose} disabled={publishing || acknowledgingFailure}>
      Cancel
    </button>
    {#if status.kind === "ready" || status.kind === "no_changes"}
      <button
        type="button"
        class="primary"
        onclick={submit}
        disabled={publishing || status.kind !== "ready" || message.trim() === ""}
      >
        {publishing ? "Publishing…" : "Commit & Push"}
      </button>
    {/if}
  {/snippet}
</Modal>

<style>
  .muted { color: var(--text-muted); }
  .error { color: var(--accent-red, #c14a3c); white-space: pre-wrap; }
  .section-title {
    margin: 0 0 6px;
    font-size: var(--font-size-sm);
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .file-list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    max-height: 160px;
    overflow: auto;
  }
  .file-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 8px;
    font-size: var(--font-size-sm);
  }
  .file-status {
    font-family: var(--font-mono);
    font-size: var(--font-size-3xs);
    padding: 1px 5px;
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-muted);
    text-transform: uppercase;
  }
  .file-path { color: var(--text-primary); }
  .file-old { color: var(--text-muted); font-style: italic; }
  .note {
    margin: 8px 0 0;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }
  .field { display: flex; flex-direction: column; gap: 4px; margin-top: 12px; }
  .field-label { font-size: var(--font-size-sm); color: var(--text-secondary); }
  .message {
    width: 100%;
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    padding: 6px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    resize: vertical;
  }
  .secondary,
  .primary {
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
  }
  .primary {
    background: var(--accent-blue);
    color: var(--text-on-accent);
    border-color: var(--accent-blue);
  }
  .primary:disabled,
  .secondary:disabled {
    opacity: var(--opacity-disabled);
    cursor: not-allowed;
  }
</style>
