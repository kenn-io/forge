<script lang="ts">
  import XIcon from "@lucide/svelte/icons/x";
  import type { DiffReviewDraftComment } from "../../stores/diff-review-draft.svelte.js";
  import { getStores } from "../../context.js";

  interface Props {
    comment: DiffReviewDraftComment;
  }

  const { comment }: Props = $props();
  const { diffReviewDraft } = getStores();
  const submitting = $derived(diffReviewDraft.isSubmitting());

  function lineLabel(comment: DiffReviewDraftComment): string {
    if (comment.start_line != null && comment.start_line !== comment.line) {
      return `${comment.path}:${comment.start_line}-${comment.line}`;
    }
    return `${comment.path}:${comment.line}`;
  }
</script>

<div
  class="inline-draft-comment"
  data-draft-comment-id={comment.id}
  tabindex="-1"
>
  <div class="draft-comment-header">
    <span class="draft-comment-state">Draft</span>
    <span class="draft-comment-location">{lineLabel(comment)}</span>
    <button
      class="draft-comment-delete"
      title="Delete draft comment"
      aria-label="Delete draft comment"
      onclick={() => void diffReviewDraft.deleteComment(comment.id)}
      disabled={submitting}
    >
      <XIcon size={13} />
    </button>
  </div>
  <p class="draft-comment-body">{comment.body}</p>
</div>

<style>
  .inline-draft-comment {
    position: sticky;
    left: 12px;
    box-sizing: border-box;
    margin: 6px 0 8px;
    padding: 8px;
    border: 1px solid color-mix(in srgb, var(--accent-blue) 46%, var(--border-muted));
    border-radius: 6px;
    background: color-mix(in srgb, var(--accent-blue) 10%, var(--bg-surface));
    width: calc(100% - 24px);
    max-width: calc(100% - 24px);
    min-width: 0;
    scroll-margin-block: 96px;
  }

  .inline-draft-comment:focus {
    outline: 2px solid var(--accent-blue);
    outline-offset: 2px;
  }

  @supports (width: 100cqw) {
    .inline-draft-comment {
      width: calc(100cqw - 24px);
      max-width: calc(100cqw - 24px);
    }
  }

  @container (max-width: 520px) {
    .inline-draft-comment {
      left: 8px;
      margin: 6px 0 8px;
      width: calc(100cqw - 16px);
      max-width: calc(100cqw - 16px);
    }
  }

  .draft-comment-header {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .draft-comment-state {
    flex-shrink: 0;
    padding: 1px 6px;
    border-radius: 999px;
    background: color-mix(in srgb, var(--accent-blue) 16%, var(--bg-inset));
    color: var(--accent-blue);
    font-size: var(--font-size-2xs);
    font-weight: 700;
    text-transform: uppercase;
  }

  .draft-comment-location {
    min-width: 0;
    overflow: hidden;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .draft-comment-delete {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 24px;
    height: 24px;
    margin-left: auto;
    padding: 0;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--bg-surface);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .draft-comment-delete:disabled {
    opacity: 0.55;
    cursor: default;
  }

  .draft-comment-body {
    margin: 6px 0 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
</style>
