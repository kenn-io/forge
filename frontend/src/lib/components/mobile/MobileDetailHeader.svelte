<script lang="ts">
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";

  interface Props {
    itemType: "pr" | "issue";
    number: number;
    backLabel: string;
    onBack: () => void;
  }

  let { itemType, number, backLabel, onBack }: Props = $props();
</script>

<header class="mobile-detail-header">
  <button
    type="button"
    class="mobile-detail-header__back"
    aria-label={`Back to ${backLabel.toLowerCase()}`}
    onclick={onBack}
  >
    <ArrowLeftIcon size="20" strokeWidth="2" aria-hidden="true" />
    <strong>{backLabel}</strong>
  </button>
  <span class="mobile-detail-header__badge" class:issue={itemType === "issue"}>
    {itemType === "pr" ? "PR" : "Issue"} #{number}
  </span>
</header>

<style>
  .mobile-detail-header {
    flex: 0 0 auto;
    min-height: 3.5rem;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 0.375rem 0.625rem;
    border-bottom: thin solid var(--border-default);
    background: var(--bg-surface);
  }

  .mobile-detail-header__back {
    min-width: 0;
    min-height: 2.75rem;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0 0.625rem;
    border: 0;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    background: transparent;
    font: inherit;
    text-align: left;
  }

  .mobile-detail-header__back strong {
    max-width: 12rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-primary);
    font-size: var(--font-size-md);
  }

  .mobile-detail-header__back:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 2px;
  }

  .mobile-detail-header__badge {
    flex: 0 0 auto;
    padding: 0.25rem 0.625rem;
    border-radius: 999px;
    color: var(--text-on-accent);
    background: var(--accent-green);
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    font-weight: 700;
  }

  .mobile-detail-header__badge.issue {
    background: var(--accent-amber);
  }
</style>
