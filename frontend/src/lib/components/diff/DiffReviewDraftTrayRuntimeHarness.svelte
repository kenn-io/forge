<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import type { AppRuntime } from "../../app/runtime.js";
  import DiffReviewDraftTray from "./DiffReviewDraftTray.svelte";

  let { runtime, ...props }: ComponentProps<typeof DiffReviewDraftTray> & { runtime: AppRuntime } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

<DiffReviewDraftTray {...props} />
