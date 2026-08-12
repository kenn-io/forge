<script lang="ts">
  import XtermTerminalPane from "./XtermTerminalPane.svelte";

  interface TerminalPaneProps {
    workspaceId?: string | undefined;
    websocketPath?: string | undefined;
    reconnectOnExit?: boolean | undefined;
    active?: boolean | undefined;
    renderingEnabled?: boolean | undefined;
    autoFocus?: boolean | undefined;
    cursorWheelInput?: boolean;
    disabled?: boolean;
    onExit?: ((code: number) => void) | undefined;
    onConnectionChange?: ((connected: boolean) => void) | undefined;
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
    renderingEnabled = undefined,
    autoFocus = undefined,
    cursorWheelInput = false,
    disabled = false,
    onExit = undefined,
    onConnectionChange = undefined,
    initialStatus = undefined,
  }: TerminalPaneProps = $props();

  let xtermPane = $state<XtermTerminalPane | null>(null);

  export function focus(): void {
    xtermPane?.focus();
  }

  export function sendInput(data: string): boolean {
    return xtermPane?.sendInput(data) ?? false;
  }
</script>

<XtermTerminalPane
  bind:this={xtermPane}
  {workspaceId}
  {websocketPath}
  {reconnectOnExit}
  {active}
  {renderingEnabled}
  {autoFocus}
  {cursorWheelInput}
  {disabled}
  {onExit}
  {onConnectionChange}
  {initialStatus}
/>
