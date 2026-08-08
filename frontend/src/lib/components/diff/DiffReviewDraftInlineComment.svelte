<script lang="ts">
  import { onDestroy, tick } from "svelte";
  import CheckIcon from "@lucide/svelte/icons/check";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import XIcon from "@lucide/svelte/icons/x";
  import type { DiffReviewDraftComment } from "../../stores/diff-review-draft.svelte.js";
  import { getStores } from "../../context.js";

  interface Props {
    comment: DiffReviewDraftComment;
  }

  const { comment }: Props = $props();
  const { diffReviewDraft } = getStores();
  const submitting = $derived(diffReviewDraft.isSubmitting());
  let editing = $state(false);
  let draftBody = $state("");
  let saving = $state(false);
  let editorElement: HTMLTextAreaElement | undefined = $state();
  const editStateID = $derived(`inline:${comment.id}`);
  const editDisabled = $derived(submitting || saving);
  const saveDisabled = $derived(editDisabled || draftBody.trim() === "");

  function lineLabel(comment: DiffReviewDraftComment): string {
    if (comment.start_line != null && comment.start_line !== comment.line) {
      return `${comment.path}:${comment.start_line}-${comment.line}`;
    }
    return `${comment.path}:${comment.line}`;
  }

  function beginEdit(): void {
    draftBody = comment.body;
    editing = true;
    reportEditState(true);
    void tick().then(() => editorElement?.focus());
  }

  function cancelEdit(): void {
    draftBody = comment.body;
    editing = false;
    reportEditState(false);
  }

  function draftDirty(body: string): boolean {
    return body.trim() !== comment.body;
  }

  function reportEditState(active: boolean, body = draftBody): void {
    diffReviewDraft.setCommentEditState(editStateID, {
      active,
      dirty: active && draftDirty(body),
    });
  }

  function handleDraftBodyInput(event: Event): void {
    draftBody = (event.currentTarget as HTMLTextAreaElement).value;
    reportEditState(true);
  }

  async function saveEdit(): Promise<void> {
    const nextBody = draftBody.trim();
    if (!nextBody || saveDisabled) return;
    if (nextBody === comment.body) {
      editing = false;
      reportEditState(false);
      return;
    }
    saving = true;
    try {
      const ok = await diffReviewDraft.editComment(comment, nextBody);
      if (ok) {
        editing = false;
        reportEditState(false);
      }
    } finally {
      saving = false;
    }
  }

  onDestroy(() => {
    reportEditState(false);
  });
</script>

<div
  class="inline-draft-comment"
  data-draft-comment-id={comment.id}
  tabindex="-1"
>
  <div class="draft-comment-header">
    <span class="draft-comment-state">Draft</span>
    <span class="draft-comment-location">{lineLabel(comment)}</span>
    <div class="draft-comment-actions">
      {#if editing}
        <button
          class="draft-comment-action"
          type="button"
          title="Save draft comment"
          aria-label="Save draft comment"
          onclick={() => void saveEdit()}
          disabled={saveDisabled}
        >
          <CheckIcon size={13} />
        </button>
        <button
          class="draft-comment-action"
          type="button"
          title="Cancel editing draft comment"
          aria-label="Cancel editing draft comment"
          onclick={cancelEdit}
          disabled={editDisabled}
        >
          <XIcon size={13} />
        </button>
      {:else}
        <button
          class="draft-comment-action"
          type="button"
          title="Edit draft comment"
          aria-label="Edit draft comment"
          onclick={beginEdit}
          disabled={submitting}
        >
          <PencilIcon size={13} />
        </button>
        <button
          class="draft-comment-action"
          type="button"
          title="Delete draft comment"
          aria-label="Delete draft comment"
          onclick={() => void diffReviewDraft.deleteComment(comment.id)}
          disabled={submitting}
        >
          <XIcon size={13} />
        </button>
      {/if}
    </div>
  </div>
  {#if editing}
    <textarea
      bind:this={editorElement}
      value={draftBody}
      class="draft-comment-editor"
      aria-label="Draft comment body"
      rows="3"
      disabled={editDisabled}
      oninput={handleDraftBodyInput}
    ></textarea>
  {:else}
    <p class="draft-comment-body">{comment.body}</p>
  {/if}
</div>

<style>
  .inline-draft-comment {
    box-sizing: border-box;
    margin: 6px 12px 8px;
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

  @container (max-width: 520px) {
    .inline-draft-comment {
      margin: 6px 8px 8px;
      width: calc(100% - 16px);
      max-width: calc(100% - 16px);
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

  .draft-comment-actions {
    display: flex;
    flex-shrink: 0;
    gap: 4px;
    margin-left: auto;
  }

  .draft-comment-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    padding: 0;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--bg-surface);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .draft-comment-action:disabled {
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

  .draft-comment-editor {
    box-sizing: border-box;
    width: 100%;
    min-height: 76px;
    margin-top: 6px;
    resize: vertical;
    padding: 7px 8px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    line-height: 1.42;
  }
</style>
