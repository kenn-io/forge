<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import WorkspaceRightSidebar from "./WorkspaceRightSidebar.svelte";

  const { runtime, sidebarProps }: {
    runtime: AppRuntime;
    sidebarProps: ComponentProps<typeof WorkspaceRightSidebar>;
  } = $props();

  setAppRuntime(untrack(() => runtime));
</script>

<WorkspaceRightSidebar {...sidebarProps} />
