<script lang="ts">
  import { getStores } from "../../context.js";
  import CommentEditor from "./CommentEditor.svelte";
  import {
    beginCommentSubmit,
    clearCommentSubmitError,
    clearCommentDraft,
    finishCommentSubmit,
    getCommentDraft,
    getCommentDraftKey,
    getCommentSubmitError,
    isCommentSubmitPending,
    setCommentSubmitError,
    setCommentDraft,
  } from "./comment-drafts.svelte.js";

  const { detail } = getStores();

  interface Props {
    owner: string;
    name: string;
    number: number;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    disabled?: boolean;
    /** Shown under the editor when the box is disabled for a reason
     * the user can act on (e.g. missing write credential). */
    disabledReason?: string | undefined;
  }

  const {
    owner,
    name,
    number,
    provider,
    platformHost,
    repoPath,
    disabled = false,
    disabledReason = undefined,
  }: Props = $props();

  const currentDraftKey = $derived(
    getCommentDraftKey("pull", owner, name, number, platformHost),
  );
  const body = $derived(
    getCommentDraft("pull", owner, name, number, platformHost),
  );

  const isEmpty = $derived(body.trim() === "");
  const visibleError = $derived(
    getCommentSubmitError("pull", owner, name, number, platformHost),
  );
  const isPostingCurrent = $derived(
    isCommentSubmitPending("pull", owner, name, number, platformHost),
  );

  async function handleSubmit(): Promise<void> {
    if (isEmpty || isPostingCurrent || disabled) return;
    const submittedOwner = owner;
    const submittedName = name;
    const submittedNumber = number;
    const submittedBody = body.trim();
    const submittedPlatformHost = platformHost;
    beginCommentSubmit(
      "pull",
      submittedOwner,
      submittedName,
      submittedNumber,
      submittedPlatformHost,
    );
    clearCommentSubmitError(
      "pull",
      submittedOwner,
      submittedName,
      submittedNumber,
      submittedPlatformHost,
    );
    try {
      await detail.submitComment(
        submittedOwner,
        submittedName,
        submittedNumber,
        submittedBody,
      );
      const storeError = detail.getDetailError();
      if (storeError !== null) {
        setCommentSubmitError(
          "pull",
          submittedOwner,
          submittedName,
          submittedNumber,
          storeError,
          submittedPlatformHost,
        );
      } else {
        clearCommentDraft(
          "pull",
          submittedOwner,
          submittedName,
          submittedNumber,
          submittedPlatformHost,
        );
        clearCommentSubmitError(
          "pull",
          submittedOwner,
          submittedName,
          submittedNumber,
          submittedPlatformHost,
        );
      }
    } finally {
      finishCommentSubmit(
        "pull",
        submittedOwner,
        submittedName,
        submittedNumber,
        submittedPlatformHost,
      );
    }
  }
</script>

<div class="comment-box">
  {#key `pull:${owner}/${name}/${number}`}
    <div class="comment-editor-shell">
      <CommentEditor
        {owner}
        {name}
        {provider}
        {platformHost}
        {repoPath}
        value={body}
        disabled={isPostingCurrent || disabled}
        oninput={(nextBody) => {
          setCommentDraft("pull", owner, name, number, nextBody, platformHost);
        }}
        onsubmit={() => {
          void handleSubmit();
        }}
      />
      <button
        class="submit-btn"
        onclick={() => void handleSubmit()}
        disabled={isEmpty || isPostingCurrent || disabled}
        title={disabled && disabledReason !== undefined ? disabledReason : undefined}
      >
        {isPostingCurrent ? "Posting…" : "Comment"}
      </button>
    </div>
  {/key}
  {#if disabled && disabledReason !== undefined}
    <p class="error-msg">{disabledReason}</p>
  {/if}
  {#if visibleError !== null}
    <p class="error-msg">{visibleError}</p>
  {/if}
</div>

<style>
  .comment-box {
    display: flex;
    flex-direction: column;
    gap: var(--focus-detail-space-sm, 0.62rem);
  }

  .error-msg {
    font-size: var(--font-size-sm);
    color: var(--accent-red);
  }

  .comment-editor-shell {
    position: relative;
  }

  .comment-editor-shell :global(.comment-editor-input) {
    min-height: 8.62rem;
    max-height: 75dvh;
    padding-bottom: calc(var(--focus-detail-hit-target, 3.05rem) + var(--focus-detail-space-sm, 0.57rem));
  }

  .submit-btn {
    position: absolute;
    right: var(--focus-detail-space-sm, 0.62rem);
    bottom: var(--focus-detail-space-sm, 0.62rem);
    font-size: var(--font-size-root);
    font-weight: 500;
    padding: var(--focus-detail-space-xs, 0.46rem) var(--focus-detail-space-md, 1.08rem);
    background: var(--accent-blue);
    color: #fff;
    border-radius: var(--radius-sm);
    cursor: pointer;
    z-index: 1;
    transition: opacity 0.15s;
  }

  .submit-btn:hover:not(:disabled) {
    opacity: 0.85;
  }

  .submit-btn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
</style>
