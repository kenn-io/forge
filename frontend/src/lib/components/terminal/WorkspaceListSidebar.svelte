<script lang="ts">
  import { onMount, tick } from "svelte";
  import { navigate } from "../../stores/router.svelte.ts";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import GitBranchIcon from "@lucide/svelte/icons/git-branch";
  import ArrowUpIcon from "@lucide/svelte/icons/arrow-up";
  import ArrowDownIcon from "@lucide/svelte/icons/arrow-down";
  import SearchIcon from "@lucide/svelte/icons/search";
  import { apiErrorMessage, client } from "../../api/runtime.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import type { components } from "@middleman/ui/api/schema";
  import {
    DiffStats,
    FilterDropdown,
    LeftSidebarToggle,
  } from "@middleman/ui";
  import {
    createRepoLabelFormatter,
    repoIdentityKey,
    type RepoLabelIdentity,
  } from "@middleman/ui/utils/repo-label";
  import ProviderIcon from "../provider/ProviderIcon.svelte";
  import ConfirmDialog from "../shared/ConfirmDialog.svelte";
  import {
    defaultWorkspaceListDisplayOptions,
    defaultWorkspaceListSort,
    loadWorkspaceListDisplayOptions,
    loadWorkspaceListSort,
    saveWorkspaceListDisplayOptions,
    saveWorkspaceListSort,
    workspaceListSortOptions,
    type WorkspaceListDisplayOptions,
    type WorkspaceListSort,
  } from "./workspaceListSort.ts";

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
    item_last_activity_at?: string | null;
    mr_title?: string | null;
    mr_state?: string | null;
    mr_is_draft?: boolean | null;
    mr_ci_status?: string | null;
    mr_review_decision?: string | null;
    mr_additions?: number | null;
    mr_deletions?: number | null;
    commits_ahead?: number | null;
    commits_behind?: number | null;
    fleet_host_key?: string;
    fleet_host_name?: string;
  }

  type HostSummary = components["schemas"]["HostSummary"];

  interface Props {
    selectedId: string;
    selectedHostKey?: string | undefined;
    onOpenItemSidebar?: (
      workspaceId: string,
      tab: "pr" | "issue",
      hostKey?: string,
    ) => void;
    onWorkspaceListStateChange?: (
      state: { status: "loading" | "retrying" | "loaded"; total: number },
    ) => void;
    isWorkspaceActionDisabled?: (
      workspaceId: string,
      hostKey?: string,
    ) => boolean;
    onWorkspaceDeletePendingChange?: (
      workspaceId: string,
      hostKey: string | undefined,
      pending: boolean,
    ) => void;
    isSidebarToggleEnabled?: boolean;
    onCollapseSidebar?: (() => void) | undefined;
  }

  const {
    selectedId,
    selectedHostKey = undefined,
    onOpenItemSidebar,
    onWorkspaceListStateChange,
    isWorkspaceActionDisabled,
    onWorkspaceDeletePendingChange,
    isSidebarToggleEnabled = false,
    onCollapseSidebar,
  }: Props = $props();

  const basePath = (
    window.__BASE_PATH__ ?? "/"
  ).replace(/\/$/, "");

  let workspaces = $state.raw<Workspace[]>([]);
  let fleetHosts = $state.raw<HostSummary[]>([]);
  let fleetError = $state<string | null>(null);
  let fleetLoaded = $state(false);
  let collapsedGroups = $state<string[]>([]);
  let searchQuery = $state("");
  let sortMode = $state<WorkspaceListSort>(loadWorkspaceListSort());
  let workspaceListStatus = $state<"loading" | "retrying" | "loaded">("loading");
  let fetchInFlight = false;
  let contextMenu = $state<{
    ws: Workspace;
    x: number;
    y: number;
  } | null>(null);
  let deleteConfirmWorkspace = $state<Workspace | null>(null);
  let workspaceAction = $state<{
    workspaceKey: string;
    action: "push" | "pull" | "reveal" | "delete";
  } | null>(null);
  let contextMenuEl = $state<HTMLDivElement | null>(null);
  let contextMenuStyle = $state("");

  const workspaceListLoadTimeoutMs = 10_000;
  let displayOptions = $state<WorkspaceListDisplayOptions>(
    loadWorkspaceListDisplayOptions(),
  );

  type WorkspaceGroup = {
    key: string;
    items: Workspace[];
  };

  const normalizedSearchQuery = $derived(
    searchQuery.trim().toLowerCase(),
  );
  const deleteConfirmBusy = $derived(
    deleteConfirmWorkspace !== null &&
      workspaceActionMatches(deleteConfirmWorkspace, "delete"),
  );

  const visibleWorkspaces = $derived.by(() => {
    if (!normalizedSearchQuery) return workspaces;
    return workspaces.filter((ws) =>
      workspaceMatchesSearch(ws, normalizedSearchQuery),
    );
  });

  const sidebarCountLabel = $derived(
    normalizedSearchQuery
      ? `${visibleWorkspaces.length}/${workspaces.length}`
      : `${workspaces.length}`,
  );

  const grouped = $derived.by<WorkspaceGroup[]>(() => {
    const groups: WorkspaceGroup[] = [];
    for (const ws of visibleWorkspaces) {
      const key = repoIdentityKey(workspaceRepoIdentity(ws));
      const group = groups.find((candidate) => candidate.key === key);
      if (group) {
        group.items.push(ws);
      } else {
        groups.push({ key, items: [ws] });
      }
    }
    return groups;
  });

  const sortLabel = $derived(
    workspaceListSortOptions.find(
      (option) => option.value === sortMode,
    )?.label ?? "Org / repo",
  );

  const viewBadgeCount = $derived(
    Number(sortMode !== defaultWorkspaceListSort) +
      Number(
        displayOptions.showOrgNames !==
          defaultWorkspaceListDisplayOptions.showOrgNames,
      ) +
      Number(
        displayOptions.showDiffStats !==
          defaultWorkspaceListDisplayOptions.showDiffStats,
      ),
  );

  const viewSections = $derived.by(() => [
    {
      title: "Sorting",
      items: workspaceListSortOptions.map((option) => ({
        id: option.value,
        label: option.label,
        description: option.description,
        active: sortMode === option.value,
        closeOnSelect: true,
        onSelect: () => setSort(option.value),
      })),
    },
    {
      title: "Visibility",
      items: [
        {
          id: "show-org-names",
          label: "Show org names",
          description: "Include owner or organization names in workspace repo labels.",
          active: displayOptions.showOrgNames,
          onSelect: () =>
            setDisplayOption(
              "showOrgNames",
              !displayOptions.showOrgNames,
            ),
        },
        {
          id: "show-diff-stats",
          label: "Show PR diff stats",
          description: "Show additions and deletions for linked pull request workspaces.",
          active: displayOptions.showDiffStats,
          onSelect: () =>
            setDisplayOption(
              "showDiffStats",
              !displayOptions.showDiffStats,
            ),
        },
      ],
    },
  ]);

  const reachableFleetHosts = $derived(
    fleetHosts.filter((host) => host.reachable).length,
  );

  const hasRemoteFleetHosts = $derived(
    hasNonSelfFleetHost(fleetHosts),
  );

  const showFleetStatus = $derived(
    fleetError !== null || (fleetLoaded && hasRemoteFleetHosts),
  );

  // Flat ordering for timestamp sorts. The org/repo mode keeps
  // the API order (created_at DESC) inside each repo group.
  // "Activity" means terminal output only (tmux_last_output_at).
  // "Item activity" means the synced PR/issue last_activity_at.
  // Missing timestamps fall back to workspace creation time.
  const sortedFlat = $derived.by(() => {
    const stamp = sortMode === "activity"
      ? (ws: Workspace) =>
          timeValue(ws.tmux_last_output_at) || timeValue(ws.created_at)
      : sortMode === "item-activity"
        ? (ws: Workspace) =>
            timeValue(ws.item_last_activity_at) ||
            timeValue(ws.created_at)
        : (ws: Workspace) => timeValue(ws.created_at);
    return [...visibleWorkspaces].sort(
      (a, b) => stamp(b) - stamp(a) || a.id.localeCompare(b.id),
    );
  });

  function setSort(sort: WorkspaceListSort): void {
    sortMode = sort;
    saveWorkspaceListSort(sort);
  }

  function setDisplayOption(
    key: keyof WorkspaceListDisplayOptions,
    value: boolean,
  ): void {
    displayOptions = { ...displayOptions, [key]: value };
    saveWorkspaceListDisplayOptions(displayOptions);
  }

  $effect(() => {
    if (!contextMenu) return;

    function closeForOutsideClick(event: MouseEvent): void {
      if (
        contextMenuEl &&
        event.target instanceof Node &&
        contextMenuEl.contains(event.target)
      ) {
        return;
      }
      closeContextMenu();
    }

    function closeForEscape(event: KeyboardEvent): void {
      if (event.key === "Escape") closeContextMenu();
    }

    function reposition(): void {
      positionContextMenu();
    }

    document.addEventListener("mousedown", closeForOutsideClick);
    document.addEventListener("keydown", closeForEscape);
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", closeContextMenu, true);
    return () => {
      document.removeEventListener("mousedown", closeForOutsideClick);
      document.removeEventListener("keydown", closeForEscape);
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", closeContextMenu, true);
    };
  });

  $effect(() => {
    onWorkspaceListStateChange?.({
      status: workspaceListStatus,
      total: workspaces.length,
    });
  });

  function timeValue(value: string | null | undefined): number {
    if (!value) return 0;
    const ms = Date.parse(value);
    return Number.isNaN(ms) ? 0 : ms;
  }

  function hasNonSelfFleetHost(hosts: HostSummary[]): boolean {
    return hosts.some((host) => host.kind !== "self");
  }

  function localWorkspacesOnly(items: Workspace[]): Workspace[] {
    return items.filter((ws) => !ws.fleet_host_key);
  }

  const repoLabelFormatter = $derived.by(() =>
    createRepoLabelFormatter(
      workspaces.map(workspaceRepoIdentity),
      { showOrgNames: displayOptions.showOrgNames },
    ),
  );

  const showProviderIcons = $derived.by(() => {
    const providers: string[] = [];
    for (const ws of workspaces) {
      const provider = workspaceProvider(ws);
      const normalized = provider?.toLowerCase();
      if (normalized && !providers.includes(normalized)) {
        providers.push(normalized);
      }
    }
    return providers.length > 1;
  });

  async function fetchWorkspaces(): Promise<void> {
    if (fetchInFlight) return;
    fetchInFlight = true;
    const abortController = new AbortController();
    let timeoutHandle: number | undefined;
    try {
      timeoutHandle = window.setTimeout(() => {
        abortController.abort();
      }, workspaceListLoadTimeoutMs);
      const { data } = await client.GET("/workspaces", {
        signal: abortController.signal,
      });
      if (!data) {
        if (workspaces.length === 0) workspaceListStatus = "retrying";
        return;
      }
      const local = (data.workspaces ?? []) as Workspace[];
      const remote = fleetLoaded && !fleetError && hasRemoteFleetHosts
        ? await fetchPeerWorkspaces(abortController.signal)
        : [];
      workspaces = [
        ...local,
        ...(hasRemoteFleetHosts ? remote : []),
      ];
      workspaceListStatus = "loaded";
    } catch {
      // Network error; keep stale list.
      if (workspaces.length === 0) workspaceListStatus = "retrying";
    } finally {
      if (timeoutHandle !== undefined) {
        window.clearTimeout(timeoutHandle);
      }
      fetchInFlight = false;
    }
  }

  async function fetchPeerWorkspaces(
    signal: AbortSignal,
  ): Promise<Workspace[]> {
    const peers = fleetHosts.filter(
      (host) =>
        host.reachable &&
        host.kind !== "self",
    );
    const lists = await Promise.all(
      peers.map(async (host) => {
        try {
          const { data } = await client.GET(
            "/fleet/hosts/{host_key}/workspaces",
            {
              params: { path: { host_key: host.configKey } },
              signal,
            },
          );
          const workspaces = (data as { workspaces?: Workspace[] } | undefined)?.workspaces ?? [];
          return workspaces.map((ws) => ({
            ...ws,
            fleet_host_key: host.configKey,
            fleet_host_name: host.name,
          }));
        } catch {
          return [];
        }
      }),
    );
    return lists.flat();
  }

  async function fetchFleetStatus(): Promise<void> {
    try {
      const { data, error } = await client.GET("/snapshot", {
        params: { query: { include_peers: true } },
      });
      if (error) {
        fleetError = error.detail ?? error.title ?? "Fleet unavailable";
        fleetLoaded = true;
        return;
      }
      const nextHosts = (data?.hosts ?? []) as HostSummary[];
      fleetHosts = nextHosts;
      fleetError = null;
      fleetLoaded = true;
      if (!hasNonSelfFleetHost(nextHosts)) {
        workspaces = localWorkspacesOnly(workspaces);
      }
      void fetchWorkspaces();
    } catch {
      fleetError = "Fleet unavailable";
      fleetLoaded = true;
    }
  }

  function toggleGroup(key: string): void {
    collapsedGroups = collapsedGroups.includes(key)
      ? collapsedGroups.filter((candidate) => candidate !== key)
      : [...collapsedGroups, key];
  }

  function displayName(ws: Workspace): string {
    return ws.mr_title ?? ws.git_head_ref;
  }

  function updateSearch(event: Event): void {
    searchQuery = event.currentTarget instanceof HTMLInputElement
      ? event.currentTarget.value
      : "";
  }

  function workspaceMatchesSearch(
    ws: Workspace,
    query: string,
  ): boolean {
    const itemKind = ws.item_type === "issue" ? "issue" : "pr";
    const itemNumber = String(ws.item_number);
    const haystack = [
      displayName(ws),
      ws.git_head_ref,
      shortBranch(ws.git_head_ref),
      ws.platform_host,
      ws.repo_owner,
      ws.repo_name,
      ws.repo?.repo_path,
      `${ws.repo_owner}/${ws.repo_name}`,
      `${ws.platform_host}/${ws.repo_owner}/${ws.repo_name}`,
      itemNumber,
      `#${itemNumber}`,
      `${itemKind} ${itemNumber}`,
      `${itemKind} #${itemNumber}`,
    ];

    return haystack.some((value) =>
      value?.toLowerCase().includes(query),
    );
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

  function workspaceRepoIdentity(ws: Workspace): RepoLabelIdentity {
    return {
      provider: ws.repo?.provider ?? "",
      platformHost: ws.platform_host,
      owner: ws.repo_owner,
      name: ws.repo_name,
      repoPath: ws.repo?.repo_path,
    };
  }

  function repoLabel(ws: Workspace): string {
    return repoLabelFormatter.format(workspaceRepoIdentity(ws));
  }

  function workspaceProvider(ws: Workspace): string | undefined {
    return ws.repo?.provider;
  }

  function workspaceHost(ws: Workspace): HostSummary | undefined {
    if (ws.fleet_host_key) {
      return fleetHosts.find(
        (host) => host.configKey === ws.fleet_host_key,
      );
    }
    return fleetHosts.find((host) => host.kind === "self");
  }

  function isRemoteWorkspace(ws: Workspace): boolean {
    return Boolean(ws.fleet_host_key);
  }

  function revealLabel(ws: Workspace): string {
    const platform = workspaceHost(ws)?.platform?.toLowerCase() ?? "";
    if (platform === "darwin" || platform === "macos") {
      return "Reveal in Finder";
    }
    if (platform.includes("win")) {
      return "Open containing folder";
    }
    return "Reveal in file manager";
  }

  function providerItemURL(ws: Workspace): string | null {
    const provider = workspaceProvider(ws)?.toLowerCase();
    const repoPath = ws.repo?.repo_path ?? `${ws.repo_owner}/${ws.repo_name}`;
    const encodedPath = repoPath
      .split("/")
      .map((part) => encodeURIComponent(part))
      .join("/");
    const host = ws.platform_host;
    if (!host || !encodedPath) return null;
    if (provider === "github") {
      const kind = ws.item_type === "issue" ? "issues" : "pull";
      return `https://${host}/${encodedPath}/${kind}/${ws.item_number}`;
    }
    if (provider === "gitlab") {
      const kind = ws.item_type === "issue" ? "issues" : "merge_requests";
      return `https://${host}/${encodedPath}/-/${kind}/${ws.item_number}`;
    }
    if (provider === "gitea" || provider === "forgejo") {
      const kind = ws.item_type === "issue" ? "issues" : "pulls";
      return `https://${host}/${encodedPath}/${kind}/${ws.item_number}`;
    }
    return null;
  }

  function providerLabel(ws: Workspace): string {
    const provider = workspaceProvider(ws);
    if (!provider) return "Open item on provider";
    return `Open item on ${provider}`;
  }

  function canPush(ws: Workspace): boolean {
    const ahead = ws.commits_ahead ?? 0;
    const behind = ws.commits_behind ?? 0;
    return ahead > 0 && behind === 0;
  }

  function canPull(ws: Workspace): boolean {
    const ahead = ws.commits_ahead ?? 0;
    const behind = ws.commits_behind ?? 0;
    return behind > 0 && ahead === 0;
  }

  function syncActionDetail(ws: Workspace): string {
    if (canPush(ws)) {
      return `${ws.commits_ahead ?? 0} ahead`;
    }
    if (canPull(ws)) {
      return `${ws.commits_behind ?? 0} behind`;
    }
    return "";
  }

  async function openContextMenu(
    event: MouseEvent,
    ws: Workspace,
  ): Promise<void> {
    event.preventDefault();
    event.stopPropagation();
    contextMenu = {
      ws,
      x: event.clientX,
      y: event.clientY,
    };
    await tick();
    positionContextMenu();
  }

  function positionContextMenu(): void {
    if (!contextMenu || !contextMenuEl) return;
    const margin = 8;
    const width = contextMenuEl.offsetWidth;
    const height = contextMenuEl.offsetHeight;
    const left = Math.min(
      Math.max(margin, contextMenu.x),
      Math.max(margin, window.innerWidth - width - margin),
    );
    const top = Math.min(
      Math.max(margin, contextMenu.y),
      Math.max(margin, window.innerHeight - height - margin),
    );
    contextMenuStyle = `left: ${left}px; top: ${top}px;`;
  }

  function closeContextMenu(): void {
    contextMenu = null;
    contextMenuStyle = "";
  }

  async function copyMenuText(
    value: string,
    successMessage: string,
  ): Promise<void> {
    closeContextMenu();
    try {
      await navigator.clipboard.writeText(value);
      showFlash(successMessage);
    } catch {
      showFlash("Could not copy to clipboard.");
    }
  }

  async function refreshWorkspaceStatus(ws: Workspace): Promise<void> {
    if (workspaceActionsDisabled(ws)) return;
    closeContextMenu();
    const { error, response } = ws.fleet_host_key
      ? await client.POST("/fleet/hosts/{host_key}/workspaces/{id}/refresh", {
          params: {
            path: { host_key: ws.fleet_host_key, id: ws.id },
          },
        })
      : await client.POST("/workspaces/{id}/refresh", {
          params: { path: { id: ws.id } },
        });
    if (!response.ok) {
      showFlash(apiErrorMessage(error, `Refresh failed (${response.status})`));
      return;
    }
    await fetchWorkspaces();
  }

  function workspaceActionMatches(
    ws: Workspace,
    action?: "push" | "pull" | "reveal" | "delete",
  ): boolean {
    return (
      workspaceAction?.workspaceKey === workspaceRowKey(ws) &&
      (action === undefined || workspaceAction.action === action)
    );
  }

  function workspaceActionsDisabled(ws: Workspace): boolean {
    return isWorkspaceActionDisabled?.(ws.id, ws.fleet_host_key) ?? false;
  }

  function workspaceBusyLabel(ws: Workspace): string {
    if (workspaceActionsDisabled(ws)) return "Deleting workspace";
    if (!workspaceActionMatches(ws)) return "";
    if (workspaceAction?.action === "push") return "Pushing branch";
    if (workspaceAction?.action === "pull") return "Pulling branch";
    if (workspaceAction?.action === "reveal") return "Opening folder";
    if (workspaceAction?.action === "delete") return "Deleting workspace";
    return "";
  }

  function startWorkspaceAction(
    ws: Workspace,
    action: "push" | "pull" | "reveal" | "delete",
  ): boolean {
    if (workspaceAction !== null || workspaceActionsDisabled(ws)) return false;
    workspaceAction = { workspaceKey: workspaceRowKey(ws), action };
    return true;
  }

  function finishWorkspaceAction(ws: Workspace): void {
    if (workspaceActionMatches(ws)) {
      workspaceAction = null;
    }
  }

  async function syncWorkspaceBranch(
    ws: Workspace,
    action: "push" | "pull",
  ): Promise<void> {
    if (!startWorkspaceAction(ws, action)) return;
    const label = action === "push" ? "Push branch" : "Pull remote changes";
    try {
      const { error, response } = ws.fleet_host_key
        ? await client.POST(
            action === "push"
              ? "/fleet/hosts/{host_key}/workspaces/{id}/push"
              : "/fleet/hosts/{host_key}/workspaces/{id}/pull",
            {
              params: {
                path: { host_key: ws.fleet_host_key, id: ws.id },
              },
            },
          )
        : await client.POST(
            action === "push" ? "/workspaces/{id}/push" : "/workspaces/{id}/pull",
            {
              params: { path: { id: ws.id } },
            },
          );
      if (!response.ok) {
        showFlash(apiErrorMessage(error, `${label} failed (${response.status})`));
        return;
      }
      await fetchWorkspaces();
      closeContextMenu();
    } catch (err) {
      showFlash(err instanceof Error ? err.message : `${label} failed.`);
    } finally {
      finishWorkspaceAction(ws);
    }
  }

  async function revealWorkspacePath(ws: Workspace): Promise<void> {
    if (!startWorkspaceAction(ws, "reveal")) return;
    const label = revealLabel(ws);
    try {
      const { error, response } = ws.fleet_host_key
        ? await client.POST("/fleet/hosts/{host_key}/workspaces/{id}/reveal", {
            params: {
              path: { host_key: ws.fleet_host_key, id: ws.id },
            },
          })
        : await client.POST("/workspaces/{id}/reveal", {
            params: { path: { id: ws.id } },
          });
      if (!response.ok) {
        showFlash(apiErrorMessage(error, `${label} failed (${response.status})`));
        return;
      }
      closeContextMenu();
    } catch (err) {
      showFlash(err instanceof Error ? err.message : `${label} failed.`);
    } finally {
      finishWorkspaceAction(ws);
    }
  }

  function openDeleteWorkspaceDialog(ws: Workspace): void {
    deleteConfirmWorkspace = ws;
    closeContextMenu();
  }

  function closeDeleteWorkspaceDialog(): void {
    if (deleteConfirmWorkspace && workspaceActionMatches(deleteConfirmWorkspace, "delete")) return;
    deleteConfirmWorkspace = null;
  }

  async function confirmDeleteWorkspaceFromList(): Promise<void> {
    const ws = deleteConfirmWorkspace;
    if (!ws || workspaceActionsDisabled(ws) || !startWorkspaceAction(ws, "delete")) return;
    onWorkspaceDeletePendingChange?.(ws.id, ws.fleet_host_key, true);
    try {
      const { error, response } = ws.fleet_host_key
        ? await client.DELETE("/fleet/hosts/{host_key}/workspaces/{id}", {
            params: {
              path: { host_key: ws.fleet_host_key, id: ws.id },
            },
          })
        : await client.DELETE("/workspaces/{id}", {
            params: { path: { id: ws.id } },
          });
      if (!response.ok && response.status !== 204) {
        const fallback = response.status === 409
          ? "Workspace has uncommitted changes. Open it to force delete."
          : `Delete failed (${response.status})`;
        showFlash(apiErrorMessage(error, fallback));
        return;
      }
      await fetchWorkspaces();
      closeContextMenu();
      if (isSelectedWorkspace(ws)) {
        navigate("/workspaces");
      }
    } catch (err) {
      showFlash(err instanceof Error ? err.message : "Delete failed.");
    } finally {
      onWorkspaceDeletePendingChange?.(ws.id, ws.fleet_host_key, false);
      finishWorkspaceAction(ws);
      deleteConfirmWorkspace = null;
    }
  }

  function openProviderItem(ws: Workspace): void {
    const url = providerItemURL(ws);
    closeContextMenu();
    if (!url) {
      showFlash("Provider URL is not available for this workspace.");
      return;
    }
    window.open(url, "_blank", "noopener,noreferrer");
  }

  function copyProviderItemURL(ws: Workspace): void {
    const url = providerItemURL(ws);
    if (!url) {
      closeContextMenu();
      showFlash("Provider URL is not available for this workspace.");
      return;
    }
    void copyMenuText(url, "Copied item URL.");
  }

  function workspaceRoute(ws: Workspace): string {
    if (ws.fleet_host_key) {
      return `/terminal/fleet/${encodeURIComponent(ws.fleet_host_key)}/${encodeURIComponent(ws.id)}`;
    }
    return `/terminal/${encodeURIComponent(ws.id)}`;
  }

  function workspaceRowKey(ws: Workspace): string {
    return `${ws.fleet_host_key ?? "self"}:${ws.id}`;
  }

  function isSelectedWorkspace(ws: Workspace): boolean {
    return (
      ws.id === selectedId &&
      (ws.fleet_host_key ?? undefined) === selectedHostKey
    );
  }

  function handleItemBubbleClick(
    e: MouseEvent | KeyboardEvent,
    ws: Workspace,
  ): void {
    e.stopPropagation();
    e.preventDefault();
    const tab = ws.item_type === "issue" ? "issue" : "pr";

    if (onOpenItemSidebar) {
      onOpenItemSidebar(ws.id, tab, ws.fleet_host_key);
      return;
    }
    navigate(workspaceRoute(ws));
  }

  onMount(() => {
    void fetchWorkspaces();
    void fetchFleetStatus();
    const pollHandle = window.setInterval(() => {
      void fetchWorkspaces();
    }, 5_000);
    const fleetPollHandle = window.setInterval(() => {
      void fetchFleetStatus();
    }, 15_000);

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
      window.clearInterval(fleetPollHandle);
      source.close();
    };
  });
