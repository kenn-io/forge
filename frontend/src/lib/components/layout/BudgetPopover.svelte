<script lang="ts">
  import type { RateLimitHostStatus } from "@middleman/ui/api/types";
  import { budgetColor, formatCompact, syncBudgetColor } from "./budget-utils";

  interface Props {
    hosts: Record<string, RateLimitHostStatus>;
    onclose: () => void;
  }

  let { hosts, onclose }: Props = $props();

  let popoverEl: HTMLDivElement | undefined = $state();

  function handleClickOutside(e: MouseEvent) {
    if (popoverEl && !popoverEl.contains(e.target as Node)) {
      onclose();
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      onclose();
    }
  }

  $effect(() => {
    // Delay registration to avoid catching the click that opened the popover.
    const id = setTimeout(() => {
      document.addEventListener("click", handleClickOutside);
    }, 0);
    document.addEventListener("keydown", handleKeydown);
    return () => {
      clearTimeout(id);
      document.removeEventListener("click", handleClickOutside);
      document.removeEventListener("keydown", handleKeydown);
    };
  });

  function hostEntries() {
    return Object.entries(hosts);
  }

  function ratio(remaining: number, limit: number): number {
    if (limit <= 0) return -1;
    return remaining / limit;
  }

  function resetText(resetAt: string): string {
    if (!resetAt) return "";
    const ms = new Date(resetAt).getTime() - Date.now();
    if (ms <= 0) return "";
    const min = Math.ceil(ms / 60_000);
    return `resets ${min}m`;
  }

  function isHostFresh(h: RateLimitHostStatus): boolean {
    const restFresh = h.known && h.rate_limit > 0 && h.rate_remaining >= 0;
    const gqlFresh = (h.gql_known ?? false) && (h.gql_limit ?? 0) > 0 && (h.gql_remaining ?? -1) >= 0;
    return restFresh || gqlFresh;
  }

  function hostHealthColor(h: RateLimitHostStatus): string {
    if (h.sync_paused) return "var(--budget-red)";
    if (!isHostFresh(h)) return "var(--text-muted)";
    const restOk = h.known && h.rate_limit > 0 && h.rate_remaining >= 0;
    const gqlOk = (h.gql_known ?? false) && (h.gql_limit ?? 0) > 0 && (h.gql_remaining ?? -1) >= 0;
    const rr = restOk ? h.rate_remaining / h.rate_limit : 1;
    const gr = gqlOk ? (h.gql_remaining ?? 0) / (h.gql_limit ?? 1) : 1;
    return budgetColor(Math.min(rr, gr));
  }

  const singleHost = $derived(hostEntries().length === 1);
</script>

<div
  class="budget-popover"
  role="dialog"
  aria-label="API Budget"
  bind:this={popoverEl}
