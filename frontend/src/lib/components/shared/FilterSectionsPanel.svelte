<script lang="ts">
  // Renders FilterDropdown sections inline for the phone shell, where a
  // trigger-anchored popover does not fit. A row with a submenu expands it in place.
  // Mount it inside .mobile-shell, which defines the --mobile-chrome-space-* tokens.
  import CheckIcon from "@lucide/svelte/icons/check";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import XIcon from "@lucide/svelte/icons/x";
  import type { FilterDropdownItem, FilterDropdownSection } from "@kenn-io/kit-ui";

  interface Props {
    sections: FilterDropdownSection[];
    resetLabel: string;
    onReset: () => void;
  }

  const { sections, resetLabel, onReset }: Props = $props();

  const panelId = $props.id();
  let openSubmenu = $state<string | null>(null);
</script>

{#snippet values(item: FilterDropdownItem)}
  {#if item.count !== undefined}<span class="filter-sections-panel__count">{item.count}</span>{/if}
  {#if item.detail}<span class="filter-sections-panel__detail">{item.detail}</span>{/if}
{/snippet}

{#snippet description(item: FilterDropdownItem)}
  {#if item.description}
    <span id="{panelId}-{item.id}-description" class="kit-sr-only">{item.description}</span>
  {/if}
{/snippet}

<div class="filter-sections-panel">
  {#each sections as section, index (section.title ?? index)}
    <section class="filter-sections-panel__section" aria-label={section.title}>
      {#if section.title}
        <h3 class="filter-sections-panel__title">{section.title}</h3>
      {/if}
      <div class="filter-sections-panel__items">
        {#each section.items as item (item.id)}
          {#if item.submenu}
            {@const expanded = openSubmenu === item.id}
            <button
              type="button"
              class="filter-sections-panel__item"
              class:filter-sections-panel__item--active={item.active}
              title={item.description}
              aria-describedby={item.description ? `${panelId}-${item.id}-description` : undefined}
              aria-expanded={expanded}
              aria-controls="{panelId}-{item.id}"
              onclick={() => (openSubmenu = expanded ? null : item.id)}
            >
              <span class="filter-sections-panel__label">{item.label}</span>
              {@render values(item)}
              <ChevronDownIcon size={14} class={expanded ? "filter-sections-panel__chevron--open" : ""} aria-hidden="true" />
            </button>
            {@render description(item)}
            {#if expanded}
              <div id="{panelId}-{item.id}" class="filter-sections-panel__submenu" role="group" aria-label={item.label}>
                {@render item.submenu()}
              </div>
            {/if}
          {:else}
            <button
              type="button"
              class="filter-sections-panel__item"
              class:filter-sections-panel__item--active={item.active}
              class:filter-sections-panel__item--excluded={item.excluded}
              title={item.description}
              aria-describedby={item.description ? `${panelId}-${item.id}-description` : undefined}
              aria-pressed={item.active || item.excluded === true}
              disabled={item.disabled}
              onclick={item.onSelect}
            >
              {#if item.excluded}
                <XIcon size={13} strokeWidth={2.4} aria-hidden="true" />
              {:else if item.active}
                <CheckIcon size={13} strokeWidth={2.4} aria-hidden="true" />
              {/if}
              <span class="filter-sections-panel__label">{item.label}</span>{#if item.excluded}<span class="kit-sr-only">, excluded</span>{/if}
              {@render values(item)}
            </button>
            {@render description(item)}
          {/if}
        {/each}
      </div>
    </section>
  {/each}
  <button type="button" class="filter-sections-panel__reset" onclick={onReset}>
    {resetLabel}
  </button>
</div>

<style>
  .filter-sections-panel {
    display: flex;
    flex-direction: column;
    gap: var(--mobile-chrome-space-sm);
    width: 100%;
  }

  .filter-sections-panel__title {
    margin: 0 0 var(--space-2);
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-muted);
  }

  .filter-sections-panel__items {
    display: flex;
    flex-wrap: wrap;
    gap: var(--mobile-chrome-space-xs);
  }

  .filter-sections-panel__item,
  .filter-sections-panel__reset {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    min-height: var(--mobile-chrome-hit-target);
    padding: var(--mobile-chrome-space-xs) var(--mobile-chrome-space-sm);
    border: thin solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
    line-height: 1.3;
    white-space: nowrap;
    cursor: pointer;
  }

  .filter-sections-panel__item--active {
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 12%, transparent);
    border-color: color-mix(in srgb, var(--accent-blue) 34%, transparent);
  }

  .filter-sections-panel__item--excluded {
    color: var(--accent-red);
    background: color-mix(in srgb, var(--accent-red) 12%, transparent);
    border-color: color-mix(in srgb, var(--accent-red) 34%, transparent);
  }

  .filter-sections-panel__count,
  .filter-sections-panel__detail {
    font-weight: 400;
    color: var(--text-muted);
  }

  .filter-sections-panel__item :global(.filter-sections-panel__chevron--open) {
    transform: rotate(180deg);
  }

  .filter-sections-panel__submenu {
    flex: 1 0 100%;
    border: thin solid var(--border-muted);
    border-radius: var(--radius-lg);
  }

  .filter-sections-panel__reset {
    align-self: flex-start;
  }

</style>
