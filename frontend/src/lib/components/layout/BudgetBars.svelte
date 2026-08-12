<script lang="ts">
  import type { RateLimitHostStatus } from "../../api/types.js";
  import { budgetColor, worstCaseRatio } from "./budget-utils";

  interface Props {
    providerPools: Record<string, RateLimitHostStatus>;
    onclick?: () => void;
    expanded?: boolean;
  }

  let { providerPools, onclick, expanded = false }: Props = $props();

  function resourceAtReserve(resource: "rest" | "graphql"): boolean {
    return Object.values(providerPools).some((pool) => {
      const status = pool[resource];
      return (
        status.known &&
        status.limit > 0 &&
        status.remaining >= 0 &&
        status.remaining <= pool.reserve_buffer
      );
    });
  }

  function restEntries() {
    return Object.values(providerPools).map((pool) => pool.rest);
  }

  function gqlEntries() {
    return Object.values(providerPools).map((pool) => pool.graphql);
  }

  function restRatio() {
    return worstCaseRatio(restEntries());
  }

  function gqlRatio() {
    return worstCaseRatio(gqlEntries());
  }

  function barColor(ratio: number, atReserve: boolean): string {
    if (atReserve) return "var(--budget-red)";
    return budgetColor(ratio);
  }

  const rr = $derived(restRatio());
  const gr = $derived(gqlRatio());
  const restAtReserve = $derived(resourceAtReserve("rest"));
  const gqlAtReserve = $derived(resourceAtReserve("graphql"));
</script>

<button
  type="button"
  class="budget-bars"
  {onclick}
  aria-haspopup="dialog"
  aria-expanded={expanded}
  aria-label="Show provider quota details"
>

  <span class="budget-bar-group">
    <span
      class="budget-label"
      style:color={rr >= 0 ? barColor(rr, restAtReserve) : "var(--text-muted)"}
    >{rr >= 0 ? "REST" : "--"}</span>
    <span class="budget-track">
      {#if rr >= 0}
        <span
          class="budget-fill"
          style:width="{Math.max(rr * 100, 0)}%"
          style:background={barColor(rr, restAtReserve)}
        ></span>
      {/if}
    </span>
  </span>

  <span class="budget-bar-group">
    <span
      class="budget-label"
      style:color={gr >= 0 ? barColor(gr, gqlAtReserve) : "var(--text-muted)"}
    >{gr >= 0 ? "GQL" : "--"}</span>
    <span class="budget-track">
      {#if gr >= 0}
        <span
          class="budget-fill"
          style:width="{Math.max(gr * 100, 0)}%"
          style:background={barColor(gr, gqlAtReserve)}
        ></span>
      {/if}
    </span>
  </span>
</button>

<style>
  .budget-bars {
    /* reset button defaults */
    appearance: none;
    border: none;
    background: none;
    font: inherit;
    color: inherit;

    display: flex;
    align-items: center;
    gap: 4px;
    cursor: pointer;
    padding: 1px 4px;
    border-radius: 3px;
  }
  .budget-bars:hover {
    background: var(--bg-hover, rgba(255, 255, 255, 0.05));
  }
  .budget-bars:focus-visible {
    outline: 2px solid var(--accent-green);
    outline-offset: 1px;
  }
  .budget-bar-group {
    display: flex;
    align-items: center;
    gap: var(--space-1);
  }
  .budget-label {
    font-size: 0.9em;
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.3px;
  }
  .budget-track {
    display: inline-block;
    width: 32px;
    height: 4px;
    background: var(--budget-bar-bg);
    border-radius: 2px;
    overflow: hidden;
  }
  .budget-fill {
    display: block;
    height: 100%;
    margin-left: auto;
    border-radius: 2px;
  }
</style>
