<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import type XtermTerminalPane from "./XtermTerminalPane.svelte";
  import type { TerminalKey } from "./terminal-key.js";

  interface TerminalPaneProps {
    workspaceId?: string | undefined;
    websocketPath?: string | undefined;
    fleetHostKey?: string | undefined;
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
    fleetHostKey = undefined,
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
  let focusRequested = false;

  function setTerminalPane(pane: XtermTerminalPane | null): void {
    xtermPane = pane;
    if (pane && focusRequested) {
      focusRequested = false;
      pane.focus();
    }
  }

  export function focus(): void {
    if (xtermPane) xtermPane.focus();
    else focusRequested = true;
  }

  export function sendInput(data: string): boolean {
    return xtermPane?.sendInput(data) ?? false;
  }

  export function sendPastedInput(data: string, suffix = ""): boolean {
    return xtermPane?.sendPastedInput(data, suffix) ?? false;
  }

  export function sendKey(key: TerminalKey): boolean {
    return xtermPane?.sendKey(key) ?? false;
  }
</script>

{#await import("./XtermTerminalPane.svelte")}
  <p role="status">Loading terminal...</p>
{:then { default: Terminal }}
  <Terminal
    bind:this={() => xtermPane, setTerminalPane}
    {workspaceId}
    {websocketPath}
    {fleetHostKey}
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
{:catch}
  <div role="alert">
    <p>Could not load terminal.</p>
    <Button onclick={() => window.location.reload()}>Reload</Button>
  </div>
{/await}
