<script lang="ts">
  import { Card, CopyButton } from "@kenn-io/kit-ui";
  import type { Snippet } from "svelte";

  interface Props {
    source: string;
    copied: boolean;
    oncopy: () => void;
    headerActions?: Snippet;
    children: Snippet;
  }

  const { source, copied, oncopy, headerActions, children }: Props = $props();

  let collapsed = $state(false);
  const isLong = $derived(source.length > 1_500);

  function toggleCollapsed(): void {
    collapsed = !collapsed;
  }
</script>

<div class="detail-description">
  <div class="detail-description__header">
    <span class="detail-description__title">Description</span>
    <div class="detail-description__actions">
      <CopyButton
        class={copied ? "body-copy body-copy--copied" : "body-copy"}
        {copied}
        onclick={oncopy}
        ariaLabel="Copy to clipboard"
        copiedAriaLabel="Copied!"
        title="Copy to clipboard"
        copiedTitle="Copied!"
      />
      {#if headerActions}
        {@render headerActions()}
      {/if}
      {#if isLong}
        <button
          type="button"
          class="detail-description__toggle"
          aria-label={isLong && collapsed ? "Expand description" : "Collapse description"}
          aria-expanded={!(isLong && collapsed)}
          onclick={toggleCollapsed}
        >
          {isLong && collapsed ? "Expand" : "Collapse"}
        </button>
      {/if}
    </div>
  </div>
  <div class="detail-description__card-wrap">
    <Card
      level="inset"
      padding="none"
      class={isLong && collapsed
        ? "detail-description-card detail-description-card--compact"
        : "detail-description-card"}
    >
      {@render children()}
    </Card>
  </div>
</div>

<style>
  .detail-description {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .detail-description__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .detail-description__title {
    color: var(--text-primary);
    font-size: var(--font-size-md);
    font-weight: var(--font-weight-semibold);
    letter-spacing: var(--letter-spacing-label, 0.05em);
    text-transform: var(--label-transform, uppercase);
  }

  .detail-description__actions {
    display: flex;
    align-items: center;
    gap: var(--space-4);
  }

  .detail-description__toggle {
    padding: 0;
    border: 0;
    background: none;
    color: var(--text-muted);
    cursor: pointer;
    font-family: inherit;
    font-size: var(--font-size-2xs);
  }

  .detail-description__toggle:hover {
    color: var(--accent-blue);
  }

  /* The description is the item's lead text, not a quoted block: it sits
     on the page surface without a well. Card's public custom properties
     carry the change. */
  .detail-description__card-wrap :global(.kit-card.detail-description-card) {
    --kit-card-bg: transparent;
    --kit-card-border: transparent;
  }

  :global(.detail-description-card) {
    overflow: hidden;
  }

  :global(.detail-description-card--compact) {
    max-height: 320px;
    overflow-y: auto;
  }

  @media (max-width: 640px) {
    .detail-description__toggle {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-width: var(--detail-mobile-hit-target, 37px);
      min-height: var(--detail-mobile-hit-target, 37px);
      padding: var(--detail-mobile-space-xs, var(--space-3));
      font-size: var(--detail-mobile-type-sm, var(--font-size-sm));
    }

    .detail-description__header :global(.kit-copy-btn.body-copy) {
      min-width: var(--detail-mobile-hit-target, 37px);
      min-height: var(--detail-mobile-hit-target, 37px);
      padding: var(--detail-mobile-space-xs, var(--space-3));
    }
  }
</style>
