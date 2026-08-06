<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import DiffReviewThreadInlineComment from "./DiffReviewThreadInlineComment.svelte";

  const runtime = makeAppRuntime();
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const props: Omit<ComponentProps<typeof DiffReviewThreadInlineComment>, "runtime"> = $props();
</script>

<DiffReviewThreadInlineComment {runtime} {...props} />
