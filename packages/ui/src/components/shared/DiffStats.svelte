<script lang="ts">
  import { formatDiffStat } from "../../utils/diff-stats.js";

  interface Props {
    additions: number;
    deletions: number;
    dimZeros?: boolean;
  }

  const {
    additions,
    deletions,
    dimZeros = false,
  }: Props = $props();

  function lineLabel(count: number, singular: string): string {
    return `${count} ${singular}${count === 1 ? "" : "s"}`;
  }
</script>

<span
  class="diff-stats"
  aria-label={`${lineLabel(additions, "addition")}, ${lineLabel(deletions, "deletion")}`}
>
  <span
    class="diff-stats__add"
    class:diff-stats__value--dim={dimZeros && additions === 0}
  >
    +{formatDiffStat(additions)}
  </span>
  <span
    class="diff-stats__del"
    class:diff-stats__value--dim={dimZeros && deletions === 0}
  >
    −{formatDiffStat(deletions)}
  </span>
</span>

<style>
  .diff-stats {
    display: inline-flex;
    gap: 6px;
    align-items: baseline;
    font-family: var(--font-mono);
    font-weight: 400;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  .diff-stats__add,
  .diff-stats__del {
    min-width: 0;
  }

  .diff-stats__add {
    color: var(--accent-green);
  }

  .diff-stats__del {
    color: var(--accent-red);
  }

  .diff-stats__value--dim {
    opacity: 0.35;
  }
</style>
