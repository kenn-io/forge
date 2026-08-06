<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import IssueDetail from "./IssueDetail.svelte";

  const { runtime, detailProps }: {
    runtime: AppRuntime;
    detailProps: ComponentProps<typeof IssueDetail>;
  } = $props();

  setAppRuntime(untrack(() => runtime));
</script>

<IssueDetail {...detailProps} />
