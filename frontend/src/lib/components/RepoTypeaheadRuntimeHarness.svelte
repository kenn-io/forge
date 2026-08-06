<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../app/runtime-context.js";
  import { client } from "../api/runtime.js";
  import { makeTestAppRuntime } from "../testing/effect-layers.js";
  import RepoTypeahead from "./RepoTypeahead.svelte";

  const runtime = makeTestAppRuntime(client);
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const props: ComponentProps<typeof RepoTypeahead> = $props();
</script>

<RepoTypeahead {...props} />
