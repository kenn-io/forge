<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import type { MCPSettings as MCPSettingsType } from "../../api/types.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import MCPSettings from "./MCPSettings.svelte";

  interface Props {
    mcp: MCPSettingsType;
    onUpdate: (mcp: MCPSettingsType) => void;
  }

  let { mcp, onUpdate }: Props = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<MCPSettings {mcp} {onUpdate} />
