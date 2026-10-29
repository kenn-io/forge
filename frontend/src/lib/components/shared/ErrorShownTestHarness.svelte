<script lang="ts">
  import { untrack } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import ErrorShown from "./ErrorShown.svelte";

  const { runtime, shown = true, mounted = true, copies = 1, retrying = false }: { runtime: AppRuntime; shown?: boolean; mounted?: boolean; copies?: number; retrying?: boolean } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

{#if mounted}
  {#each Array(copies) as _, i (i)}
    {#if i !== 0 || !retrying}
      <ErrorShown kind="pr_viewed" scope="github:acme/widgets#42" {shown} />
    {/if}
  {/each}
{/if}
