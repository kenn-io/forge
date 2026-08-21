<script lang="ts">
  import { IconButton, SearchInput } from "@kenn-io/kit-ui";
  import FunnelIcon from "@lucide/svelte/icons/funnel";

  interface Props {
    value?: string;
    placeholder: string;
    searchAriaLabel: string;
    filterAriaLabel?: string;
    filterControls: string;
    filtersExpanded: boolean;
    filtersActive?: boolean;
    oninput: (value: string) => void;
    ontoggle: () => void;
  }

  let {
    value = $bindable(""),
    placeholder,
    searchAriaLabel,
    filterAriaLabel = "Filters",
    filterControls,
    filtersExpanded,
    filtersActive = filtersExpanded,
    oninput,
    ontoggle,
  }: Props = $props();
</script>

<div class="mobile-triage-search-bar">
  <div class="mobile-triage-search-bar__search">
    <SearchInput
      bind:value
      size="sm"
      block
      {placeholder}
      ariaLabel={searchAriaLabel}
      {oninput}
    />
  </div>

  <div class="mobile-triage-search-bar__filter">
    <IconButton
      size="md"
      tone="info"
      ariaLabel={filterAriaLabel}
      title="Filters"
      ariaExpanded={filtersExpanded}
      ariaControls={filterControls}
      ariaPressed={filtersActive}
      onclick={ontoggle}
    >
      <FunnelIcon size={18} strokeWidth={2} aria-hidden="true" />
    </IconButton>
  </div>
</div>

<style>
  .mobile-triage-search-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-default);
    flex-shrink: 0;
    background: var(--bg-surface);
  }

  .mobile-triage-search-bar__search {
    flex: 1;
    min-width: 0;
  }

  .mobile-triage-search-bar__filter {
    display: none;
  }

  :global(.mobile-main) .mobile-triage-search-bar {
    --mobile-triage-space-sm: 10px;
    order: 1;
    gap: var(--mobile-triage-space-sm);
    padding: var(--mobile-triage-space-sm) 13px;
    border-bottom: thin solid var(--border-default);
  }

  :global(.mobile-main) .mobile-triage-search-bar__filter {
    display: flex;
    align-items: stretch;
    flex: 0 0 auto;
  }

  :global(.mobile-main) .mobile-triage-search-bar__filter :global(.kit-icon-button) {
    width: 44px;
    height: 100%;
    min-height: 44px;
    border: thin solid var(--border-default);
    border-radius: 8.5px;
    background: var(--bg-inset);
  }

  :global(.mobile-main) .mobile-triage-search-bar__search :global(.kit-search-input) {
    min-height: 44px;
    border-radius: 8.5px;
    font-size: var(--font-size-md);
  }
</style>
