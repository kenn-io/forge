<script lang="ts">
  import { CopyButton, IconButton } from "@kenn-io/kit-ui";
  import InfoIcon from "@lucide/svelte/icons/info";
  import { untrack } from "svelte";
  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";
  import { mountTerminalPopover } from "./terminal-popover.js";
  import type { WorkspaceDetail } from "./workspace-detail.js";

  const { workspace, hostVisible = true }: { workspace: WorkspaceDetail; hostVisible?: boolean } = $props();
  let open = $state(false);
  let anchor = $state<HTMLDivElement>();
  let panel = $state<HTMLDivElement>();

  $effect(() => {
    if (!hostVisible) open = false;
  });

  $effect(() => {
    if (!open) return;
    return untrack(() => pushModalFrame("workspace-info", []));
  });

  function dismiss(event: PointerEvent): void {
    if (open && event.target instanceof Node && !anchor?.contains(event.target) && !panel?.contains(event.target)) {
      open = false;
    }
  }

  function onKeydown(event: KeyboardEvent): void {
    if (!open || event.key !== "Escape") return;
    event.preventDefault();
    event.stopPropagation();
    open = false;
    anchor?.querySelector("button")?.focus();
  }
</script>

<svelte:window onpointerdowncapture={dismiss} onkeydowncapture={onKeydown} />

<div bind:this={anchor} class="workspace-info">
  <IconButton
    size="sm"
    ariaLabel="Workspace info"
    title="Workspace info"
    ariaHaspopup="dialog"
    ariaExpanded={open}
    onclick={() => { open = !open; }}
  >
    <InfoIcon size="13" strokeWidth="2" aria-hidden="true" />
  </IconButton>
  {#if open}
    <div
      bind:this={panel}
      class="workspace-info-popover kit-popover-card"
      role="dialog"
      aria-label="Workspace info"
      tabindex="-1"
      {@attach (node) => {
        const cleanup = mountTerminalPopover(node, anchor!);
        node.focus();
        return cleanup;
      }}
    >
      <strong>{workspace.mr_title || workspace.git_head_ref}</strong>
      <dl>
        <dt>Repository</dt>
        <dd>{workspace.repo_owner}/{workspace.repo_name}{#if workspace.item_number > 0}{` #${workspace.item_number}`}{/if}</dd>
        <dt>Branch</dt>
        <dd><code>{workspace.git_head_ref}</code></dd>
        <dt>Worktree</dt>
        <dd class="worktree-path">
          <code>{workspace.worktree_path}</code>
          <CopyButton text={workspace.worktree_path} ariaLabel="Copy worktree path" title="Copy worktree path" />
        </dd>
      </dl>
    </div>
  {/if}
</div>

<style>
  .workspace-info {
    display: inline-flex;
  }

  .workspace-info-popover {
    position: fixed;
    z-index: var(--z-popover);
    box-sizing: border-box;
    width: min(400px, calc(100vw - 24px));
    max-height: calc(100dvh - 24px);
    overflow: auto;
    overflow-wrap: anywhere;
    padding: var(--space-5);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  dl {
    display: grid;
    gap: var(--space-2);
    margin: var(--space-4) 0 0;
  }

  dt {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  dd {
    margin: 0 0 var(--space-3);
  }

  .worktree-path {
    display: flex;
    align-items: flex-start;
    gap: var(--space-3);
    margin-bottom: 0;
  }

  code {
    min-width: 0;
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }
</style>
