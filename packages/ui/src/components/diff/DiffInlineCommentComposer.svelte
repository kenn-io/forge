<script lang="ts">
  import SendIcon from "@lucide/svelte/icons/send";
  import XIcon from "@lucide/svelte/icons/x";
  import { onMount, tick } from "svelte";
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
  let composerEl: HTMLDivElement | undefined = $state();
  let textareaEl: HTMLTextAreaElement | undefined = $state();
  let composerWidth = $state<string | undefined>();
  let composerOffset = $state<string | undefined>();
  const submitting = $derived(diffReviewDraft.isSubmitting());
  const error = $derived(diffReviewDraft.getError());

  onMount(() => {
    let animationFrame = 0;
    const scheduleLayout = () => {
      if (animationFrame) cancelAnimationFrame(animationFrame);
      animationFrame = requestAnimationFrame(() => {
        animationFrame = 0;
        updateComposerWidth();
      });
    };
    const container = composerEl ? layoutContainer(composerEl) : null;
    const resizeObserver = typeof ResizeObserver !== "undefined"
      ? new ResizeObserver(scheduleLayout)
      : undefined;
    if (container) {
      container.addEventListener("scroll", scheduleLayout, { passive: true });
      resizeObserver?.observe(container);
    }
    if (composerEl) resizeObserver?.observe(composerEl);
    window.addEventListener("resize", scheduleLayout);

    void tick().then(() => {
      textareaEl?.focus({ preventScroll: true });
      scheduleLayout();
    });

    return () => {
      if (animationFrame) cancelAnimationFrame(animationFrame);
      container?.removeEventListener("scroll", scheduleLayout);
      window.removeEventListener("resize", scheduleLayout);
      resizeObserver?.disconnect();
    };
  });

  async function submit(): Promise<void> {
    const nextBody = body.trim();
    if (!nextBody) return;
    const ok = await diffReviewDraft.createComment(nextBody, range);
    if (ok) {
      body = "";
      onclose?.();
    }
  }

  function updateComposerWidth(): void {
    if (!composerEl) return;
    const container = layoutContainer(composerEl);
    if (!container) {
      composerWidth = undefined;
      return;
    }
    const containerRect = container.getBoundingClientRect();
    const composerRect = composerEl.getBoundingClientRect();
    const currentOffset = Number.parseFloat(composerOffset ?? "0") || 0;
    const naturalLeft = composerRect.left - currentOffset;
    const available = Math.floor(containerRect.width - 24);
    const offset = Math.round(containerRect.left + 12 - naturalLeft);
    composerWidth = available > 0 ? `${available}px` : undefined;
    composerOffset = offset === 0 ? undefined : `${offset}px`;
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
  class="inline-composer"
  bind:this={composerEl}
  style:--inline-composer-width={composerWidth}
  style:--inline-composer-offset={composerOffset}
>
  <textarea
    bind:this={textareaEl}
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
    position: sticky;
    left: 12px;
    box-sizing: border-box;
    margin: 6px 0 8px;
    padding: 8px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-surface);
    width: var(--inline-composer-width, calc(100% - 24px));
    max-width: var(--inline-composer-width, calc(100% - 24px));
    min-width: 0;
    overflow: hidden;
    transform: translateX(var(--inline-composer-offset, 0));
  }

  @container (max-width: 520px) {
    .inline-composer {
      left: 8px;
      margin: 6px 0 8px;
    }
  }

  textarea {
    box-sizing: border-box;
    width: 100%;
    max-width: 100%;
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
    flex-wrap: wrap;
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
