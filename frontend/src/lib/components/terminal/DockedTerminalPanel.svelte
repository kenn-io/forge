<script lang="ts">
  import type { RuntimeSession } from "@middleman/ui/api/types";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import PanelBottomIcon from "@lucide/svelte/icons/panel-bottom";
  import PanelTopIcon from "@lucide/svelte/icons/panel-top";
  import Columns2Icon from "@lucide/svelte/icons/columns-2";
  import Rows2Icon from "@lucide/svelte/icons/rows-2";
  import MoveIcon from "@lucide/svelte/icons/move";
  import TerminalSplitTree from "./TerminalSplitTree.svelte";
  import {
    clearActiveTerminalDrag,
    readRuntimeSessionDrag,
    startRuntimeSessionDrag,
  } from "./terminal-drag";
  import type {
    PaneNode,
    SplitDirection,
    TerminalDock,
  } from "./terminal-layout";
  import {
    MAX_TERMINAL_LEAVES,
    collectSessionKeys,
    countLeaves,
  } from "./terminal-layout";

  interface Props {
    workspaceId: string;
    workspaceHostKey?: string | undefined;
    sessions: RuntimeSession[];
    displayLabels: Record<string, string>;
    tree: PaneNode | null;
    activeSessionKey: string | null;
    open: boolean;
    dock: TerminalDock;
    height: number;
    loading?: boolean;
    onToggle?: (() => void) | undefined;
    onNewTerminal?: (() => void) | undefined;
    onSplit?: ((direction: SplitDirection) => void) | undefined;
    onSelect?: ((sessionKey: string) => void) | undefined;
    onClose?: ((session: RuntimeSession) => void) | undefined;
    onRename?: ((session: RuntimeSession) => void) | undefined;
    onMoveToWorkflow?: ((sessionKey: string) => void) | undefined;
    onDock?: ((dock: TerminalDock) => void) | undefined;
    onResize?: ((height: number) => void) | undefined;
    onDropSession?: ((sessionKey: string) => void) | undefined;
    onExit?: ((session: RuntimeSession) => void) | undefined;
    onRatioChange?: ((splitId: string, ratio: number) => void) | undefined;
    onSplitSession?:
      | ((
          sessionKey: string,
          targetLeafID: string,
          direction: SplitDirection,
          placement: "before" | "after",
        ) => void)
      | undefined;
  }

  const {
    workspaceId,
    workspaceHostKey = undefined,
    sessions,
    displayLabels,
    tree,
    activeSessionKey,
    open,
    dock,
    height,
    loading = false,
    onToggle,
    onNewTerminal,
    onSplit,
    onSelect,
    onClose,
    onRename,
    onMoveToWorkflow,
    onDock,
    onResize,
    onDropSession,
    onExit,
    onRatioChange,
    onSplitSession,
  }: Props = $props();

  const visibleKeys = $derived(collectSessionKeys(tree));
  const canSplit = $derived(
    open && sessions.length > 0 && countLeaves(tree) < MAX_TERMINAL_LEAVES,
  );
  const showSelector = $derived(sessions.length > 1);

  function labelFor(session: RuntimeSession): string {
    return displayLabels[session.key] ?? session.label;
  }

  function startSessionDrag(
    event: DragEvent,
    session: RuntimeSession,
  ): void {
    startRuntimeSessionDrag(event, {
      workspaceId: session.workspace_id,
      sessionKey: session.key,
    });
  }

  function readDroppedSession(event: DragEvent): string | null {
    return readRuntimeSessionDrag(event, workspaceId);
  }

  function handleDragOver(event: DragEvent): void {
    if (readDroppedSession(event) === null) return;
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
  }

  function handleDrop(event: DragEvent): void {
    const sessionKey = readDroppedSession(event);
    if (sessionKey === null) return;
    event.preventDefault();
    onDropSession?.(sessionKey);
    clearActiveTerminalDrag();
  }

  function startPanelResize(event: PointerEvent): void {
    if (dock !== "bottom") return;
    event.preventDefault();
    const startY = event.clientY;
    const startHeight = height;

    function onPointerMove(moveEvent: PointerEvent): void {
      onResize?.(startHeight + startY - moveEvent.clientY);
    }

    function onPointerUp(): void {
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerup", onPointerUp);
    }

    window.addEventListener("pointermove", onPointerMove);
    window.addEventListener("pointerup", onPointerUp, { once: true });
  }
