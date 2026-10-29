<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import type { AppRuntime } from "../../app/runtime.js";
  import PullDetailPane from "./PullDetailPane.svelte";

  const {
    runtime,
    paneProps,
    keepConversationMounted = false,
  }: {
    runtime: AppRuntime;
    paneProps: ComponentProps<typeof PullDetailPane>;
    keepConversationMounted?: boolean;
  } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

{#if keepConversationMounted}
  <PullDetailPane {...paneProps} tabKey="conversation" visible={paneProps.visible && paneProps.tabKey === "conversation"} />
{/if}
{#if !keepConversationMounted || paneProps.tabKey === "files"}
  <PullDetailPane {...paneProps} />
{/if}
