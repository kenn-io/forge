<script lang="ts">
  import { dismissable } from "@kenn-io/kit-ui";
  import type {
    LocalSyncCeilingStatus,
    RateLimitHostStatus,
    RateLimitResourceStatus,
  } from "@kenn-forge/ui/api/types";
  import { budgetColor, formatCompact, syncBudgetColor } from "./budget-utils";

  interface Props {
    providerPools: Record<string, RateLimitHostStatus>;
    localCeilings: Record<string, LocalSyncCeilingStatus>;
    onclose: () => void;
  }

  let { providerPools, localCeilings, onclose }: Props = $props();

  let popoverEl: HTMLDivElement | undefined = $state();

  // kit StatusBar runs with overflow="visible" so this can be the natural
  // bottom-anchored absolute panel inside the relative .budget-wrapper (the
  // sanctioned kit popover recipe). The wrapper counts as "inside" so a
  // press on the BudgetBars trigger reaches its own toggle instead of
  // racing a dismiss-then-reopen.
  $effect(() => {
    return dismissable({
      owners: () => [popoverEl?.parentElement],
      dismiss: onclose,
      escapeFocus: () => popoverEl?.parentElement?.querySelector("button"),
    });
  });

  // One entry per credential principal. GitHub meters each principal
  // separately, so a host with an App installation and a PAT shows two.
  function poolEntries() {
    return Object.entries(providerPools);
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

  function resourceFresh(resource: RateLimitResourceStatus): boolean {
    return resource.known && resource.limit > 0 && resource.remaining >= 0;
  }

  function resourceColor(resource: RateLimitResourceStatus, reserve: number): string {
    if (!resourceFresh(resource)) return "var(--text-muted)";
    if (resource.remaining <= reserve) return "var(--budget-red)";
    return budgetColor(resource.remaining / resource.limit);
  }

  function poolResources(pool: RateLimitHostStatus): RateLimitResourceStatus[] {
    return [pool.rest, pool.graphql];
  }

  function isPoolFresh(pool: RateLimitHostStatus): boolean {
    return poolResources(pool).some(resourceFresh);
  }

  function poolHealthColor(pool: RateLimitHostStatus): string {
    if (pool.sync_paused) return "var(--budget-red)";
    const known = poolResources(pool).filter(resourceFresh);
    if (known.length === 0) return "var(--text-muted)";
    if (known.some((resource) => resource.remaining <= pool.reserve_buffer)) {
      return "var(--budget-red)";
    }
    return budgetColor(Math.min(...known.map((r) => r.remaining / r.limit)));
  }

  function poolAtReserve(pool: RateLimitHostStatus): boolean {
    return poolResources(pool).some(
      (resource) => resourceFresh(resource) && resource.remaining <= pool.reserve_buffer,
    );
  }

  function resourceRows(pool: RateLimitHostStatus) {
    return [
      { label: "REST", resource: pool.rest, unit: "req" },
      { label: "GraphQL", resource: pool.graphql, unit: "pts" },
    ];
  }

  // Local ceilings are kenn-forge's own hourly guard, not GitHub quota, so
  // they render in their own section rather than beside the provider pools.
  function ceilingEntries() {
    return Object.entries(localCeilings).filter(([, ceiling]) => ceiling.limit > 0);
  }
</script>

<div
  class="budget-popover kit-popover-card"
  role="dialog"
  aria-label="API quota and local sync ceiling"
  bind:this={popoverEl}
>
  <div class="popover-header">Provider quota</div>

  {#each poolEntries() as [poolKey, pool], i (poolKey)}
    {#if i > 0}
      <div class="popover-divider"></div>
    {/if}

    <div class="host-section">
      <div class="host-name">
        <span
          class="health-dot"
          class:health-dot--unknown={!isPoolFresh(pool)}
          style:background={poolHealthColor(pool)}
        ></span>
        <span class="host-identity">
          <span>{pool.platform_host || poolKey}</span>
          <span class="principal-label">{pool.principal_label}</span>
        </span>
      </div>

      {#each resourceRows(pool) as { label, resource, unit } (label)}
        <div class="budget-row">
          <span class="row-label">{label}</span>
          {#if resourceFresh(resource) && ratio(resource.remaining, resource.limit) >= 0}
            {@const resourceRatio = ratio(resource.remaining, resource.limit)}
            <span class="row-bar-cell">
              <span class="bar-track">
                <span
                  class="bar-fill"
                  style:width="{Math.max(resourceRatio * 100, 2)}%"
                  style:background={resourceColor(resource, pool.reserve_buffer)}
                ></span>
              </span>
            </span>
            <span class="row-value">
              {formatCompact(resource.remaining)} / {formatCompact(resource.limit)} <span class="row-unit">{unit}</span>
              {#if resetText(resource.reset_at)}<span class="row-reset"> · {resetText(resource.reset_at)}</span>{/if}
            </span>
          {:else}
            <span class="row-bar-cell"></span>
            <span class="row-unknown">not yet observed</span>
          {/if}
        </div>
      {/each}
      {#if poolAtReserve(pool)}
        <div class="reserve-indicator">provider reserve reached</div>
      {/if}
      {#if pool.sync_paused}
        <div class="throttle-indicator throttle-paused">sync paused</div>
      {:else if pool.sync_throttle_factor > 1}
        <div class="throttle-indicator">sync {pool.sync_throttle_factor}x slower</div>
      {/if}
    </div>
  {/each}

  {#if ceilingEntries().length > 0}
    <div class="popover-section-divider"></div>
    <div class="popover-header local-ceiling-header">Local sync ceiling</div>
    {#each ceilingEntries() as [key, ceiling] (key)}
      <div class="ceiling-section">
        <div class="credential-name">{ceiling.platform_host || key} · {ceiling.principal_label}</div>
        <div class="budget-row budget-row--ceiling">
          <span class="row-label">Process guard</span>
          <span class="row-bar-cell"></span>
          <span class="row-value">
            <span
              class="budget-spent"
              style:color={syncBudgetColor(ceiling.spent, ceiling.limit)}
            >{formatCompact(ceiling.spent)}</span> / {formatCompact(ceiling.limit)} <span class="row-unit">requests</span>
          </span>
          <span class="row-note">Emergency ceiling for background sync; provider quota above is authoritative.</span>
        </div>
      </div>
    {/each}
  {/if}
</div>

<style>
  .budget-popover {
    position: absolute;
    right: 0;
    bottom: calc(100% + 4px);
    width: 320px;
    max-height: 400px;
    overflow-y: auto;
    padding: 12px 16px;
    z-index: var(--z-popover, 1000);
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
  .budget-row--ceiling {
    align-items: start;
  }

  .credential-name {
    color: var(--text-secondary);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    margin: 7px 0 5px;
  }

  .popover-section-divider {
    border-top: 1px solid var(--border-default);
    margin: 12px 0 10px;
  }

  .local-ceiling-header {
    margin-bottom: 6px;
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
  .reserve-indicator {
    color: var(--accent-red);
    font-size: var(--font-size-2xs);
    margin-top: 5px;
  }
</style>
