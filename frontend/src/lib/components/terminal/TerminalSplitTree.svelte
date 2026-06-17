<script lang="ts">
  import type { RuntimeSession } from "@middleman/ui/api/types";
  import XIcon from "@lucide/svelte/icons/x";
  import MoveIcon from "@lucide/svelte/icons/move";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import SparklesIcon from "@lucide/svelte/icons/sparkles";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import Self from "./TerminalSplitTree.svelte";
  import TerminalPane from "./TerminalPane.svelte";
  import type { PaneNode, SplitDirection, SplitEdge } from "./terminal-layout";
  import {
    clampRatio,
    splitEdgeFromPoint,
    splitPlacementForEdge,
  } from "./terminal-layout";
  import {
    clearActiveTerminalDrag,
    readRuntimeSessionDrag,
    startRuntimeSessionDrag,
  } from "./terminal-drag";
  import { workspaceSessionWebSocketPath } from "../../api/workspace-runtime.js";

  interface BorderTrim {
    top?: boolean;
    right?: boolean;
    bottom?: boolean;
    left?: boolean;
  }

  type BorderEdge = keyof BorderTrim;

  interface Props {
    workspaceId: string;
    workspaceHostKey?: string | undefined;
    node: PaneNode;
    sessions: RuntimeSession[];
    displayLabels: Record<string, string>;
    activeSessionKey: string | null;
    borderTrim?: BorderTrim | undefined;
    onSelect?: ((sessionKey: string) => void) | undefined;
    onClose?: ((session: RuntimeSession) => void) | undefined;
    onRename?: ((session: RuntimeSession) => void) | undefined;
    onMoveToWorkflow?: ((sessionKey: string) => void) | undefined;
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
    node,
    sessions,
    displayLabels,
    activeSessionKey,
    borderTrim = {},
    onSelect,
    onClose,
    onRename,
    onMoveToWorkflow,
    onExit,
    onRatioChange,
    onSplitSession,
  }: Props = $props();

  let splitEl = $state<HTMLDivElement | null>(null);
  let dropTargetsVisible = $state(false);
  let activeSplitEdge = $state<SplitEdge | null>(null);

  function sessionForKey(sessionKey: string): RuntimeSession | null {
    return sessions.find((session) => session.key === sessionKey) ?? null;
  }

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
    const sessionKey = readRuntimeSessionDrag(event, workspaceId);
    if (
      sessionKey === null ||
      (node.type === "leaf" && sessionKey === node.sessionKey) ||
      !sessionForKey(sessionKey)
    ) {
      return null;
    }
    return sessionKey;
  }

  function splitEdgeFromEvent(event: DragEvent): SplitEdge | null {
    const target = event.currentTarget;
    if (!(target instanceof HTMLElement)) return null;
    const rect = target.getBoundingClientRect();
    return splitEdgeFromPoint(rect, event.clientX, event.clientY);
  }

  function handleDragOver(event: DragEvent): void {
    if (node.type !== "leaf" || readDroppedSession(event) === null) return;
    event.preventDefault();
    event.stopPropagation();
    dropTargetsVisible = true;
    activeSplitEdge = splitEdgeFromEvent(event);
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
  }

  function hideDropTargets(): void {
    dropTargetsVisible = false;
    activeSplitEdge = null;
  }

  function handleDragLeave(event: DragEvent): void {
    const current = event.currentTarget;
    const next = event.relatedTarget;
    if (
      current instanceof HTMLElement &&
      next instanceof Node &&
      current.contains(next)
    ) {
      return;
    }
    hideDropTargets();
  }

  function dropSplit(event: DragEvent): void {
    if (node.type !== "leaf") return;
    const sessionKey = readDroppedSession(event);
    const edge = splitEdgeFromEvent(event);
    if (sessionKey === null || edge === null) return;
    event.preventDefault();
    event.stopPropagation();
    hideDropTargets();
    const { direction, placement } = splitPlacementForEdge(edge);
    onSplitSession?.(sessionKey, node.id, direction, placement);
    clearActiveTerminalDrag();
  }

  function startResize(event: PointerEvent): void {
    if (node.type !== "split" || !splitEl) return;
    event.preventDefault();
    const rect = splitEl.getBoundingClientRect();
    const direction = node.direction;
    const splitId = node.id;
    const pointerId = event.pointerId;
    (event.currentTarget as HTMLElement).setPointerCapture(pointerId);

    function onPointerMove(moveEvent: PointerEvent): void {
      const ratio =
        direction === "horizontal"
          ? (moveEvent.clientX - rect.left) / Math.max(1, rect.width)
          : (moveEvent.clientY - rect.top) / Math.max(1, rect.height);
      onRatioChange?.(splitId, clampRatio(ratio));
    }

    function onPointerUp(upEvent: PointerEvent): void {
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerup", onPointerUp);
      try {
        (event.currentTarget as HTMLElement).releasePointerCapture(
          upEvent.pointerId,
        );
      } catch {
        // Pointer capture may already be gone after a browser-cancelled drag.
      }
    }

    window.addEventListener("pointermove", onPointerMove);
    window.addEventListener("pointerup", onPointerUp, { once: true });
  }

  function inheritTrim(target: BorderTrim, edge: BorderEdge): void {
    if (borderTrim[edge]) {
      target[edge] = true;
    }
  }

  function firstChildTrim(direction: SplitDirection): BorderTrim {
    if (direction === "horizontal") {
      const trim: BorderTrim = { right: true };
      inheritTrim(trim, "top");
      inheritTrim(trim, "bottom");
      inheritTrim(trim, "left");
      return trim;
    }

    const trim: BorderTrim = { bottom: true };
    inheritTrim(trim, "top");
    inheritTrim(trim, "right");
    inheritTrim(trim, "left");
    return trim;
  }

  function secondChildTrim(direction: SplitDirection): BorderTrim {
    if (direction === "horizontal") {
      const trim: BorderTrim = { left: true };
      inheritTrim(trim, "top");
      inheritTrim(trim, "right");
      inheritTrim(trim, "bottom");
      return trim;
    }

    const trim: BorderTrim = { top: true };
    inheritTrim(trim, "right");
    inheritTrim(trim, "bottom");
    inheritTrim(trim, "left");
    return trim;
  }
