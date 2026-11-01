<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { makeAppRuntime } from "../app/runtime.js";
  import { setAppRuntime } from "../app/runtime-context.js";
  import { getRoute } from "../stores/router.svelte.js";
  import MobileWorklistView from "./MobileWorklistView.svelte";

  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const route = $derived(getRoute());
</script>

{#if route.page === "mobile-worklists"}
  <MobileWorklistView worklistId={route.worklistId} />
{/if}