>
  <div class="popover-header">API Budget</div>

  {#each hostEntries() as [hostname, h], i (hostname)}
    {#if i > 0}
      <div class="popover-divider"></div>
    {/if}

    <div class="host-section">
      {#if !singleHost}
        <div class="host-name">
          <span
            class="health-dot"
            class:health-dot--unknown={!isHostFresh(h)}
            style:background={hostHealthColor(h)}
          ></span>
          {hostname}
        </div>
      {/if}

      <!-- REST -->
      <div class="budget-row">
        <span class="row-label">REST</span>
        {#if h.known && ratio(h.rate_remaining, h.rate_limit) >= 0}
          {@const rr = ratio(h.rate_remaining, h.rate_limit)}
          <span class="row-bar-cell">
            <span class="bar-track">
              <span
                class="bar-fill"
                style:width="{Math.max(rr * 100, 2)}%"
                style:background={budgetColor(rr)}
              ></span>
            </span>
          </span>
          <span class="row-value">
            {formatCompact(h.rate_remaining)} / {formatCompact(h.rate_limit)} <span class="row-unit">req</span>
            {#if resetText(h.rate_reset_at)}<span class="row-reset"> · {resetText(h.rate_reset_at)}</span>{/if}
          </span>
        {:else}
          <span class="row-bar-cell"></span>
          <span class="row-unknown">not yet observed</span>
        {/if}
      </div>

      <!-- GraphQL -->
      <div class="budget-row">
        <span class="row-label">GraphQL</span>
        {#if (h.gql_known ?? false) && ratio(h.gql_remaining ?? -1, h.gql_limit ?? -1) >= 0}
          {@const gr = ratio(h.gql_remaining ?? -1, h.gql_limit ?? -1)}
          <span class="row-bar-cell">
            <span class="bar-track">
              <span
                class="bar-fill"
                style:width="{Math.max(gr * 100, 2)}%"
                style:background={budgetColor(gr)}
              ></span>
            </span>
          </span>
          <span class="row-value">
            {formatCompact(h.gql_remaining ?? 0)} / {formatCompact(h.gql_limit ?? 0)} <span class="row-unit">pts</span>
            {#if resetText(h.gql_reset_at ?? "")}<span class="row-reset"> · {resetText(h.gql_reset_at ?? "")}</span>{/if}
          </span>
        {:else}
          <span class="row-bar-cell"></span>
          <span class="row-unknown">not yet observed</span>
        {/if}
      </div>

      <!-- Eager refresh budget -->
      {#if h.budget_limit > 0}
        <div class="budget-row budget-row--eager">
          <span class="row-label">Eager refresh</span>
          <span class="row-bar-cell"></span>
          <span class="row-value">
            <span
              class="budget-spent"
              class:budget-spent--over={h.budget_spent > h.budget_limit}
              style:color={syncBudgetColor(h.budget_spent, h.budget_limit)}
            >{formatCompact(h.budget_spent)}</span> / {formatCompact(h.budget_limit)} <span class="row-unit">budgeted req/hr</span>
          </span>
          <span class="row-note">
            {#if h.budget_spent > h.budget_limit}
              Details, comments, and backfills are paused.
            {:else}
              Details, comments, and backfills pause when spent.
            {/if}
          </span>
        </div>
      {/if}

      <!-- Throttle -->
      {#if h.sync_paused}
        <div class="throttle-indicator throttle-paused">sync paused</div>
      {:else if h.sync_throttle_factor > 1}
        <div class="throttle-indicator">sync {h.sync_throttle_factor}x slower</div>
      {/if}
    </div>
  {/each}
</div>

<style>
  .budget-popover {
    position: absolute;
    bottom: calc(100% + 4px);
    right: 0;
    width: 320px;
    max-height: 400px;
    overflow-y: auto;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: 8px;
    padding: 12px 16px;
    box-shadow: 0 -4px 24px rgba(0, 0, 0, 0.2);
    z-index: 100;
    font-size: var(--font-size-xs);
  }
  .popover-header {
    font-size: var(--font-size-2xs);
    text-transform: uppercase;
    letter-spacing: 0.8px;
    color: var(--text-muted);
    margin-bottom: 10px;
  }
  .popover-divider {
    border-top: 1px solid var(--border-muted);
    margin: 10px 0;
  }
  .host-name {
    font-weight: 600;
    color: var(--text-primary);
    margin-bottom: 8px;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .health-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .budget-row {
    /* label | bar | value (with inline reset) */
    display: grid;
    grid-template-columns: 78px 42px 1fr;
    align-items: center;
    column-gap: 8px;
    margin-bottom: 6px;
  }
  .budget-row--eager {
    align-items: start;
  }
  .row-label {
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }
  .row-bar-cell {
    display: flex;
    align-items: center;
  }
  .bar-track {
    width: 100%;
    height: 5px;
    background: var(--budget-bar-bg);
    border-radius: 3px;
    overflow: hidden;
  }
  .bar-fill {
    height: 100%;
    border-radius: 3px;
    transition: width 0.5s ease;
  }
  .row-value {
    color: var(--text-primary);
    font-size: var(--font-size-2xs);
    font-variant-numeric: tabular-nums;
  }
  .row-unit {
    color: var(--text-muted);
  }
  .row-reset {
    color: var(--text-muted);
    font-size: 0.9em;
    opacity: 0.7;
  }
  .row-note {
    display: block;
    grid-column: 3;
    margin-top: 1px;
    color: var(--text-muted);
    font-size: 0.9em;
    line-height: 1.2;
  }
  .row-unknown {
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    font-style: italic;
  }
  .budget-spent {
    color: var(--budget-blue);
    font-weight: 600;
  }
  .budget-spent--over {
    font-weight: 700;
  }
  .throttle-indicator {
    font-size: var(--font-size-2xs);
    color: var(--accent-amber);
    margin-top: 4px;
  }
  .throttle-paused {
    color: var(--accent-red);
  }
</style>
