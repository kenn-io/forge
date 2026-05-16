<script lang="ts">
  import SendIcon from "@lucide/svelte/icons/send";
  import XIcon from "@lucide/svelte/icons/x";
  import type { DiffReviewLineRange } from "../../stores/diff-review-draft.svelte.js";
  import { getStores } from "../../context.js";
  import ActionButton from "../shared/ActionButton.svelte";

  interface Props {
    range: DiffReviewLineRange;
    onclose?: (() => void) | undefined;
  }

  const { range, onclose }: Props = $props();
  const { diffReviewDraft } = getStores();

  let body = $state("");
  const submitting = $derived(diffReviewDraft.isSubmitting());
  const error = $derived(diffReviewDraft.getError());

  async function submit(): Promise<void> {
    const nextBody = body.trim();
    if (!nextBody) return;
    const ok = await diffReviewDraft.createComment(nextBody, range);
    if (ok) {
      body = "";
      onclose?.();
    }
  }
</script>

<div class="inline-composer">
  <textarea
    bind:value={body}
    placeholder="Leave a comment"
    disabled={submitting}
    rows="3"
  ></textarea>
  {#if error}
    <p class="composer-error">{error}</p>
  {/if}
  <div class="composer-actions">
    <ActionButton
      class="composer-btn"
      size="sm"
      onclick={onclose}
      disabled={submitting}
    >
      <XIcon size={14} />
      Cancel
    </ActionButton>
    <ActionButton
      class="composer-btn composer-btn--primary"
      tone="info"
      surface="solid"
      size="sm"
      onclick={() => void submit()}
      disabled={submitting || body.trim() === ""}
    >
      <SendIcon size={14} />
      {submitting ? "Saving..." : "Add comment"}
    </ActionButton>
  </div>
</div>

<style>
  .inline-composer {
    margin: 6px 12px 8px 116px;
    padding: 8px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-surface);
    max-width: min(720px, calc(100% - 128px));
  }

  textarea {
    width: 100%;
    min-height: 72px;
    resize: vertical;
    padding: 8px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--bg-inset);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-md);
  }

  .composer-error {
    margin-top: 6px;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .composer-actions {
    display: flex;
    justify-content: flex-end;
    gap: 6px;
    margin-top: 8px;
  }

  :global(.composer-btn.action-button) {
    min-height: 28px;
  }

  :global(.composer-btn--primary.action-button) {
    border-color: var(--accent-blue);
    background: var(--accent-blue);
    color: #fff;
  }
</style>
