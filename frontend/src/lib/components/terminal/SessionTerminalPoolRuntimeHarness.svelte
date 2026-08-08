<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, setContext, untrack } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { STORES_KEY } from "../../context.js";
  import SessionTerminalPool from "./SessionTerminalPool.svelte";

  let { settings }: { settings: unknown } = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  setContext(STORES_KEY, { settings: untrack(() => settings) });
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<SessionTerminalPool />
