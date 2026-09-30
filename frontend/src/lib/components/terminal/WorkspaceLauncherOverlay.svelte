<script lang="ts">
  import { IconButton } from "@kenn-io/kit-ui";
  import XIcon from "@lucide/svelte/icons/x";
  import { untrack } from "svelte";
  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";
  import type { LaunchTarget, QuickAction, RuntimeSession } from "../../api/types.js";
  import WorkspaceLauncher from "./WorkspaceLauncher.svelte";

  // Contained by the terminal frame. The rest of Forge remains interactive.
  interface Props {
    open: boolean;
    launchTargets: LaunchTarget[];
    sessions: RuntimeSession[];
    displayLabels?: Record<string, string>;
    launchingKey?: string | null;
    readonly?: boolean;
    quickActions?: QuickAction[];
    onClose: () => void;
    onLaunch: (targetKey: string) => void;
    onQuickAction?: (action: QuickAction) => void;
    onOpenSession: (sessionKey: string) => void;
  }

  const {
    open,
    launchTargets,
    sessions,
    displayLabels = {},
    launchingKey = null,
    readonly = false,
    quickActions = [],
    onClose,
    onLaunch,
    onQuickAction,
    onOpenSession,
  }: Props = $props();

  let panel = $state<HTMLElement>();
  let focused = $state(false);

  $effect(() => {
    if (!open || !focused) return;
    return untrack(() => pushModalFrame("workspace-launcher", []));
  });

  function onKeydown(event: KeyboardEvent): void {
    if (!open || event.key !== "Escape" || !panel?.contains(document.activeElement)) return;
    event.preventDefault();
    event.stopPropagation();
    onClose();
  }
</script>

<svelte:window onkeydowncapture={onKeydown} />

{#if open}
  <div class="launcher-overlay">
    <div
      bind:this={panel}
      class="launcher-panel"
      role="dialog"
      aria-label="Launch a session"
      aria-modal="false"
      tabindex="-1"
      onfocusin={() => { focused = true; }}
      onfocusout={(event) => { focused = event.relatedTarget instanceof Node && !!panel?.contains(event.relatedTarget); }}
      {@attach (node) => {
        const previous = document.activeElement;
        (node.querySelector<HTMLElement>(".launch-card:not(:disabled)") ?? node).focus();
        return () => {
          if (node.contains(document.activeElement) && previous instanceof HTMLElement && previous.isConnected) previous.focus();
        };
      }}
    >
      <header>
        <h2>Launch a session</h2>
        <IconButton ariaLabel="Close" title="Close launcher" size="sm" onclick={onClose}>
          <XIcon size={16} aria-hidden="true" />
        </IconButton>
      </header>
      <div class="launcher-body">
        <WorkspaceLauncher
          {launchTargets}
          {sessions}
          {displayLabels}
          {launchingKey}
          {readonly}
          {quickActions}
          onLaunch={onLaunch}
          {onQuickAction}
          onOpenSession={onOpenSession}
        />
      </div>
    </div>
  </div>
{/if}

<style>
  .launcher-overlay {
    position: absolute;
    inset: 0;
    /* xterm's canvas and scrollbar layers reach z-index 11. */
    z-index: 20;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--space-4);
    pointer-events: none;
  }

  .launcher-panel {
    display: flex;
    flex-direction: column;
    width: min(620px, 100%);
    max-height: 100%;
    min-height: 0;
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-primary);
    pointer-events: auto;
  }

  header {
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-4);
    border-bottom: 1px solid var(--border-default);
  }

  h2 {
    margin: 0;
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-semibold);
  }

  .launcher-body {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    overflow-y: auto;
  }
</style>
