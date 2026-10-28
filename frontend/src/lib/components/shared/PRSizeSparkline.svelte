<script lang="ts">
  import type { PRDiffSizePoint } from "../../api/generated/models/pRDiffSizePoint.js";
  import { prSizeSummary } from "./pr-size-history.js";

  let { history = [], currentHead, currentBase }: {
    history?: readonly PRDiffSizePoint[] | undefined;
    currentHead?: string | undefined;
    currentBase?: string | undefined;
  } = $props();

  const summary = $derived(prSizeSummary(history, currentHead, currentBase));
  const last = $derived(summary.points.at(-1));
</script>

{#if summary.points.length > 0}
  <svg
    class={["pr-size-sparkline", summary.tone, { past: summary.past }]}
    width="52"
    height="20"
    viewBox="0 0 52 20"
    role="img"
    aria-label={summary.label}
  >
    <path class="zero" d="M2 18H50" />
    {#if summary.points.length > 1}
      <polyline points={summary.points.map((point) => `${point.x},${point.y}`).join(" ")} />
    {/if}
    {#if last}
      <circle cx={last.x} cy={last.y} r="1.75" />
    {/if}
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
