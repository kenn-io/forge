<script lang="ts">
  import { onMount, tick } from "svelte";
  import XIcon from "@lucide/svelte/icons/x";
  import type { DiffReviewDraftComment } from "../../stores/diff-review-draft.svelte.js";
  import ActionButton from "../shared/ActionButton.svelte";

  interface Props {
    comment: DiffReviewDraftComment;
    location: string;
    disabled: boolean;
    onjump?: ((comment: DiffReviewDraftComment) => void) | undefined;
    ondelete: (id: string) => void;
  }

  const { comment, location, disabled, onjump, ondelete }: Props = $props();

  let expanded = $state(false);
  let truncated = $state(false);
  let bodyElement: HTMLParagraphElement | undefined = $state();
  let measureFrame: number | undefined;

  function measureTruncation(): void {
    if (!bodyElement) {
      truncated = false;
      return;
    }
    truncated = bodyElement.scrollHeight > bodyElement.clientHeight + 1
      || bodyElement.scrollWidth > bodyElement.clientWidth + 1;
  }

  function queueMeasure(): void {
    if (measureFrame !== undefined) {
      cancelAnimationFrame(measureFrame);
    }
    measureFrame = requestAnimationFrame(() => {
      measureFrame = undefined;
      measureTruncation();
    });
  }

  function toggleExpanded(): void {
    expanded = !expanded;
    void tick().then(queueMeasure);
  }

  function scheduleMeasure(_body: string, _expanded: boolean): void {
    void tick().then(queueMeasure);
  }

  onMount(() => {
    const observer = typeof ResizeObserver === "undefined"
      ? undefined
      : new ResizeObserver(queueMeasure);
    if (bodyElement) observer?.observe(bodyElement);
    queueMeasure();

    return () => {
      observer?.disconnect();
      if (measureFrame !== undefined) {
        cancelAnimationFrame(measureFrame);
      }
    };
  });

  $effect(() => scheduleMeasure(comment.body, expanded));
</script>

<div class="draft-item">
  <div class="draft-content">
    <button
      class="draft-jump"
      type="button"
      onclick={() => onjump?.(comment)}
    >
      {location}
    </button>
    <p
      bind:this={bodyElement}
      class={["draft-body", expanded && "draft-body--expanded"]}
    >
      {comment.body}
    </p>
    {#if truncated || expanded}
      <button class="draft-expand" type="button" onclick={toggleExpanded}>
        {expanded ? "Show less" : "Show full comment"}
      </button>
    {/if}
  </div>
  <ActionButton
    class="icon-btn"
    title="Delete draft comment"
    ariaLabel="Delete draft comment"
    size="sm"
    onclick={() => ondelete(comment.id)}
    disabled={disabled}
  >
    <XIcon size={13} />
  </ActionButton>
</div>

<style>
  .draft-item {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 26px;
    align-items: start;
    gap: 10px;
    min-width: 0;
    padding: 8px 8px 8px 10px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    background: color-mix(in srgb, var(--bg-inset) 84%, var(--bg-surface));
  }

  .draft-content {
    display: grid;
    gap: 3px;
    min-width: 0;
  }

  .draft-body {
    display: -webkit-box;
    margin: 0;
    overflow: hidden;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    line-height: 1.42;
    overflow-wrap: anywhere;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
  }

  .draft-body--expanded {
    display: block;
    line-clamp: unset;
    -webkit-line-clamp: unset;
  }

  .draft-jump {
    display: block;
    max-width: 100%;
    padding: 0;
    border: 0;
    overflow: hidden;
    background: transparent;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    line-height: 1.35;
    text-align: left;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: pointer;
  }

  .draft-jump:hover {
    color: var(--accent-blue);
    text-decoration: underline;
  }

  .draft-expand {
    justify-self: start;
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--accent-blue);
    font-size: var(--font-size-xs);
    font-weight: 600;
    line-height: 1.4;
    cursor: pointer;
  }

  .draft-expand:hover {
    text-decoration: underline;
  }

  :global(.icon-btn.action-button) {
    width: 26px;
    height: 26px;
    min-height: 26px;
    padding: 0;
  }
</style>
