<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import DiffReviewThreadInlineComment from "./DiffReviewThreadInlineComment.svelte";

  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const props: Omit<ComponentProps<typeof DiffReviewThreadInlineComment>, "runtime"> = $props();
</script>

<DiffReviewThreadInlineComment {runtime} {...props} />
