<script lang="ts">
  import type { PRDiffSizePoint } from "../../api/generated/models/pRDiffSizePoint.js";
  import { prSizeSummary } from "./pr-size-history.js";

  let { history = [], currentHead, currentBase, compact = false }: {
    history?: readonly PRDiffSizePoint[] | undefined;
    currentHead?: string | undefined;
    currentBase?: string | undefined;
    compact?: boolean;
  } = $props();

  const summary = $derived(prSizeSummary(history, currentHead, currentBase));
</script>

{#if summary.points.length > 0}
  <svg
    class={["pr-size-sparkline", summary.tone, { compact, past: summary.past }]}
    width="70"
    height="24"
    viewBox="0 0 70 24"
    role="img"
    aria-label={summary.label}
  >
    <path class="zero" d="M3 21H67" />
    {#if summary.points.length > 1}
      <polyline points={summary.points.map((point) => `${point.x},${point.y}`).join(" ")} />
    {/if}
    {#each summary.points.slice(-1) as point (point)}
      <circle cx={point.x} cy={point.y} r="2.5" />
    {/each}
  </svg>
{:else}
  <span class="kit-sr-only" role="img" aria-label={summary.label}></span>
{/if}

<style>
  .pr-size-sparkline {
    display: inline-block;
    flex-shrink: 0;
    color: var(--text-secondary);
    vertical-align: middle;
  }
  .warning { color: var(--accent-amber); }
  .success { color: var(--accent-green); }
  .past { color: var(--text-muted); }
  .compact { width: 48px; height: 20px; margin-left: var(--space-2); }
  path, polyline {
    fill: none;
    stroke: currentColor;
    stroke-width: 1.5;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .zero { stroke-width: 1; opacity: 0.25; }
  .past polyline { stroke-dasharray: 2 2; }
  circle { fill: currentColor; }
</style>
