<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import MinusIcon from "@lucide/svelte/icons/minus";
  import { SearchInput } from "@kenn-io/kit-ui";

  import type { LabelFilterChoice, LabelFilterMode } from "../../utils/label-filter.js";

  interface Props {
    choices: LabelFilterChoice[];
    oncycle: (name: string) => void;
  }

  let { choices, oncycle }: Props = $props();

  let query = $state("");

  const visibleChoices = $derived.by(() => {
    const needle = query.trim().toLowerCase();
    if (needle === "") return choices;
    return choices.filter((choice) => choice.key.includes(needle));
  });

  const stateText: Record<LabelFilterMode | "neutral", string> = {
    neutral: "not filtered",
    require: "required",
    exclude: "excluded",
  };

  const nextAction: Record<LabelFilterMode | "neutral", string> = {
    neutral: "Click to require this label",
    require: "Click to exclude this label",
    exclude: "Click to stop filtering by this label",
  };

  function dotStyle(color: string): string | undefined {
    if (color === "") return undefined;
    return `--label-color: #${color.replace(/^#/, "")}`;
  }
</script>

<div class="label-filter">
  <div class="label-filter__search">
    <SearchInput bind:value={query} size="sm" block placeholder="Search labels" ariaLabel="Search labels" />
  </div>
  {#each visibleChoices as choice (choice.key)}
    {@const state = choice.mode ?? "neutral"}
    <button
      class={["label-filter__row", `label-filter__row--${state}`]}
      type="button"
      title={nextAction[state]}
      onclick={() => oncycle(choice.name)}
    >
      <span class="label-filter__state" aria-hidden="true">
        {#if state === "require"}
          <CheckIcon size={10} strokeWidth={2.4} />
        {:else if state === "exclude"}
          <MinusIcon size={10} strokeWidth={2.4} />
        {/if}
      </span>
      <span class="label-filter__dot" style={dotStyle(choice.color)} aria-hidden="true"></span>
      <span class="label-filter__name">{choice.name}</span>
      <span class="kit-sr-only">, {stateText[state]}</span>
      {#if choice.count > 0}
        <span class="label-filter__count">{choice.count}</span>
      {/if}
    </button>
  {:else}
    <div class="label-filter__empty">
      {choices.length === 0 ? "No labels on loaded pull requests" : "No matching labels"}
    </div>
  {/each}
</div>

<style>
  .label-filter {
    display: flex;
    flex-direction: column;
  }

  .label-filter__search {
    margin: 2px 8px 4px;
  }

  .label-filter__row {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 12px;
    border: 0;
    color: var(--text-secondary);
    background: transparent;
    font-family: inherit;
    font-size: var(--font-size-xs);
    text-align: left;
    cursor: pointer;
  }

  .label-filter__row:hover {
    background: var(--bg-surface-hover);
  }

  .label-filter__row:focus-visible {
    outline: var(--focus-ring);
    outline-offset: -2px;
  }

  .label-filter__state {
    width: 14px;
    height: 14px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    color: white;
  }

  .label-filter__row--require .label-filter__state {
    border-color: var(--accent-green);
    background: var(--accent-green);
  }

  .label-filter__row--exclude .label-filter__state {
    border-color: var(--accent-red);
    background: var(--accent-red);
  }

  .label-filter__dot {
    width: 8px;
    height: 8px;
    flex-shrink: 0;
    border-radius: var(--radius-dot, 50%);
    background: var(--label-color, var(--text-muted));
  }

  .label-filter__name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .label-filter__row--neutral .label-filter__name {
    color: var(--text-muted);
  }

  .label-filter__row--require .label-filter__name {
    color: var(--text-primary);
  }

  .label-filter__row--exclude .label-filter__name {
    color: var(--text-muted);
    text-decoration: line-through;
  }

  .label-filter__count {
    flex-shrink: 0;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .label-filter__empty {
    padding: 8px 12px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-align: center;
  }
</style>
