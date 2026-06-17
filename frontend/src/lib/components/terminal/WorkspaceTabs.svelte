<script lang="ts">
  import type { RuntimeSession } from "@middleman/ui/api/types";
  import XIcon from "@lucide/svelte/icons/x";
  import HouseIcon from "@lucide/svelte/icons/house";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import SparklesIcon from "@lucide/svelte/icons/sparkles";
  import MoveIcon from "@lucide/svelte/icons/move";
  import {
    clearActiveTerminalDrag,
    startRuntimeSessionDrag,
  } from "./terminal-drag";

  interface WorkspaceTabsProps {
    workspaceId?: string;
    activeKey: string;
    sessions: RuntimeSession[];
    displayLabels?: Record<string, string>;
    terminalOpen?: boolean;
    onSelectHome?: () => void;
    onSelectTerminal?: () => void;
    onSelectSession?: (sessionKey: string) => void;
    onCloseTerminal?: () => void;
    onMoveSessionToTerminal?: (sessionKey: string) => void;
    onCloseSession?: (sessionKey: string) => void;
  }

  const {
    workspaceId = "",
    activeKey,
    sessions,
    displayLabels = {},
    terminalOpen = false,
    onSelectHome,
    onSelectTerminal,
    onSelectSession,
    onCloseTerminal,
    onMoveSessionToTerminal,
    onCloseSession,
  }: WorkspaceTabsProps = $props();

  function sessionStatusClass(status: string): string {
    if (status === "running") return "running";
    if (status === "starting") return "starting";
    return "exited";
  }

  function labelFor(session: RuntimeSession): string {
    return displayLabels[session.key] ?? session.label;
  }

  function startSessionDrag(
    event: DragEvent,
    session: RuntimeSession,
  ): void {
    if (!workspaceId) return;
    startRuntimeSessionDrag(event, { workspaceId, sessionKey: session.key });
  }
</script>

