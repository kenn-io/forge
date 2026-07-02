<script lang="ts">
  import { ColorLabel } from "@kenn-io/kit-ui";
  import type { Label } from "../../api/types.js";

  interface Props {
    labels: Pick<Label, "name" | "color">[];
    /** Compact rows (sidebar list items) show the first two labels plus a
     * passive +N overflow and cap pill width; the default row wraps. */
    compact?: boolean;
  }

  let { labels, compact = false }: Props = $props();

  const visible = $derived(compact ? labels.slice(0, 2) : labels);
  const overflow = $derived(labels.length - visible.length);
</script>

{#if labels.length > 0}
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
</style>
