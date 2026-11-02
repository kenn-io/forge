<script lang="ts">
  import type { Snippet } from "svelte";

  // Stands in for PRListView: renders only the sidebar list the Queues page supplies.
  const { sidebarList }: { sidebarList?: Snippet } = $props();
</script>

{@render sidebarList?.()}
