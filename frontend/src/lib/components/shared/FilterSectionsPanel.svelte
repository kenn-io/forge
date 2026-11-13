<script lang="ts">
  // Renders the shared filter sections inline for the phone shell, with the
  // phone's controls: one select per pick-one section, an Any/Yes/No select per
  // three-way filter, a toggle per on/off filter, and an in-place row for a
  // submenu such as Labels. Every phone list (PRs, issues, Activity) uses it.
  // Mount it inside .mobile-shell, which defines the --mobile-chrome-space-* tokens.
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import { SelectDropdown, Toggle } from "@kenn-io/kit-ui";

  import type { FilterChoice, FilterSection } from "./list-filter-menus.js";

  interface Props {
    sections: FilterSection[];
    resetLabel: string;
    onReset: () => void;
  }

  const { sections, resetLabel, onReset }: Props = $props();

  const ANY = "any";
  const TRI_OPTIONS = [
    { value: ANY, label: "Any" },
    { value: "require", label: "Yes" },
    { value: "exclude", label: "No" },
  ];

  const panelId = $props.id();
  let openSubmenu = $state<string | null>(null);

  function pickOptions(section: FilterSection) {
    const options = section.items.map((item) => ({ value: item.id, label: item.label }));
    return section.pick?.defaultId === undefined ? [{ value: ANY, label: "Any" }, ...options] : options;
  }

  function pick(section: FilterSection, value: string): void {
    if (value === ANY) section.pick?.clear?.();
    else section.items.find((item) => item.id === value && !item.active)?.onSelect();
  }

  function describedBy(item: FilterChoice): string | undefined {
    return item.description ? `${panelId}-${item.id}-description` : undefined;
  }

  function toggleDescription(item: FilterChoice): { ariaDescribedby?: string } {
    const id = describedBy(item);
    return id ? { ariaDescribedby: id } : {};
  }
</script>

