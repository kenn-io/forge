<script lang="ts">
  import { ColorLabel } from "@kenn-io/kit-ui";
  import type { Attachment } from "svelte/attachments";
  import type { Label } from "../../api/types.js";

  interface Props {
    labels: Pick<Label, "name" | "color">[];
    /** Compact rows (sidebar list items) show up to two labels that fit whole
     * plus a passive +N overflow and cap pill width; the default row wraps.
     * The row must be a direct child of the flex line it shares. The sibling
     * that gives way declares its floor with min-width and flex-basis, and
     * stops at max-content so spare room is not taken from the labels. */
    compact?: boolean;
  }

  let { labels, compact = false }: Props = $props();

  // A first chip narrower than this would read as a fragment.
  const MIN_CHIP_WIDTH = 44;
  const BADGE_WIDTH = 24;

  let fitted = $state(2);

  const candidates = $derived(compact ? labels.slice(0, 2) : labels);
  const shown = $derived(compact ? Math.min(fitted, candidates.length) : candidates.length);
  const overflow = $derived(labels.length - shown);

  function width(el: Element): number {
    return el.getBoundingClientRect().width;
  }

  /** The chip's width with room to spare, whatever it is squeezed to now. */
  function naturalChipWidth(chip: Element): number {
    return Math.min(chip.scrollWidth, parseFloat(getComputedStyle(chip).maxWidth));
  }

  /** Space left on the flex line once every sibling is at its smallest. */
  function labelBudget(row: HTMLElement, line: HTMLElement): number {
    const gap = parseFloat(getComputedStyle(line).columnGap) || 0;
    const siblings = [...line.children].filter((child) => child !== row);
    return siblings.reduce(
      (budget, sibling) => budget - gap - (parseFloat(getComputedStyle(sibling).minWidth) || width(sibling)),
      width(line),
    );
  }

  function chipsThatFit(row: HTMLElement, line: HTMLElement, chips: Element[]): number {
    const budget = labelBudget(row, line);
    const [first, second] = chips.map(naturalChipWidth);
    if (first === undefined) return 0;
    if (second !== undefined) {
      const gap = parseFloat(getComputedStyle(row).columnGap) || 0;
      const badge = labels.length > 2 ? gap + BADGE_WIDTH : 0;
      if (first + gap + second + badge <= budget) return 2;
    }
    const badge = labels.length > 1 ? BADGE_WIDTH : 0;
    return Math.min(first, MIN_CHIP_WIDTH) + badge <= budget ? 1 : 0;
  }

  // Chips that do not fit stay mounted as hidden measures, so the choice is a
  // pure function of the line's width and the chips' own widths.
  const fitChips: Attachment<HTMLElement> = (row) => {
    const line = row.parentElement;
    if (!line) return;
    const chips = candidates.map((_, index) => row.children.item(index)!);
    const observer = new ResizeObserver(() => {
      // A hidden pane reports no width, which says nothing about the fit.
      if (width(line) > 0) fitted = chipsThatFit(row, line, chips);
    });
    observer.observe(line);
    for (const chip of chips) observer.observe(chip);
    return () => observer.disconnect();
  };
</script>

{#if labels.length > 0}
  <span class={["label-row", compact && "label-row--compact"]} {@attach compact ? fitChips : undefined}>
    {#each candidates as label, index (label.name)}
      {#if compact}
        <ColorLabel size="sm" name={label.name} color={label.color} class={index >= shown ? "label-row__spare" : ""} />
      {:else}
        <ColorLabel name={label.name} color={label.color} />
      {/if}
    {/each}
    {#if overflow > 0}
      <span class="label-more">+{overflow}</span>
    {/if}
  </span>
{/if}

<style>
  .label-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
  }

  /* Shrinks only once the title has reached its floor. */
  .label-row--compact {
    position: relative;
    flex: 0 1 auto;
    flex-wrap: nowrap;
    overflow: hidden;
  }

  .label-row--compact :global(.kit-color-label) {
    max-width: 90px;
  }

  .label-row--compact :global(.label-row__spare) {
    position: absolute;
    right: 0;
    visibility: hidden;
  }

  .label-more {
    flex-shrink: 0;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }
</style>
