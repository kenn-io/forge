<script lang="ts">
  import { Chip } from "@kenn-io/kit-ui";

  interface Props {
    count: number;
    capped: boolean;
    noun: string;
  }

  const { count, capped, noun }: Props = $props();
</script>

<Chip size="xs" tone="muted" uppercase={false} class="list-count-chip">
  <span class="list-count-chip__value">{count}{capped ? "+" : ""}</span> {noun}
</Chip>

<style>
  :global(.list-count-chip) {
    flex-shrink: 0;
  }

  /* The chip starts the filter bar, so its width sets where every control
     after it sits, including an open filter menu's trigger. Counts change
     while the list syncs; equal-width digits in a slot sized for four keep
     the chip, and the controls after it, still. */
  .list-count-chip__value {
    display: inline-block;
    min-width: 4ch;
    text-align: end;
    font-variant-numeric: tabular-nums;
  }
</style>
