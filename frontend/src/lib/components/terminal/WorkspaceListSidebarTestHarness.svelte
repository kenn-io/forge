<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";

  const props: ComponentProps<typeof WorkspaceListSidebar> = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(runtime);
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<WorkspaceListSidebar {...props} />
