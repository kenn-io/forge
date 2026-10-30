<script lang="ts">
  import { ColorLabel } from "@kenn-io/kit-ui";
  import type { Attachment } from "svelte/attachments";
  import type { Label } from "../../api/types.js";

  interface Props {
    labels: Pick<Label, "name" | "color">[];
    /** Compact rows (sidebar list items) show up to two labels that fit whole
     * plus a passive +N overflow; the default row wraps. A chip is never cut:
     * one that cannot fit whole counts toward the +N instead.
     * The row must be a direct child of the flex line it shares. The sibling
     * that gives way declares its floor with min-width and flex-basis, and
     * stops at max-content so spare room is not taken from the labels. */
    compact?: boolean;
  }

  let { labels, compact = false }: Props = $props();

  const BADGE_WIDTH = 24;

  // Indexes into candidates of the chips that fit.
  let fitted = $state([0, 1]);

  const candidates = $derived(compact ? labels.slice(0, 2) : labels);
  const shownCount = $derived(
    compact ? candidates.filter((_, index) => fitted.includes(index)).length : candidates.length,
  );
  const overflow = $derived(labels.length - shownCount);

  function width(el: Element): number {
    return el.getBoundingClientRect().width;
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

  // scrollWidth is a chip's full width whatever it is squeezed to now.
  function chipsThatFit(row: HTMLElement, line: HTMLElement, chips: Element[]): number[] {
    const budget = labelBudget(row, line);
    const gap = parseFloat(getComputedStyle(row).columnGap) || 0;
    const [first, second] = chips.map((chip) => chip.scrollWidth);
    if (first !== undefined && second !== undefined) {
      const badge = labels.length > 2 ? gap + BADGE_WIDTH : 0;
      if (first + gap + second + badge <= budget) return [0, 1];
    }
    const room = budget - (labels.length > 1 ? gap + BADGE_WIDTH : 0);
    if (first !== undefined && first <= room) return [0];
    if (second !== undefined && second <= room) return [1];
    return [];
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
        <ColorLabel size="sm" name={label.name} color={label.color} class={fitted.includes(index) ? "" : "label-row__spare"} />
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