<div class="workspace-tabs" role="tablist" aria-label="Workspace tabs">
  <button
    role="tab"
    aria-selected={activeKey === "home"}
    class={["tab", "tab-home", { active: activeKey === "home" }]}
    onclick={() => onSelectHome?.()}
  >
    <span class="tab-icon" aria-hidden="true">
      <HouseIcon size="13" strokeWidth="2" />
    </span>
    <span class="tab-label">Home</span>
  </button>

  {#if terminalOpen}
    <div
      class={[
        "tab-with-close",
        "tab",
        "tab-terminal",
        { active: activeKey === "terminal" },
      ]}
    >
      <button
        role="tab"
        aria-selected={activeKey === "terminal"}
        class="tab-button"
        onclick={() => onSelectTerminal?.()}
      >
        <span class="tab-icon" aria-hidden="true">
          <TerminalIcon size="13" strokeWidth="2" />
        </span>
        <span class="tab-label">Terminal</span>
      </button>
      <button
        class="tab-close"
        aria-label="Close terminal"
        title="Close tab"
        onclick={() => onCloseTerminal?.()}
      >
        <XIcon size="12" strokeWidth="2.25" aria-hidden="true" />
      </button>
    </div>
  {/if}

  {#each sessions as session (session.key)}
    <div
      class={[
        "tab-with-close",
        "tab",
        { active: activeKey === `session:${session.key}` },
      ]}
    >
      <button
        role="tab"
        draggable={workspaceId !== ""}
        ondragstart={(event) => startSessionDrag(event, session)}
        ondragend={clearActiveTerminalDrag}
        aria-selected={activeKey === `session:${session.key}`}
        class="tab-button"
        onclick={() => onSelectSession?.(session.key)}
      >
        <span class="tab-icon" aria-hidden="true">
          {#if session.kind === "plain_shell"}
            <TerminalIcon size="13" strokeWidth="2" />
          {:else}
            <SparklesIcon size="13" strokeWidth="2" />
          {/if}
        </span>
        <span class="tab-label">{labelFor(session)}</span>
        <span
          class={["status-dot", sessionStatusClass(session.status)]}
          title={session.status}
        ></span>
      </button>
      <button
        class="tab-action"
        aria-label={`Move ${labelFor(session)} to terminal`}
        title="Move to terminal"
        onclick={() => onMoveSessionToTerminal?.(session.key)}
      >
        <MoveIcon size="12" strokeWidth="2.25" aria-hidden="true" />
      </button>
      <button
        class="tab-close"
        aria-label={`Close ${labelFor(session)}`}
        title="Close tab"
        onclick={() => onCloseSession?.(session.key)}
      >
        <XIcon size="12" strokeWidth="2.25" aria-hidden="true" />
      </button>
    </div>
  {/each}
</div>

<style>
  .workspace-tabs {
    display: flex;
    align-items: stretch;
    gap: 0;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
    height: 100%;
  }

  .workspace-tabs::-webkit-scrollbar {
    width: 0;
    height: 0;
  }

  /* Shared tab chrome — applies to plain buttons and the close-bearing wrapper. */
  .tab {
    position: relative;
    display: inline-flex;
    align-items: center;
    height: 100%;
    border: 0;
    border-right: 1px solid var(--border-muted);
    background: transparent;
    color: var(--text-muted);
    font: inherit;
    font-size: var(--font-size-sm);
    font-weight: 500;
    letter-spacing: 0.005em;
    cursor: pointer;
    flex-shrink: 0;
    transition: background-color 80ms ease, color 80ms ease;
  }

  .tab-home {
    padding: 0 12px;
    gap: 6px;
  }

  .tab:hover:not(.active) {
    background: var(--bg-surface-hover);
    color: var(--text-secondary);
  }

  .tab.active {
    background: var(--bg-surface);
    color: var(--text-primary);
    font-weight: 600;
    /* Pull the active tab down by 1px so its bottom edge meets the
     * editor surface — JetBrains-style "this tab owns the content". */
    margin-bottom: -1px;
    border-bottom: 1px solid var(--bg-surface);
  }

  /* The 2px top accent stripe on the active tab. */
  .tab.active::before {
    content: "";
    position: absolute;
    inset: 0 0 auto 0;
    height: 2px;
    background: var(--accent-blue);
    pointer-events: none;
  }

  .tab-with-close {
    padding: 0 4px 0 10px;
    gap: 4px;
  }

  .tab-button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 100%;
    padding: 0 4px 0 0;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    cursor: inherit;
  }

  .tab-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .tab.active .tab-icon {
    color: var(--accent-blue);
  }

  .tab-label {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 18ch;
  }

  .tab-close {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: transparent;
    font: inherit;
    cursor: pointer;
    transition: color 80ms ease, background-color 80ms ease;
  }

  .tab-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: transparent;
    font: inherit;
    cursor: pointer;
    transition: color 80ms ease, background-color 80ms ease;
  }

  .tab-with-close:hover .tab-action,
  .tab-with-close.active .tab-action,
  .tab-action:focus-visible,
  .tab-with-close:hover .tab-close,
  .tab-with-close.active .tab-close,
  .tab-close:focus-visible {
    color: var(--text-muted);
  }

  .tab-action:hover,
  .tab-action:focus-visible,
  .tab-close:hover,
  .tab-close:focus-visible {
    background: var(--bg-inset);
    color: var(--text-primary);
  }

  .tab-action:focus-visible,
  .tab-close:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
  }

  .status-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent-green);
    box-shadow: 0 0 0 2px var(--bg-surface);
    flex-shrink: 0;
    margin-left: 2px;
  }

  .tab:not(.active) .status-dot {
    box-shadow: none;
  }

  .status-dot.starting {
    background: var(--accent-amber);
    animation: pulse 1.4s ease-in-out infinite;
  }

  .status-dot.exited {
    background: var(--text-muted);
  }

  @keyframes pulse {
    0%, 100% { opacity: 0.5; }
    50% { opacity: 1; }
  }
</style>
