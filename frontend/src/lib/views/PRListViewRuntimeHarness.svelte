<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import type { GeneratedClient } from "../api/generated-api.js";
  import { makeAppRuntime } from "../app/runtime.js";
  import { setAppRuntime } from "../app/runtime-context.js";
  import PRListView from "./PRListView.svelte";

  const { client, ...props }: ComponentProps<typeof PRListView> & { client?: GeneratedClient } = $props();
  const runtime = makeAppRuntime(untrack(() => client));
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<PRListView {...props} />
