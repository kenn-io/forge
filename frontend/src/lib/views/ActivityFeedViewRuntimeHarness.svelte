<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../app/runtime.js";
  import { setAppRuntime } from "../app/runtime-context.js";
  import ActivityFeedView from "./ActivityFeedView.svelte";

  const { client, ...props }: ComponentProps<typeof ActivityFeedView> & { client?: import("../api/generated-api.js").GeneratedClient } = $props();
  const runtime = makeAppRuntime(untrack(() => client));
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<ActivityFeedView {...props} />
