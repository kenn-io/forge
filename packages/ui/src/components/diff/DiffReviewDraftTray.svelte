<script lang="ts">
  import SendIcon from "@lucide/svelte/icons/send";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import XIcon from "@lucide/svelte/icons/x";
  import { getStores } from "../../context.js";
  import ActionButton from "../shared/ActionButton.svelte";
  import SelectDropdown from "../shared/SelectDropdown.svelte";

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
  const error = $derived(diffReviewDraft.getError());

  async function publish(): Promise<void> {
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
</script>

{#if comments.length > 0}
  <section class="draft-tray" aria-label="Draft review comments">
    <div class="tray-header">
      <strong>{comments.length} draft {comments.length === 1 ? "comment" : "comments"}</strong>
      <ActionButton
        class="icon-btn"
        title="Discard review draft"
        ariaLabel="Discard review draft"
        size="sm"
        onclick={() => void diffReviewDraft.discard()}
        disabled={submitting}
      >
        <TrashIcon size={14} />
      </ActionButton>
    </div>
    <div class="draft-list">
      {#each comments as comment (comment.id)}
        <div class="draft-item">
          <div class="draft-meta">
            <span>{comment.path}:{comment.line}</span>
          </div>
          <p>{comment.body}</p>
          <ActionButton
            class="icon-btn"
            title="Delete draft comment"
            ariaLabel="Delete draft comment"
            size="sm"
            onclick={() => void diffReviewDraft.deleteComment(comment.id)}
            disabled={submitting}
          >
            <XIcon size={13} />
          </ActionButton>
        </div>
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
      <ActionButton
        class="publish-btn"
        tone="info"
        surface="solid"
        size="sm"
        onclick={() => void publish()}
        disabled={submitting || supportedActions.length === 0}
      >
        <SendIcon size={14} />
        {submitting ? "Publishing..." : "Publish review"}
      </ActionButton>
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
    box-shadow: 0 -8px 20px rgb(0 0 0 / 0.12);
  }

  .tray-header,
  .publish-row,
  .draft-item {
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
    gap: 6px;
    max-height: 160px;
    overflow: auto;
    margin-bottom: 8px;
  }

  .draft-item {
    justify-content: space-between;
    padding: 6px 8px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--bg-inset);
  }

  .draft-item p {
    flex: 1;
    min-width: 0;
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .draft-meta {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  textarea {
    width: 100%;
    resize: vertical;
    padding: 7px 8px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
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

  :global(.review-action-select .select-dropdown-trigger),
  :global(.publish-btn.action-button) {
    min-height: 28px;
    font-size: var(--font-size-sm);
  }

  :global(.publish-btn.action-button) {
    border-color: var(--accent-blue);
    background: var(--accent-blue);
    color: #fff;
  }

  :global(.icon-btn.action-button) {
    width: 26px;
    height: 26px;
    min-height: 26px;
    padding: 0;
  }

  .tray-error {
    margin-top: 6px;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }
</style>
