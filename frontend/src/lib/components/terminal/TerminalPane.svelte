<script lang="ts">
  import { getStores } from "@middleman/ui";
  import type { TerminalRenderer } from "@middleman/ui/api/types";
  import GhosttyTerminalPane from "./GhosttyTerminalPane.svelte";
  import XtermTerminalPane from "./XtermTerminalPane.svelte";

  interface TerminalPaneProps {
    workspaceId?: string;
    websocketPath?: string;
    reconnectOnExit?: boolean;
    active?: boolean;
    disabled?: boolean;
    onExit?: (code: number) => void;
    // When the session is already exited at mount time, skip the
    // WebSocket connect — the server's attach endpoint returns 404
    // for non-running sessions, which would loop scheduleReconnect.
    initialStatus?: string;
  }

  const props: TerminalPaneProps = $props();
  const { settings: settingsStore } = getStores();

  function normalizeRenderer(renderer: string | null | undefined): TerminalRenderer {
    return renderer === "ghostty-web" ? "ghostty-web" : "xterm";
  }

  const terminalRenderer = $derived(
    normalizeRenderer(settingsStore.getTerminalRenderer()),
  );
</script>

{#if terminalRenderer === "ghostty-web"}
  <GhosttyTerminalPane {...props} />
{:else}
  <XtermTerminalPane {...props} />
{/if}
