<script lang="ts">
  import { untrack } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import ErrorShown from "./ErrorShown.svelte";

  const { runtime, shown = true, mounted = true, copies = 1 }: { runtime: AppRuntime; shown?: boolean; mounted?: boolean; copies?: number } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

{#if mounted}
  {#each Array(copies) as _, i (i)}
    <ErrorShown kind="pr_viewed" scope="github:acme/widgets#42" {shown} />
  {/each}
{/if}
