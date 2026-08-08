<script lang="ts">
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import WorkspaceHost from "./WorkspaceHost.svelte";
  import { untrack } from "svelte";

  interface Props {
    runtime: AppRuntime;
  }

  const { runtime }: Props = $props();
  setAppRuntime(untrack(() => runtime));
</script>

<WorkspaceHost />
