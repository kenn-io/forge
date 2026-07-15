<script lang="ts">
  import KataResizableSash from "./KataResizableSash.svelte";

  interface Props {
    orientation?: "horizontal" | "vertical";
    primarySize?: number;
    minPrimary?: number;
    minSecondary?: number;
    onResize?: (size: number) => void;
  }

  let {
    orientation = "horizontal",
    primarySize = 300,
    minPrimary = 200,
    minSecondary = 200,
    onResize = () => {},
  }: Props = $props();
</script>

<KataResizableSash
  {orientation}
  {primarySize}
  {minPrimary}
  {minSecondary}
  ariaLabel="Resize Kata panes"
  {onResize}
>
  {#snippet primary()}Primary{/snippet}
  {#snippet secondary()}Secondary{/snippet}
</KataResizableSash>
