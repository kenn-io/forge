<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import DiffSummaryChip from "./DiffSummaryChip.svelte";

  const { runtime, chipProps }: {
    runtime: AppRuntime;
    chipProps: ComponentProps<typeof DiffSummaryChip>;
  } = $props();

  setAppRuntime(untrack(() => runtime));
</script>

<DiffSummaryChip {...chipProps} />
