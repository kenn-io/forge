<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import type { GeneratedClient } from "../../api/generated-api.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import Palette from "./Palette.svelte";

  type Props = { client?: GeneratedClient | undefined } & ComponentProps<typeof Palette>;
  const { client, ...props }: Props = $props();
  const runtime = makeAppRuntime(untrack(() => client));
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<Palette {...props} />
