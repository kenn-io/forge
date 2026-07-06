<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import type { ReviewThread, ReviewThreadContext } from "../diff/review-thread-context.js";
  import PierreFileDiff from "../diff/PierreFileDiff.svelte";
  import { buildSuggestionDiffFile } from "../../utils/markdown-suggestions.js";

  interface Props {
    thread: ReviewThread;
    context: ReviewThreadContext;
    replacement: string;
    currentHeadSHA?: string | undefined;
    applying?: boolean;
    batched?: boolean;
    error?: string | null;
    onCommit?: (() => void) | undefined;
    onToggleBatch?: (() => void) | undefined;
  }

  const {
    thread,
    context,
    replacement,
    currentHeadSHA = "",
    applying = false,
    batched = false,
    error = null,
    onCommit,
    onToggleBatch,
  }: Props = $props();

  const file = $derived(buildSuggestionDiffFile(thread, context, replacement));
  const reviewedHeadSHA = $derived(thread.diff_head_sha ?? "");
  const showActions = $derived(onCommit !== undefined || onToggleBatch !== undefined);
  const canApply = $derived(
    !context.outdated &&
      thread.side.toLowerCase() !== "left" &&
      reviewedHeadSHA !== "" &&
      currentHeadSHA !== "" &&
      reviewedHeadSHA === currentHeadSHA &&
      onCommit !== undefined,
  );
  const disabledReason = $derived.by(() => {
    if (context.outdated) return "The original diff context is not available";
    if (thread.side.toLowerCase() === "left") return "Suggestions on removed lines cannot be applied";
    if (reviewedHeadSHA === "") return "The suggestion is missing a reviewed head commit";
    if (currentHeadSHA === "") return "The current pull request head is not known yet";
    if (reviewedHeadSHA !== currentHeadSHA) {
      return "The suggestion was reviewed on an older head commit";
    }
    if (onCommit === undefined) return "Suggestion application is unavailable";
    return "";
  });
</script>

<div class="review-suggestion">
  <div class="review-suggestion__header">
    <span>Suggested change</span>
    <span class="review-suggestion__path">{file.path}</span>
  </div>
  <div class="review-suggestion__diff">
    <PierreFileDiff {file} viewMode="unified" wordWrap={true} />
  </div>
  {#if error}
    <p class="review-suggestion__error">{error}</p>
  {/if}
  {#if showActions}
    <div class="review-suggestion__actions">
      <button
        class="review-suggestion__action review-suggestion__action--primary"
        type="button"
        onclick={onCommit}
        disabled={!canApply || applying}
        title={!canApply ? disabledReason : undefined}
      >
        <CheckIcon size={14} />
        {applying ? "Committing..." : "Commit suggestion"}
      </button>
      <button
        class="review-suggestion__action"
        class:review-suggestion__action--selected={batched}
        type="button"
        onclick={onToggleBatch}
        disabled={(!canApply && !batched) || applying || onToggleBatch === undefined}
        title={!canApply && !batched ? disabledReason : undefined}
      >
        <PlusIcon size={14} />
        {batched ? "Remove from batch" : "Add suggestion to batch"}
      </button>
    </div>
  {/if}
</div>

<style>
  .review-suggestion {
    overflow: hidden;
    margin: var(--space-3, 0.75rem) 0;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
  }

  .review-suggestion__header {
    display: flex;
    align-items: center;
    gap: var(--space-2, 0.5rem);
    min-width: 0;
    padding: 0.55rem 0.75rem;
    border-bottom: 1px solid var(--border-muted);
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
  }

  .review-suggestion__path {
    overflow: hidden;
    min-width: 0;
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-weight: 500;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .review-suggestion__diff {
    background: var(--bg-primary);
  }

  .review-suggestion__error {
    margin: 0;
    padding: 0.5rem 0.75rem 0;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .review-suggestion__actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2, 0.5rem);
    padding: 0.55rem 0.75rem 0.65rem;
    border-top: 1px solid var(--border-muted);
  }

  .review-suggestion__action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.35rem;
    min-height: 2rem;
    padding: 0 0.65rem;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface-elevated);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    font-weight: 600;
    cursor: pointer;
  }

  .review-suggestion__action:hover:not(:disabled),
  .review-suggestion__action:focus-visible {
    border-color: var(--border-default);
    background: var(--bg-surface-hover);
  }

  .review-suggestion__action:focus-visible {
    outline: 2px solid var(--focus-ring, var(--accent-blue));
    outline-offset: 2px;
  }

  .review-suggestion__action:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }

  .review-suggestion__action--primary {
    border-color: color-mix(in srgb, var(--accent-green) 70%, var(--border-muted));
    background: var(--accent-green);
    color: white;
  }

  .review-suggestion__action--primary:hover:not(:disabled),
  .review-suggestion__action--primary:focus-visible {
    border-color: color-mix(in srgb, var(--accent-green) 80%, black);
    background: color-mix(in srgb, var(--accent-green) 90%, black);
  }

  .review-suggestion__action--selected {
    border-color: color-mix(in srgb, var(--accent-blue) 65%, var(--border-muted));
    color: var(--accent-blue);
  }
</style>
