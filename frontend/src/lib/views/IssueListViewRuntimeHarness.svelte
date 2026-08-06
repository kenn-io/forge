<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../app/runtime.js";
  import { setAppRuntime } from "../app/runtime-context.js";
  import IssueListView from "./IssueListView.svelte";

  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const props: ComponentProps<typeof IssueListView> = $props();
</script>

<IssueListView {...props} />
