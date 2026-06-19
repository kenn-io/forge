<script lang="ts">
  import type { RateLimitHostStatus } from "@middleman/ui/api/types";
  import { budgetColor, worstCaseRatio } from "./budget-utils";

  interface Props {
    hosts: Record<string, RateLimitHostStatus>;
    onclick?: () => void;
    expanded?: boolean;
  }

  let { hosts, onclick, expanded = false }: Props = $props();

  function anyPaused(): boolean {
    return Object.values(hosts).some((h) => h.sync_paused);
  }

  function restEntries() {
    return Object.values(hosts).map((h) => ({
      remaining: h.rate_remaining,
      limit: h.rate_limit,
      known: h.known,
    }));
  }

  function gqlEntries() {
    return Object.values(hosts).map((h) => ({
      remaining: h.gql_remaining ?? -1,
      limit: h.gql_limit ?? -1,
      known: h.gql_known ?? false,
    }));
  }

  function restRatio() {
    return worstCaseRatio(restEntries());
  }

  function gqlRatio() {
    return worstCaseRatio(gqlEntries());
  }

  function barColor(ratio: number): string {
    if (anyPaused()) return "var(--budget-red)";
    return budgetColor(ratio);
  }

  const rr = $derived(restRatio());
  const gr = $derived(gqlRatio());
  const paused = $derived(anyPaused());
</script>

<button
  type="button"
  class="budget-bars"
  {onclick}
  aria-haspopup="dialog"
  aria-expanded={expanded}
>

  <span class="budget-bar-group">
    <span
      class="budget-label"
      style:color={rr >= 0 ? barColor(rr) : paused ? "var(--budget-red)" : "var(--text-muted)"}
    >{rr >= 0 ? "REST" : "--"}</span>
    <span class="budget-track">
      {#if rr >= 0}
        <span
          class="budget-fill"
          style:width="{Math.max(rr * 100, 2)}%"
          style:background={barColor(rr)}
        ></span>
      {/if}
    </span>
  </span>

  <span class="budget-bar-group">
    <span
      class="budget-label"
      style:color={gr >= 0 ? barColor(gr) : paused ? "var(--budget-red)" : "var(--text-muted)"}
    >{gr >= 0 ? "GQL" : "--"}</span>
    <span class="budget-track">
      {#if gr >= 0}
        <span
          class="budget-fill"
          style:width="{Math.max(gr * 100, 2)}%"
          style:background={barColor(gr)}
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
    gap: 3px;
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
    border-radius: 2px;
    transition: width 0.5s ease;
  }
</style>
