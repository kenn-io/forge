<script lang="ts">
  import { onMount } from "svelte";
  import { navigate } from "../../stores/router.svelte.ts";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import GitBranchIcon from "@lucide/svelte/icons/git-branch";
  import ArrowUpIcon from "@lucide/svelte/icons/arrow-up";
  import ArrowDownIcon from "@lucide/svelte/icons/arrow-down";
  import { client } from "../../api/runtime.js";
  import { LeftSidebarToggle } from "@middleman/ui";
  import ProviderIcon from "../provider/ProviderIcon.svelte";

  interface Workspace {
    id: string;
    repo?: {
      provider: string;
      platform_host: string;
      owner: string;
      name: string;
      repo_path: string;
    };
    platform_host: string;
    repo_owner: string;
    repo_name: string;
    item_type: "pull_request" | "issue";
    item_number: number;
    git_head_ref: string;
    worktree_path: string;
    tmux_session: string;
    tmux_pane_title?: string | null;
    tmux_working?: boolean;
    tmux_activity_source?:
      | "title"
      | "output"
      | "none"
      | "unknown"
      | null;
    tmux_last_output_at?: string | null;
    status: string;
    error_message?: string | null;
    created_at: string;
    mr_title?: string | null;
    mr_state?: string | null;
    mr_is_draft?: boolean | null;
    mr_ci_status?: string | null;
    mr_review_decision?: string | null;
    mr_additions?: number | null;
    mr_deletions?: number | null;
    commits_ahead?: number | null;
    commits_behind?: number | null;
  }

  interface Props {
    selectedId: string;
    onOpenItemSidebar?: (workspaceId: string, tab: "pr" | "issue") => void;
    isSidebarToggleEnabled?: boolean;
    onCollapseSidebar?: (() => void) | undefined;
  }

  const {
    selectedId,
    onOpenItemSidebar,
    isSidebarToggleEnabled = false,
    onCollapseSidebar,
  }: Props = $props();

  const basePath = (
    window.__BASE_PATH__ ?? "/"
  ).replace(/\/$/, "");

  let workspaces = $state.raw<Workspace[]>([]);
  let collapsedGroups = $state<Set<string>>(new Set());

  type GroupedWorkspaces = Map<string, Workspace[]>;

  const grouped: GroupedWorkspaces = $derived.by(() => {
    const map = new Map<string, Workspace[]>();
    for (const ws of workspaces) {
      const key =
        `${ws.platform_host}/${ws.repo_owner}` +
        `/${ws.repo_name}`;
      const list = map.get(key);
      if (list) {
        list.push(ws);
      } else {
        map.set(key, [ws]);
      }
    }
    return map;
  });

  const showProviderIcons = $derived.by(() => {
    const providers = new Set<string>();
    for (const ws of workspaces) {
      const provider = workspaceProvider(ws);
      if (provider) providers.add(provider.toLowerCase());
    }
    return providers.size > 1;
  });

  async function fetchWorkspaces(): Promise<void> {
    try {
      const { data } = await client.GET("/workspaces");
      if (!data) return;
      workspaces = (data.workspaces ?? []) as Workspace[];
    } catch {
      // Network error; keep stale list.
    }
  }

  function toggleGroup(key: string): void {
    const next = new Set(collapsedGroups);
    if (next.has(key)) {
      next.delete(key);
    } else {
      next.add(key);
    }
    collapsedGroups = next;
  }

  function displayName(ws: Workspace): string {
    return ws.mr_title ?? ws.git_head_ref;
  }

  function statusDotClass(ws: Workspace): string {
    if (ws.status === "ready") return "status-dot ready";
    if (ws.status === "error") return "status-dot error";
    return "status-dot pending";
  }

  function workingTitle(ws: Workspace): string {
    const title = ws.tmux_pane_title?.trim();
    const source = ws.tmux_activity_source;
    if (source && source !== "unknown" && title) {
      return `Working (${source}): ${title}`;
    }
    if (source && source !== "unknown") {
      return `Working (${source})`;
    }
    return title || "Working";
  }

  function itemStateClass(ws: Workspace): string {
    if (ws.item_type === "issue") {
      return ws.mr_state === "closed" ? "closed" : "open";
    }
    if (ws.mr_is_draft) return "draft";
    if (ws.mr_state === "merged") return "merged";
    if (ws.mr_state === "closed") return "closed";
    return "open";
  }

  function shortBranch(ref: string): string {
    return ref.replace(/^refs\/heads\//, "");
  }

  function formatDiff(value: number): string {
    if (value < 1000) return String(value);
    if (value < 10_000) {
      return `${(value / 1000).toFixed(1)}k`;
    }
    return `${Math.round(value / 1000)}k`;
  }

  function shortRepo(repoKey: string): string {
    // platform/owner/name → owner/name (the platform host crowds
    // the rail and is rarely useful at a glance).
    const parts = repoKey.split("/");
    if (parts.length >= 3) {
      return parts.slice(-2).join("/");
    }
    return repoKey;
  }

  function workspaceProvider(ws: Workspace): string | undefined {
    return ws.repo?.provider;
  }

  function handleItemBubbleClick(
    e: MouseEvent | KeyboardEvent,
    ws: Workspace,
  ): void {
    e.stopPropagation();
    e.preventDefault();
    const tab = ws.item_type === "issue" ? "issue" : "pr";

    if (onOpenItemSidebar) {
      onOpenItemSidebar(ws.id, tab);
      return;
    }
    navigate(`/terminal/${ws.id}`);
  }

  onMount(() => {
    void fetchWorkspaces();
    const pollHandle = window.setInterval(() => {
      void fetchWorkspaces();
    }, 5_000);

    const evtUrl = `${basePath}/api/v1/events`;
    const source = new EventSource(evtUrl);
    source.addEventListener(
      "workspace_status",
      () => {
        void fetchWorkspaces();
      },
    );

    return () => {
      window.clearInterval(pollHandle);
      source.close();
    };
  });
</script>

<div class="workspace-list-sidebar">
  <div class="sidebar-header">
    <span class="sidebar-header-label">Workspaces</span>
    <span class="sidebar-header-count">{workspaces.length}</span>
    {#if isSidebarToggleEnabled && onCollapseSidebar}
      <LeftSidebarToggle
        state="expanded"
        label="Workspaces sidebar"
        onclick={onCollapseSidebar}
        class="left-sidebar-toggle--push left-sidebar-toggle--compact"
      />
    {/if}
  </div>
  <div class="sidebar-list">
    {#each [...grouped] as [repoKey, items] (repoKey)}
      {@const collapsed = collapsedGroups.has(repoKey)}
      <button
        class={["group-header", { collapsed }]}
        onclick={() => toggleGroup(repoKey)}
      >
        <ChevronDownIcon
          class="group-chevron"
          size="12"
          strokeWidth="2.25"
          aria-hidden="true"
        />
        {#if showProviderIcons && workspaceProvider(items[0]!)}
          <ProviderIcon
            provider={workspaceProvider(items[0]!)!}
            size={14}
            class="group-provider-icon"
          />
        {/if}
        <span class="group-label">{shortRepo(repoKey)}</span>
        <span class="group-count">{items.length}</span>
      </button>
      {#if !collapsed}
        {#each items as ws (ws.id)}
          {@const adds = ws.mr_additions}
          {@const dels = ws.mr_deletions}
          {@const showDiff =
            ws.item_type === "pull_request" &&
            ((adds ?? 0) > 0 || (dels ?? 0) > 0)}
          {@const ahead = ws.commits_ahead ?? 0}
          {@const behind = ws.commits_behind ?? 0}
          {@const showPush = ahead > 0 || behind > 0}
          <div
            class={["ws-row", { selected: ws.id === selectedId }]}
            onclick={(e) => {
              // The PR/issue bubble is a focusable child button; let
              // its own click handler run without the row also
              // navigating to the terminal route.
              if (e.target !== e.currentTarget &&
                e.target instanceof Element &&
                e.target.closest(".item-bubble")) {
                return;
              }
              navigate(`/terminal/${ws.id}`);
            }}
            onkeydown={(e) => {
              // Ignore keydowns that originate inside a nested
              // interactive element (e.g. the PR bubble button).
              // Without this guard, pressing Enter on the bubble
              // would navigate to the workspace before the bubble's
              // own click handler could open the sidebar tab.
              if (e.target !== e.currentTarget) return;
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                navigate(`/terminal/${ws.id}`);
              }
            }}
            tabindex="0"
            role="button"
          >
            <div class="ws-row-text">
              <div class="ws-row-title">
                <span
                  class={statusDotClass(ws)}
                  class:spinning={ws.status === "creating"}
                  aria-hidden="true"
                ></span>
                <span class="ws-name">{displayName(ws)}</span>
                {#if ws.tmux_working}
                  <span
                    class="working-pulse"
                    title={workingTitle(ws)}
                    aria-label={workingTitle(ws)}
                  ></span>
                {/if}
              </div>
              <div class="ws-row-meta">
                <span class="branch-chip" title={ws.git_head_ref}>
                  <GitBranchIcon
                    class="branch-icon"
                    size="10"
                    strokeWidth="2"
                    aria-hidden="true"
                  />
                  <span class="branch-name">
                    {shortBranch(ws.git_head_ref)}
                  </span>
                </span>
                {#if showPush}
                  <span
                    class="push-state"
                    title={`${ahead} ahead, ${behind} behind upstream`}
                  >
                    {#if ahead > 0}
                      <span class="push-ahead">
                        <ArrowUpIcon
                          size="9"
                          strokeWidth="2.5"
                          aria-hidden="true"
                        />{ahead}
                      </span>
                    {/if}
                    {#if behind > 0}
                      <span class="push-behind">
                        <ArrowDownIcon
                          size="9"
                          strokeWidth="2.5"
                          aria-hidden="true"
                        />{behind}
                      </span>
                    {/if}
                  </span>
                {/if}
                {#if showDiff}
                  <span class="diff-stats">
                    {#if adds != null}
                      <span class="add">+{formatDiff(adds)}</span>
                    {/if}
                    {#if dels != null}
                      <span class="del">−{formatDiff(dels)}</span>
                    {/if}
                  </span>
                {/if}
              </div>
            </div>
            <button
              class={["item-bubble", itemStateClass(ws)]}
              onclick={(e) => handleItemBubbleClick(e, ws)}
              onkeydown={(e) => {
                // Stop Enter/Space from bubbling to the row,
                // since the row's keyboard handler also navigates.
                if (e.key === "Enter" || e.key === " ") {
                  e.stopPropagation();
                }
              }}
              title={ws.item_type === "issue"
                ? `Open issue #${ws.item_number}`
                : `Open PR #${ws.item_number}`}
            >
              #{ws.item_number}
            </button>
          </div>
        {/each}
      {/if}
    {/each}
  </div>
</div>

<style>
  .workspace-list-sidebar {
    width: 100%;
    height: 100%;
    background: var(--bg-inset);
    display: flex;
    flex-direction: column;
    overflow: hidden;
    /* Establish a tighter type rhythm independent of the document
     * default, so the rail reads as a tool window rather than a
     * loosely-styled page section. */
    font-feature-settings: "tnum" 1, "calt" 1;
    /* Drive width-aware hiding (diff stats first, then push counts)
     * off the rail's own width rather than the viewport. The rail
     * is user-resizable, so a viewport media query would lie. */
    container-type: inline-size;
    container-name: workspace-rail;
  }

  .sidebar-header {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    padding: 0 4px 0 12px;
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
  }

  .sidebar-header-label {
    font-size: var(--font-size-xs);
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--text-muted);
  }

  .sidebar-header-count {
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    opacity: 0.7;
  }

  .sidebar-list {
    flex: 1;
    overflow-y: auto;
    padding: 2px 0 8px;
  }

  .sidebar-list::-webkit-scrollbar {
    width: 8px;
  }

  .sidebar-list::-webkit-scrollbar-thumb {
    background: var(--border-muted);
    border-radius: 4px;
    border: 2px solid var(--bg-inset);
  }

  .sidebar-list::-webkit-scrollbar-thumb:hover {
    background: var(--text-muted);
  }

  .group-header {
    display: flex;
    align-items: center;
    gap: 4px;
    width: 100%;
    padding: 4px 10px 4px 8px;
    margin-top: 6px;
    border: 0;
    background: transparent;
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-muted);
    text-align: left;
    cursor: pointer;
    letter-spacing: 0;
    transition: color 80ms ease;
  }

  .group-header:first-of-type {
    margin-top: 2px;
  }

  .group-header:hover {
    color: var(--text-secondary);
  }

  :global(.group-chevron) {
    color: var(--text-muted);
    flex-shrink: 0;
    transition: transform 100ms ease;
  }

  :global(.group-provider-icon) {
    color: var(--text-secondary);
  }

  .group-header.collapsed :global(.group-chevron) {
    transform: rotate(-90deg);
  }

  .group-label {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-secondary);
  }

  .group-count {
    flex-shrink: 0;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    opacity: 0.65;
    padding: 0 1px;
  }

  .ws-row {
    /* Two columns: a flex-shrinking text region on the left (which
     * holds two lines — title + meta) and a fixed-width bubble
     * pinned to the right. The bubble lives outside .ws-row-text,
     * so push counts or diff stats in the meta line can never
     * shift it left or off-screen — its X is anchored to the rail's
     * right edge for every row. */
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 4px 8px 5px 14px;
    border-left: 2px solid transparent;
    cursor: pointer;
    position: relative;
    outline: none;
  }

  .ws-row:hover {
    background: var(--bg-surface-hover);
  }

  .ws-row:focus-visible {
    background: var(--bg-surface-hover);
    box-shadow: inset 0 0 0 1px var(--accent-blue);
  }

  .ws-row.selected {
    background: var(--bg-surface);
    border-left-color: var(--accent-blue);
  }

  .ws-row.selected:hover {
    background: color-mix(in srgb, var(--accent-blue) 8%, var(--bg-surface));
  }

  .ws-row-text {
    /* Stacks the title and meta lines inside the left column. Has
     * to set min-width:0 so its own content can shrink rather than
     * pushing the bubble off-screen. */
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .ws-row-title {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .ws-row-meta {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .status-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .status-dot.ready {
    background: var(--accent-green);
  }

  .status-dot.error {
    background: var(--accent-red);
  }

  .status-dot.pending {
    background: var(--accent-amber);
  }

  .status-dot.spinning {
    animation: pulse 1.2s ease-in-out infinite;
  }

  @keyframes pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.3;
    }
  }

  .ws-name {
    flex: 1;
    min-width: 0;
    font-size: var(--font-size-md);
    font-weight: 500;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    letter-spacing: 0.005em;
    line-height: 1.35;
  }

  .ws-row.selected .ws-name {
    font-weight: 600;
  }

  .working-pulse {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent-amber);
    box-shadow: 0 0 6px color-mix(in srgb, var(--accent-amber) 70%, transparent);
    animation: workingBlink 1.4s ease-in-out infinite;
    flex-shrink: 0;
  }

  @keyframes workingBlink {
    0%,
    100% {
      opacity: 1;
      transform: scale(1);
    }
    50% {
      opacity: 0.45;
      transform: scale(0.8);
    }
  }

  .branch-chip {
    /* Lives on the meta line; takes whatever width is left after
     * push state and diff stats and truncates with ellipsis. */
    display: inline-flex;
    align-items: center;
    gap: 3px;
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-weight: 500;
    color: var(--text-secondary);
    letter-spacing: 0;
    /* Tabular numerals + slightly tighter tracking turn the branch
     * line into a JetBrains-style "ref chip" rather than soft prose. */
    font-variant-numeric: tabular-nums;
  }

  .branch-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  :global(.branch-icon) {
    color: var(--text-muted);
    flex-shrink: 0;
    margin-right: 1px;
  }

  .item-bubble {
    /* GitHub-style state pill: a soft solid pastel fill with a
     * near-black foreground for legibility. The bg is mostly the
     * accent color but blended toward white so the swatch reads as
     * "soft solid"; the fg is the same accent darkened toward black
     * so the number always has high contrast against the bg. The
     * literal white/black anchors keep the look identical across
     * light and dark themes (matching GitHub label semantics).
     * Sits in its own flex column with align-self:flex-start so
     * it pins to the row's top edge regardless of the meta line's
     * height. */
    flex-shrink: 0;
    align-self: flex-start;
    margin-top: 1px;
    height: 16px;
    padding: 0 6px;
    border: 1px solid transparent;
    border-radius: 8px;
    background: var(--bubble-bg);
    color: var(--bubble-fg);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    font-weight: 700;
    line-height: 1;
    letter-spacing: 0.01em;
    cursor: pointer;
    transition: background-color 80ms ease, border-color 80ms ease,
      color 80ms ease;
  }

  .item-bubble.open {
    --bubble-bg: color-mix(in srgb, var(--accent-green) 70%, #ffffff);
    --bubble-fg: color-mix(in srgb, var(--accent-green) 25%, #0a0d14);
  }

  .item-bubble.merged {
    --bubble-bg: color-mix(in srgb, var(--accent-purple) 70%, #ffffff);
    --bubble-fg: color-mix(in srgb, var(--accent-purple) 25%, #0a0d14);
  }

  .item-bubble.closed {
    --bubble-bg: color-mix(in srgb, var(--accent-red) 70%, #ffffff);
    --bubble-fg: color-mix(in srgb, var(--accent-red) 25%, #0a0d14);
  }

  .item-bubble.draft {
    --bubble-bg: color-mix(in srgb, var(--text-muted) 55%, #ffffff);
    --bubble-fg: #0a0d14;
  }

  .item-bubble:hover {
    border-color: color-mix(in srgb, var(--bubble-fg) 50%, transparent);
  }

  .item-bubble:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 1px;
  }

  .push-state {
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    font-variant-numeric: tabular-nums;
    color: var(--text-secondary);
  }

  .push-ahead,
  .push-behind {
    display: inline-flex;
    align-items: center;
    gap: 1px;
  }

  .push-ahead {
    color: var(--accent-green);
  }

  .push-behind {
    color: var(--accent-amber);
  }

  .diff-stats {
    flex-shrink: 0;
    display: inline-flex;
    gap: 4px;
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    font-variant-numeric: tabular-nums;
    color: var(--text-muted);
  }

  .diff-stats .add {
    color: var(--accent-green);
  }

  .diff-stats .del {
    color: var(--accent-red);
  }

  /* Width-aware hiding: shed least-critical chrome first as the
   * rail narrows. Push state outranks diff stats because branch
   * hygiene matters more for "should I open this workspace?" than
   * line counts. */
  @container workspace-rail (max-width: 260px) {
    .diff-stats {
      display: none;
    }
  }

  @container workspace-rail (max-width: 220px) {
    .push-state {
      display: none;
    }
  }
</style>
