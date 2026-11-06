<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import { SearchInput, type TypeaheadOption } from "@kenn-io/kit-ui";

  interface Props {
    options: TypeaheadOption[];
    /** The chosen author, or "" for anyone. */
    value: string;
    loading?: boolean;
    error?: string;
    onselect: (author: string) => void;
  }

  let { options, value, loading = false, error = "", onselect }: Props = $props();

  let query = $state("");

  // A chosen author no loaded item carries stays listed, so it can be seen and cleared.
  const allOptions = $derived(
    value === "" || options.some((option) => option.name === value)
      ? options
      : [{ name: value, label: value }, ...options],
  );

  const visibleOptions = $derived.by(() => {
    const needle = query.trim().toLowerCase();
    if (needle === "") return allOptions;
    return allOptions.filter(
      (option) => option.label.toLowerCase().includes(needle) || option.name.toLowerCase().includes(needle),
    );
  });
</script>

<div class="author-filter">
  <div class="author-filter__search">
    <SearchInput bind:value={query} size="sm" block placeholder="Filter authors" ariaLabel="Filter authors" />
  </div>
  {#if query.trim() === ""}
    {@render row("", "Anyone")}
  {/if}
  {#each visibleOptions as option (option.name)}
    {@render row(option.name, option.label)}
  {/each}
  {#if loading}
    <div class="author-filter__note">Loading authors…</div>
  {:else if error !== ""}
    <div class="author-filter__note author-filter__note--error">{error}</div>
  {:else if visibleOptions.length === 0}
    <div class="author-filter__note">{allOptions.length === 0 ? "No authors loaded" : "No matching authors"}</div>
  {/if}
</div>

{#snippet row(name: string, label: string)}
  <button
    class={["author-filter__row", value === name && "author-filter__row--active"]}
    type="button"
    aria-pressed={value === name}
    onclick={() => onselect(name)}
  >
    <span class="author-filter__name">{label}</span>
    <span class="author-filter__check" aria-hidden="true">
      {#if value === name}
        <CheckIcon size={10} strokeWidth={2.2} />
      {/if}
    </span>
  </button>
{/snippet}

<style>
  .author-filter {
    display: flex;
    flex-direction: column;
  }

  .author-filter__search {
    margin: 2px 8px 4px;
  }

  .author-filter__row {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 12px;
    border: 0;
    color: var(--text-muted);
    background: transparent;
    font-family: inherit;
    font-size: var(--font-size-xs);
    text-align: left;
    cursor: pointer;
  }

  .author-filter__row:hover {
    background: var(--bg-surface-hover);
  }

  .author-filter__row:focus-visible {
    outline: var(--focus-ring);
    outline-offset: -2px;
  }

  .author-filter__row--active {
    color: var(--text-primary);
  }

  .author-filter__name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .author-filter__check {
    width: 14px;
    height: 14px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--accent-green);
  }

  .author-filter__note {
    padding: 8px 12px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-align: center;
  }

  .author-filter__note--error {
    color: var(--accent-red);
  }
</style>
