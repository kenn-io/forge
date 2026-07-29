<script lang="ts">
  import { Card, CopyButton } from "@kenn-io/kit-ui";
  import type { Snippet } from "svelte";

  interface Props {
    source: string;
    itemKey: string;
    copied: boolean;
    oncopy: () => void;
    headerActions?: Snippet;
    children: Snippet;
  }

  const { source, itemKey, copied, oncopy, headerActions, children }: Props = $props();

  let collapsedItemKey = $state<string | null>(null);
  const isLong = $derived(source.length > 1_500);
  const collapsed = $derived(isLong && collapsedItemKey === itemKey);

  function toggleCollapsed(): void {
    collapsedItemKey = collapsed ? null : itemKey;
  }
</script>

<div class="detail-description__header">
  <span class="detail-description__title">Description</span>
  <div class="detail-description__actions">
    {#if headerActions}
      {@render headerActions()}
    {/if}
    {#if isLong}
      <button
        type="button"
        class="detail-description__toggle"
        aria-label={collapsed ? "Expand description" : "Collapse description"}
        aria-expanded={!collapsed}
        onclick={toggleCollapsed}
      >
        {collapsed ? "Expand" : "Collapse"}
      </button>
    {/if}
  </div>
</div>
<div class="detail-description__card-wrap">
  <CopyButton
    class={copied ? "body-copy body-copy--copied" : "body-copy"}
    {copied}
    onclick={oncopy}
    revealOnHover
    ariaLabel="Copy to clipboard"
    copiedAriaLabel="Copied!"
    title="Copy to clipboard"
    copiedTitle="Copied!"
  />
  <Card
    level="inset"
    padding="none"
    class={collapsed
      ? "detail-description-card detail-description-card--compact"
      : "detail-description-card"}
  >
    {@render children()}
  </Card>
</div>

<style>
  .detail-description__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .detail-description__title {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    font-weight: 600;
    letter-spacing: 0.05em;
    text-transform: uppercase;
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

  .detail-description__card-wrap {
    position: relative;
  }

  .detail-description__card-wrap :global(.kit-copy-btn.body-copy) {
    position: absolute;
    top: var(--space-3);
    right: var(--space-3);
    z-index: 1;
  }

  .detail-description__card-wrap:hover :global(.kit-copy-btn.body-copy),
  .detail-description__card-wrap :global(.kit-copy-btn.body-copy--copied) {
    opacity: 1;
  }

  :global(.detail-description-card) {
    overflow: hidden;
  }

  :global(.detail-description-card--compact) {
    max-height: 320px;
    overflow-y: auto;
  }

  @media (max-width: 640px) {
    .detail-description__card-wrap :global(.kit-copy-btn.body-copy) {
      position: static;
      min-width: var(--detail-mobile-hit-target, 37px);
      min-height: var(--detail-mobile-hit-target, 37px);
      padding: var(--detail-mobile-space-xs, var(--space-3));
      opacity: 1;
    }
  }
</style>
