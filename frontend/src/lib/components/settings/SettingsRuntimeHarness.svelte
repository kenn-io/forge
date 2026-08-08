<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type Component } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";

  interface Props {
    component: Component;
    componentProps: Record<string, unknown>;
  }

  let { component: Child, componentProps }: Props = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<Child {...componentProps} />
