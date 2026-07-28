<script lang="ts">
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
</script>

<XtermTerminalPane
  {workspaceId}
  {websocketPath}
  {reconnectOnExit}
  {active}
  {disabled}
  {onExit}
  {initialStatus}
/>
