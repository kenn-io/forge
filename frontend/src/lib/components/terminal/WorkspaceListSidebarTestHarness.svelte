<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";

  const { showSidebar = true, ...props }: ComponentProps<typeof WorkspaceListSidebar> & {
    showSidebar?: boolean;
  } = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(runtime);
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

{#if showSidebar}
  <WorkspaceListSidebar {...props} />
{/if}
