<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import MobileWorkspaceList from "./MobileWorkspaceList.svelte";

  const { showList = true, ...props }: ComponentProps<typeof MobileWorkspaceList> & {
    showList?: boolean;
  } = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(runtime);
  onDestroy(() => Effect.runFork(runtime.disposeEffect));
</script>

{#if showList}
  <MobileWorkspaceList {...props} />
{/if}
