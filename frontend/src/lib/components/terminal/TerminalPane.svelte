<script lang="ts">
  import { getStores } from "@middleman/ui";
  import type { TerminalRenderer } from "@middleman/ui/api/types";
  import GhosttyTerminalPane from "./GhosttyTerminalPane.svelte";
  import XtermTerminalPane from "./XtermTerminalPane.svelte";

  interface TerminalPaneProps {
    workspaceId?: string | undefined;
    websocketPath?: string | undefined;
    reconnectOnExit?: boolean | undefined;
    active?: boolean | undefined;
    disabled?: boolean;
    onExit?: ((code: number) => void) | undefined;
    // When the session is already exited at mount time, skip the
    // WebSocket connect — the server's attach endpoint returns 404
    // for non-running sessions, which would loop scheduleReconnect.
    initialStatus?: string | undefined;
  }

  let {
    workspaceId = undefined,
    websocketPath = undefined,
    reconnectOnExit = undefined,
    active = undefined,
    disabled = false,
    onExit = undefined,
    initialStatus = undefined,
  }: TerminalPaneProps = $props();
  const { settings: settingsStore } = getStores();

  function normalizeRenderer(renderer: string | null | undefined): TerminalRenderer {
    return renderer === "ghostty-web" ? "ghostty-web" : "xterm";
  }

  const terminalRenderer = $derived(
    normalizeRenderer(settingsStore.getTerminalRenderer()),
  );
</script>

{#if terminalRenderer === "ghostty-web"}
  <GhosttyTerminalPane
    {workspaceId}
    {websocketPath}
    {reconnectOnExit}
    {active}
    {disabled}
    {onExit}
    {initialStatus}
  />
{:else}
  <XtermTerminalPane
    {workspaceId}
    {websocketPath}
    {reconnectOnExit}
    {active}
    {disabled}
    {onExit}
    {initialStatus}
  />
{/if}
