<script lang="ts">
  import XtermTerminalPane from "./XtermTerminalPane.svelte";

  interface TerminalPaneProps {
    workspaceId?: string | undefined;
    websocketPath?: string | undefined;
    reconnectOnExit?: boolean | undefined;
    active?: boolean | undefined;
    autoFocus?: boolean | undefined;
    cursorWheelInput?: boolean;
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
    autoFocus = undefined,
    cursorWheelInput = false,
    disabled = false,
    onExit = undefined,
    initialStatus = undefined,
  }: TerminalPaneProps = $props();

  let xtermPane = $state<XtermTerminalPane | null>(null);

  export function focus(): void {
    xtermPane?.focus();
  }
</script>

<XtermTerminalPane
  bind:this={xtermPane}
  {workspaceId}
  {websocketPath}
  {reconnectOnExit}
  {active}
  {autoFocus}
  {cursorWheelInput}
  {disabled}
  {onExit}
  {initialStatus}
/>
