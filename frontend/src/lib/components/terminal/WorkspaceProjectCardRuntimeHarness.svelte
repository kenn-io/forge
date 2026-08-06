<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import WorkspaceProjectCard from "./WorkspaceProjectCard.svelte";

  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const props: ComponentProps<typeof WorkspaceProjectCard> = $props();
</script>

<WorkspaceProjectCard {...props} />
