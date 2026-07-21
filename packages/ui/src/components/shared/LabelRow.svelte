<script module lang="ts">
  /* Raw hex is intentional: label colors are caller-supplied hex values,
   * not theme tokens. Mirrors kit-ui ColorLabel's private normalization. */
  const DOT_FALLBACK_COLOR = "#6e7781";

  /** Expands/validates a hex label color (with or without `#`, 3 or 6
   * digits). Invalid input falls back to a neutral gray. */
  function normalizeLabelColor(color: string): string {
    const hex = color.trim().replace(/^#/, "");

    if (/^[0-9a-fA-F]{3}$/.test(hex)) {
      return `#${hex
        .split("")
        .map((char) => `${char}${char}`)
        .join("")
        .toLowerCase()}`;
    }

    if (/^[0-9a-fA-F]{6}$/.test(hex)) {
      return `#${hex.toLowerCase()}`;
    }

    return DOT_FALLBACK_COLOR;
  }
</script>

<script lang="ts">
  import { ColorLabel } from "@kenn-io/kit-ui";
  import type { Label } from "../../api/types.js";

  interface Props {
    labels: Pick<Label, "name" | "color">[];
    /** Compact rows (sidebar list items) show the first two labels plus a
     * passive +N overflow and cap pill width; the default row wraps. */
    compact?: boolean;
    /** Dots render one small color circle per label (max 4) with names in
     * the tooltip and screen-reader text — used inline on title lines. */
    dots?: boolean;
  }

  let { labels, compact = false, dots = false }: Props = $props();

  const visible = $derived(compact ? labels.slice(0, 2) : labels);
  const overflow = $derived(labels.length - visible.length);
  const dotLabels = $derived(labels.slice(0, 4));
  const labelNames = $derived(labels.map((l) => l.name).join(", "));
</script>

{#if labels.length > 0}
  {#if dots}
    <span class="label-dots" title={labelNames} aria-hidden="true">
      {#each dotLabels as label (label.name)}
        <span class="label-dot" style="background: {normalizeLabelColor(label.color)}"></span>
      {/each}
    </span>
    <span class="kit-sr-only">Labels: {labelNames}</span>
  {:else}
    <span class={["label-row", compact && "label-row--compact"]}>
      {#each visible as label (label.name)}
        {#if compact}
          <ColorLabel size="sm" name={label.name} color={label.color} />
        {:else}
          <ColorLabel name={label.name} color={label.color} />
        {/if}
      {/each}
      {#if overflow > 0}
        <span class="label-more">+{overflow}</span>
      {/if}
    </span>
  {/if}
{/if}

<style>
  .label-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
  }

  .label-row--compact {
    flex-wrap: nowrap;
    overflow: hidden;
  }

  .label-row--compact :global(.kit-color-label) {
    max-width: 120px;
  }

  .label-more {
    flex-shrink: 0;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .label-dots {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    flex-shrink: 0;
  }

  .label-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
  }
</style>
