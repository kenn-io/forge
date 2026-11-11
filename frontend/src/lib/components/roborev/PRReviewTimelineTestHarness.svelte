<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { createAppStores } from "../../app-stores.svelte.js";
  import { createPRRoborevTimeline } from "../../stores/roborev/pr-timeline.svelte.js";
  import PRReviewTimeline from "./PRReviewTimeline.svelte";
  import PRReviewTimelineDrawer from "./PRReviewTimelineDrawer.svelte";

  const { runtime, timelineProps }: {
    runtime: AppRuntime;
    timelineProps: Omit<ComponentProps<typeof PRReviewTimeline>, "timeline">;
  } = $props();

  const appRuntime = untrack(() => runtime);
  setAppRuntime(appRuntime);
  const timeline = createPRRoborevTimeline({
    runtime: appRuntime,
    baseStores: createAppStores({ runtime: appRuntime }).stores,
    owner: "pr-review-timeline-test",
    onError: () => {},
  });
</script>

<PRReviewTimeline {timeline} {...timelineProps} />
<PRReviewTimelineDrawer {timeline} />
