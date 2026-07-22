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
  import { ColorLabel, Tooltip } from "@kenn-io/kit-ui";
  import type { Label } from "../../api/types.js";

  interface Props {
    labels: Pick<Label, "name" | "color">[];
    /** Dots render one small color circle per label (max 4) with names in
     * the tooltip and screen-reader text — used inline on title lines and
     * sidebar list items. */
    dots?: boolean;
  }

  let { labels, dots = false }: Props = $props();

  const dotLabels = $derived(labels.slice(0, 4));
  const labelNames = $derived(labels.map((l) => l.name).join(", "));
</script>

{#if labels.length > 0}
  {#if dots}
    <span class="label-dots-wrap">
      <Tooltip text={labelNames} focusable openDelayMs={0}>
        <span class="label-dots" aria-hidden="true">
          {#each dotLabels as label (label.name)}
            <span class="label-dot" style="background: {normalizeLabelColor(label.color)}"></span>
          {/each}
        </span>
      </Tooltip>
    </span>
    <span class="kit-sr-only">Labels: {labelNames}</span>
  {:else}
    <span class="label-row">
      {#each labels as label (label.name)}
        <ColorLabel name={label.name} color={label.color} />
      {/each}
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

  /* Tooltip's trigger span is a component-internal element, not one this
   * component's scoped CSS ordinarily reaches — :global() targets it, but
   * scoping it under .label-dots-wrap keeps the rule from leaking to other
   * Tooltip consumers. It replaces .label-dots as the flex item on the
   * title/meta line, so it needs the same non-shrinking, vertically
   * centered sizing .label-dots used to provide directly. */
  .label-dots-wrap {
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
  }

  .label-dots-wrap :global(.kit-tooltip-trigger) {
    flex-shrink: 0;
  }

  .label-dots {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
  }

  .label-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
  }
</style>
