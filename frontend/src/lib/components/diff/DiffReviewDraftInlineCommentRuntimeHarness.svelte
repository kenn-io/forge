<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import DiffReviewDraftInlineComment from "./DiffReviewDraftInlineComment.svelte";

  const runtime = makeAppRuntime();
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const props: Omit<ComponentProps<typeof DiffReviewDraftInlineComment>, "runtime"> = $props();
</script>

<DiffReviewDraftInlineComment {runtime} {...props} />
