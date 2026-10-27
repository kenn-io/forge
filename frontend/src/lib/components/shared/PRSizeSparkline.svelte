<script lang="ts">
  import { Tooltip } from "@kenn-io/kit-ui";
  import type { PRDiffSizePoint } from "../../api/generated/models/pRDiffSizePoint.js";
  import { prSizeSummary } from "./pr-size-history.js";

  let { history = [], currentHead, currentBase, compact = false }: {
    history?: readonly PRDiffSizePoint[] | undefined;
    currentHead?: string | undefined;
    currentBase?: string | undefined;
    compact?: boolean;
  } = $props();

  const summary = $derived(prSizeSummary(history, currentHead, currentBase));
  const first = $derived(history.at(0));
  const latest = $derived(history.at(-1));
  const explanation = "Total PR diff: added + deleted lines against the base, at up to 32 observed head/base revisions. Equal spacing shows revision order, not elapsed time or per-commit churn. Starts when Forge observes confirmed counts; intermediate heads may be missing.";
  const state = $derived(history.length === 0
    ? "No confirmed history yet. Earlier revisions are unavailable."
    : summary.past
      ? "Current revision has no confirmed size yet; showing past observations."
      : history.length === 1 ? "One observed revision; no trend yet." : `${history.length} observed revisions; change is relative to the first retained observation.`);
  const accessibleLabel = $derived(`${summary.label}. ${state} ${explanation}`);
</script>

<Tooltip focusable={!compact}>
  {#snippet content()}
    <div class="size-tooltip">
      <strong>{summary.label}</strong>
      <span>{explanation}</span>
      <span>{state}</span>
      {#if first && latest}
        <span>First: {first.head_sha.slice(0, 7)} · {new Date(first.observed_at).toLocaleString()}</span>
        <span>Latest: {latest.head_sha.slice(0, 7)} · {new Date(latest.observed_at).toLocaleString()}</span>
      {/if}
    </div>
  {/snippet}
  <span class={["pr-size-sparkline", { compact, past: summary.past }]} role="img" aria-label={accessibleLabel}>
    <span>PR size</span>
    {#if summary.points.length > 0}
      <svg width="70" height="24" viewBox="0 0 70 24" aria-hidden="true">
        {#if summary.points.length > 1}
          <polyline points={summary.points.map((point) => `${point.x},${point.y}`).join(" ")} fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" />
        {/if}
        {#each summary.points as point, index (point)}
          <circle cx={point.x} cy={point.y} r={index === summary.points.length - 1 ? 2.5 : 1.5} fill="currentColor" />
        {/each}
      </svg>
      <span>{summary.past ? "past" : summary.delta === null ? "1 revision" : summary.signed}</span>
    {:else}
      <span>—</span>
    {/if}
  </span>
</Tooltip>

<style>
  .pr-size-sparkline {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
    vertical-align: middle;
  }
  svg { color: var(--accent-blue); flex-shrink: 0; }
  .past svg { color: var(--text-muted); }
  .compact { margin-left: var(--space-2); }
  .compact svg { width: 48px; height: 20px; }
  .size-tooltip {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    max-width: 320px;
    font-size: var(--font-size-sm);
    white-space: normal;
  }
</style>
