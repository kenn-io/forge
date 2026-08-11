<script lang="ts">
  import type { RuntimeSession } from "../../api/types.js";
  import DockedTerminalPanel from "./DockedTerminalPanel.svelte";
  import type { PaneNode } from "./terminal-layout";

  interface Props {
    height?: number;
    disabled?: boolean;
    onResize?: (height: number) => void;
    sessions?: RuntimeSession[];
    tree?: PaneNode | null;
    activeSessionKey?: string | null;
    dragScope?: string;
    paneKeyForSession?: (sessionKey: string) => string | null;
    onInputActivate?: () => void;
  }

  let {
    height = 300,
    disabled = false,
    onResize = () => {},
    sessions = [],
    tree = null,
    activeSessionKey = null,
    dragScope = undefined,
    paneKeyForSession = undefined,
    onInputActivate = undefined,
  }: Props = $props();
</script>

<DockedTerminalPanel
  workspaceId="ws-1"
  {sessions}
  displayLabels={{}}
  {tree}
  {activeSessionKey}
  open={true}
  dock="bottom"
  {height}
  {disabled}
  {onResize}
  {dragScope}
  {paneKeyForSession}
  {onInputActivate}
/>
