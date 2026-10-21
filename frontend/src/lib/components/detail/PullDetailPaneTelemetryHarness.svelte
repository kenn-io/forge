<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import type { AppRuntime } from "../../app/runtime.js";
  import PullDetailPane from "./PullDetailPane.svelte";

  const { runtime, paneProps }: { runtime: AppRuntime; paneProps: ComponentProps<typeof PullDetailPane> } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

<PullDetailPane {...paneProps} tabKey="conversation" visible={paneProps.visible && paneProps.tabKey === "conversation"} />
{#if paneProps.tabKey === "files"}
  <PullDetailPane {...paneProps} />
{/if}