{#snippet values(item: FilterChoice)}
  {#if item.count !== undefined}<span class="filter-sections-panel__count">{item.count}</span>{/if}
  {#if item.detail}<span class="filter-sections-panel__detail">{item.detail}</span>{/if}
{/snippet}

{#snippet description(item: FilterChoice)}
  {#if item.description}
    <span id={describedBy(item)} class="kit-sr-only">{item.description}</span>
  {/if}
{/snippet}

<div class="filter-sections-panel">
  {#each sections as section, index (section.title ?? index)}
    <section class="filter-sections-panel__section" aria-label={section.title}>
      {#if section.pick}
        <div class="filter-sections-panel__cell filter-sections-panel__select" data-pick>
          <span class="filter-sections-panel__title">{section.title}</span>
          <SelectDropdown
            class="filter-sections-panel__dropdown"
            title={section.title ?? ""}
            value={section.items.find((item) => item.active)?.id ?? ANY}
            options={pickOptions(section)}
            onchange={(value) => pick(section, value)}
          />
        </div>
      {:else}
        {#if section.title}
          <h3 class="filter-sections-panel__title filter-sections-panel__heading">{section.title}</h3>
        {/if}
        {#each section.items as item (item.id)}
          {#if item.submenu}
            {@const expanded = openSubmenu === item.id}
            <button
              type="button"
              class="filter-sections-panel__cell filter-sections-panel__item filter-sections-panel__row"
              class:filter-sections-panel__item--active={item.active}
              title={item.description}
              aria-describedby={describedBy(item)}
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
          {:else if item.tri}
            {@const tri = item.tri}
            <div
              class="filter-sections-panel__cell filter-sections-panel__item filter-sections-panel__select"
              title={item.description}
              data-tri={tri.value ?? ANY}
            >
              <span class="filter-sections-panel__title filter-sections-panel__label">{item.label}</span>
              {@render values(item)}
              <SelectDropdown
                class="filter-sections-panel__dropdown"
                title={item.label}
                value={tri.value ?? ANY}
                options={TRI_OPTIONS}
                onchange={(value) => tri.set(value === "require" || value === "exclude" ? value : undefined)}
              />
            </div>
            {@render description(item)}
          {:else}
            <div class="filter-sections-panel__cell filter-sections-panel__item filter-sections-panel__toggle" title={item.description}>
              <Toggle
                checked={item.active}
                disabled={item.disabled ?? false}
                {...toggleDescription(item)}
                onchange={item.onSelect}
              >
                <span class="filter-sections-panel__label">{item.label}</span>
                {@render values(item)}
              </Toggle>
            </div>
            {@render description(item)}
          {/if}
        {/each}
      {/if}
    </section>
  {/each}
  <button type="button" class="filter-sections-panel__reset" onclick={onReset}>
    {resetLabel}
  </button>
</div>

<style>
  .filter-sections-panel {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--mobile-chrome-space-xs);
    width: 100%;
  }

  /* Sections only group their cells; the cells sit on the panel's two-column grid. */
  .filter-sections-panel__section {
    display: contents;
  }

  .filter-sections-panel__heading {
    grid-column: 1 / -1;
    margin: var(--space-2) 0 0;
  }

  .filter-sections-panel__title {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 750;
    letter-spacing: 0.01em;
  }

  .filter-sections-panel__cell {
    min-width: 0;
    min-height: var(--mobile-chrome-hit-target);
  }

  .filter-sections-panel__select,
  .filter-sections-panel__row {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: center;
    gap: var(--mobile-chrome-space-xs);
    padding: 0 var(--mobile-chrome-space-sm);
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    color: var(--text-secondary);
  }

  .filter-sections-panel__select:has(.filter-sections-panel__count, .filter-sections-panel__detail) {
    grid-template-columns: auto auto minmax(0, 1fr);
  }

  .filter-sections-panel__row {
    grid-column: 1 / -1;
    grid-template-columns: auto minmax(0, 1fr) auto;
    font-size: var(--font-size-sm);
    font-weight: 750;
    text-align: left;
    cursor: pointer;
  }

  .filter-sections-panel__item--active {
    color: var(--accent-blue);
    border-color: color-mix(in srgb, var(--accent-blue) 34%, transparent);
  }

  .filter-sections-panel__toggle {
    display: flex;
    padding: 0 var(--space-2);
  }

  .filter-sections-panel__toggle :global(.kit-toggle) {
    width: 100%;
    min-height: var(--mobile-chrome-hit-target);
  }

  .filter-sections-panel__select :global(.filter-sections-panel__dropdown) {
    width: 100%;
    min-width: 0;
  }

  .filter-sections-panel__select :global(.filter-sections-panel__dropdown .kit-select-dropdown__trigger) {
    height: auto;
    min-height: calc(var(--mobile-chrome-hit-target) - var(--mobile-chrome-space-sm));
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 750;
  }

  .filter-sections-panel__select :global(.filter-sections-panel__dropdown .kit-select-dropdown__list) {
    left: 0;
    right: auto;
    width: min(260px, calc(100vw - (var(--mobile-chrome-space-sm) * 2)));
    max-width: calc(100vw - (var(--mobile-chrome-space-sm) * 2));
    padding: var(--space-2);
    border-radius: var(--radius-md);
  }

  .filter-sections-panel__select :global(.filter-sections-panel__dropdown .kit-select-dropdown__option) {
    min-height: var(--mobile-chrome-hit-target);
    padding: var(--mobile-chrome-space-xs) var(--mobile-chrome-space-sm);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
    line-height: 1.2;
  }

  .filter-sections-panel__count,
  .filter-sections-panel__detail {
    font-weight: 400;
    color: var(--text-muted);
  }

  .filter-sections-panel__row :global(.filter-sections-panel__chevron--open) {
    transform: rotate(180deg);
  }

  .filter-sections-panel__submenu :global(.label-filter__row),
  .filter-sections-panel__submenu :global(.author-filter__row) {
    min-height: var(--mobile-chrome-hit-target);
  }

  .filter-sections-panel__submenu {
    grid-column: 1 / -1;
    border: thin solid var(--border-muted);
    border-radius: var(--radius-md);
  }

  .filter-sections-panel__reset {
    grid-column: 1 / -1;
    min-height: var(--mobile-chrome-hit-target);
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
    cursor: pointer;
  }
</style>
