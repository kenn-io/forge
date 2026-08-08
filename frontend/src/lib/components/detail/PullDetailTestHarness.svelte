<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import PullDetail from "./PullDetail.svelte";

  const { runtime, detailProps }: {
    runtime: AppRuntime;
    detailProps: ComponentProps<typeof PullDetail>;
  } = $props();

  setAppRuntime(untrack(() => runtime));
</script>

<PullDetail {...detailProps} />
