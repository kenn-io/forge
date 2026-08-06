<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import type { AppRuntime } from "../../app/runtime.js";
  import DiffFile from "./DiffFile.svelte";

  let { runtime, ...props }: ComponentProps<typeof DiffFile> & { runtime: AppRuntime } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

<DiffFile {...props} />
