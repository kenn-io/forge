<script lang="ts">
  import ItemFilters from "../shared/ItemFilters.svelte";
  import { workspaceAuthorOptions, workspaceMatchesAuthor } from "./workspace-author-filter.js";
  import QuerySearchInput from "../shared/QuerySearchInput.svelte";
  import { buildCanonicalProviderItemURL } from "../../utils/item-reference.js";
  import {
    copyToClipboard,
    formatRelativeTime,
    formatTimestamp,
    IconButton,
    Menu,
    MenuContent,
    MenuItem,
    MenuSeparator,
    StatusDot,
    type MenuAnchorPoint,
    type StatusDotStatus,
  } from "@kenn-io/kit-ui";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import PinIcon from "@lucide/svelte/icons/pin";
  import PinOffIcon from "@lucide/svelte/icons/pin-off";
  import CopyIcon from "@lucide/svelte/icons/copy";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import LinkIcon from "@lucide/svelte/icons/link";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import Trash2Icon from "@lucide/svelte/icons/trash-2";
  import SquareIcon from "@lucide/svelte/icons/square";
  import ZapIcon from "@lucide/svelte/icons/zap";
  import {
    openNewWorkspaceDialog,
    type NewWorkspaceRepoSeed,
  } from "../../stores/new-workspace.svelte.js";
  import { onMount, tick } from "svelte";
  import { Effect, Result, Stream } from "effect";
  import { navigate } from "../../stores/router.svelte.ts";
  import GitBranchIcon from "@lucide/svelte/icons/git-branch";
  import ArrowUpIcon from "@lucide/svelte/icons/arrow-up";
  import ArrowDownIcon from "@lucide/svelte/icons/arrow-down";
  import { apiErrorMessage } from "../../api/runtime.js";
  import { ApiProblemError } from "../../api/effect-errors.js";
  import { ProblemCodes, problemConflictReason } from "../../api/problems.js";
  import {
    executeGeneratedApiRequest,
    executeOpaqueGeneratedApiRequest,
  } from "../../api/generated-api.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import type { QuickAction } from "../../api/types.js";
  import { runQuickActionOnWorkspaces } from "../../stores/bulk-quick-action.js";
  import {
    quickActionDisabledReason,
    quickActionTarget,
    sortQuickActionsByLabel,
  } from "../../stores/workspace-quick-actions.js";
  import { stopSessionsInWorkspaces } from "../../stores/workspace-sessions-stop.js";
  import type { HostSummary as GeneratedHostSummary } from "../../api/generated/models/index.js";
  import { DiffStats, ScrollBox, SidebarToggle } from "@kenn-io/kit-ui";
  import GroupedSidebarSection from "../shared/GroupedSidebarSection.svelte";
  import SidebarTitlePopover from "../sidebar/SidebarTitlePopover.svelte";
  import type { WorkspaceItemIdentity } from "../../workspace-inline.js";
  import {
    createRepoLabelFormatter,
    repoIdentityKey,
    type RepoLabelIdentity,
  } from "../../utils/repo-label.js";
  import ProviderIcon from "../provider/ProviderIcon.svelte";
  import ConfirmDialog from "../shared/ConfirmDialog.svelte";
  import {
    defaultWorkspaceListDisplayOptions,
    defaultWorkspaceListSort,
    loadWorkspaceListDisplayOptions,
    loadWorkspaceListSort,
    loadPinnedWorkspaces,
    saveWorkspaceListDisplayOptions,
    saveWorkspaceListSort,
    savePinnedWorkspaces,
    workspaceAgentStatePriority,
    workspaceAgentStateSortTime,
    workspaceListSortTimestamp,
    workspaceListSortOptions,
    type WorkspaceListDisplayOptions,
    type WorkspaceListSort,
  } from "./workspaceListSort.ts";
  import {
    WorkspaceListWorkflow,
    makeWorkspaceRefreshHub,
    removeWorkspaceListEntry,
    workspaceListLifecycle,
    type WorkspaceListSnapshot,
  } from "./workspace-list-workflow.js";
  import { workspaceEventStream } from "./workspace-event-stream.js";
  import {
    decodeWorkspaceList,
    retainDegradedHostWorkspaces,
    type WorkspaceListItem,
  } from "./workspace-list-schema.js";
  import { parseRepoFilterValue } from "../../stores/filter.svelte.js";
  import {
    isEmptySearchQuery,
    matchesSearchQuery,
    parseSearchQuery,
    type SearchQuery,
  } from "../../utils/search-query.js";
  import {
    canonicalRepoFilterValue,
    type RepoFilterIdentity,
  } from "../../utils/repo-filter-values.js";
  import { setWorkspaceRepoCatalog } from "../../stores/workspace-repo-catalog.svelte.js";
  import { loadFleetSnapshot } from "../../api/fleet-snapshot.js";
  import { repositoryKeyFromWire } from "../../api/repository-key.js";
  import { workspaceItemAccent, workspaceItemState, type WorkspaceItemState } from "./workspace-item-state.js";

  type Workspace = WorkspaceListItem;
  type WorkspaceActionKind = "push" | "pull" | "reveal" | "delete" | "stop";

  type HostSummary = GeneratedHostSummary;
  type CatalogLoadStatus = "loading" | "loaded" | "failed";

  interface Props {
    selectedId: string;
    selectedRepos?: string | undefined;
    selectedHostKey?: string | undefined;
    onOpenItemSidebar?: (
      workspaceId: string,
      tab: "pr" | "issue" | "kata",
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
    // Reports a successful delete from this list's own delete flow, so a
    // hosting shell (e.g. an inline claimant) can react even though this
    // sidebar isn't the one navigating away. Mirrors WorkspaceTerminalView's
    // own onWorkspaceDeleted contract.
    onWorkspaceDeleted?:
      | ((workspaceId: string, hostKey?: string, identity?: WorkspaceItemIdentity) => void)
      | undefined;
    isSidebarToggleEnabled?: boolean;
    onCollapseSidebar?: (() => void) | undefined;
    // False while this instance is parked in a hidden host: every dialog
    // unmounts (its state persists for reopen) the same way
    // WorkspaceTerminalView's own dialogs do. Defaults to true so
    // standalone/embedded usage (this component mounted outside the
    // reparenting host) is unaffected.
    hostVisible?: boolean;
  }

  const {
    selectedId,
    selectedRepos = undefined,
    selectedHostKey = undefined,
    onOpenItemSidebar,
    onWorkspaceListStateChange,
    isWorkspaceActionDisabled,
    onWorkspaceDeletePendingChange,
    onWorkspaceDeleted,
    isSidebarToggleEnabled = false,
    onCollapseSidebar,
    hostVisible = true,
  }: Props = $props();

  const runtime = getAppRuntime();
  const { events: eventsStore, detail, settings } = getStores();
  const quickActions = $derived(sortQuickActionsByLabel(settings.getQuickActions()));
  const launchTargets = $derived(settings.getLaunchTargets());

  const doneAcknowledgementsStorageKey =
    "kenn-forge:workspace-agent-done-acknowledgements/v1";

  let workspaces = $state.raw<Workspace[]>([]);
  let localCatalogStatus = $state<CatalogLoadStatus>("loading");
  let fleetCatalogStatus = $state<CatalogLoadStatus>("loading");
  let peerCatalogStatus = $state<CatalogLoadStatus>("loading");
  const workspaceRepoCatalogComplete = $derived(
    localCatalogStatus === "loaded" &&
      fleetCatalogStatus === "loaded" &&
      peerCatalogStatus === "loaded",
  );

  $effect(() => {
    setWorkspaceRepoCatalog(
      workspaceListStatus === "loaded" ? workspaces : undefined,
      workspaceRepoCatalogComplete,
    );
  });
  let fleetHosts = $state.raw<HostSummary[]>([]);
  let fleetError = $state<string | null>(null);
  let fleetPeerErrors = $state.raw<Record<string, string>>({});
  let collapsedGroups = $state<string[]>([]);
  let searchQuery = $state("");
  let author = $state("");
  let sortMode = $state<WorkspaceListSort>(loadWorkspaceListSort());
  let pinnedWorkspaceKeys = $state.raw(loadPinnedWorkspaces());
  let pinsCollapsed = $state(false);
  let workspaceListStatus = $state<"loading" | "retrying" | "loaded">("loading");
  // Kept after close so the menu can return focus to the right-clicked row.
  let contextMenu = $state<{
    ws: Workspace;
    anchor: MenuAnchorPoint;
    row: HTMLElement | undefined;
  } | null>(null);
  let contextMenuOpen = $state(false);
  // Row keys picked with shift-click (range) or Cmd/Ctrl-click (toggle).
  // Separate from the opened workspace, which stays route-owned.
  let multiSelectedKeys = $state.raw<ReadonlySet<string>>(new Set());
  let selectionAnchorKey = $state<string | null>(null);
  let deleteConfirmWorkspace = $state<Workspace | null>(null);
  let deleteConfirmForce = $state(false);
  let bulkDeleteConfirm = $state.raw<Workspace[] | null>(null);
  let stopSessionsConfirm = $state.raw<Workspace[] | null>(null);
  let quickActionConfirm = $state.raw<{ action: QuickAction; targets: Workspace[] } | null>(null);
  // A bulk delete holds one key per in-flight row; each finished row drops
  // its key so the remaining rows keep their busy state.
  let workspaceAction = $state<{
    workspaceKeys: readonly string[];
    action: WorkspaceActionKind;
  } | null>(null);
  // Row elements keyed by workspaceRowKey so the snippet-rendered rows can
  // each anchor their own truncation popover.
  let rowEls = $state<Record<string, HTMLElement | null>>({});
  let acknowledgedDoneStates = $state.raw<Record<string, string>>(
    loadDoneAcknowledgements(),
  );

  const workspaceListLoadTimeoutMs = 10_000;
  let displayOptions = $state<WorkspaceListDisplayOptions>(
    loadWorkspaceListDisplayOptions(),
  );

  const refreshWorkspaces = makeWorkspaceRefreshHub(
    Effect.suspend(loadWorkspaces).pipe(
      Effect.catch(() =>
        Effect.sync(() => {
          localCatalogStatus = "failed";
          fleetCatalogStatus = "failed";
          peerCatalogStatus = "failed";
          if (workspaces.length === 0) workspaceListStatus = "retrying";
        }),
      ),
    ),
  );
  const refreshFleet = refreshWorkspaces;
  let requestApplicationWorkspaceRefresh = refreshWorkspaces.request;
  const workspaceRefreshOwner = $props.id();

  type WorkspaceGroup = {
    key: string;
    items: Workspace[];
  };

  const parsedSearchQuery = $derived(parseSearchQuery(searchQuery));
  const hasSearchQuery = $derived(!isEmptySearchQuery(parsedSearchQuery));
  const selectedRepoValues = $derived(new Set(parseRepoFilterValue(selectedRepos)));
  const deleteConfirmBusy = $derived(
    deleteConfirmWorkspace !== null &&
      workspaceActionMatches(deleteConfirmWorkspace, "delete"),
  );

  const scopedWorkspaces = $derived.by(() => {
    const repoIdentities = workspaces.map(workspaceRepoFilterIdentity);
    return selectedRepoValues.size === 0
      ? workspaces
      : workspaces.filter((workspace) => {
          const value = canonicalRepoFilterValue(
            workspaceRepoFilterIdentity(workspace),
            repoIdentities,
          );
          return value !== null && selectedRepoValues.has(value);
        });
  });
  const authorOptions = $derived(workspaceAuthorOptions(scopedWorkspaces));
  const visibleWorkspaces = $derived(scopedWorkspaces.filter((ws) =>
    workspaceMatchesAuthor(ws, author) &&
    (!hasSearchQuery || workspaceMatchesSearch(ws, parsedSearchQuery)),
  ));

  const pinnedWorkspaces = $derived(
    pinnedWorkspaceKeys.flatMap((key) =>
      visibleWorkspaces.filter((ws) => workspaceRowKey(ws) === key),
    ),
  );
  const unpinnedWorkspaces = $derived(visibleWorkspaces.filter((ws) => !isPinned(ws)));

  const sidebarCountLabel = $derived(
    hasSearchQuery || author || selectedRepoValues.size > 0
      ? `${visibleWorkspaces.length}/${workspaces.length}`
      : `${workspaces.length}`,
  );

  const grouped = $derived.by<WorkspaceGroup[]>(() => {
    const groups: WorkspaceGroup[] = [];
    for (const ws of unpinnedWorkspaces) {
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
    Number(Boolean(author)) + Number(hasSearchQuery) +
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
      selectionMode: "single" as const,
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
          id: "hide-org-name",
          label: "Hide org name",
          description: "Hide owner or organization names in workspace repo labels.",
          active: !displayOptions.showOrgNames,
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

  const fleetDegraded = $derived(
    fleetError !== null || Object.keys(fleetPeerErrors).length > 0,
  );
  const showWorkspaceHostBadges = $derived(
    fleetHosts.length > 1 &&
      fleetHosts.find((host) => host.kind === "self")?.federationRole === "hub",
  );

  // Flat ordering for status and timestamp sorts. The org/repo mode keeps
  // the API order (created_at DESC) inside each repo group.
  // Agent status follows the same attention-first priority as the server's
  // aggregate hook state, then keeps newer workspaces first within a status.
  // "Activity" means terminal output only (tmux_last_output_at).
  // "Item activity" means the synced PR/issue last_activity_at.
  // Missing timestamps fall back to workspace creation time.
  const sortedFlat = $derived.by(() => {
    if (sortMode === "agent-status") {
      return [...unpinnedWorkspaces].sort(
        (a, b) =>
          workspaceAgentStatePriority(b.agent_state) - workspaceAgentStatePriority(a.agent_state) ||
          timeValue(workspaceAgentStateSortTime(b)) - timeValue(workspaceAgentStateSortTime(a)) ||
          a.id.localeCompare(b.id),
      );
    }
    const stamp = sortMode === "activity"
      ? (ws: Workspace) =>
          timeValue(ws.tmux_last_output_at) || timeValue(ws.created_at)
      : sortMode === "item-activity"
        ? (ws: Workspace) =>
            timeValue(ws.item_last_activity_at) ||
            timeValue(ws.created_at)
        : (ws: Workspace) => timeValue(ws.created_at);
    return [...unpinnedWorkspaces].sort(
      (a, b) => stamp(b) - stamp(a) || a.id.localeCompare(b.id),
    );
  });

  // Rows in on-screen order, skipping collapsed sections, so a shift-click
  // range and bulk actions only ever cover rows the user can see.
  const renderedWorkspaces = $derived.by<Workspace[]>(() => {
    const rows = !hasSearchQuery && pinsCollapsed ? [] : [...pinnedWorkspaces];
    if (sortMode !== "repo") return [...rows, ...sortedFlat];
    for (const group of grouped) {
      if (!hasSearchQuery && collapsedGroups.includes(group.key)) continue;
      rows.push(...group.items);
    }
    return rows;
  });
  const multiSelectedWorkspaces = $derived(
    renderedWorkspaces.filter((ws) => multiSelectedKeys.has(workspaceRowKey(ws))),
  );

  function resetFilters(): void {
    author = "";
    searchQuery = "";
    setSort(defaultWorkspaceListSort);
    displayOptions = { ...defaultWorkspaceListDisplayOptions };
    saveWorkspaceListDisplayOptions(displayOptions);
  }

  function setSort(sort: WorkspaceListSort): void {
    sortMode = sort;
    saveWorkspaceListSort(sort);
  }

  function isPinned(ws: Workspace): boolean {
    return pinnedWorkspaceKeys.includes(workspaceRowKey(ws));
  }

  async function togglePin(ws: Workspace): Promise<void> {
    const key = workspaceRowKey(ws);
    if (isPinned(ws)) {
      pinnedWorkspaceKeys = pinnedWorkspaceKeys.filter((candidate) => candidate !== key);
      const groupKey = repoIdentityKey(workspaceRepoIdentity(ws));
      collapsedGroups = collapsedGroups.filter((candidate) => candidate !== groupKey);
    } else {
      pinnedWorkspaceKeys = [...pinnedWorkspaceKeys, key];
    }
    savePinnedWorkspaces(pinnedWorkspaceKeys);
    pinsCollapsed = false;
    closeContextMenu();
    // Pinning moves the row into another list, replacing the element the
    // menu would return focus to. The destination list is expanded above so
    // the new row renders.
    await tick();
    rowEls[key]?.focus();
  }

  function setDisplayOption(
    key: keyof WorkspaceListDisplayOptions,
    value: boolean,
  ): void {
    displayOptions = { ...displayOptions, [key]: value };
    saveWorkspaceListDisplayOptions(displayOptions);
  }

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

  function fleetHostName(hostKey: string): string {
    const name = fleetHosts.find((host) => host.configKey === hostKey)?.name.trim();
    return name || hostKey;
  }

  function workspaceHostName(ws: Workspace): string {
    return ws.fleet_host_name?.trim() || workspaceHost(ws)?.name.trim() || ws.fleet_host_key || "This machine";
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

  function failureMessage(failure: unknown, fallback: string): string {
    if (failure instanceof ApiProblemError) return apiErrorMessage(failure.problem, fallback);
    return failure instanceof Error ? failure.message : fallback;
  }

  function loadWorkspaces() {
    return Effect.sync(() => {
      localCatalogStatus = "loading";
      fleetCatalogStatus = "loading";
      peerCatalogStatus = "loading";
    }).pipe(
      Effect.andThen(Effect.gen(function* () {
        const workflow = yield* WorkspaceListWorkflow;
        const data = yield* loadFleetSnapshot().pipe(
          Effect.timeout(`${workspaceListLoadTimeoutMs} millis`),
        );
        const decoded = yield* decodeWorkspaceList({ workspaces: data.workspaces });
        const nextHosts = data.hosts ?? [];
        const aggregateComplete = !(data.aggregateIncomplete ?? false);
        const nextWorkspaces = retainDegradedHostWorkspaces(
          workspaces,
          decoded.filter((workspace) => workspace.visible !== false),
          nextHosts,
          data.aggregateIncomplete ?? false,
        );
        yield* Effect.sync(() => {
          workflow.snapshot = { workspaces: nextWorkspaces, hosts: nextHosts, aggregateComplete };
          applySnapshot(workflow.snapshot);
        });
      })),
    );
  }

  function applySnapshot(snapshot: WorkspaceListSnapshot): void {
    reconcileDoneAcknowledgements(snapshot.workspaces);
    workspaces = snapshot.workspaces;
    fleetHosts = snapshot.hosts;
    fleetPeerErrors = Object.fromEntries(
      fleetHosts
        .filter((host) => host.kind !== "self" && host.error)
        .map((host) => [host.configKey, host.error ?? "Host unavailable"]),
    );
    fleetError = null;
    workspaceListStatus = "loaded";
    localCatalogStatus = "loaded";
    fleetCatalogStatus = snapshot.aggregateComplete ? "loaded" : "failed";
    peerCatalogStatus = snapshot.aggregateComplete ? "loaded" : "failed";
  }

  function toggleGroup(key: string): void {
    collapsedGroups = collapsedGroups.includes(key)
      ? collapsedGroups.filter((candidate) => candidate !== key)
      : [...collapsedGroups, key];
  }

  function displayName(ws: Workspace): string {
    if (ws.item_type === "kata_task") {
      return ws.kata?.title?.trim() || ws.git_head_ref;
    }
    return ws.mr_title ?? ws.git_head_ref;
  }

  function kataIdentityLabel(ws: Workspace): string {
    return (
      ws.kata?.short_id?.trim() ||
      ws.kata?.qualified_id?.trim() ||
      "Kata"
    );
  }

  // An ad-hoc workspace has no provider item at creation, but the daemon links
  // one once its branch is pushed and a PR appears. That detected PR is the
  // workspace's item number for display, opening, and search.
  function adHocPRNumber(ws: Workspace): number | null {
    if (ws.item_type !== "adhoc") return null;
    const number = ws.associated_pr_number;
    return number && number > 0 ? number : null;
  }

  function itemBubbleNumber(ws: Workspace): number | null {
    if (
      (ws.item_type === "pull_request" || ws.item_type === "issue") &&
      !ws.source_item_visible
    ) {
      return null;
    }
    return ws.item_type === "adhoc" ? adHocPRNumber(ws) : ws.item_number;
  }

  function hasItemBubble(ws: Workspace): boolean {
    if (ws.item_type === "kata_task") return true;
    return itemBubbleNumber(ws) !== null;
  }

  function itemBubbleLabel(ws: Workspace): string {
    if (ws.item_type === "kata_task") return kataIdentityLabel(ws);
    return `#${itemBubbleNumber(ws)}`;
  }

  function itemBubbleTitle(ws: Workspace): string {
    if (ws.item_type === "kata_task") {
      const title = ws.kata?.title?.trim();
      const identity = kataIdentityLabel(ws);
      return title ? `Open Kata task ${identity}: ${title}` : `Open Kata task ${identity}`;
    }
    return ws.item_type === "issue"
      ? `Open issue #${itemBubbleNumber(ws)}`
      : `Open PR #${itemBubbleNumber(ws)}`;
  }

  function updateSearch(value: string): void {
    searchQuery = value;
  }

  function workspaceMatchesSearch(
    ws: Workspace,
    query: SearchQuery,
  ): boolean {
      const haystack: Array<string | undefined> = [
      displayName(ws),
      ws.git_head_ref,
      shortBranch(ws.git_head_ref),
      ws.platform_host,
      ws.repo_owner,
      ws.repo_name,
      ws.repo?.repo_path,
      `${ws.repo_owner}/${ws.repo_name}`,
      `${ws.platform_host}/${ws.repo_owner}/${ws.repo_name}`,
    ];

    if (ws.item_type === "kata_task") {
      haystack.push(
        "kata",
        ws.kata?.short_id,
        ws.kata?.qualified_id,
        ws.kata?.title,
        ws.kata?.project_name,
        // Durable identifiers so a Kata workspace stays findable by its task
        // key even when it has no short/qualified ID to display.
        ws.kata?.daemon_id,
        ws.kata?.project_uid,
        ws.kata?.issue_uid,
        ws.item_key,
      );
    } else if (ws.item_type === "adhoc") {
      // Without a detected PR there is no number or item title to match on:
      // the branch (already in the haystack) plus the kind keywords are what
      // a user would type.
      haystack.push("adhoc", "new work");
      const prNumber = adHocPRNumber(ws);
      if (prNumber !== null) {
        haystack.push(
          String(prNumber),
          `#${prNumber}`,
          `pr ${prNumber}`,
          `pr #${prNumber}`,
        );
      }
    } else {
      const itemKind = ws.item_type === "issue" ? "issue" : "pr";
      const itemNumber = String(ws.item_number);
      haystack.push(
        itemNumber,
        `#${itemNumber}`,
        `${itemKind} ${itemNumber}`,
        `${itemKind} #${itemNumber}`,
      );
    }

    return matchesSearchQuery(query, haystack);
  }

  function workspaceStatus(ws: Workspace): StatusDotStatus {
    if (workspaceMerging(ws)) return "working";
    if (ws.status === "ready") return "idle";
    if (ws.status === "creating" || ws.status === "deleting") return "working";
    if (ws.status === "error" || ws.status === "deletion_failed") return "unclean";
    return "stale";
  }

  function workspaceStatusLabel(ws: Workspace): string {
    if (workspaceMerging(ws)) return "Merging";
    if (ws.status === "ready") return "Workspace ready";
    if (ws.status === "creating") return "Creating workspace";
    if (ws.status === "error") return "Workspace error";
    if (ws.status === "deleting") return "Deleting workspace";
    if (ws.status === "deletion_failed") return "Deletion failed";
    return `Workspace ${ws.status}`;
  }

  function workspaceMerging(ws: Workspace): boolean {
    if (!ws.repo) return false;
    const number = ws.item_type === "pull_request" ? ws.item_number : (ws.associated_pr_number ?? 0);
    return number > 0 && detail.isPullMerging({
      provider: ws.repo.provider,
      platformHost: ws.repo.platform_host,
      repositoryKey: repositoryKeyFromWire(ws.repo),
      owner: ws.repo.owner,
      name: ws.repo.name,
      repoPath: ws.repo.repo_path,
    }, number);
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

  function agentStatePresentation(ws: Workspace): {
    label: "Working" | "Approval" | "Input" | "Done" | "Idle";
    status: StatusDotStatus;
    tone: "working" | "approval" | "input" | "done" | "idle";
  } | null {
    switch (ws.agent_state) {
      case "working":
        return { label: "Working", status: "working", tone: "working" };
      case "approval":
        return { label: "Approval", status: "waiting", tone: "approval" };
      case "input":
        return { label: "Input", status: "waiting", tone: "input" };
      case "done": {
        const version = doneStateVersion(ws);
        if (sortMode !== "agent-status" && version !== null && acknowledgedDoneStates[workspaceRowKey(ws)] === version) {
          return null;
        }
        return { label: "Done", status: "idle", tone: "done" };
      }
      case "idle":
        return sortMode === "agent-status" ? { label: "Idle", status: "idle", tone: "idle" } : null;
      default:
        return null;
    }
  }

  function loadDoneAcknowledgements(): Record<string, string> {
    try {
      const stored = window.sessionStorage.getItem(doneAcknowledgementsStorageKey);
      if (!stored) return {};
      const parsed: unknown = JSON.parse(stored);
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
        return {};
      }
      const acknowledgements: Record<string, string> = {};
      for (const [workspaceKey, version] of Object.entries(parsed)) {
        if (typeof version === "string" && version.trim()) {
          acknowledgements[workspaceKey] = version;
        }
      }
      return acknowledgements;
    } catch {
      return {};
    }
  }

  function saveDoneAcknowledgements(value: Record<string, string>): void {
    try {
      if (Object.keys(value).length === 0) {
        window.sessionStorage.removeItem(doneAcknowledgementsStorageKey);
      } else {
        window.sessionStorage.setItem(doneAcknowledgementsStorageKey, JSON.stringify(value));
      }
    } catch {
      // Storage is optional; the in-memory acknowledgement still applies.
    }
  }

  function doneStateVersion(ws: Workspace): string | null {
    if (ws.agent_state !== "done") return null;
    return ws.agent_state_updated_at?.trim() || null;
  }

  function reconcileDoneAcknowledgements(items: Workspace[]): void {
    let next = acknowledgedDoneStates;
    for (const ws of items) {
      const workspaceKey = workspaceRowKey(ws);
      const acknowledgedVersion = next[workspaceKey];
      if (!acknowledgedVersion || acknowledgedVersion === doneStateVersion(ws)) continue;
      if (next === acknowledgedDoneStates) next = { ...acknowledgedDoneStates };
      delete next[workspaceKey];
    }
    if (next === acknowledgedDoneStates) return;
    acknowledgedDoneStates = next;
    saveDoneAcknowledgements(next);
  }

  function openWorkspace(ws: Workspace): void {
    if (
      ws.status === "deleting" ||
      !workspaceOperationAvailable(ws, "workspaceRead") ||
      !workspaceOperationAvailable(ws, "terminalAttach")
    ) return;
    const doneVersion = doneStateVersion(ws);
    if (sortMode !== "agent-status" && doneVersion !== null) {
      acknowledgedDoneStates = {
        ...acknowledgedDoneStates,
        [workspaceRowKey(ws)]: doneVersion,
      };
      saveDoneAcknowledgements(acknowledgedDoneStates);
    }
    navigate(workspaceRoute(ws));
  }

  function itemState(ws: Workspace): WorkspaceItemState {
    return workspaceItemState(ws, ws.item_type === "issue" ? "issue" : "pr");
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

  function workspaceRepoFilterIdentity(ws: Workspace): RepoFilterIdentity {
    return {
      ...(ws.repo?.provider === undefined ? {} : { provider: ws.repo.provider }),
      platformHost: ws.platform_host,
      repoPath: ws.repo?.repo_path ?? `${ws.repo_owner}/${ws.repo_name}`,
      isGlob: false,
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

  function workspaceOperationAvailable(
    ws: Workspace,
    operation: "workspaceRead" | "workspaceWrite" | "terminalAttach",
  ): boolean {
    if (!isRemoteWorkspace(ws)) return true;
    return workspaceHost(ws)?.operationAvailability[operation]?.available === true;
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
    // Kata and unlinked ad-hoc workspaces have no provider item.
    if (ws.item_type === "kata_task") return null;
    const itemNumber = itemBubbleNumber(ws);
    if (itemNumber === null) return null;
    const provider = workspaceProvider(ws)?.toLowerCase();
    if (!provider) return null;
    return buildCanonicalProviderItemURL({
      provider,
      platformHost: ws.platform_host,
      owner: ws.repo_owner,
      name: ws.repo_name,
      repoPath: ws.repo?.repo_path ?? `${ws.repo_owner}/${ws.repo_name}`,
      number: itemNumber,
      itemType: ws.item_type === "issue" ? "issue" : "pr",
    }) ?? null;
  }

  function providerLabel(ws: Workspace): string {
    const provider = workspaceProvider(ws);
    if (!provider) return "Open item on provider";
    return `Open item on ${provider}`;
  }

  // Counts measured against the PR head ref (fork PRs) name no push or pull
  // target: the branch has no upstream, so branch sync would only fail.
  function canPush(ws: Workspace): boolean {
    if (ws.commits_vs_pr_head === true) return false;
    const ahead = ws.commits_ahead ?? 0;
    const behind = ws.commits_behind ?? 0;
    return ws.branch_upstream_missing === true || (ahead > 0 && behind === 0);
  }

  function canPull(ws: Workspace): boolean {
    if (ws.commits_vs_pr_head === true) return false;
    const ahead = ws.commits_ahead ?? 0;
    const behind = ws.commits_behind ?? 0;
    return behind > 0 && ahead === 0;
  }

  function syncActionDetail(ws: Workspace): string {
    if (ws.branch_upstream_missing === true) {
      return "";
    }
    if (canPush(ws)) {
      return `${ws.commits_ahead ?? 0} ahead`;
    }
    if (canPull(ws)) {
      return `${ws.commits_behind ?? 0} behind`;
    }
    return "";
  }

  function clearMultiSelection(): void {
    multiSelectedKeys = new Set();
    selectionAnchorKey = null;
  }

  function isMultiSelected(ws: Workspace): boolean {
    return multiSelectedKeys.has(workspaceRowKey(ws));
  }

  // The opened workspace starts a selection the way the focused file does in
  // a file manager, so shift-click from it selects the range in between.
  function openedRowKey(): string | null {
    const opened = renderedWorkspaces.find(isSelectedWorkspace);
    return opened ? workspaceRowKey(opened) : null;
  }

  function selectWorkspaceRange(ws: Workspace): void {
    const keys = renderedWorkspaces.map(workspaceRowKey);
    const clickedKey = workspaceRowKey(ws);
    const anchorKey = [selectionAnchorKey, openedRowKey()].find(
      (key) => key !== null && keys.includes(key),
    ) ?? clickedKey;
    const from = keys.indexOf(anchorKey);
    const to = keys.indexOf(clickedKey);
    multiSelectedKeys = new Set(keys.slice(Math.min(from, to), Math.max(from, to) + 1));
    selectionAnchorKey = anchorKey;
  }

  function toggleWorkspaceSelection(ws: Workspace): void {
    const key = workspaceRowKey(ws);
    const next = new Set(multiSelectedWorkspaces.map(workspaceRowKey));
    if (next.size === 0) {
      const opened = openedRowKey();
      if (opened !== null) next.add(opened);
    }
    if (next.has(key)) next.delete(key);
    else next.add(key);
    multiSelectedKeys = next;
    selectionAnchorKey = key;
  }

  // Shift extends a range and Cmd/Ctrl toggles a row; neither opens the
  // workspace. A plain activation drops the selection and opens the row.
  function activateRow(event: MouseEvent | KeyboardEvent, ws: Workspace): void {
    if (event.shiftKey) {
      selectWorkspaceRange(ws);
    } else if (event.metaKey || event.ctrlKey) {
      toggleWorkspaceSelection(ws);
    } else {
      clearMultiSelection();
      openWorkspace(ws);
    }
  }

  function openContextMenu(event: MouseEvent, ws: Workspace): void {
    event.preventDefault();
    event.stopPropagation();
    // Right-clicking outside the selection acts on that row alone.
    if (!isMultiSelected(ws)) clearMultiSelection();
    contextMenu = {
      ws,
      anchor: { x: event.clientX, y: event.clientY },
      row: event.currentTarget instanceof HTMLElement ? event.currentTarget : undefined,
    };
    contextMenuOpen = true;
  }

  function closeContextMenu(): void {
    contextMenuOpen = false;
  }

  function copyMenuText(value: string, successMessage: string): void {
    closeContextMenu();
    runtime.runCommand(
      Effect.promise(() => copyToClipboard(value)).pipe(
        Effect.flatMap((copied) =>
          Effect.sync(() => {
            showFlash(copied ? successMessage : "Could not copy to clipboard.", copied ? undefined : { tone: "danger" });
          }),
        ),
      ),
      {
        operation: "copy workspace text",
        safeContext: {},
        onFailure: () => showFlash("Could not copy to clipboard.", { tone: "danger" }),
      },
    );
  }

  function refreshWorkspaceStatus(ws: Workspace): void {
    if (workspaceActionsDisabled(ws)) return;
    closeContextMenu();
    const hostKey = ws.fleet_host_key;
    const refresh = hostKey
      ? executeOpaqueGeneratedApiRequest<unknown>("refresh remote workspace", (generatedClient, signal) =>
          (hostKey.startsWith("devbox:") ? generatedClient.DevboxesService.refreshDevboxWorkspace({ connectionId: hostKey.slice(7),  id: ws.id }, { signal }) : generatedClient.FleetService.refreshFleetWorkspace({ hostKey, id: ws.id }, { signal })),
        ).pipe(Effect.asVoid)
      : executeGeneratedApiRequest("refresh workspace", (generatedClient, signal) =>
          generatedClient.WorkspacesService.refreshWorkspace({ id: ws.id }, { signal }),
        ).pipe(Effect.asVoid);
    runtime.runCommand(refresh.pipe(Effect.tap(() => Effect.sync(requestApplicationWorkspaceRefresh))), {
      operation: "refresh workspace status",
      safeContext: { workspaceId: ws.id, remote: Boolean(ws.fleet_host_key) },
      onFailure: (failure) => showFlash(failureMessage(failure, "Refresh failed."), { tone: "danger" }),
    });
  }

  function workspaceActionMatches(
    ws: Workspace,
    action?: WorkspaceActionKind,
  ): boolean {
    return (
      workspaceAction !== null &&
      workspaceAction.workspaceKeys.includes(workspaceRowKey(ws)) &&
      (action === undefined || workspaceAction.action === action)
    );
  }

  function workspaceActionsDisabled(ws: Workspace): boolean {
    return ws.status === "deleting" || ws.status === "deletion_failed" ||
      !workspaceOperationAvailable(ws, "workspaceWrite") ||
      (isWorkspaceActionDisabled?.(ws.id, ws.fleet_host_key) ?? false);
  }

  function workspaceBusyLabel(ws: Workspace): string {
    if (!workspaceActionMatches(ws)) return "";
    if (workspaceAction?.action === "push") return "Pushing branch";
    if (workspaceAction?.action === "pull") return "Pulling branch";
    if (workspaceAction?.action === "reveal") return "Opening folder";
    if (workspaceAction?.action === "delete") return "Deleting workspace";
    if (workspaceAction?.action === "stop") return "Stopping sessions";
    return "";
  }

  function workspaceDeletable(ws: Workspace): boolean {
    return ws.status !== "deleting" &&
      workspaceOperationAvailable(ws, "workspaceWrite") &&
      !(isWorkspaceActionDisabled?.(ws.id, ws.fleet_host_key) ?? false);
  }

  function startWorkspaceAction(
    ws: Workspace,
    action: "push" | "pull" | "reveal" | "delete",
  ): boolean {
    if (
      workspaceAction !== null ||
      !(action === "delete" ? workspaceDeletable(ws) : !workspaceActionsDisabled(ws))
    ) return false;
    workspaceAction = { workspaceKeys: [workspaceRowKey(ws)], action };
    return true;
  }

  function finishWorkspaceAction(ws: Workspace): void {
    if (!workspaceAction || !workspaceActionMatches(ws)) return;
    const key = workspaceRowKey(ws);
    const remaining = workspaceAction.workspaceKeys.filter((candidate) => candidate !== key);
    workspaceAction = remaining.length > 0 ? { ...workspaceAction, workspaceKeys: remaining } : null;
  }

  function branchSyncRequest(ws: Workspace, action: "push" | "pull") {
    const hostKey = ws.fleet_host_key;
    return hostKey
      ? action === "push"
        ? executeOpaqueGeneratedApiRequest<unknown>("push remote workspace branch", (generatedClient, signal) =>
            (hostKey.startsWith("devbox:") ? generatedClient.DevboxesService.pushDevboxWorkspace({ connectionId: hostKey.slice(7),  id: ws.id }, { signal }) : generatedClient.FleetService.pushFleetWorkspaceBranch({ hostKey, id: ws.id }, { signal })),
          ).pipe(Effect.asVoid)
        : executeOpaqueGeneratedApiRequest<unknown>("pull remote workspace branch", (generatedClient, signal) =>
            (hostKey.startsWith("devbox:") ? generatedClient.DevboxesService.pullDevboxWorkspace({ connectionId: hostKey.slice(7),  id: ws.id }, { signal }) : generatedClient.FleetService.pullFleetWorkspaceBranch({ hostKey, id: ws.id }, { signal })),
          ).pipe(Effect.asVoid)
      : action === "push"
        ? executeGeneratedApiRequest("push workspace branch", (generatedClient, signal) =>
            generatedClient.WorkspacesService.pushWorkspaceBranch({ id: ws.id }, { signal }),
          ).pipe(Effect.asVoid)
        : executeGeneratedApiRequest("pull workspace branch", (generatedClient, signal) =>
            generatedClient.WorkspacesService.pullWorkspaceBranch({ id: ws.id }, { signal }),
          ).pipe(Effect.asVoid);
  }

  function syncWorkspaceBranch(ws: Workspace, action: "push" | "pull"): void {
    if (!startWorkspaceAction(ws, action)) return;
    const label = action === "push" ? "Push branch" : "Pull remote changes";
    runtime.runCommand(
      branchSyncRequest(ws, action).pipe(
        Effect.tap(() =>
          Effect.sync(() => {
            requestApplicationWorkspaceRefresh();
            closeContextMenu();
          }),
        ),
        Effect.ensuring(Effect.sync(() => finishWorkspaceAction(ws))),
      ),
      {
        operation: `${action} workspace branch`,
        safeContext: { workspaceId: ws.id, remote: Boolean(ws.fleet_host_key) },
        onFailure: (failure) => showFlash(failureMessage(failure, `${label} failed.`), { tone: "danger" }),
      },
    );
  }

  // Why a bulk pull skipped a workspace, or null when there was nothing to pull.
  function bulkPullProblem(failure: unknown): string | null {
    if (!(failure instanceof ApiProblemError)) return failureMessage(failure, "Pull failed.");
    if (problemConflictReason(failure.problem) === "in_sync") return null;
    if (failure.problem.code === ProblemCodes.worktreeDirty) return "has uncommitted changes";
    if (failure.problem.code === ProblemCodes.branchConflict) return "has local commits the remote does not have";
    return failureMessage(failure, "Pull failed.");
  }

  // Fast-forwards each workspace from its remote branch. The server fetches
  // first and refuses dirty or diverged worktrees, so nothing local is lost
  // and no confirmation is needed. One message reports the result.
  function pullWorkspaceBranches(targets: Workspace[]): void {
    closeContextMenu();
    if (targets.length === 0 || workspaceAction !== null) return;
    clearMultiSelection();
    workspaceAction = { workspaceKeys: targets.map(workspaceRowKey), action: "pull" };
    runtime.runCommand(
      Effect.forEach(
        targets,
        (ws) => Effect.result(branchSyncRequest(ws, "pull")).pipe(
          Effect.map((result) => ({ ws, result })),
          Effect.ensuring(Effect.sync(() => finishWorkspaceAction(ws))),
        ),
        { concurrency: 4 },
      ).pipe(
        Effect.flatMap((outcomes) =>
          Effect.sync(() => {
            requestApplicationWorkspaceRefresh();
            const pulled = outcomes.filter(({ result }) => Result.isSuccess(result)).length;
            const problems = outcomes.flatMap(({ ws, result }) => {
              const problem = Result.isFailure(result) ? bulkPullProblem(result.failure) : null;
              return problem === null ? [] : [`${displayName(ws)}: ${problem}.`];
            });
            const current = outcomes.length - pulled - problems.length;
            const counts = [
              `Pulled ${pulled} of ${outcomes.length} workspaces`,
              ...(current > 0 ? [`${current} already up to date`] : []),
            ].join("; ");
            showFlash(
              problems.length === 0 ? `${counts}.` : `${counts}. ${problems.length} skipped: ${problems.join(" ")}`,
              { tone: problems.length === 0 ? "success" : problems.length === outcomes.length ? "danger" : "warning" },
            );
          }),
        ),
      ),
      {
        operation: "pull workspace branches",
        safeContext: { count: targets.length },
        onFailure: (failure) => showFlash(failureMessage(failure, "Pull failed."), { tone: "danger" }),
      },
    );
  }

  // Fleet workspaces cannot take an agent handoff, so quick actions run
  // only in local and devbox workspaces that finished or are finishing setup.
  function quickActionAvailable(ws: Workspace): boolean {
    return !workspaceActionsDisabled(ws) &&
      ws.status !== "error" &&
      (!isRemoteWorkspace(ws) || ws.fleet_host_key?.startsWith("devbox:") === true);
  }

  function quickActionAgentLabel(action: QuickAction): string {
    return quickActionTarget(action, launchTargets)?.label ?? action.agent;
  }

  function chooseQuickAction(action: QuickAction, targets: Workspace[]): void {
    closeContextMenu();
    if (targets.length === 1) runQuickAction(action, targets);
    else quickActionConfirm = { action, targets };
  }

  // Launches finish in the background; one message reports them all.
  function runQuickAction(action: QuickAction, targets: Workspace[]): void {
    quickActionConfirm = null;
    clearMultiSelection();
    runQuickActionOnWorkspaces(
      runtime,
      targets.map((ws) => ({ id: ws.id, hostKey: ws.fleet_host_key ?? undefined, label: displayName(ws) })),
      action,
    );
  }

  function openStopSessionsDialog(targets: Workspace[]): void {
    stopSessionsConfirm = targets;
    closeContextMenu();
  }

  function workspaceListMessage(verb: string, targets: Workspace[]): string {
    const names = targets.slice(0, 3).map((ws) => `"${displayName(ws)}"`);
    const more = targets.length - names.length;
    return `${verb} ${names.join(", ")}${more > 0 ? ` and ${more} more` : ""}?`;
  }

  // Stops every launched agent, ACP chat, and shell like closing their tabs,
  // but keeps the workspaces. Rows show a busy state until their sessions
  // are stopped; one message then reports the result.
  function confirmStopSessions(): void {
    const targets = (stopSessionsConfirm ?? []).filter((ws) => !workspaceActionsDisabled(ws));
    if (targets.length === 0 || workspaceAction !== null) return;
    stopSessionsConfirm = null;
    clearMultiSelection();
    workspaceAction = { workspaceKeys: targets.map(workspaceRowKey), action: "stop" };
    stopSessionsInWorkspaces(
      runtime,
      targets.map((ws) => ({ workspaceId: ws.id, hostKey: ws.fleet_host_key ?? undefined, label: displayName(ws), ws })),
      ({ ws }) => {
        finishWorkspaceAction(ws);
        requestApplicationWorkspaceRefresh();
      },
    );
  }

  function revealWorkspacePath(ws: Workspace): void {
    if (!startWorkspaceAction(ws, "reveal")) return;
    const label = revealLabel(ws);
    const hostKey = ws.fleet_host_key;
    const command = hostKey
      ? executeOpaqueGeneratedApiRequest<unknown>("reveal remote workspace path", (generatedClient, signal) =>
          generatedClient.FleetService.revealFleetWorkspace({ hostKey, id: ws.id }, { signal }),
        ).pipe(Effect.asVoid)
      : executeGeneratedApiRequest("reveal workspace path", (generatedClient, signal) =>
          generatedClient.WorkspacesService.revealWorkspace({ id: ws.id }, { signal }),
        ).pipe(Effect.asVoid);
    runtime.runCommand(
      command.pipe(
        Effect.tap(() => Effect.sync(closeContextMenu)),
        Effect.ensuring(Effect.sync(() => finishWorkspaceAction(ws))),
      ),
      {
        operation: "reveal workspace path",
        safeContext: { workspaceId: ws.id, remote: Boolean(ws.fleet_host_key) },
        onFailure: (failure) => showFlash(failureMessage(failure, `${label} failed.`), { tone: "danger" }),
      },
    );
  }

  function openDeleteWorkspaceDialog(ws: Workspace, force = false): void {
    deleteConfirmWorkspace = ws;
    deleteConfirmForce = force;
    closeContextMenu();
  }

  function closeDeleteWorkspaceDialog(): void {
    if (deleteConfirmWorkspace && workspaceActionMatches(deleteConfirmWorkspace, "delete")) return;
    deleteConfirmWorkspace = null;
    deleteConfirmForce = false;
  }

  function deleteFailureMessage(failure: unknown): string {
    const problem = failure instanceof ApiProblemError ? failure.problem : undefined;
    const fallback = problem?.status === 409
      ? "Workspace has uncommitted changes. Open it to force delete."
      : "Delete failed.";
    return failureMessage(failure, fallback);
  }

  // Deletes one workspace and evicts it from this list. Callers mark the
  // workspace pending and reserve its action key before running this.
  function deleteWorkspaceFromList(ws: Workspace, force: boolean) {
    const hostKey = ws.fleet_host_key;
    const command = hostKey
      ? executeOpaqueGeneratedApiRequest<unknown>("delete remote workspace", (generatedClient, signal) =>
          (hostKey.startsWith("devbox:") ? generatedClient.DevboxesService.deleteDevboxWorkspace({ connectionId: hostKey.slice(7),  id: ws.id },
            force ? { force: true } : undefined,
            { signal },
          ) : generatedClient.FleetService.deleteFleetWorkspace(
            { hostKey, id: ws.id },
            force ? { force: true } : undefined,
            { signal },
          )),
        ).pipe(Effect.asVoid)
      : executeGeneratedApiRequest("delete workspace", (generatedClient, signal) =>
          generatedClient.WorkspacesService.deleteWorkspace(
            { id: ws.id },
            force ? { force: true } : undefined,
            { signal },
          ),
        ).pipe(Effect.asVoid);
    return command.pipe(
      Effect.tap(() => removeWorkspaceListEntry(ws.id, hostKey)),
      Effect.tap(() =>
        Effect.sync(() => {
          workspaces = workspaces.filter(
            (candidate) => candidate.id !== ws.id || candidate.fleet_host_key !== hostKey,
          );
          // Report the deletion regardless of selection so a hosting shell's
          // inline claim cannot briefly reclaim a workspace this list destroyed.
          onWorkspaceDeleted?.(
            ws.id,
            hostKey,
            ws.repo
              ? {
                  provider: ws.repo.provider,
                  platformHost: ws.repo.platform_host,
                  owner: ws.repo.owner,
                  name: ws.repo.name,
                  repoPath: ws.repo.repo_path,
                  number: ws.item_number,
                  itemType: ws.item_type,
                }
              : undefined,
          );
        }),
      ),
      Effect.ensuring(
        Effect.sync(() => {
          onWorkspaceDeletePendingChange?.(ws.id, hostKey, false);
          finishWorkspaceAction(ws);
        }),
      ),
    );
  }

  function confirmDeleteWorkspaceFromList(): void {
    const ws = deleteConfirmWorkspace;
    if (!ws || !startWorkspaceAction(ws, "delete")) return;
    onWorkspaceDeletePendingChange?.(ws.id, ws.fleet_host_key, true);
    runtime.runCommand(
      deleteWorkspaceFromList(ws, deleteConfirmForce).pipe(
        Effect.tap(() =>
          Effect.sync(() => {
            requestApplicationWorkspaceRefresh();
            closeContextMenu();
            if (isSelectedWorkspace(ws)) navigate("/workspaces");
          }),
        ),
        Effect.ensuring(
          Effect.sync(() => {
            deleteConfirmWorkspace = null;
            deleteConfirmForce = false;
          }),
        ),
      ),
      {
        operation: "delete workspace",
        safeContext: { workspaceId: ws.id, remote: Boolean(ws.fleet_host_key) },
        onFailure: (failure) => showFlash(deleteFailureMessage(failure), { tone: "danger" }),
      },
    );
  }

  function openBulkDeleteDialog(targets: Workspace[]): void {
    bulkDeleteConfirm = targets;
    closeContextMenu();
  }

  function closeBulkDeleteDialog(): void {
    bulkDeleteConfirm = null;
  }

  // Deletes without force, so the server keeps any workspace with
  // uncommitted changes; those are reported rather than discarded. The
  // dialog closes at once and each row disappears as its delete finishes.
  function confirmBulkDelete(): void {
    const targets = (bulkDeleteConfirm ?? []).filter(workspaceDeletable);
    if (targets.length === 0 || workspaceAction !== null) return;
    bulkDeleteConfirm = null;
    clearMultiSelection();
    workspaceAction = { workspaceKeys: targets.map(workspaceRowKey), action: "delete" };
    for (const ws of targets) onWorkspaceDeletePendingChange?.(ws.id, ws.fleet_host_key, true);
    runtime.runCommand(
      Effect.forEach(
        targets,
        (ws) => Effect.result(deleteWorkspaceFromList(ws, false)).pipe(
          Effect.map((result) => ({ ws, result })),
        ),
        { concurrency: 4 },
      ).pipe(
        Effect.flatMap((outcomes) =>
          Effect.sync(() => {
            requestApplicationWorkspaceRefresh();
            const failures = outcomes.flatMap(({ ws, result }) =>
              Result.isFailure(result) ? [`${displayName(ws)}: ${deleteFailureMessage(result.failure)}`] : [],
            );
            if (outcomes.some(({ ws, result }) => Result.isSuccess(result) && isSelectedWorkspace(ws))) {
              navigate("/workspaces");
            }
            if (failures.length > 0) {
              showFlash(
                `Could not delete ${failures.length} of ${targets.length} workspaces. ${failures.join(" ")}`,
                { tone: "danger" },
              );
            }
          }),
        ),
      ),
      {
        operation: "delete workspaces",
        safeContext: { count: targets.length },
        onFailure: (failure) => showFlash(failureMessage(failure, "Delete failed."), { tone: "danger" }),
      },
    );
  }

  function openProviderItem(ws: Workspace): void {
    const url = providerItemURL(ws);
    closeContextMenu();
    if (!url) {
      showFlash("Provider URL is not available for this workspace.", { tone: "danger" });
      return;
    }
    window.open(url, "_blank", "noopener,noreferrer");
  }

  function copyProviderItemURL(ws: Workspace): void {
    const url = providerItemURL(ws);
    if (!url) {
      closeContextMenu();
      showFlash("Provider URL is not available for this workspace.", { tone: "danger" });
      return;
    }
    copyMenuText(url, "Copied item URL.");
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

  // Preselects the repository of the workspace the user is already looking at,
  // which is nearly always the repo they want to start more work in.
  function newWorkspaceSeedRepo(): NewWorkspaceRepoSeed | undefined {
    const current = workspaces.find(isSelectedWorkspace);
    const provider = current?.repo?.provider;
    if (!current || !provider) return undefined;
    return {
      provider,
      platformHost: current.platform_host,
      repositoryKey: repositoryKeyFromWire(current.repo),
      owner: current.repo_owner,
      name: current.repo_name,
    };
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
    if (!workspaceOperationAvailable(ws, "workspaceRead")) return;
    const tab =
      ws.item_type === "kata_task"
        ? "kata"
        : ws.item_type === "issue"
          ? "issue"
          : "pr";

    if (onOpenItemSidebar) {
      onOpenItemSidebar(ws.id, tab, ws.fleet_host_key);
      return;
    }
    navigate(workspaceRoute(ws));
  }

  onMount(() => {
    const workspaceEvents = workspaceEventStream(eventsStore.subscribeWorkspaceEvents).pipe(
      Stream.filter((signal) =>
        signal._tag === "Status" ||
        signal._tag === "Open" ||
        signal._tag === "ReconnectStale" ||
        signal._tag === "Associated"
      ),
    );
    const execution = runtime.runCommand(
      Effect.scoped(
        Effect.gen(function* () {
          const workflow = yield* WorkspaceListWorkflow;
          if (workflow.snapshot) applySnapshot(workflow.snapshot);
          requestApplicationWorkspaceRefresh = workflow.request;
          yield* workflow.claim(workspaceRefreshOwner, refreshWorkspaces.request);
          yield* workspaceListLifecycle({
            refreshWorkspaces,
            refreshFleet,
            workspaceEvents,
          });
        }),
      ),
      {
        operation: "run workspace list lifecycle",
        safeContext: {},
        onFailure: (failure) => {
          if (workspaces.length === 0) workspaceListStatus = "retrying";
          console.error("Workspace list lifecycle stopped", failure);
        },
      },
    );
    return execution.interrupt;
  });
</script>

<div class="workspace-list-sidebar">
  <div class="sidebar-header">
    <span class="sidebar-header-label">Workspaces</span>
    <span class="sidebar-header-count">{sidebarCountLabel}</span>
    <IconButton
      class="workspace-new-button"
      size="sm"
      ariaLabel="New workspace"
      title="New workspace"
      onclick={() => openNewWorkspaceDialog(newWorkspaceSeedRepo())}
    >
      <PlusIcon size="13" strokeWidth="2.2" aria-hidden="true" />
    </IconButton>
    <div class="workspace-sort">
      <ItemFilters
        panelLabel="Workspace filters"
        title="Filter workspaces"
        align="end"
        {author}
        {authorOptions}
        authorLoading={workspaceListStatus !== "loaded" && workspaces.length === 0}
        compact
        detail={sortLabel}
        badgeCount={viewBadgeCount}
        sections={viewSections}
        onAuthorSelect={(value) => { author = value; }}
        onReset={resetFilters}
      />
    </div>
    {#if isSidebarToggleEnabled && onCollapseSidebar}
      <!-- No --push here: .workspace-sort's auto margin already
           claims the free space, and a second auto margin would
           split it and strand the sort trigger mid-header. -->
      <SidebarToggle
        state="expanded"
        label="Workspaces sidebar"
        onclick={onCollapseSidebar}
        class="kit-sidebar-toggle--compact"
      />
    {/if}
  </div>
  <div class="workspace-filter">
    <QuerySearchInput
      value={searchQuery}
      size="sm"
      block
      placeholder="Filter workspaces"
      ariaLabel="Filter workspaces"
      oninput={updateSearch}
    />
  </div>
  {#if fleetDegraded}
    <section class="fleet-status" aria-label="Fleet hosts">
      <div class="fleet-status-heading">
        <span class="fleet-status-title">Fleet</span>
        <span class="fleet-status-count">degraded</span>
      </div>
      {#if fleetError}
        <p class="fleet-status-error">{fleetError}</p>
      {:else}
        {#each Object.entries(fleetPeerErrors) as [hostKey, message] (hostKey)}
          <p class="fleet-status-error">
            <span>{fleetHostName(hostKey)} degraded</span>: {message}
          </p>
        {/each}
      {/if}
    </section>
  {/if}
  <ScrollBox class="sidebar-list" label="Workspaces">
    {#snippet children()}
    {#if pinnedWorkspaces.length > 0}
      <GroupedSidebarSection
        label="Pinned"
        count={pinnedWorkspaces.length}
        collapsed={!hasSearchQuery && pinsCollapsed}
        onclick={() => { pinsCollapsed = !pinsCollapsed; }}
      >
        {#snippet leading()}
          <PinIcon size={12} aria-hidden="true" />
        {/snippet}
        {#each pinnedWorkspaces as ws (workspaceRowKey(ws))}
          {@render workspaceRow(ws, true)}
        {/each}
      </GroupedSidebarSection>
    {/if}
    {#if sortMode === "repo"}
    {#each grouped as { key: repoKey, items } (repoKey)}
      {@const collapsed =
        !hasSearchQuery && collapsedGroups.includes(repoKey)}
      <GroupedSidebarSection
        label={repoLabel(items[0]!)}
        count={items.length}
        {collapsed}
        onclick={() => toggleGroup(repoKey)}
      >
        {#snippet leading()}
          {#if showProviderIcons && workspaceProvider(items[0]!)}
            <ProviderIcon
              provider={workspaceProvider(items[0]!)!}
              size={14}
              class="group-provider-icon"
            />
          {/if}
        {/snippet}
        {#each items as ws (workspaceRowKey(ws))}
          {@render workspaceRow(ws, false)}
        {/each}
      </GroupedSidebarSection>
    {/each}
    {:else}
      {#each sortedFlat as ws (workspaceRowKey(ws))}
        {@render workspaceRow(ws, true)}
      {/each}
    {/if}

    {#snippet worktreeDirtyIndicator()}
      <span
        class="worktree-dirty"
        title="Dirty worktree"
        role="img"
        aria-label="Dirty worktree"
      >
        <PencilIcon size={10} strokeWidth={2.2} aria-hidden="true" />
      </span>
    {/snippet}

    {#snippet workspaceRow(ws: Workspace, showRepo: boolean)}
          {@const adds = ws.mr_additions}
          {@const dels = ws.mr_deletions}
          {@const ahead = ws.commits_ahead ?? 0}
          {@const behind = ws.commits_behind ?? 0}
          {@const showPush = ahead > 0 || behind > 0}
          {@const agentState = agentStatePresentation(ws)}
          {@const sortTimestamp = workspaceListSortTimestamp(ws, sortMode)}
          {@const workspaceReadable = workspaceOperationAvailable(ws, "workspaceRead") && workspaceOperationAvailable(ws, "terminalAttach")}
          <div
            class={["ws-row", {
              selected: isSelectedWorkspace(ws),
              "multi-selected": isMultiSelected(ws),
              "context-target": contextMenuOpen && contextMenu !== null && workspaceRowKey(contextMenu.ws) === workspaceRowKey(ws),
            }]}
            bind:this={rowEls[workspaceRowKey(ws)]}
            aria-pressed={multiSelectedKeys.size > 0 ? isMultiSelected(ws) : undefined}
            onmousedown={(e) => {
              // Shift-click selects a range of rows, not the text between them.
              if (e.shiftKey) e.preventDefault();
            }}
            onclick={(e) => {
              // The PR/issue bubble is a focusable child button; let
              // its own click handler run without the row also
              // navigating to the terminal route.
              if (e.target !== e.currentTarget &&
                e.target instanceof Element &&
                e.target.closest(".item-bubble")) {
                return;
              }
              activateRow(e, ws);
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
                activateRow(e, ws);
              } else if (e.key === "Escape" && multiSelectedKeys.size > 0) {
                e.preventDefault();
                clearMultiSelection();
              }
            }}
            oncontextmenu={(e) => openContextMenu(e, ws)}
            aria-disabled={!workspaceReadable}
            tabindex={workspaceReadable ? 0 : -1}
            role="button"
          >
            <div class="ws-row-text">
              <div class="ws-row-title">
                <StatusDot
                  animated
                  status={workspaceStatus(ws)}
                  label={workspaceStatusLabel(ws)}
                  size={6}
                />
                <span class="ws-name">{displayName(ws)}</span>
                {#if showWorkspaceHostBadges}
                  <span
                    class="workspace-host-badge"
                    title={`Runs on ${workspaceHostName(ws)}`}
                  >{workspaceHostName(ws)}{ws.fleet_host_key?.startsWith("devbox:") ? " · Devbox" : ""}</span>
                {/if}
                {#if workspaceMerging(ws)}
                  <span class="workspace-lifecycle-state workspace-lifecycle-state--merging">Merging</span>
                {:else if ws.status === "deleting" || ws.status === "deletion_failed"}
                  <span
                    class={["workspace-lifecycle-state", `workspace-lifecycle-state--${ws.status}`]}
                    title={ws.error_message ?? workspaceStatusLabel(ws)}
                  >{workspaceStatusLabel(ws)}</span>
                {/if}
                {#if agentState}
                  <span
                    class={["agent-state", `agent-state--${agentState.tone}`]}
                    title={`Agent ${agentState.label.toLowerCase()}`}
                  >
                    <StatusDot
                      animated
                      status={agentState.status}
                      label={`Agent ${agentState.label.toLowerCase()}`}
                      size={6}
                    />
                    <span>{agentState.label}</span>
                  </span>
                {:else if workspaceActionMatches(ws)}
                  <StatusDot status="working" label={workspaceBusyLabel(ws)} size={6} animated />
                {:else if ws.agent_state == null && ws.tmux_working}
                  <StatusDot status="working" label={workingTitle(ws)} size={6} animated />
                {/if}
              </div>
              <div class="ws-row-meta">
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
                {#if ws.worktree_dirty}
                  <!-- The dirty pencil follows the branch chip, not the aside
                       column: there it would either stack as a third line under
                       the bubble and time, making dirty rows taller, or widen
                       the aside and shift the text column. Sitting before the
                       push and diff chips, it borrows width from the branch
                       name when the line is full, so those chips keep the same
                       position as on a clean row. -->
                  {@render worktreeDirtyIndicator()}
                {/if}
                {#if showPush}
                  <span
                    class="push-state"
                    title={`${ahead} ahead, ${behind} behind ${ws.commits_vs_pr_head === true ? "PR head" : "upstream"}`}
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
            <div class="ws-row-aside">
              <!-- Keep the item column stable when a workspace has no linked
                   provider item. The empty slot is layout only, never a
                   disabled or misleading #0 control. -->
              {#if hasItemBubble(ws)}
                <button
                  class={[
                    "item-bubble",
                    itemState(ws),
                    ws.item_type === "kata_task" && "item-bubble--kata",
                  ]}
                  style:--item-accent={workspaceItemAccent[itemState(ws)]}
                  onclick={(e) => handleItemBubbleClick(e, ws)}
                  disabled={!workspaceOperationAvailable(ws, "workspaceRead")}
                  onkeydown={(e) => {
                    // Stop Enter/Space from bubbling to the row,
                    // since the row's keyboard handler also navigates.
                    if (e.key === "Enter" || e.key === " ") {
                      e.stopPropagation();
                    }
                  }}
                  title={itemBubbleTitle(ws)}
                >
                  {itemBubbleLabel(ws)}
                </button>
              {:else}
                <span class="item-bubble-slot" aria-hidden="true"></span>
              {/if}
              {#if sortTimestamp}
                <time
                  class="workspace-sort-time"
                  datetime={sortTimestamp.at}
                  title={`${sortTimestamp.label}: ${formatTimestamp(sortTimestamp.at)}`}
                  aria-label={`${sortTimestamp.label}: ${formatRelativeTime(sortTimestamp.at)}`}
                >{formatRelativeTime(sortTimestamp.at)}</time>
              {/if}
            </div>
          </div>
          <SidebarTitlePopover
            target={rowEls[workspaceRowKey(ws)] ?? undefined}
            title={displayName(ws)}
            repository={repoLabel(ws)}
            branch={shortBranch(ws.git_head_ref)}
            truncationSelector=".ws-name"
          />
    {/snippet}
    {#if workspaceListStatus === "loading" && workspaces.length === 0}
      <p class="filter-empty">Loading workspaces...</p>
    {:else if workspaceListStatus === "retrying" && workspaces.length === 0}
      <p class="filter-empty">Still loading workspaces. Retrying...</p>
    {:else if visibleWorkspaces.length === 0 && (hasSearchQuery || author || selectedRepoValues.size > 0)}
      <p class="filter-empty">No workspaces match.</p>
    {:else if visibleWorkspaces.length === 0}
      <p class="filter-empty">No workspaces yet.</p>
    {/if}
    {/snippet}
  </ScrollBox>

</div>

{#snippet quickActionItems(targets: Workspace[], selectedCount: number)}
  {#if quickActions.length > 0}
    <MenuSeparator />
    {#each quickActions as action (action.label)}
      {@const reason = quickActionDisabledReason(action, launchTargets) ||
        (targets.length === 0 ? "Not available in these workspaces" : "")}
      <MenuItem disabled={reason !== ""} textValue={action.label} onselect={() => chooseQuickAction(action, targets)}>
        {#snippet icon()}<ZapIcon size={14} />{/snippet}
        {#snippet trailing()}{reason || quickActionAgentLabel(action)}{/snippet}
        {selectedCount === 1
          ? action.label
          : targets.length === selectedCount
            ? `${action.label} in ${selectedCount} workspaces...`
            : `${action.label} in ${targets.length} of ${selectedCount} workspaces...`}
      </MenuItem>
    {/each}
  {/if}
{/snippet}

<Menu bind:open={contextMenuOpen} anchor={contextMenu?.anchor} returnFocus={contextMenu?.row} class="workspace-context-menu-root">
  <MenuContent ariaLabel="Workspace actions" class="workspace-context-menu">
    {#if contextMenu && multiSelectedWorkspaces.length > 1 && isMultiSelected(contextMenu.ws)}
      {@const selectedCount = multiSelectedWorkspaces.length}
      {@const deletable = multiSelectedWorkspaces.filter(workspaceDeletable)}
      {@const writable = multiSelectedWorkspaces.filter((ws) => !workspaceActionsDisabled(ws))}
      {@const quickActionable = multiSelectedWorkspaces.filter(quickActionAvailable)}
      <MenuItem
        disabled={workspaceAction !== null || writable.length === 0}
        onselect={() => openStopSessionsDialog(writable)}
      >
        {#snippet icon()}<SquareIcon size={14} />{/snippet}
        {writable.length === selectedCount
          ? `Stop sessions in ${selectedCount} workspaces...`
          : `Stop sessions in ${writable.length} of ${selectedCount} workspaces...`}
      </MenuItem>
      <MenuItem
        disabled={workspaceAction !== null || writable.length === 0}
        onselect={() => pullWorkspaceBranches(writable)}
      >
        {#snippet icon()}<ArrowDownIcon size={14} />{/snippet}
        {writable.length === selectedCount
          ? `Pull remote changes in ${selectedCount} workspaces`
          : `Pull remote changes in ${writable.length} of ${selectedCount} workspaces`}
      </MenuItem>
      {@render quickActionItems(quickActionable, selectedCount)}
      <MenuSeparator />
      <MenuItem
        disabled={workspaceAction !== null || deletable.length === 0}
        tone="danger"
        onselect={() => openBulkDeleteDialog(deletable)}
      >
        {#snippet icon()}<Trash2Icon size={14} />{/snippet}
        {deletable.length === selectedCount
          ? `Delete ${selectedCount} workspaces...`
          : `Delete ${deletable.length} of ${selectedCount} workspaces...`}
      </MenuItem>
      <MenuSeparator />
      <MenuItem onselect={() => { clearMultiSelection(); closeContextMenu(); }}>
        Clear selection
      </MenuItem>
    {:else if contextMenu}
      {@const menuWorkspace = contextMenu.ws}
      {@const localWorkspace = !isRemoteWorkspace(menuWorkspace)}
      {@const itemURL = providerItemURL(menuWorkspace)}
      {@const actionBusy = workspaceAction !== null}
      {@const actionDisabled = actionBusy || workspaceActionsDisabled(menuWorkspace)}
      {@const deleteDisabled = actionBusy || !workspaceOperationAvailable(menuWorkspace, "workspaceWrite") || (isWorkspaceActionDisabled?.(menuWorkspace.id, menuWorkspace.fleet_host_key) ?? false)}
      <MenuItem onselect={() => void togglePin(menuWorkspace)}>
        {#snippet icon()}
          {#if isPinned(menuWorkspace)}<PinOffIcon size={14} />{:else}<PinIcon size={14} />{/if}
        {/snippet}
        {isPinned(menuWorkspace) ? "Unpin workspace" : "Pin workspace"}
      </MenuItem>

      <MenuSeparator />
      {#if canPush(menuWorkspace)}
        <MenuItem disabled={actionDisabled} onselect={() => void syncWorkspaceBranch(menuWorkspace, "push")}>
          {#snippet icon()}<ArrowUpIcon size={14} />{/snippet}
          {workspaceActionMatches(menuWorkspace, "push") ? "Pushing..." : "Push branch"}
          {#snippet trailing()}{syncActionDetail(menuWorkspace)}{/snippet}
        </MenuItem>
      {:else if canPull(menuWorkspace)}
        <MenuItem disabled={actionDisabled} onselect={() => void syncWorkspaceBranch(menuWorkspace, "pull")}>
          {#snippet icon()}<ArrowDownIcon size={14} />{/snippet}
          {workspaceActionMatches(menuWorkspace, "pull") ? "Pulling..." : "Pull remote changes"}
          {#snippet trailing()}{syncActionDetail(menuWorkspace)}{/snippet}
        </MenuItem>
      {/if}
      <MenuItem disabled={actionDisabled} onselect={() => void refreshWorkspaceStatus(menuWorkspace)}>
        {#snippet icon()}<RefreshCwIcon size={14} />{/snippet}
        Refresh git status
      </MenuItem>

      <MenuSeparator />
      <MenuItem
        disabled={actionBusy}
        onselect={() => void copyMenuText(shortBranch(menuWorkspace.git_head_ref), "Copied branch name.")}
      >
        {#snippet icon()}<CopyIcon size={14} />{/snippet}
        Copy branch name
      </MenuItem>
      {#if localWorkspace}
        <MenuItem disabled={actionBusy} onselect={() => void copyMenuText(menuWorkspace.worktree_path, "Copied worktree path.")}>
          {#snippet icon()}<CopyIcon size={14} />{/snippet}
          Copy worktree path
        </MenuItem>
        {#if !menuWorkspace.fleet_host_key?.startsWith("devbox:")}
          <MenuItem disabled={actionDisabled} onselect={() => void revealWorkspacePath(menuWorkspace)}>
            {#snippet icon()}<FolderOpenIcon size={14} />{/snippet}
            {workspaceActionMatches(menuWorkspace, "reveal") ? "Opening..." : revealLabel(menuWorkspace)}
          </MenuItem>
        {/if}
      {/if}

      {#if itemURL}
        <MenuSeparator />
        <MenuItem disabled={actionBusy} onselect={() => openProviderItem(menuWorkspace)}>
          {#snippet icon()}<ExternalLinkIcon size={14} />{/snippet}
          {providerLabel(menuWorkspace)}
        </MenuItem>
        <MenuItem disabled={actionBusy} onselect={() => copyProviderItemURL(menuWorkspace)}>
          {#snippet icon()}<LinkIcon size={14} />{/snippet}
          Copy item URL
        </MenuItem>
      {/if}

      {#if quickActionAvailable(menuWorkspace)}
        {@render quickActionItems([menuWorkspace], 1)}
      {/if}
      <MenuSeparator />
      <MenuItem
        disabled={actionDisabled}
        onselect={() => openStopSessionsDialog([menuWorkspace])}
      >
        {#snippet icon()}<SquareIcon size={14} />{/snippet}
        {workspaceActionMatches(menuWorkspace, "stop") ? "Stopping sessions..." : "Stop all sessions..."}
      </MenuItem>
      <MenuItem
        disabled={deleteDisabled || menuWorkspace.status === "deleting"}
        tone="danger"
        onselect={() => openDeleteWorkspaceDialog(menuWorkspace)}
      >
        {#snippet icon()}<Trash2Icon size={14} />{/snippet}
        {workspaceActionMatches(menuWorkspace, "delete") ? "Deleting..." : menuWorkspace.status === "deletion_failed" ? "Retry deletion..." : "Delete workspace..."}
      </MenuItem>
      {#if menuWorkspace.status === "deletion_failed"}
        <MenuItem disabled={deleteDisabled} tone="danger" onselect={() => openDeleteWorkspaceDialog(menuWorkspace, true)}>
          {#snippet icon()}<Trash2Icon size={14} />{/snippet}
          Force delete workspace...
        </MenuItem>
      {/if}
    {/if}
  </MenuContent>
</Menu>

<ConfirmDialog
  open={deleteConfirmWorkspace !== null && hostVisible}
  title={deleteConfirmForce ? "Force delete workspace?" : deleteConfirmWorkspace?.status === "deletion_failed" ? "Retry workspace deletion?" : "Delete workspace?"}
  message={deleteConfirmWorkspace
    ? `Delete workspace "${displayName(deleteConfirmWorkspace)}"?`
    : ""}
  hint={deleteConfirmForce
    ? "This discards uncommitted changes and removes the managed worktree and runtime sessions."
    : deleteConfirmWorkspace?.error_message ?? "This removes its managed worktree and runtime sessions."}
  confirmLabel={deleteConfirmForce ? "Force delete workspace" : deleteConfirmWorkspace?.status === "deletion_failed" ? "Retry deletion" : "Delete workspace"}
  pendingLabel="Deleting…"
  busy={deleteConfirmBusy}
  tone="danger"
  frameId="workspace-sidebar-delete"
  onCancel={closeDeleteWorkspaceDialog}
  onConfirm={() => void confirmDeleteWorkspaceFromList()}
/>

<ConfirmDialog
  open={bulkDeleteConfirm !== null && hostVisible}
  title={`Delete ${bulkDeleteConfirm?.length ?? 0} workspaces?`}
  message={bulkDeleteConfirm ? workspaceListMessage("Delete", bulkDeleteConfirm) : ""}
  hint="This removes their managed worktrees and runtime sessions. Workspaces with uncommitted changes are kept."
  confirmLabel={`Delete ${bulkDeleteConfirm?.length ?? 0} workspaces`}
  tone="danger"
  frameId="workspace-sidebar-bulk-delete"
  onCancel={closeBulkDeleteDialog}
  onConfirm={() => void confirmBulkDelete()}
/>

<ConfirmDialog
  open={stopSessionsConfirm !== null && hostVisible}
  title={stopSessionsConfirm?.length === 1
    ? "Stop all sessions?"
    : `Stop sessions in ${stopSessionsConfirm?.length ?? 0} workspaces?`}
  message={stopSessionsConfirm ? workspaceListMessage("Stop every session in", stopSessionsConfirm) : ""}
  hint="Running agents, chats, and shells are stopped and their tabs close. The workspaces, their worktrees, and their terminals stay."
  confirmLabel={stopSessionsConfirm?.length === 1
    ? "Stop sessions"
    : `Stop sessions in ${stopSessionsConfirm?.length ?? 0} workspaces`}
  tone="danger"
  frameId="workspace-sidebar-stop-sessions"
  onCancel={() => { stopSessionsConfirm = null; }}
  onConfirm={() => void confirmStopSessions()}
/>

<ConfirmDialog
  open={quickActionConfirm !== null && hostVisible}
  title={`Run "${quickActionConfirm?.action.label ?? ""}" in ${quickActionConfirm?.targets.length ?? 0} workspaces?`}
  message={quickActionConfirm
    ? workspaceListMessage(`Run "${quickActionConfirm.action.label}" in`, quickActionConfirm.targets)
    : ""}
  hint={quickActionConfirm
    ? `${quickActionAgentLabel(quickActionConfirm.action)} starts in each workspace with the action's prompt.`
    : ""}
  confirmLabel={`Run in ${quickActionConfirm?.targets.length ?? 0} workspaces`}
  frameId="workspace-sidebar-quick-action"
  onCancel={() => { quickActionConfirm = null; }}
  onConfirm={() => { if (quickActionConfirm) runQuickAction(quickActionConfirm.action, quickActionConfirm.targets); }}
/>

<style>
  .workspace-list-sidebar {
    width: 100%;
    height: 100%;
    background: var(--sidebar-list-bg, var(--bg-surface));
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
    gap: var(--space-3);
    min-height: 40px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-surface);
    flex-shrink: 0;
  }

  .sidebar-header-label {
    font-size: var(--font-size-xs);
    font-weight: 700;
    letter-spacing: var(--letter-spacing-label, 0.08em);
    text-transform: var(--label-transform, uppercase);
    color: var(--text-muted);
  }

  .sidebar-header-count {
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    opacity: 0.7;
  }

  .workspace-filter {
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
    flex-shrink: 0;
  }

  .workspace-sort {
    /* Claims the header's free space so the sort trigger (and the
     * collapse toggle after it) sit flush right. */
    margin-left: auto;
    min-width: 0;
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
    letter-spacing: var(--letter-spacing-label, 0.08em);
    text-transform: var(--label-transform, uppercase);
    color: var(--text-muted);
  }

  .fleet-status-count {
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    color: var(--accent-amber);
  }

  .fleet-status-error {
    margin: 0;
    color: var(--accent-amber);
    font-size: var(--font-size-xs);
    line-height: 1.35;
  }

  .filter-empty {
    margin: 14px 12px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    line-height: 1.4;
  }

  :global(.group-provider-icon) {
    color: var(--text-secondary);
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
    gap: var(--space-4);
    padding: var(--sidebar-row-padding, 10px 12px);
    border-bottom: 1px solid var(--sidebar-list-border-muted, var(--border-muted));
    border-start-start-radius: 0;
    border-end-start-radius: 0;
    border-left: 3px solid transparent;
    background: var(--sidebar-row-bg, var(--bg-surface));
    cursor: pointer;
    position: relative;
    outline: none;
  }

  .ws-row:hover {
    background: var(--sidebar-row-hover-bg, var(--bg-surface-hover));
  }

  .ws-row:focus-visible {
    background: var(--bg-surface-hover);
    box-shadow: inset 0 0 0 1px var(--accent-blue);
  }

  .ws-row.selected {
    background: var(--bg-row-selected, var(--bg-surface));
    border-left-color: var(--accent-blue);
  }

  .ws-row.selected:hover {
    background: var(--sidebar-row-hover-bg, var(--bg-surface-hover));
  }

  .ws-row.multi-selected,
  .ws-row.multi-selected:hover {
    background: color-mix(in srgb, var(--accent-blue) 14%, var(--sidebar-row-bg, var(--bg-surface)));
  }

  /* Marks the row a context menu acts on, as desktop file lists do. */
  .ws-row.context-target {
    box-shadow: inset 0 0 0 1px var(--accent-blue);
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

  .workspace-host-badge {
    flex: 0 1 auto;
    min-width: 0;
    max-width: 38%;
    overflow: hidden;
    padding: 1px 5px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-subtle);
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    line-height: 1.35;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .workspace-lifecycle-state {
    flex-shrink: 0;
    font-size: var(--font-size-xs);
    font-weight: 600;
    line-height: 1;
  }

  .workspace-lifecycle-state--deleting,
  .workspace-lifecycle-state--merging {
    color: var(--accent-blue);
  }

  .workspace-lifecycle-state--deletion_failed {
    color: var(--accent-red);
  }

  .agent-state {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
    font-size: var(--font-size-xs);
    font-weight: 600;
    line-height: 1;
  }

  .agent-state--working {
    color: var(--accent-green);
  }

  .agent-state--approval {
    color: var(--accent-amber);
    --status-waiting: var(--accent-amber);
  }

  .agent-state--input {
    color: var(--accent-purple);
    --status-waiting: var(--accent-purple);
  }

  .agent-state--done {
    color: var(--accent-green);
  }

  .agent-state--idle {
    color: var(--text-muted);
  }


  .repo-context {
    /* Flat sorts drop the per-repo group headers, so each row
     * carries its own repo context on the meta line. Caps at half
     * the line so the branch chip always keeps some room. */
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
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
    gap: var(--space-1);
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

  .worktree-dirty {
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
    color: var(--text-muted);
    opacity: 0.5;
    line-height: 1;
  }

  .ws-row-aside {
    flex-shrink: 0;
    align-self: flex-start;
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: var(--space-1);
    min-width: 44px;
  }

  .item-bubble-slot {
    display: block;
    width: 100%;
    height: 16px;
    margin-top: 1px;
  }

  .workspace-sort-time {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    white-space: nowrap;
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

  /* Bubble fills mix the state accent toward pure white, and the ink toward
     --bubble-ink, in both themes. kit-ui-check deliberately permits pure
     black/white shade constants inside color-mix — white here is a shade
     anchor, not a palette color. */
  .item-bubble {
    --bubble-bg: color-mix(in srgb, var(--item-accent) 70%, #ffffff);
    --bubble-fg: color-mix(in srgb, var(--item-accent) 25%, var(--bubble-ink));
  }

  /* Kata identity labels are short slugs/qualified IDs rather than a
   * fixed-width number, so cap the width and ellipsize to keep the row
   * layout stable. */
  .item-bubble--kata {
    max-width: 117px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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

  /* The menu root only hosts context; it must not add a box to the layout. */
  :global(.kit-menu.workspace-context-menu-root) {
    display: contents;
  }

  :global(.kit-menu__content.workspace-context-menu) {
    min-width: 224px;
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

</style>
