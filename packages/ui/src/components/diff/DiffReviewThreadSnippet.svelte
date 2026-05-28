<script lang="ts">
  import CheckCircleIcon from "@lucide/svelte/icons/check-circle";
  import CircleIcon from "@lucide/svelte/icons/circle";
  import ArrowRightIcon from "@lucide/svelte/icons/arrow-right";
  import type { SelectedLineRange } from "@pierre/diffs";
  import type { DiffFile } from "../../api/types.js";
  import { getStores } from "../../context.js";
  import PierreFileDiff from "./PierreFileDiff.svelte";
  import type {
    ReviewThread,
    ReviewThreadContext,
    ReviewThreadContextLine,
  } from "./review-thread-context.js";
  import {
    reviewThreadStartLine,
    reviewThreadStartSide,
    reviewThreadTargetLine,
    reviewThreadTargetSide,
  } from "./review-thread-context.js";
  import { patchPath } from "./pierre-diff.js";

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
  const diffStore = stores.diff;
  const diffReviewDraft = stores.diffReviewDraft;
  const submitting = $derived(diffReviewDraft?.isSubmitting() ?? false);
  const tabWidth = $derived(diffStore.getTabWidth());
  const contextDiff = $derived(context?.lines.length ? diffFileForContext(context) : null);
  const selectedRanges = $derived<SelectedLineRange[]>([{
    start: reviewThreadStartLine(thread),
    end: reviewThreadTargetLine(thread),
    side: pierreSide(reviewThreadStartSide(thread)),
    ...(reviewThreadStartSide(thread) !== reviewThreadTargetSide(thread) && {
      endSide: pierreSide(reviewThreadTargetSide(thread)),
    }),
  }]);

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

  function pierreSide(side: "left" | "right"): "deletions" | "additions" {
    return side === "left" ? "deletions" : "additions";
  }

  function diffFileForContext(context: ReviewThreadContext): DiffFile {
    const oldLineCount = context.lines.filter((line) => line.type !== "add").length;
    const newLineCount = context.lines.filter((line) => line.type !== "delete").length;
    const oldStart = firstLineNumber(context.lines, "oldNum") ??
      Math.max(1, (firstLineNumber(context.lines, "newNum") ?? 1) - 1);
    const newStart = firstLineNumber(context.lines, "newNum") ??
      Math.max(1, (firstLineNumber(context.lines, "oldNum") ?? 1) - 1);
    const oldPath = patchPath(`a/${context.path}`);
    const newPath = patchPath(`b/${context.path}`);
    const patch = [
      `diff --git ${oldPath} ${newPath}`,
      `--- ${oldPath}`,
      `+++ ${newPath}`,
      `@@ -${oldStart},${oldLineCount} +${newStart},${newLineCount} @@`,
      ...context.lines.map(patchLine),
      "",
    ].join("\n");
    return {
      path: context.path,
      old_path: context.path,
      status: "modified",
      is_binary: false,
      is_whitespace_only: false,
      additions: context.lines.filter((line) => line.type === "add").length,
      deletions: context.lines.filter((line) => line.type === "delete").length,
      patch,
      hunks: [{
        old_start: oldStart,
        old_count: oldLineCount,
        new_start: newStart,
        new_count: newLineCount,
        lines: context.lines.map((line) => ({
          type: line.type,
          content: line.content,
          ...(line.oldNum != null && { old_num: line.oldNum }),
          ...(line.newNum != null && { new_num: line.newNum }),
        })),
      }],
    };
  }

  function firstLineNumber(
    lines: ReviewThreadContextLine[],
    key: "oldNum" | "newNum",
  ): number | undefined {
    return lines.find((line) => line[key] != null)?.[key];
  }

  function patchLine(line: ReviewThreadContextLine): string {
    const prefix = line.type === "add" ? "+" : line.type === "delete" ? "-" : " ";
    return `${prefix}${line.content}`;
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

  {#if contextDiff}
    <div class="thread-code" aria-label="Commented diff context">
      <PierreFileDiff
        file={contextDiff}
        active
        wordWrap
        {tabWidth}
        selectedRanges={selectedRanges}
      />
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
    overflow: hidden;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--diff-bg);
    container-type: inline-size;
  }

  .thread-outdated {
    margin: 6px 0 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
</style>
