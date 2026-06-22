<script lang="ts">
  import type {
    LaunchTarget,
    RuntimeSession,
  } from "@middleman/ui/api/types";
  import GitBranchIcon from "@lucide/svelte/icons/git-branch";
  import FolderIcon from "@lucide/svelte/icons/folder";
  import PlayIcon from "@lucide/svelte/icons/play";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import SparklesIcon from "@lucide/svelte/icons/sparkles";
  import BoxIcon from "@lucide/svelte/icons/box";
  import { isVisibleLaunchTarget } from "./launchTargets";

  interface WorkspaceHomeWorkspace {
    id: string;
    repo_owner: string;
    repo_name: string;
    item_number: number;
    git_head_ref: string;
    worktree_path: string;
    mr_title?: string | null;
  }

  interface WorkspaceHomeProps {
    workspace?: WorkspaceHomeWorkspace;
    launchTargets: LaunchTarget[];
    sessions: RuntimeSession[];
    displayLabels?: Record<string, string>;
    launchingKey?: string | null;
    readonly?: boolean;
    showHeader?: boolean;
    onLaunch?: (targetKey: string) => void;
    onOpenSession?: (sessionKey: string) => void;
  }

  const {
    workspace,
    launchTargets,
    sessions,
    displayLabels = {},
    launchingKey = null,
    readonly = false,
    showHeader = true,
    onLaunch,
    onOpenSession,
  }: WorkspaceHomeProps = $props();

  const visibleTargets = $derived(launchTargets.filter(isVisibleLaunchTarget));

  function title(): string {
    return workspace?.mr_title ?? workspace?.git_head_ref ?? "Workspace";
  }

  function sourceLabel(target: LaunchTarget): string {
    if (target.source === "config") return "configured";
    if (target.kind === "plain_shell") return "shell";
    return "detected";
  }

  function targetLabel(target: LaunchTarget): string {
    return target.kind === "plain_shell" ? "Shell" : target.label;
  }

  function statusLabel(status: string): string {
    if (status === "running") return "Running";
    if (status === "starting") return "Starting";
    if (status === "exited") return "Exited";
    return status;
  }

  function labelFor(session: RuntimeSession): string {
    return displayLabels[session.key] ?? session.label;
  }
</script>

