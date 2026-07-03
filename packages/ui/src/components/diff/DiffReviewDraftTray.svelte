<script lang="ts">
  import { Button, IconButton } from "@kenn-io/kit-ui";
  import SendIcon from "@lucide/svelte/icons/send";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import { getStores } from "../../context.js";
  import type { DiffReviewDraftComment } from "../../stores/diff-review-draft.svelte.js";
    import { SelectDropdown } from "@kenn-io/kit-ui";
  import DiffReviewDraftTrayItem from "./DiffReviewDraftTrayItem.svelte";

  interface Props {
    onjump?: ((comment: DiffReviewDraftComment) => void) | undefined;
  }

  const { onjump }: Props = $props();
  const { diffReviewDraft } = getStores();

  let body = $state("");
  let action = $state("comment");
  const comments = $derived(diffReviewDraft.getComments());
  const draft = $derived(diffReviewDraft.getDraft());
  const supportedActions = $derived(draft?.supported_actions ?? []);
  const actionOptions = $derived(
    supportedActions.map((option) => ({
      value: option,
      label: reviewActionLabel(option),
    })),
  );
  const selectedAction = $derived(
    supportedActions.includes(action)
      ? action
      : (supportedActions[0] ?? "comment"),
  );
  const submitting = $derived(diffReviewDraft.isSubmitting());
  const pendingCommentEdits = $derived(diffReviewDraft.hasPendingCommentEdits());
  const error = $derived(diffReviewDraft.getError());
  const draftActionDisabled = $derived(submitting || pendingCommentEdits);

  async function publish(): Promise<void> {
    if (pendingCommentEdits) return;
    const ok = await diffReviewDraft.publish(selectedAction, body);
    if (ok) {
      body = "";
    }
  }

  function reviewActionLabel(option: string): string {
    if (option === "request_changes") return "Request changes";
    if (option === "approve") return "Approve";
    return "Comment";
  }

  function commentLocation(comment: DiffReviewDraftComment): string {
    if (comment.start_line != null && comment.start_line !== comment.line) {
      return `${comment.path}:${comment.start_line}-${comment.line}`;
    }
    return `${comment.path}:${comment.line}`;
  }
</script>

{#if comments.length > 0}
  <section class="draft-tray" aria-label="Draft review comments">
    <div class="tray-header">
      <strong>{comments.length} draft {comments.length === 1 ? "comment" : "comments"}</strong>
      <IconButton
        size="sm"
        tone="danger"
        ariaLabel="Discard review draft"
        onclick={() => void diffReviewDraft.discard()}
        disabled={draftActionDisabled}
      >
        <TrashIcon size={14} />
      </IconButton>
    </div>
    <div class="draft-list">
      {#each comments as comment (comment.id)}
        <DiffReviewDraftTrayItem
          {comment}
          location={commentLocation(comment)}
          disabled={submitting}
          {onjump}
          ondelete={(id) => void diffReviewDraft.deleteComment(id)}
          onsave={(draftComment, draftBody) => diffReviewDraft.editComment(draftComment, draftBody)}
          oneditstatechange={(id, state) => diffReviewDraft.setCommentEditState(id, state)}
        />
      {/each}
    </div>
    <textarea
      bind:value={body}
      placeholder="Review summary"
      rows="2"
      disabled={submitting}
    ></textarea>
    {#if error}
      <p class="tray-error">{error}</p>
    {/if}
    <div class="publish-row">
      <SelectDropdown
        class="review-action-select"
        value={selectedAction}
        options={actionOptions}
        onchange={(value) => { action = value; }}
        title="Review action"
        disabled={submitting || supportedActions.length === 0}
      />
      <Button
        class="publish-btn"
        tone="info"
        surface="solid"
        size="sm"
        title={pendingCommentEdits ? "Save or cancel draft comment edits before publishing" : undefined}
        onclick={() => void publish()}
        disabled={draftActionDisabled || supportedActions.length === 0}
      >
        <SendIcon size={14} />
        {submitting ? "Publishing..." : "Publish review"}
      </Button>
    </div>
  </section>
{/if}

<style>
  .draft-tray {
    position: sticky;
    bottom: 0;
    z-index: 5;
    padding: 10px 12px;
    border-top: 1px solid var(--border-default);
    background: var(--bg-surface);
    /* kit-ui-check-ignore: upward tray shadow; kit shadow tokens only cast downward */
    box-shadow: 0 -8px 20px rgb(0 0 0 / 0.12);
  }

  .tray-header,
  .publish-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .tray-header {
    justify-content: space-between;
    margin-bottom: 8px;
    font-size: var(--font-size-md);
  }

  .draft-list {
    display: grid;
    gap: 8px;
    max-height: 188px;
    overflow-x: hidden;
    overflow-y: auto;
    margin-bottom: 8px;
    padding-right: 2px;
  }

  textarea {
    width: 100%;
    min-height: 64px;
    resize: vertical;
    padding: 7px 8px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
  }

  .publish-row {
    justify-content: flex-end;
    margin-top: 8px;
  }

  :global(.review-action-select) {
    min-width: 150px;
  }

  :global(.review-action-select .kit-select-dropdown__trigger),
  :global(.publish-btn.kit-button) {
    min-height: 28px;
    font-size: var(--font-size-sm);
  }

  :global(.publish-btn.kit-button) {
    border-color: var(--accent-blue);
    background: var(--accent-blue);
    color: var(--bg-surface);
  }

  .tray-error {
    margin-top: 6px;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  @media (max-width: 760px) {
    .publish-row {
      align-items: stretch;
      flex-wrap: wrap;
    }

    :global(.review-action-select) {
      flex: 1 1 150px;
    }

    :global(.publish-btn.kit-button) {
      flex: 1 1 170px;
    }
  }
</style>
