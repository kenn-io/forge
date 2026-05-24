<script lang="ts">
  import CheckCircleIcon from "@lucide/svelte/icons/check-circle";
  import CircleIcon from "@lucide/svelte/icons/circle";
  import ArrowRightIcon from "@lucide/svelte/icons/arrow-right";
  import { getStores } from "../../context.js";
  import type {
    ReviewThread,
    ReviewThreadContext,
    ReviewThreadContextLine,
  } from "./review-thread-context.js";

  interface Props {
    thread: ReviewThread;
    context?: ReviewThreadContext | null;
    canResolve?: boolean;
    onchanged?: (() => void | Promise<void>) | undefined;
    jumpToDiff?: (() => void) | undefined;
  }

  const {
    thread,
    context = null,
    canResolve = false,
    onchanged,
    jumpToDiff,
  }: Props = $props();
  const stores = getStores();
  const diffReviewDraft = stores.diffReviewDraft;
  const submitting = $derived(diffReviewDraft?.isSubmitting() ?? false);

  function lineClass(line: ReviewThreadContextLine): string {
    if (line.type === "add") return "context-line context-line--add";
    if (line.type === "delete") return "context-line context-line--del";
    return "context-line";
  }

  async function toggleResolved(): Promise<void> {
    if (!canResolve || !diffReviewDraft) return;
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
  <div class="thread-header">
    <div class="thread-path">
      <span>{context?.lineLabel ?? `${thread.path}:${thread.line}`}</span>
      {#if thread.resolved}
        <span class="thread-state">Resolved</span>
      {/if}
      {#if context?.outdated}
        <span class="thread-state thread-state--outdated">Outdated</span>
      {/if}
    </div>
    <div class="thread-actions">
      {#if jumpToDiff}
        <button
          class="thread-action"
          onclick={jumpToDiff}
          title="Jump to diff"
          aria-label="Jump to diff"
        >
          <ArrowRightIcon size={14} />
          Diff
        </button>
      {/if}
      {#if canResolve}
        <button
          class="thread-action"
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

  {#if context?.lines.length}
    <div class="thread-code" aria-label="Commented diff context">
      {#each context.lines as line (line.key)}
        <div class={lineClass(line)} class:context-line--target={line.target}>
          <span class="context-num">{line.oldNum ?? ""}</span>
          <span class="context-num">{line.newNum ?? ""}</span>
          <span class="context-marker">{line.type === "add" ? "+" : line.type === "delete" ? "-" : " "}</span>
          <code>{line.content}</code>
        </div>
      {/each}
    </div>
  {:else if context?.outdated}
    <p class="thread-outdated">Diff context is no longer present in the loaded diff.</p>
  {/if}
</div>

<style>
  .thread-snippet {
    margin-bottom: 8px;
    padding: 6px 8px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--bg-inset);
  }

  .thread-snippet--resolved {
    opacity: 0.75;
  }

  .thread-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
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

  .thread-state--outdated {
    color: var(--accent-orange);
  }

  .thread-actions {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-shrink: 0;
  }

  .thread-action {
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

  .thread-action:disabled {
    opacity: 0.55;
    cursor: default;
  }

  .thread-code {
    margin-top: 6px;
    overflow-x: auto;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--diff-bg);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .context-line {
    display: grid;
    grid-template-columns: minmax(2.5ch, auto) minmax(2.5ch, auto) 1.5ch minmax(0, 1fr);
    min-width: max-content;
    color: var(--diff-text);
    line-height: 18px;
  }

  .context-line--add {
    background: var(--diff-add-bg);
  }

  .context-line--del {
    background: var(--diff-del-bg);
  }

  .context-line--target {
    box-shadow: inset 3px 0 0 var(--accent-blue);
  }

  .context-num,
  .context-marker {
    color: var(--diff-line-num);
    user-select: none;
  }

  .context-num {
    padding: 0 6px;
    text-align: right;
    background: var(--diff-bg);
  }

  .context-line--add .context-num {
    background: var(--diff-add-gutter);
  }

  .context-line--del .context-num {
    background: var(--diff-del-gutter);
  }

  .context-marker {
    text-align: center;
  }

  code {
    padding: 0 8px 0 4px;
    white-space: pre;
  }

  .thread-outdated {
    margin: 6px 0 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
</style>