<section class={["workspace-home", readonly && "readonly"]} aria-label="Worktree Home">
  {#if showHeader && workspace}
    <header class="home-header">
      <h2 class="home-title">{title()}</h2>
      <div class="home-meta">
        <span class="meta-chip">
          {workspace.repo_owner}/{workspace.repo_name}
          <span class="meta-chip-num">#{workspace.item_number}</span>
        </span>
        <span class="meta-chip mono">
          <GitBranchIcon size="11" strokeWidth="2" aria-hidden="true" />
          {workspace.git_head_ref}
        </span>
        <span class="meta-chip mono path" title={workspace.worktree_path}>
          <FolderIcon size="11" strokeWidth="2" aria-hidden="true" />
          {workspace.worktree_path}
        </span>
      </div>
    </header>
  {/if}

  <div class="home-section">
    <div class="section-bar">
      <PlayIcon
        class="section-icon"
        size="12"
        strokeWidth="2.25"
        aria-hidden="true"
      />
      <span class="section-title">Launch</span>
      <span class="section-count">{visibleTargets.length}</span>
    </div>
    <div class="launch-grid">
      {#each visibleTargets as target (target.key)}
        {@const isShell = target.kind === "plain_shell"}
        {@const isAgent = target.kind === "agent"}
        {@const isLaunching = launchingKey === target.key}
        <button
          class="launch-card"
          disabled={readonly || !target.available || isLaunching}
          title={target.disabled_reason ?? targetLabel(target)}
          aria-label={targetLabel(target)}
          onclick={() => onLaunch?.(target.key)}
        >
          <span class="card-icon" aria-hidden="true">
            {#if isShell}
              <TerminalIcon size="14" strokeWidth="2" />
            {:else if isAgent}
              <SparklesIcon size="14" strokeWidth="2" />
            {:else}
              <BoxIcon size="14" strokeWidth="2" />
            {/if}
          </span>
          <span class="card-label">{targetLabel(target)}</span>
          <span class="card-source">{sourceLabel(target)}</span>
          {#if isLaunching}
            <span class="card-status">starting…</span>
          {/if}
        </button>
      {/each}
    </div>
  </div>

  {#if sessions.length > 0}
    <div class="home-section">
      <div class="section-bar">
        <span class="section-title">Active sessions</span>
        <span class="section-count">{sessions.length}</span>
      </div>
      <div class="session-list">
        {#each sessions as session (session.key)}
          <button
            class="session-row"
            disabled={readonly}
            onclick={() => onOpenSession?.(session.key)}
          >
            <span
              class={["session-dot", session.status]}
              aria-hidden="true"
            ></span>
            <span class="session-label">{labelFor(session)}</span>
            <span class="session-status">{statusLabel(session.status)}</span>
          </button>
        {/each}
      </div>
    </div>
  {/if}
</section>

<style>
  .workspace-home {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 14px 16px 18px;
    min-width: 0;
    height: 100%;
    overflow: auto;
    background: var(--bg-primary);
    color: var(--text-primary);
  }

  .workspace-home.readonly {
    pointer-events: none;
  }

  .home-header {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding-bottom: 12px;
    border-bottom: 1px solid var(--border-muted);
  }

  .home-title {
    margin: 0;
    font-size: var(--font-size-lg);
    line-height: 1.3;
    font-weight: 600;
    color: var(--text-primary);
    letter-spacing: -0.005em;
  }

  .home-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    font-size: var(--font-size-xs);
  }

  .meta-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 20px;
    padding: 0 7px;
    border: 1px solid var(--border-muted);
    border-radius: 3px;
    background: var(--bg-surface);
    color: var(--text-secondary);
    line-height: 1;
    font-weight: 500;
    white-space: nowrap;
  }

  .meta-chip.mono {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    background: var(--bg-inset);
    color: var(--text-secondary);
  }

  .meta-chip.path {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .meta-chip-num {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-weight: 500;
  }

  .home-section {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .section-bar {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 18px;
    color: var(--text-muted);
  }

  :global(.section-icon) {
    color: var(--accent-green);
  }

  .workspace-home.readonly :global(.section-icon) {
    color: var(--text-muted);
  }

  .section-title {
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }

  .section-count {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 16px;
    height: 14px;
    padding: 0 4px;
    border-radius: 3px;
    background: var(--bg-inset);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    font-family: var(--font-mono);
  }

  .launch-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 4px;
  }

  .launch-card {
    display: grid;
    grid-template-columns: 16px 1fr auto;
    align-items: center;
    gap: 4px 8px;
    /* min-height instead of fixed height: when a card flips into the
     * launching state it adds .card-status as a second grid row; a
     * fixed height clipped that row, leaving "starting…" overlapping
     * the label. */
    min-height: 32px;
    padding: 5px 10px;
    border: 1px solid var(--border-muted);
    border-radius: 3px;
    background: var(--bg-surface);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
    transition: border-color 80ms ease, background-color 80ms ease,
      color 80ms ease;
  }

  .launch-card:hover:not(:disabled) {
    border-color: var(--accent-blue);
    background: color-mix(
      in srgb,
      var(--accent-blue) 6%,
      var(--bg-surface)
    );
  }

  .launch-card:hover:not(:disabled) .card-icon {
    color: var(--accent-blue);
  }

  .launch-card:disabled {
    cursor: not-allowed;
    color: var(--text-muted);
    opacity: 0.6;
  }

  .card-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--text-secondary);
    transition: color 80ms ease;
  }

  .card-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 600;
    letter-spacing: 0.005em;
  }

  .card-source {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-family: var(--font-mono);
    letter-spacing: 0;
  }

  .card-status {
    grid-column: 1 / -1;
    margin-top: -2px;
    color: var(--accent-amber);
    font-size: var(--font-size-xs);
    font-family: var(--font-mono);
  }

  .session-list {
    display: flex;
    flex-direction: column;
  }

  .session-row {
    display: grid;
    grid-template-columns: 8px 1fr auto;
    align-items: center;
    gap: 10px;
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border-muted);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
    transition: background-color 80ms ease, color 80ms ease;
  }

  .session-row + .session-row {
    border-top: 0;
  }

  .session-row:first-child {
    border-radius: 3px 3px 0 0;
  }

  .session-row:last-child {
    border-radius: 0 0 3px 3px;
  }

  .session-row:only-child {
    border-radius: 3px;
  }

  .session-row:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .session-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--accent-green);
    flex-shrink: 0;
    box-shadow: 0 0 0 2px
      color-mix(in srgb, var(--accent-green) 25%, transparent);
  }

  .session-dot.starting {
    background: var(--accent-amber);
    box-shadow: 0 0 0 2px
      color-mix(in srgb, var(--accent-amber) 25%, transparent);
    animation: pulse 1.4s ease-in-out infinite;
  }

  .session-dot.exited {
    background: var(--text-muted);
    box-shadow: none;
  }

  .session-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
  }

  .session-status {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-family: var(--font-mono);
    letter-spacing: 0;
  }

  @keyframes pulse {
    0%, 100% { opacity: 0.55; }
    50% { opacity: 1; }
  }
</style>