</script>

{#if node.type === "leaf"}
  {@const session = sessionForKey(node.sessionKey)}
  <div
    class={[
      "terminal-leaf",
      {
        active: activeSessionKey === node.sessionKey,
        "single-session": sessions.length <= 1,
        "trim-top": borderTrim.top,
        "trim-right": borderTrim.right,
        "trim-bottom": borderTrim.bottom,
        "trim-left": borderTrim.left,
      },
    ]}
  >
    {#if session}
      {#if sessions.length > 1}
        <div
          class="leaf-header"
          role="group"
          aria-label={`${labelFor(session)} terminal pane`}
          draggable="true"
          ondragstart={(event) => startSessionDrag(event, session)}
          ondragend={clearActiveTerminalDrag}
        >
          <button
            class="leaf-title"
            draggable="true"
            ondragstart={(event) => startSessionDrag(event, session)}
            ondragend={clearActiveTerminalDrag}
            onclick={() => onSelect?.(session.key)}
            aria-label={`Focus ${labelFor(session)}`}
          >
            <span class="leaf-icon" aria-hidden="true">
              {#if session.kind === "plain_shell"}
                <TerminalIcon size="12" strokeWidth="2" />
              {:else}
                <SparklesIcon size="12" strokeWidth="2" />
              {/if}
            </span>
            <span class="leaf-label">{labelFor(session)}</span>
            <span class={["leaf-dot", session.status]}></span>
          </button>
          <div class="leaf-actions">
            <button
              class="leaf-action"
              title="Rename"
              aria-label={`Rename ${labelFor(session)}`}
              onclick={() => onRename?.(session)}
            >
              <PencilIcon size="12" strokeWidth="2" aria-hidden="true" />
            </button>
            <button
              class="leaf-action"
              title="Move to workflow"
              aria-label={`Move ${labelFor(session)} to workflow`}
              onclick={() => onMoveToWorkflow?.(session.key)}
            >
              <MoveIcon size="12" strokeWidth="2" aria-hidden="true" />
            </button>
            <button
              class="leaf-action"
              title="Close"
              aria-label={`Close ${labelFor(session)}`}
              onclick={() => onClose?.(session)}
            >
              <XIcon size="12" strokeWidth="2.2" aria-hidden="true" />
            </button>
          </div>
        </div>
      {/if}
      {#key session.key}
        <div
          class={[
            "terminal-leaf-body",
            { "show-drop-targets": dropTargetsVisible },
          ]}
          role="group"
          aria-label={`${labelFor(session)} split drop targets`}
          onpointerdown={() => onSelect?.(session.key)}
          onfocusin={() => onSelect?.(session.key)}
          ondragover={handleDragOver}
          ondragleave={handleDragLeave}
          ondrop={dropSplit}
        >
          <TerminalPane
            websocketPath={workspaceSessionWebSocketPath(
              workspaceId,
              session.key,
              workspaceHostKey,
            )}
            reconnectOnExit={false}
            active={activeSessionKey === session.key}
            onExit={() => onExit?.(session)}
            initialStatus={session.status}
          />
          <div
            class={[
              "split-preview",
              activeSplitEdge,
              { active: dropTargetsVisible && activeSplitEdge !== null },
            ]}
            aria-hidden="true"
          ></div>
        </div>
      {/key}
    {:else}
      <div class="missing-session">Session unavailable</div>
    {/if}
  </div>
{:else}
  <div
    bind:this={splitEl}
    class={["terminal-split", node.direction]}
    style={`--first-ratio: ${node.ratio}; --second-ratio: ${1 - node.ratio};`}
  >
    <div class="split-child first">
      <Self
        {workspaceId}
        {workspaceHostKey}
        node={node.first}
        {sessions}
        {displayLabels}
        {activeSessionKey}
        borderTrim={firstChildTrim(node.direction)}
        {onSelect}
        {onClose}
        {onRename}
        {onMoveToWorkflow}
        {onExit}
        {onRatioChange}
        {onSplitSession}
      />
    </div>
    <button
      class="split-divider"
      aria-label="Resize split"
      onpointerdown={startResize}
    ></button>
    <div class="split-child second">
      <Self
        {workspaceId}
        {workspaceHostKey}
        node={node.second}
        {sessions}
        {displayLabels}
        {activeSessionKey}
        borderTrim={secondChildTrim(node.direction)}
        {onSelect}
        {onClose}
        {onRename}
        {onMoveToWorkflow}
        {onExit}
        {onRatioChange}
        {onSplitSession}
      />
    </div>
  </div>
{/if}

<style>
  .terminal-split,
  .terminal-leaf {
    min-width: 0;
    min-height: 0;
    height: 100%;
  }

  .terminal-split {
    display: flex;
    overflow: hidden;
  }

  .terminal-split.horizontal {
    flex-direction: row;
  }

  .terminal-split.vertical {
    flex-direction: column;
  }

  .split-child {
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .split-child.first {
    flex: var(--first-ratio) 1 0;
  }

  .split-child.second {
    flex: var(--second-ratio) 1 0;
  }

  .split-divider {
    flex: 0 0 var(--chrome-pane-divider-width);
    appearance: none;
    border: 0;
    padding: 0;
    background: var(--border-muted);
    cursor: col-resize;
    flex-shrink: 0;
  }

  .terminal-split.vertical > .split-divider {
    cursor: row-resize;
  }

  .split-divider:hover,
  .split-divider:focus-visible {
    background: var(--accent-blue);
    outline: none;
  }

  .terminal-leaf {
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: #0d1117;
    border: var(--chrome-border-width) solid var(--border-muted);
    border-top: 0;
  }

  .terminal-leaf.trim-right {
    border-right: 0;
  }

  .terminal-leaf.trim-left {
    border-left: 0;
  }

  .terminal-leaf.trim-bottom {
    border-bottom: 0;
  }

  .terminal-leaf.trim-top {
    border-top: 0;
  }

  .leaf-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 26px;
    flex-shrink: 0;
    border-bottom: var(--chrome-border-width) solid var(--border-muted);
    background: var(--bg-inset);
    cursor: grab;
  }

  .terminal-leaf.active .leaf-header {
    box-shadow: inset 0 var(--chrome-active-accent-width) 0 var(--accent-blue);
  }

  .leaf-title {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    height: 100%;
    padding: 0 8px;
    border: 0;
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-xs);
    font-weight: 650;
    cursor: grab;
  }

  .terminal-leaf.active .leaf-title {
    color: var(--text-primary);
  }

  .terminal-leaf-body {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .leaf-icon {
    display: inline-flex;
    color: var(--accent-blue);
    flex-shrink: 0;
  }

  .leaf-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 22ch;
  }

  .leaf-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--text-muted);
    flex-shrink: 0;
  }

  .leaf-dot.running {
    background: var(--accent-green);
  }

  .leaf-dot.starting {
    background: var(--accent-amber);
  }

  .leaf-actions {
    display: flex;
    align-items: center;
    padding-right: 4px;
  }

  .leaf-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 20px;
    height: 20px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }

  .leaf-action:hover,
  .leaf-action:focus-visible {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    outline: none;
  }

  .missing-session {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .split-preview {
    position: absolute;
    z-index: 4;
    inset: 0;
    border: var(--chrome-border-width) solid
      color-mix(in srgb, var(--accent-blue) 44%, transparent);
    opacity: 0;
    pointer-events: none;
    background: color-mix(in srgb, var(--accent-blue) 14%, transparent);
    -webkit-backdrop-filter: blur(3px) saturate(1.05);
    backdrop-filter: blur(3px) saturate(1.05);
    box-shadow: inset 0 0 0 var(--chrome-border-width)
      color-mix(in srgb, var(--accent-blue) 18%, transparent);
    transition:
      opacity 90ms ease,
      inset 90ms ease;
  }

  .terminal-leaf-body.show-drop-targets .split-preview.active {
    opacity: 1;
  }

  .split-preview.top {
    top: 0;
    right: 0;
    bottom: 50%;
    left: 0;
    border-width: 0 0 var(--chrome-active-accent-width);
    border-bottom-color: var(--accent-blue);
  }

  .split-preview.right {
    top: 0;
    right: 0;
    bottom: 0;
    left: 50%;
    border-width: 0 0 0 var(--chrome-active-accent-width);
    border-left-color: var(--accent-blue);
  }

  .split-preview.bottom {
    top: 50%;
    right: 0;
    bottom: 0;
    left: 0;
    border-width: var(--chrome-active-accent-width) 0 0;
    border-top-color: var(--accent-blue);
  }

  .split-preview.left {
    top: 0;
    right: 50%;
    bottom: 0;
    left: 0;
    border-width: 0 var(--chrome-active-accent-width) 0 0;
    border-right-color: var(--accent-blue);
  }
</style>
