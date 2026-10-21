<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import type { AppRuntime } from "../../app/runtime.js";
  import PullDetailPane from "./PullDetailPane.svelte";

  const { runtime, paneProps, files = false }: { runtime: AppRuntime; paneProps: ComponentProps<typeof PullDetailPane>; files?: boolean } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

<PullDetailPane {...paneProps} />

{#if files}<PullDetailPane {...paneProps} tabKey="files" />{/if}
