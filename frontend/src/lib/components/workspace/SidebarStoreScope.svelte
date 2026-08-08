<script lang="ts">
  import { setContext } from "svelte";
  import { STORES_KEY } from "../../context.js";
  import type { StoreInstances } from "../../types.js";
  import type { Snippet } from "svelte";

  interface Props {
    stores: StoreInstances;
    children: Snippet;
  }

  let { stores, children }: Props = $props();

  // svelte-ignore state_referenced_locally
  setContext(STORES_KEY, stores);
</script>

{@render children()}
