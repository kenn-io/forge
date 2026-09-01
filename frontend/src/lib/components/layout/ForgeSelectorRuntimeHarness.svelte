<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import ForgeSelector from "./ForgeSelector.svelte";

  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const props: ComponentProps<typeof ForgeSelector> = $props();
</script>

<ForgeSelector {...props} />
