<script lang="ts">
  import CheckCircleIcon from "@lucide/svelte/icons/check-circle";
  import CircleIcon from "@lucide/svelte/icons/circle";
  import type { components } from "../../api/generated/schema.js";
  import { getStores } from "../../context.js";

  type ReviewThread = components["schemas"]["DiffReviewThreadResponse"];

  interface Props {
    thread: ReviewThread;
    canResolve?: boolean;
    onchanged?: (() => void | Promise<void>) | undefined;
  }

  const {
    thread,
    canResolve = false,
    onchanged,
  }: Props = $props();
  const { diffReviewDraft } = getStores();
  const submitting = $derived(diffReviewDraft.isSubmitting());

  async function toggleResolved(): Promise<void> {
    if (!canResolve) return;
    const ok = await diffReviewDraft.setThreadResolved(
      thread.id,
      !thread.resolved,
    );
    if (ok) {
      await onchanged?.();
    }
  }
</script>

<div class="thread-snippet" class:thread-snippet--resolved={thread.resolved}>
  <div class="thread-path">
    <span>{thread.path}:{thread.line}</span>
    {#if thread.resolved}
      <span class="thread-state">Resolved</span>
    {/if}
  </div>
  <div class="thread-actions">
    {#if canResolve}
      <button
        class="resolve-btn"
        onclick={() => void toggleResolved()}
        disabled={submitting}
      >
        {#if thread.resolved}
          <CircleIcon size={14} />
          Reopen
        {:else}
          <CheckCircleIcon size={14} />
          Resolve
        {/if}
      </button>
    {/if}
  </div>
</div>

<style>
  .thread-snippet {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 8px;
    padding: 6px 8px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--bg-inset);
  }

  .thread-snippet--resolved {
    opacity: 0.75;
  }

  .thread-path {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .thread-state {
    padding: 1px 5px;
    border-radius: 999px;
    background: var(--bg-surface);
    color: var(--text-muted);
    font-family: var(--font-sans);
    font-size: var(--font-size-2xs);
  }

  .resolve-btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 24px;
    padding: 0 8px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--bg-surface);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    cursor: pointer;
  }

  .resolve-btn:disabled {
    opacity: 0.55;
    cursor: default;
  }
</style>
