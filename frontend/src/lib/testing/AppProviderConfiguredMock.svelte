<script lang="ts">
  import type { Snippet } from "svelte";
  import { onMount } from "svelte";
  import { configuredMockStores } from "./appProviderConfiguredStores.js";

  let {
    stores = $bindable<unknown>(),
    children,
  }: {
    stores?: unknown;
    children?: Snippet;
  } = $props();

  onMount(() => {
    stores = configuredMockStores;
  });
</script>

{@render children?.()}
