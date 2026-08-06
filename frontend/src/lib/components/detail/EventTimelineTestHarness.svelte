<script lang="ts">
  import { setContext, untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import EventTimeline from "./EventTimeline.svelte";

  const { runtime, timelineProps, context }: {
    runtime: AppRuntime;
    timelineProps: ComponentProps<typeof EventTimeline>;
    context?: Map<symbol, unknown>;
  } = $props();

  setAppRuntime(untrack(() => runtime));
  for (const [key, value] of untrack(() => context ?? new Map())) {
    setContext(key, value);
  }
</script>

<EventTimeline {...timelineProps} />
