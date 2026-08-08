<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import type { AppRuntime } from "../../app/runtime.js";
  import DiffView from "./DiffView.svelte";

  let { runtime, ...props }: ComponentProps<typeof DiffView> & { runtime: AppRuntime } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

<DiffView {...props} />
