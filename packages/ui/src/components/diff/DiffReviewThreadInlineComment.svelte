<script lang="ts">
  import type { ReviewThread } from "./review-thread-context.js";
  import { reviewThreadLineLabel } from "./review-thread-context.js";

  interface Props {
    thread: ReviewThread;
    fileLevel?: boolean;
  }

  const {
    thread,
    fileLevel = false,
  }: Props = $props();
</script>

<div
  class="inline-review-thread"
  class:inline-review-thread--file-level={fileLevel}
  data-review-thread-id={thread.id}
  tabindex="-1"
>
  <div class="review-thread-header">
    <span class="review-thread-state">Review Comment</span>
    <span class="review-thread-location">{reviewThreadLineLabel(thread)}</span>
    {#if thread.resolved}
      <span class="review-thread-status">Resolved</span>
    {/if}
    {#if fileLevel}
      <span class="review-thread-status review-thread-status--outdated">File</span>
    {/if}
  </div>
  {#if thread.author_login}
    <div class="review-thread-author">{thread.author_login}</div>
  {/if}
  <p class="review-thread-body">{thread.body}</p>
</div>

<style>
  .inline-review-thread {
    position: sticky;
    left: 12px;
    box-sizing: border-box;
    margin: 6px 0 8px;
    padding: 8px;
    border: 1px solid color-mix(in srgb, var(--accent-purple) 44%, var(--border-muted));
    border-radius: 6px;
    background: color-mix(in srgb, var(--accent-purple) 9%, var(--bg-surface));
    width: calc(100% - 24px);
    max-width: calc(100% - 24px);
    min-width: 0;
    scroll-margin-block: 96px;
  }

  .inline-review-thread--file-level {
    margin-top: 8px;
  }

  .inline-review-thread:focus {
    outline: 2px solid var(--accent-purple);
    outline-offset: 2px;
  }

  @supports (width: 100cqw) {
    .inline-review-thread {
      width: calc(100cqw - 24px);
      max-width: calc(100cqw - 24px);
    }
  }

  @container (max-width: 520px) {
    .inline-review-thread {
      left: 8px;
      margin: 6px 0 8px;
      width: calc(100cqw - 16px);
      max-width: calc(100cqw - 16px);
    }
  }

  .review-thread-header {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .review-thread-state {
    flex-shrink: 0;
    padding: 1px 6px;
    border-radius: 999px;
    background: color-mix(in srgb, var(--accent-purple) 16%, var(--bg-inset));
    color: var(--accent-purple);
    font-size: var(--font-size-2xs);
    font-weight: 700;
    text-transform: uppercase;
  }

  .review-thread-location {
    min-width: 0;
    overflow: hidden;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .review-thread-status {
    flex-shrink: 0;
    padding: 1px 5px;
    border-radius: 999px;
    background: var(--bg-inset);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .review-thread-status--outdated {
    color: var(--accent-orange);
  }

  .review-thread-author {
    margin-top: 6px;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .review-thread-body {
    margin: 6px 0 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
</style>