</script>

<section
  class={["terminal-panel", dock, { open }]}
  style={dock === "bottom" && open ? `height: ${height}px` : undefined}
  aria-label="Terminal panel"
  ondragover={handleDragOver}
  ondrop={handleDrop}
>
  {#if dock === "bottom" && open}
    <button
      class="panel-resizer"
      aria-label="Resize terminal panel"
      onpointerdown={startPanelResize}
    ></button>
  {/if}

  <div class="panel-header">
    <button
      class="panel-title"
      aria-label={open ? "Close terminal panel" : "Open terminal panel"}
      onclick={() => onToggle?.()}
    >
      <TerminalIcon size="14" strokeWidth="2" aria-hidden="true" />
      <span>Terminal</span>
      <span class="panel-count">{sessions.length}</span>
    </button>
    <div class="panel-actions">
      <button
        class="panel-action"
        title="New terminal"
        aria-label="New terminal"
        disabled={loading}
        onclick={() => onNewTerminal?.()}
      >
        <PlusIcon size="13" strokeWidth="2.2" aria-hidden="true" />
      </button>
      <button
        class="panel-action"
        title="Split right"
        aria-label="Split terminal right"
        disabled={!canSplit || loading}
        onclick={() => onSplit?.("horizontal")}
      >
        <Columns2Icon size="13" strokeWidth="2" aria-hidden="true" />
      </button>
      <button
        class="panel-action"
        title="Split down"
        aria-label="Split terminal down"
        disabled={!canSplit || loading}
        onclick={() => onSplit?.("vertical")}
      >
        <Rows2Icon size="13" strokeWidth="2" aria-hidden="true" />
      </button>
      <button
        class="panel-action"
        title={dock === "bottom" ? "Move to workflow" : "Move to bottom"}
        aria-label={dock === "bottom" ? "Move terminal panel to workflow" : "Move terminal panel to bottom"}
        onclick={() => onDock?.(dock === "bottom" ? "top" : "bottom")}
      >
        {#if dock === "bottom"}
          <PanelTopIcon size="13" strokeWidth="2" aria-hidden="true" />
        {:else}
          <PanelBottomIcon size="13" strokeWidth="2" aria-hidden="true" />
        {/if}
      </button>
      <button
        class="panel-action"
        title="Close panel"
        aria-label="Close terminal panel"
        onclick={() => onToggle?.()}
      >
        <XIcon size="13" strokeWidth="2.2" aria-hidden="true" />
      </button>
    </div>
  </div>

  {#if open}
    <div class={["panel-body", { "with-selector": showSelector }]}>
      <div class="terminal-tree">
        {#if tree && sessions.length > 0}
          <TerminalSplitTree
            {workspaceId}
            {workspaceHostKey}
            node={tree}
            {sessions}
            {displayLabels}
            {activeSessionKey}
            {onSelect}
            {onClose}
            {onRename}
            {onMoveToWorkflow}
            {onExit}
            {onRatioChange}
            {onSplitSession}
          />
        {:else}
          <div class="empty-terminal">
            <span>{loading ? "Starting terminal..." : "No terminals"}</span>
            <button
              class="empty-action"
              disabled={loading}
              onclick={() => onNewTerminal?.()}
            >
              New terminal
            </button>
          </div>
        {/if}
      </div>
      {#if showSelector}
        <div class="terminal-selector" aria-label="Terminal selector">
          {#each sessions as session (session.key)}
            <button
              draggable="true"
              ondragstart={(event) => startSessionDrag(event, session)}
              ondragend={clearActiveTerminalDrag}
              class={[
                "selector-row",
                {
                  active: activeSessionKey === session.key,
                  visible: visibleKeys.includes(session.key),
                },
              ]}
              onclick={() => onSelect?.(session.key)}
              ondblclick={() => onRename?.(session)}
            >
              <span class={["selector-dot", session.status]}></span>
              <span class="selector-label">{labelFor(session)}</span>
              <MoveIcon size="11" strokeWidth="2" aria-hidden="true" />
            </button>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</section>

<style>
  .terminal-panel {
    flex-shrink: 0;
    min-height: 30px;
    border-top: var(--chrome-border-width) solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
  }

  .terminal-panel.top {
    display: flex;
    flex-direction: column;
    height: 100%;
    border-top: 0;
  }

  .terminal-panel.bottom.open {
    position: relative;
    display: flex;
    flex-direction: column;
    border-top: 0;
  }

  .panel-resizer {
    position: absolute;
    top: calc(-1 * var(--chrome-dock-resize-hit-outset));
    left: 0;
    right: 0;
    height: var(--chrome-dock-resize-hit-size);
    border: 0;
    background: transparent;
    cursor: row-resize;
    z-index: 3;
  }

  .panel-resizer::before {
    content: "";
    position: absolute;
    left: 0;
    right: 0;
    top: var(--chrome-dock-resize-stripe-offset);
    height: var(--chrome-pane-divider-width);
    background: var(--border-default);
  }

  .panel-resizer:hover::before,
  .panel-resizer:focus-visible::before {
    background: var(--accent-blue);
  }

  .panel-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 30px;
    flex-shrink: 0;
    border-bottom: var(--chrome-border-width) solid var(--border-muted);
    background: var(--bg-inset);
  }

  .panel-title {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 100%;
    padding: 0 10px;
    border: 0;
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
    font-weight: 700;
    cursor: pointer;
  }

  .panel-title:hover {
    color: var(--text-primary);
  }

  .panel-count {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 16px;
    height: 16px;
    padding: 0 4px;
    border-radius: 3px;
    background: var(--bg-surface);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    font-family: var(--font-mono);
  }

  .panel-actions {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 0 6px;
  }

  .panel-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 23px;
    height: 22px;
    border: var(--chrome-border-width) solid transparent;
    border-radius: 3px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }

  .panel-action:hover:not(:disabled),
  .panel-action:focus-visible {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: var(--border-muted);
    outline: none;
  }

  .panel-action:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .panel-body {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    min-height: 0;
    flex: 1;
    background: #0d1117;
  }

  .panel-body.with-selector {
    grid-template-columns: minmax(0, 1fr) minmax(132px, 160px);
  }

  .terminal-tree {
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .terminal-selector {
    min-width: 0;
    overflow: auto;
    border-left: var(--chrome-border-width) solid var(--border-default);
    background: var(--bg-surface);
    padding: 4px;
  }

  .selector-row {
    display: grid;
    grid-template-columns: 8px minmax(0, 1fr) 14px;
    align-items: center;
    gap: 7px;
    width: 100%;
    height: 26px;
    padding: 0 6px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-xs);
    text-align: left;
    cursor: grab;
  }

  .selector-row:hover,
  .selector-row.active {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .selector-row.visible {
    font-weight: 650;
  }

  .selector-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--text-muted);
  }

  .selector-dot.running {
    background: var(--accent-green);
  }

  .selector-dot.starting {
    background: var(--accent-amber);
  }

  .selector-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .empty-terminal {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    height: 100%;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .empty-action {
    height: 24px;
    padding: 0 8px;
    border: var(--chrome-border-width) solid var(--border-default);
    border-radius: 3px;
    background: var(--bg-surface);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-xs);
    font-weight: 650;
    cursor: pointer;
  }

  .empty-action:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .empty-action:disabled {
    opacity: 0.6;
    cursor: wait;
  }

  @media (max-width: 760px) {
    .panel-body.with-selector {
      grid-template-columns: minmax(0, 1fr) minmax(96px, 116px);
    }

    .terminal-selector {
      padding: 3px;
    }

    .selector-row {
      gap: 5px;
      padding: 0 5px;
    }
  }
</style>
