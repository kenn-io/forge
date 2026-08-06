<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import PierreFileDiff from "./PierreFileDiff.svelte";

  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const props: ComponentProps<typeof PierreFileDiff> = $props();
</script>

<PierreFileDiff {...props} />
