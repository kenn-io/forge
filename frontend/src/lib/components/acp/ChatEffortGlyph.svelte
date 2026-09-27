<script lang="ts">
  // Authored effort glyph: rising bars, filled up to the current level, so
  // the effort chip reads at a glance without its label.
  let { level, levels }: { level: number; levels: number } = $props()
  const count = $derived(Math.max(1, Math.min(levels, 5)))
  const bars = $derived(Array.from({ length: count }, (_, index) => index))
  const barWidth = $derived(12 / count - 1)
</script>

<svg
  class="effort"
  width="14"
  height="14"
  viewBox="0 0 14 14"
  aria-hidden="true"
>
  {#each bars as index (index)}
    {@const height = 4 + (8 * (index + 1)) / count}
    <rect
      class:on={index <= level}
      x={1 + index * (barWidth + 1)}
      y={13 - height}
      width={barWidth}
      {height}
      rx="0.75"
    />
  {/each}
</svg>

<style>
  .effort {
    flex: none;
    display: block;
  }
  rect {
    fill: currentColor;
    opacity: 0.25;
  }
  rect.on {
    fill: var(--accent-blue);
    opacity: 1;
  }
</style>