</script>

<div class="workspace-list-sidebar">
  <div class="sidebar-header">
    <span class="sidebar-header-label">Workspaces</span>
    <span class="sidebar-header-count">{sidebarCountLabel}</span>
    <div class="workspace-sort">
      <FilterDropdown
        label="View"
        detail={sortLabel}
        active={viewBadgeCount > 0}
        badgeCount={viewBadgeCount}
        sections={viewSections}
        title="View workspace options"
        minWidth="220px"
        align="end"
      />
    </div>
    {#if isSidebarToggleEnabled && onCollapseSidebar}
      <!-- No --push here: .workspace-sort's auto margin already
           claims the free space, and a second auto margin would
           split it and strand the sort trigger mid-header. -->
      <LeftSidebarToggle
        state="expanded"
        label="Workspaces sidebar"
        onclick={onCollapseSidebar}
        class="left-sidebar-toggle--compact"
      />
    {/if}
  </div>
  <label class="workspace-filter">
    <SearchIcon
      class="workspace-filter-icon"
      size="13"
      strokeWidth="2.25"
      aria-hidden="true"
    />
    <input
      type="search"
      value={searchQuery}
      placeholder="Filter workspaces"
      aria-label="Filter workspaces"
      oninput={updateSearch}
    />
  </label>
  {#if showFleetStatus}
    <section class="fleet-status" aria-label="Fleet hosts">
      <div class="fleet-status-heading">
        <span class="fleet-status-title">Fleet</span>
        {#if fleetError}
          <span class="fleet-status-count error">degraded</span>
        {:else}
          <span class="fleet-status-count">
            {reachableFleetHosts}/{fleetHosts.length}
          </span>
        {/if}
      </div>
      {#if fleetError}
        <p class="fleet-status-error">{fleetError}</p>
      {:else}
        <div class="fleet-hosts">
          {#each fleetHosts as host (host.configKey)}
            <span
              class={[
                "fleet-host",
                host.kind === "self" ? "self" : "remote",
                { unreachable: !host.reachable },
              ]}
              title={`${host.name} - ${host.kind} - ${host.preferredTransport}`}
            >
              <span class="fleet-host-dot" aria-hidden="true"></span>
              <span class="fleet-host-name">{host.configKey}</span>
              <span class="fleet-host-kind">{host.kind}</span>
              <span class="fleet-host-transport">{host.preferredTransport}</span>
            </span>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
  <div class="sidebar-list">
    {#if sortMode === "repo"}
    {#each grouped as { key: repoKey, items } (repoKey)}
      {@const collapsed =
        !normalizedSearchQuery && collapsedGroups.includes(repoKey)}
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
        <span class="group-label">{repoLabel(items[0]!)}</span>
        <span class="group-count">{items.length}</span>
      </button>
      {#if !collapsed}
        {#each items as ws (workspaceRowKey(ws))}
          {@render workspaceRow(ws, false)}
        {/each}
      {/if}
    {/each}
    {:else}
      {#each sortedFlat as ws (workspaceRowKey(ws))}
        {@render workspaceRow(ws, true)}
      {/each}
    {/if}

    {#snippet workspaceRow(ws: Workspace, showRepo: boolean)}
          {@const adds = ws.mr_additions}
          {@const dels = ws.mr_deletions}
          {@const ahead = ws.commits_ahead ?? 0}
          {@const behind = ws.commits_behind ?? 0}
          {@const showPush = ahead > 0 || behind > 0}
          <div
            class={["ws-row", { selected: isSelectedWorkspace(ws) }]}
            onclick={(e) => {
              // The PR/issue bubble is a focusable child button; let
              // its own click handler run without the row also
              // navigating to the terminal route.
              if (e.target !== e.currentTarget &&
                e.target instanceof Element &&
                e.target.closest(".item-bubble")) {
                return;
              }
              navigate(workspaceRoute(ws));
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
                navigate(workspaceRoute(ws));
              }
            }}
            oncontextmenu={(e) => {
              void openContextMenu(e, ws);
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
                {:else if workspaceActionMatches(ws) || workspaceActionsDisabled(ws)}
                  <span
                    class="working-pulse"
                    title={workspaceBusyLabel(ws)}
                    aria-label={workspaceBusyLabel(ws)}
                  ></span>
                {/if}
              </div>
              <div class="ws-row-meta">
                {#if ws.fleet_host_key}
                  <span
                    class="fleet-context"
                    title={`Fleet host: ${ws.fleet_host_name ?? ws.fleet_host_key}`}
                  >
                    {ws.fleet_host_key}
                  </span>
                {/if}
                {#if showRepo}
                  <span
                    class="repo-context"
                    title={`${ws.platform_host}/${ws.repo_owner}/${ws.repo_name}`}
                  >
                    {#if showProviderIcons && workspaceProvider(ws)}
                      <ProviderIcon
                        provider={workspaceProvider(ws)!}
                        size={10}
                        class="repo-context-icon"
                      />
                    {/if}
                    <span class="repo-context-name">{repoLabel(ws)}</span>
                  </span>
                {/if}
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
                {#if displayOptions.showDiffStats &&
                  ws.item_type === "pull_request" &&
                  ((adds ?? 0) > 0 || (dels ?? 0) > 0)}
                  <span class="workspace-diff-stats">
                    <DiffStats
                      additions={adds ?? 0}
                      deletions={dels ?? 0}
                    />
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
    {/snippet}
    {#if workspaceListStatus === "loading" && workspaces.length === 0}
      <p class="filter-empty">Loading workspaces...</p>
    {:else if workspaceListStatus === "retrying" && workspaces.length === 0}
      <p class="filter-empty">Still loading workspaces. Retrying...</p>
    {:else if visibleWorkspaces.length === 0 && normalizedSearchQuery}
      <p class="filter-empty">No workspaces match.</p>
    {:else if visibleWorkspaces.length === 0}
      <p class="filter-empty">No workspaces yet.</p>
    {/if}
  </div>

</div>

{#if contextMenu}
  {@const menuWorkspace = contextMenu.ws}
  {@const localWorkspace = !isRemoteWorkspace(menuWorkspace)}
  {@const itemURL = providerItemURL(menuWorkspace)}
  {@const actionBusy = workspaceAction !== null}
  {@const actionDisabled = actionBusy || workspaceActionsDisabled(menuWorkspace)}
  <div
    class="workspace-context-menu filter-dropdown"
    bind:this={contextMenuEl}
    style={contextMenuStyle}
    role="menu"
    aria-label="Workspace actions"
    tabindex="-1"
    oncontextmenu={(e) => e.preventDefault()}
  >
    <div class="workspace-context-heading">
      <div class="workspace-context-title">
        {displayName(menuWorkspace)}
      </div>
      <div class="workspace-context-meta">
        {repoLabel(menuWorkspace)} · {shortBranch(menuWorkspace.git_head_ref)}
      </div>
    </div>

    <div class="filter-section-title">Sync branch</div>
    {#if canPush(menuWorkspace)}
      <button
        class="filter-item active"
        role="menuitem"
        type="button"
        disabled={actionDisabled}
        onclick={() => {
          void syncWorkspaceBranch(menuWorkspace, "push");
        }}
      >
        <span class="filter-dot filter-dot--success"></span>
        <span class="filter-label">{workspaceActionMatches(menuWorkspace, "push") ? "Pushing..." : "Push branch"}</span>
        <span class="workspace-context-detail">{syncActionDetail(menuWorkspace)}</span>
      </button>
    {:else if canPull(menuWorkspace)}
      <button
        class="filter-item active"
        role="menuitem"
        type="button"
        disabled={actionDisabled}
        onclick={() => {
          void syncWorkspaceBranch(menuWorkspace, "pull");
        }}
      >
        <span class="filter-dot filter-dot--warning"></span>
        <span class="filter-label">{workspaceActionMatches(menuWorkspace, "pull") ? "Pulling..." : "Pull remote changes"}</span>
        <span class="workspace-context-detail">{syncActionDetail(menuWorkspace)}</span>
      </button>
    {/if}
    <button
      class="filter-item active"
      role="menuitem"
      type="button"
      disabled={actionDisabled}
      onclick={() => {
        void refreshWorkspaceStatus(menuWorkspace);
      }}
    >
      <span class="filter-dot"></span>
      <span class="filter-label">Refresh git status</span>
    </button>

    <div class="filter-divider"></div>
    <button
      class="filter-item active"
      role="menuitem"
      type="button"
      disabled={actionBusy}
      onclick={() => {
        void copyMenuText(
          shortBranch(menuWorkspace.git_head_ref),
          "Copied branch name.",
        );
      }}
    >
      <span class="filter-dot"></span>
      <span class="filter-label">Copy branch name</span>
    </button>
    {#if localWorkspace}
      <button
        class="filter-item active"
        role="menuitem"
        type="button"
        disabled={actionBusy}
        onclick={() => {
          void copyMenuText(
            menuWorkspace.worktree_path,
            "Copied worktree path.",
          );
        }}
      >
        <span class="filter-dot"></span>
        <span class="filter-label">Copy worktree path</span>
      </button>
      <button
        class="filter-item active"
        role="menuitem"
        type="button"
        disabled={actionDisabled}
        onclick={() => {
          void revealWorkspacePath(menuWorkspace);
        }}
      >
        <span class="filter-dot"></span>
        <span class="filter-label">{workspaceActionMatches(menuWorkspace, "reveal") ? "Opening..." : revealLabel(menuWorkspace)}</span>
      </button>
    {/if}

    {#if itemURL}
      <div class="filter-divider"></div>
      <button
        class="filter-item active"
        role="menuitem"
        type="button"
        disabled={actionBusy}
        onclick={() => openProviderItem(menuWorkspace)}
      >
        <span class="filter-dot"></span>
        <span class="filter-label">{providerLabel(menuWorkspace)}</span>
      </button>
      <button
        class="filter-item active"
        role="menuitem"
        type="button"
        disabled={actionBusy}
        onclick={() => copyProviderItemURL(menuWorkspace)}
      >
        <span class="filter-dot"></span>
        <span class="filter-label">Copy item URL</span>
      </button>
    {/if}

    <div class="filter-divider"></div>
    <button
      class="filter-item active workspace-context-danger"
      role="menuitem"
      type="button"
      disabled={actionDisabled}
      onclick={() => {
        openDeleteWorkspaceDialog(menuWorkspace);
      }}
    >
      <span class="filter-dot filter-dot--danger"></span>
      <span class="filter-label">{workspaceActionMatches(menuWorkspace, "delete") ? "Deleting..." : "Delete workspace..."}</span>
    </button>
  </div>
{/if}

<ConfirmDialog
  open={deleteConfirmWorkspace !== null}
  title="Delete workspace?"
  message={deleteConfirmWorkspace
    ? `Delete workspace "${displayName(deleteConfirmWorkspace)}"?`
    : ""}
  hint="This removes its managed worktree and runtime sessions."
  confirmLabel="Delete workspace"
  pendingLabel="Deleting…"
  busy={deleteConfirmBusy}
  tone="danger"
  frameId="workspace-sidebar-delete"
  onCancel={closeDeleteWorkspaceDialog}
  onConfirm={() => void confirmDeleteWorkspaceFromList()}
/>

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

  .workspace-filter {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    margin: 6px 8px 4px;
    padding: 0 8px;
    border: 1px solid var(--border-muted);
    border-radius: 6px;
    background: var(--bg-surface);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .workspace-sort {
    /* Claims the header's free space so the sort trigger (and the
     * collapse toggle after it) sit flush right. */
    margin-left: auto;
    flex-shrink: 0;
  }

  .workspace-sort :global(.filter-btn) {
    /* Borderless inside the 28px header rail; the dropdown trigger
     * reads as header chrome rather than a standalone button. */
    min-height: 22px;
    padding: 2px 6px;
    border-color: transparent;
    background: transparent;
  }

  .workspace-sort :global(.filter-btn:hover:not(:disabled)) {
    border-color: var(--border-muted);
  }

  :global(.workspace-filter-icon) {
    flex-shrink: 0;
  }

  .workspace-filter input {
    width: 100%;
    min-width: 0;
    padding: 0;
    border: 0;
    outline: 0;
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    line-height: 1;
  }

  .workspace-filter input::placeholder {
    color: var(--text-muted);
    opacity: 0.8;
  }

  .workspace-filter:focus-within {
    border-color: var(--accent-blue);
    box-shadow: 0 0 0 1px var(--accent-blue);
  }

  .fleet-status {
    flex-shrink: 0;
    margin: 4px 8px 6px;
    padding: 7px 8px 8px;
    border-top: 1px solid var(--border-muted);
    border-bottom: 1px solid var(--border-muted);
    background: color-mix(in srgb, var(--bg-surface) 72%, transparent);
  }

  .fleet-status-heading {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 6px;
  }

  .fleet-status-title {
    font-size: var(--font-size-2xs);
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--text-muted);
  }

  .fleet-status-count {
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    color: var(--accent-green);
  }

  .fleet-status-count.error {
    color: var(--accent-amber);
  }

  .fleet-status-error {
    margin: 0;
    color: var(--accent-amber);
    font-size: var(--font-size-xs);
    line-height: 1.35;
  }

  .fleet-hosts {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
  }

  .fleet-host {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    max-width: 100%;
    min-width: 0;
    padding: 2px 6px;
    border: 1px solid var(--border-muted);
    border-radius: 999px;
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    line-height: 1.25;
  }

  .fleet-host.self {
    border-color: color-mix(in srgb, var(--accent-blue) 38%, var(--border-muted));
  }

  .fleet-host.unreachable {
    color: var(--text-muted);
    opacity: 0.78;
  }

  .fleet-host-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent-green);
    flex-shrink: 0;
  }

  .fleet-host.unreachable .fleet-host-dot {
    background: var(--accent-red);
  }

  .fleet-host-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 600;
  }

  .fleet-host-kind {
    flex-shrink: 0;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
  }

  .fleet-host-transport {
    flex-shrink: 0;
    color: var(--text-subtle);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
  }

  .fleet-context {
    max-width: 72px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--accent-blue);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    font-weight: 600;
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

  .filter-empty {
    margin: 14px 12px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    line-height: 1.4;
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

  .repo-context {
    /* Flat sorts drop the per-repo group headers, so each row
     * carries its own repo context on the meta line. Caps at half
     * the line so the branch chip always keeps some room. */
    display: inline-flex;
    align-items: center;
    gap: 3px;
    flex: 0 1 auto;
    max-width: 50%;
    min-width: 0;
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .repo-context-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  :global(.repo-context-icon) {
    color: var(--text-muted);
    flex-shrink: 0;
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

  .workspace-diff-stats {
    flex-shrink: 0;
    display: inline-flex;
    font-size: var(--font-size-2xs);
  }

  .workspace-context-menu {
    position: fixed;
    min-width: 224px;
    max-width: min(320px, calc(100vw - 16px));
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-md);
    z-index: 1000;
    padding: 4px 0;
  }

  .workspace-context-heading {
    padding: 6px 12px 7px;
    border-bottom: 1px solid var(--border-muted);
    margin-bottom: 4px;
  }

  .workspace-context-title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-primary);
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .workspace-context-meta {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    margin-top: 2px;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
  }

  .filter-section-title {
    padding: 4px 12px;
    font-size: 0.9em;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .filter-divider {
    height: 1px;
    background: var(--border-muted);
    margin: 4px 8px;
  }

  .filter-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 4px 12px;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    text-align: left;
    cursor: pointer;
    transition: background 0.08s;
    background: transparent;
    border: 0;
  }

  .filter-item:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .filter-item:not(.active) {
    opacity: 0.5;
  }

  .filter-item:disabled {
    cursor: default;
  }

  .filter-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
    background: var(--border-muted);
  }

  .filter-dot--success {
    background: var(--accent-green);
  }

  .filter-dot--warning {
    background: var(--accent-amber);
  }

  .filter-dot--danger {
    background: var(--accent-red);
  }

  .filter-label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .workspace-context-detail {
    flex-shrink: 0;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
  }

  .workspace-context-danger {
    color: var(--accent-red);
  }

  /* Width-aware hiding: shed least-critical chrome first as the
   * rail narrows. Push state outranks diff stats because branch
   * hygiene matters more for "should I open this workspace?" than
   * line counts. */
  @container workspace-rail (max-width: 260px) {
    .workspace-diff-stats {
      display: none;
    }
  }

  @container workspace-rail (max-width: 220px) {
    .push-state {
      display: none;
    }
  }

  /* The sort trigger collapses to its icon before the filter input
   * is squeezed into uselessness. */
  @container workspace-rail (max-width: 240px) {
    .workspace-sort :global(.filter-trigger-label) {
      display: none;
    }
  }
</style>
