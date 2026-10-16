<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../app/runtime.js";
  import { setAppRuntime } from "../app/runtime-context.js";
  import PRListView from "./PRListView.svelte";

  const { client, ...props }: ComponentProps<typeof PRListView> & { client?: import("../api/generated-api.js").GeneratedClient } = $props();
  const runtime = makeAppRuntime(untrack(() => client));
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<PRListView {...props} />
