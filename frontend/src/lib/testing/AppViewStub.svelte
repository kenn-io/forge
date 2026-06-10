<script lang="ts">
  import type { Snippet } from "svelte";

  let { children }: { children?: Snippet } = $props();
</script>

<div data-testid="app-view-stub">
  {@render children?.()}
</div>
