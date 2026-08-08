<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type Component } from "svelte";
  import { makeAppRuntime } from "../lib/app/runtime.js";
  import { setAppRuntime } from "../lib/app/runtime-context.js";

  type Props = { component: Component } & Record<string, unknown>;

  let { component: Child, ...childProps }: Props = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<Child {...childProps} />
