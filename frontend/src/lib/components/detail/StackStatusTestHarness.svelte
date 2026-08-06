<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import StackStatus from "./StackStatus.svelte";

  const { runtime, stackProps }: {
    runtime: AppRuntime;
    stackProps: ComponentProps<typeof StackStatus>;
  } = $props();

  setAppRuntime(untrack(() => runtime));
</script>

<StackStatus {...stackProps} />
