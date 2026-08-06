<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import TerminalPane from "./TerminalPane.svelte";

  const props: ComponentProps<typeof TerminalPane> = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(runtime);
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<TerminalPane {...props} />
