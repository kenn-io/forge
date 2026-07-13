<script lang="ts">
  import ScrollBox from "./ScrollBox.svelte";

  interface Props {
    onscroll?: (event: Event) => void;
    onviewport?: (el: HTMLDivElement | undefined) => void;
  }

  const { onscroll, onviewport }: Props = $props();

  let viewport = $state<HTMLDivElement>();

  $effect(() => {
    onviewport?.(viewport);
  });
</script>

<ScrollBox label="Test scroll region" {onscroll} bind:viewport>
  <div style="height: 800px">tall content</div>
</ScrollBox>
