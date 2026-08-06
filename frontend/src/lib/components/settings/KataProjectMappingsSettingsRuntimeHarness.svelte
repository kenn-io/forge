<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import KataProjectMappingsSettings from "./KataProjectMappingsSettings.svelte";

  const props: ComponentProps<typeof KataProjectMappingsSettings> = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<KataProjectMappingsSettings {...props} />
