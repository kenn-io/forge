<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { makeAppRuntime } from "../app/runtime.js";
  import { setAppRuntime } from "../app/runtime-context.js";
  import { getRoute } from "../stores/router.svelte.js";
  import WorklistView from "./WorklistView.svelte";

  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const route = $derived(getRoute());
</script>

{#if route.page === "worklists"}
  <WorklistView worklistId={route.worklistId} selected={route.selected} />
{/if}
