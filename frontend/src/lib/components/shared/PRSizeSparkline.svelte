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
</script>

{#if summary}
  <Tooltip text={summary.tooltip} focusable={!compact}>
    <span
      class={["pr-size-sparkline", summary.tone, { compact, past: summary.past }]}
      role="img"
      aria-label={summary.label}
    >
      <svg viewBox="0 0 70 24" aria-hidden="true">
        <path class="zero" d="M3 21H67" />
        <polyline points={summary.points.map((point) => `${point.x},${point.y}`).join(" ")} />
        {#each summary.points as point, index (point)}
          <circle cx={point.x} cy={point.y} r={index === summary.points.length - 1 ? 3 : 2.5} />
        {/each}
      </svg>
    </span>
  </Tooltip>
{/if}

<style>
  /* Matches the DiffSummaryChip pill it sits beside in the detail header. */
  .pr-size-sparkline {
    box-sizing: border-box;
    display: inline-flex;
    flex-shrink: 0;
    align-items: center;
    min-height: 22px;
    padding: 0 8px;
    border-radius: 10px;
    background: var(--bg-inset);
    color: var(--text-secondary);
    vertical-align: middle;
  }
  svg { display: block; width: 53px; height: 18px; }
  .warning { color: var(--accent-amber); }
  .success { color: var(--accent-green); }
  .past { color: var(--text-muted); }
  .compact { min-height: 0; padding: 0; margin-left: var(--space-2); background: none; }
  .compact svg { width: 48px; height: 20px; }
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
