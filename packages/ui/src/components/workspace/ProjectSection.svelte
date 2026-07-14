<script lang="ts">
  import { StatusDot } from "@kenn-io/kit-ui";
  import type { WorkspaceProject } from "../../api/types.js";
  import WorktreeRow from "./WorktreeRow.svelte";

  interface Props {
    project: WorkspaceProject;
    hostKey: string;
    selectedWorktreeKey: string | null;
    hoverCardsEnabled?: boolean;
    onCommand: (
      command: string,
      payload: Record<string, unknown>,
    ) => void;
  }

  let {
    project,
    hostKey,
    selectedWorktreeKey,
    hoverCardsEnabled = false,
    onCommand,
  }: Props = $props();

  const storageKey = $derived(
    `middleman-project-${project.key}-collapsed`,
  );

  let collapsed = $state(false);
  let showHidden = $state(false);
  let showMenu = $state(false);
  let menuX = $state(0);
  let menuY = $state(0);

  // Hydrate collapse state from localStorage on mount
  $effect(() => {
    const key = storageKey;
    try {
      collapsed = localStorage.getItem(key) === "true";
    } catch {
      // localStorage unavailable
    }
  });

  // Persist collapse state
  function toggleCollapsed(): void {
    collapsed = !collapsed;
    try {
      localStorage.setItem(storageKey, String(collapsed));
    } catch {
      // localStorage unavailable
    }
  }

  const visibleWorktrees = $derived(
    project.worktrees.filter((w) => !w.isHidden),
  );
  const hiddenWorktrees = $derived(
    project.worktrees.filter((w) => w.isHidden),
  );
  const hasStaleWorktrees = $derived(
    project.worktrees.some((w) => w.isStale),
  );

  function safeHref(url: string): string | null {
    try {
      const parsed = new URL(url);
      if (parsed.protocol === "https:" || parsed.protocol === "http:") {
        return parsed.href;
      }
    } catch {
      // malformed URL
    }
    return null;
  }

  function handleHeaderContext(e: MouseEvent): void {
    e.preventDefault();
    e.stopPropagation();
    menuX = e.clientX;
    menuY = e.clientY;
    showMenu = true;
  }

  function closeMenu(): void {
    showMenu = false;
  }

  function menuAction(
    command: string,
    payload: Record<string, unknown> = {},
  ): void {
    onCommand(command, {
      hostKey,
      projectKey: project.key,
      ...payload,
    });
    closeMenu();
  }
</script>

<svelte:window onclick={showMenu ? closeMenu : undefined} oncontextmenu={showMenu ? closeMenu : undefined} />

<section class="project-section">
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="project-header" oncontextmenu={handleHeaderContext}>
    <button class="collapse-toggle" onclick={toggleCollapsed}>
      <span class="chevron">{collapsed ? "▶" : "▼"}</span>
      <span class="project-name">{project.name}</span>
      <span class="worktree-count">
        {project.worktrees.length} worktree{project.worktrees.length !== 1 ? "s" : ""}
      </span>
      {#if hasStaleWorktrees}
        <StatusDot status="stale" label="Has stale worktrees" />
      {/if}
      {#if collapsed && hiddenWorktrees.length > 0}
        <span class="hidden-count">
          +{hiddenWorktrees.length} hidden
        </span>
      {/if}
    </button>
    {#if project.platformURL && safeHref(project.platformURL)}
      <a
        href={safeHref(project.platformURL)}
        target="_blank"
        rel="noopener"
        class="repo-link"
      >{project.platformRepo}</a>
    {:else if project.platformRepo}
      <span class="platform-repo">{project.platformRepo}</span>
    {/if}
  </div>

  {#if !collapsed}
    <div class="worktree-list">
      {#each visibleWorktrees as worktree (worktree.key)}
        <WorktreeRow
          {worktree}
          {hostKey}
          projectKey={project.key}
          isSelected={worktree.key === selectedWorktreeKey}
          {hoverCardsEnabled}
          {onCommand}
        />
      {/each}

      {#if hiddenWorktrees.length > 0}
        <button
          class="hidden-toggle"
          onclick={() => (showHidden = !showHidden)}
        >
          {showHidden
            ? "Hide"
            : `Show ${hiddenWorktrees.length} hidden`}
        </button>
      {/if}

      {#if showHidden}
        {#each hiddenWorktrees as worktree (worktree.key)}
          <WorktreeRow
            {worktree}
            {hostKey}
            projectKey={project.key}
            isSelected={worktree.key === selectedWorktreeKey}
            {onCommand}
          />
        {/each}
      {/if}
    </div>
  {/if}
</section>

{#if showMenu}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="context-menu kit-popover-card"
    style="left: {menuX}px; top: {menuY}px"
    oncontextmenu={(e) => e.preventDefault()}
  >
    <button
      class="menu-item menu-item--danger"
      onclick={() => menuAction("removeProject")}
    >
      Remove project
    </button>
    {#if hasStaleWorktrees}
      <button
        class="menu-item menu-item--danger"
        onclick={() => menuAction("removeAllStaleWorktrees")}
      >
        Remove all stale worktrees
      </button>
    {/if}
  </div>
{/if}

<style>
  .project-section {
    display: flex;
    flex-direction: column;
  }

  .project-header {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    height: 32px;
    padding: 0 10px;
    background: var(--bg-surface);
    transition: background 0.1s;
  }

  .project-header:hover {
    background: var(--bg-surface-hover);
  }

  .collapse-toggle {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: 1;
    min-width: 0;
    background: none;
    border: none;
    padding: 0;
    text-align: left;
    cursor: pointer;
  }

  .chevron {
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    flex-shrink: 0;
    width: 12px;
  }

  .project-name {
    font-size: var(--font-size-md);
    font-weight: 700;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .platform-repo {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .worktree-count {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .repo-link {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-decoration: none;
  }

  .repo-link:hover {
    text-decoration: underline;
  }

  .hidden-count {
    margin-left: auto;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    white-space: nowrap;
    flex-shrink: 0;
  }

  .worktree-list {
    display: flex;
    flex-direction: column;
    padding-left: 8px;
  }

  .hidden-toggle {
    display: block;
    width: 100%;
    padding: 4px 12px;
    text-align: left;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    background: none;
    border: none;
    cursor: pointer;
  }

  .hidden-toggle:hover {
    color: var(--text-secondary);
  }

  .context-menu {
    position: fixed;
    z-index: var(--z-popover);
    min-width: 180px;
    padding: 4px 0;
  }

  .menu-item {
    display: block;
    width: 100%;
    padding: 6px 12px;
    text-align: left;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    background: none;
    border: none;
    cursor: pointer;
  }

  .menu-item:hover {
    background: var(--bg-surface-hover);
  }

  .menu-item--danger {
    color: var(--accent-red);
  }
</style>
