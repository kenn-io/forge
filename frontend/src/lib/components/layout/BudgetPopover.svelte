<script lang="ts">
  import { dismissable } from "@kenn-io/kit-ui";
  import type {
    LocalSyncCeilingStatus,
    RateLimitHostStatus,
    RateLimitResourceStatus,
  } from "../../api/types.js";
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

  function githubUserHandle(label: string): string {
    return label.startsWith("GitHub user ") ? label.slice("GitHub user ".length) : "";
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

  function resourcesAtReserve(pool: RateLimitHostStatus): string[] {
    return resourceRows(pool)
      .filter(({ resource }) => resourceFresh(resource) && resource.remaining <= pool.reserve_buffer)
      .map(({ label }) => label);
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

  function ceilingRatio(value: number, limit: number): number {
    if (limit <= 0) return 0;
    return Math.min(Math.max(value / limit, 0), 1);
  }

  function backgroundRefreshPaused(ceiling: LocalSyncCeilingStatus): boolean {
    return ceiling.background_limit > 0 && ceiling.spent >= ceiling.background_limit;
  }

  function ceilingValueText(ceiling: LocalSyncCeilingStatus): string {
    const usage = `${ceiling.spent} requests used of ${ceiling.limit}`;
    if (ceiling.spent >= ceiling.limit) {
      return `${usage}; app sync paused; discovery reserve exhausted`;
    }
    return `${usage}; background refresh pauses at ${ceiling.background_limit}; ${Math.max(ceiling.limit - ceiling.spent, 0)} requests available for essential discovery`;
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
    {@const userHandle = githubUserHandle(pool.principal_label)}
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
          <span>{pool.platform_host || poolKey}:</span>
          {#if userHandle}
            <span class="principal-label principal-label--user">@{userHandle}</span>
          {:else}
            <span class="principal-label">{pool.principal_label}</span>
          {/if}
        </span>
      </div>

      {#each resourceRows(pool) as { label, resource, unit } (label)}
        <div class="budget-row">
          <span class="row-label">{label}</span>
          {#if resourceFresh(resource) && ratio(resource.remaining, resource.limit) >= 0}
            {@const resourceRatio = ratio(resource.remaining, resource.limit)}
            <span class="row-bar-cell">
              <span
                class="bar-track bar-track--provider"
                role="meter"
                aria-label={`${label} capacity remaining`}
                aria-valuemin="0"
                aria-valuemax={resource.limit}
                aria-valuenow={resource.remaining}
                aria-valuetext={`${resource.remaining} ${unit === "pts" ? "points" : "requests"} remaining of ${resource.limit}`}
              >
                <span
                  class="bar-fill bar-fill--provider"
                  style:width="{Math.max(resourceRatio * 100, 0)}%"
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
        <div class="reserve-indicator">{resourcesAtReserve(pool).join(" + ")} provider reserve reached</div>
      {/if}
      {#if pool.sync_throttle_factor > 1}
        <div class="throttle-indicator">sync {pool.sync_throttle_factor}x slower</div>
      {/if}
    </div>
  {/each}

  {#if ceilingEntries().length > 0}
    <div class="popover-section-divider"></div>
    <div class="popover-header local-ceiling-header">App sync limit</div>
    {#each ceilingEntries() as [key, ceiling] (key)}
      <div class="ceiling-section">
        {#if ceilingEntries().length > 1}
          {@const userHandle = githubUserHandle(ceiling.principal_label)}
          <div class="ceiling-identity">
            <span>{ceiling.platform_host || key}:</span>
            {#if userHandle}
              <span class="principal-label principal-label--user">@{userHandle}</span>
            {:else}
              <span class="principal-label">{ceiling.principal_label}</span>
            {/if}
          </div>
        {/if}
        <div class="budget-row budget-row--ceiling">
          <span class="row-label">Process guard</span>
          <span class="row-bar-cell">
            <span
              class="bar-track bar-track--local"
              role="meter"
              aria-label={`Local requests used for ${ceiling.platform_host || key}, ${ceiling.principal_label}`}
              aria-valuemin="0"
              aria-valuemax={ceiling.limit}
              aria-valuenow={Math.min(Math.max(ceiling.spent, 0), ceiling.limit)}
              aria-valuetext={ceilingValueText(ceiling)}
            >
              <span
                class="bar-fill bar-fill--local"
                style:width="{ceilingRatio(ceiling.spent, ceiling.limit) * 100}%"
                style:background={syncBudgetColor(ceiling.spent, ceiling.limit)}
              ></span>
              <span
                class="background-limit-marker"
                style:left="{ceilingRatio(ceiling.background_limit, ceiling.limit) * 100}%"
                aria-hidden="true"
              ></span>
            </span>
          </span>
          <span class="row-value">
            <span
              class="budget-spent"
              style:color={syncBudgetColor(ceiling.spent, ceiling.limit)}
            >{formatCompact(ceiling.spent)}</span> / {formatCompact(ceiling.limit)} <span class="row-unit">requests</span>
          </span>
          <span class="row-note">
            {#if ceiling.spent >= ceiling.limit}
              app sync paused · discovery reserve exhausted{#if resetText(ceiling.reset_at)} · {resetText(ceiling.reset_at)}{/if}
            {:else if backgroundRefreshPaused(ceiling)}
              background refresh paused · {formatCompact(Math.max(ceiling.limit - ceiling.spent, 0))} discovery requests before app sync pauses{#if resetText(ceiling.reset_at)} · {resetText(ceiling.reset_at)}{/if}
            {:else}
              Local guard for background refresh; provider quota above is separate.
            {/if}
          </span>
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
    max-width: calc(100vw - var(--space-4));
    max-height: 400px;
    overflow-y: auto;
    overflow-x: clip;
    box-sizing: border-box;
    padding: 12px 16px;
    z-index: var(--z-popover, 1000);
    font-size: var(--font-size-xs);
    white-space: normal;
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
    min-width: 0;
  }
  .host-identity {
    display: flex;
    min-width: 0;
    align-items: baseline;
    gap: var(--space-1);
    flex-wrap: wrap;
  }
  .host-identity > span {
    overflow-wrap: anywhere;
  }
  .principal-label--user {
    color: var(--text-secondary);
    font-family: var(--font-mono);
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
    grid-template-columns: 78px 42px minmax(0, 1fr);
    align-items: center;
    column-gap: 8px;
    margin-bottom: 6px;
  }
  .popover-section-divider {
    border-top: 1px solid var(--border-default);
    margin: 12px 0 10px;
  }

  .local-ceiling-header {
    margin-bottom: 6px;
  }
  .ceiling-section + .ceiling-section {
    margin-top: 8px;
  }
  .ceiling-identity {
    display: flex;
    align-items: baseline;
    gap: var(--space-1);
    min-width: 0;
    margin-bottom: 4px;
    color: var(--text-secondary);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    flex-wrap: wrap;
  }
  .ceiling-identity > span {
    overflow-wrap: anywhere;
  }
  .row-label {
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }
  .row-bar-cell {
    display: flex;
    align-items: center;
    min-width: 0;
  }
  .bar-track {
    position: relative;
    width: 100%;
    height: 5px;
    background: var(--budget-bar-bg);
    outline: 1px solid var(--border-muted);
    outline-offset: -1px;
    border-radius: 3px;
    overflow: hidden;
  }
  .bar-fill {
    display: block;
    height: 100%;
    border-radius: 3px;
  }
  .bar-fill--provider {
    margin-left: auto;
  }
  .background-limit-marker {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 1px;
    background: var(--text-primary);
    opacity: 0.8;
  }
  .row-value {
    min-width: 0;
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
    grid-column: 2 / -1;
    min-width: 0;
    margin-top: 1px;
    color: var(--text-muted);
    font-size: 0.9em;
    line-height: 1.2;
    white-space: normal;
    overflow-wrap: anywhere;
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
