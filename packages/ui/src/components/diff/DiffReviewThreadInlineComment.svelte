<script lang="ts">
  import MessageSquareReplyIcon from "@lucide/svelte/icons/message-square-reply";
  import SendIcon from "@lucide/svelte/icons/send";
  import XIcon from "@lucide/svelte/icons/x";
  import { onMount, tick } from "svelte";
  import type { ReviewThread } from "./review-thread-context.js";
  import { reviewThreadLineLabel } from "./review-thread-context.js";
  import ActionButton from "../shared/ActionButton.svelte";

  interface Props {
    thread: ReviewThread;
    fileLevel?: boolean;
    canReply?: boolean;
    onreply?: ((thread: ReviewThread, body: string) => Promise<boolean>) | undefined;
  }

  const {
    thread,
    fileLevel = false,
    canReply = false,
    onreply,
  }: Props = $props();

  let replying = $state(false);
  let replyBody = $state("");
  let submitting = $state(false);
  let error = $state<string | null>(null);
  let threadEl: HTMLDivElement | undefined = $state();
  let textareaEl: HTMLTextAreaElement | undefined = $state();
  let panelWidth = $state<string | undefined>();

  onMount(() => {
    let animationFrame = 0;
    const scheduleLayout = () => {
      if (animationFrame) cancelAnimationFrame(animationFrame);
      animationFrame = requestAnimationFrame(() => {
        animationFrame = 0;
        updatePanelWidth();
      });
    };
    const container = threadEl ? layoutContainer(threadEl) : null;
    const resizeObserver = typeof ResizeObserver !== "undefined"
      ? new ResizeObserver(scheduleLayout)
      : undefined;
    if (container) {
      container.addEventListener("scroll", scheduleLayout, { passive: true });
      resizeObserver?.observe(container);
    }
    if (threadEl) resizeObserver?.observe(threadEl);
    window.addEventListener("resize", scheduleLayout);
    scheduleLayout();

    return () => {
      if (animationFrame) cancelAnimationFrame(animationFrame);
      container?.removeEventListener("scroll", scheduleLayout);
      window.removeEventListener("resize", scheduleLayout);
      resizeObserver?.disconnect();
    };
  });

  function startReply(): void {
    replying = true;
    error = null;
    void tick().then(() => textareaEl?.focus({ preventScroll: true }));
  }

  function cancelReply(): void {
    replying = false;
    replyBody = "";
    error = null;
  }

  async function submitReply(): Promise<void> {
    const body = replyBody.trim();
    if (!body) {
      error = "Reply body must not be empty";
      return;
    }
    if (!onreply) return;
    submitting = true;
    error = null;
    try {
      const ok = await onreply(thread, body);
      if (ok) {
        cancelReply();
      } else {
        error = "Could not reply to thread";
      }
    } finally {
      submitting = false;
    }
  }

  function updatePanelWidth(): void {
    if (!threadEl) return;
    const container = layoutContainer(threadEl);
    if (!container) {
      panelWidth = undefined;
      return;
    }
    const containerRect = container.getBoundingClientRect();
    const threadRect = threadEl.getBoundingClientRect();
    const available = Math.floor(containerRect.right - threadRect.left - 12);
    panelWidth = available > 0 ? `${available}px` : undefined;
  }

  function layoutContainer(element: HTMLElement): HTMLElement | null {
    const root = element.getRootNode();
    if (root instanceof ShadowRoot && root.host instanceof HTMLElement) {
      return root.host.closest(".file-content") ?? root.host.closest(".diff-area");
    }
    return element.closest(".file-content") ?? element.closest(".diff-area");
  }
</script>

<div
  class="inline-review-thread"
  class:inline-review-thread--file-level={fileLevel}
  class:inline-review-thread--idle-reply={canReply && !thread.resolved && !replying}
  data-review-thread-id={thread.id}
  bind:this={threadEl}
  style:--inline-review-thread-width={panelWidth}
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
  <p
    class="review-thread-body"
    class:review-thread-body--with-idle-reply={canReply && !thread.resolved && !replying}
  >
    {thread.body}
  </p>
  {#if canReply && !thread.resolved}
    {#if replying}
      <div class="review-thread-reply">
        <textarea
          bind:this={textareaEl}
          bind:value={replyBody}
          placeholder="Reply to thread"
          disabled={submitting}
          rows="3"
        ></textarea>
        {#if error}
          <p class="review-thread-error">{error}</p>
        {/if}
        <div class="review-thread-actions">
          <ActionButton
            class="review-thread-btn"
            size="sm"
            onclick={cancelReply}
            disabled={submitting}
          >
            <XIcon size={14} />
            Cancel
          </ActionButton>
          <ActionButton
            class="review-thread-btn review-thread-btn--primary"
            tone="info"
            surface="solid"
            size="sm"
            onclick={() => void submitReply()}
            disabled={submitting || replyBody.trim() === ""}
          >
            <SendIcon size={14} />
            {submitting ? "Replying..." : "Reply"}
          </ActionButton>
        </div>
      </div>
    {:else}
      <div class="review-thread-actions review-thread-actions--idle">
        <ActionButton
          class="review-thread-btn"
          size="sm"
          surface="soft"
          tone="neutral"
          onclick={startReply}
        >
          <MessageSquareReplyIcon size={14} />
          Reply
        </ActionButton>
      </div>
    {/if}
  {/if}
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
    width: var(--inline-review-thread-width, calc(100% - 24px));
    max-width: var(--inline-review-thread-width, calc(100% - 24px));
    min-width: 0;
    scroll-margin-block: 96px;
  }

  .inline-review-thread--idle-reply {
    min-height: 78px;
  }

  .inline-review-thread--file-level {
    margin-top: 8px;
  }

  .inline-review-thread:focus {
    outline: 2px solid var(--accent-purple);
    outline-offset: 2px;
  }

  @container (max-width: 520px) {
    .inline-review-thread {
      left: 8px;
      margin: 6px 0 8px;
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

  .review-thread-body--with-idle-reply {
    padding-right: 118px;
  }

  .review-thread-reply {
    margin-top: 8px;
  }

  textarea {
    box-sizing: border-box;
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

  .review-thread-error {
    margin: 6px 0 0;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .review-thread-actions {
    display: flex;
    justify-content: flex-end;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 8px;
  }

  .review-thread-actions--idle {
    position: absolute;
    right: 12px;
    bottom: 14px;
    margin-top: 0;
  }

  @container (max-width: 420px) {
    .review-thread-actions--idle {
      position: static;
      margin-top: 8px;
    }

    .review-thread-body--with-idle-reply {
      padding-right: 0;
    }
  }

  :global(.review-thread-btn.action-button) {
    min-height: 28px;
  }

  :global(.review-thread-btn--primary.action-button) {
    border-color: var(--accent-blue);
    background: var(--accent-blue);
    color: #fff;
  }
</style>
