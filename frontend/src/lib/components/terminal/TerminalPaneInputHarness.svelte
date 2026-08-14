<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import TerminalPane from "./TerminalPane.svelte";

  interface Props {
    data: string;
    suffix?: string;
    onSend: (sent: boolean) => void;
  }

  let { data, suffix = "", onSend }: Props = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(runtime);
  onDestroy(() => Effect.runFork(runtime.disposeEffect));
  let pane = $state<TerminalPane | null>(null);
</script>

<TerminalPane bind:this={pane} workspaceId="ws-123" />
<button type="button" onclick={() => onSend(pane?.sendPastedInput(data, suffix) ?? false)}>Send pasted input</button>
