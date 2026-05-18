<script lang="ts">
  import { tick, untrack } from "svelte";
  import type {
    DiffFile,
    KanbanStatus,
    Label,
    ProviderCapabilities,
  } from "../../api/types.js";
  import type { DetailSyncMode } from "../../stores/detail.svelte.js";
  import {
    getStores, getClient, getActions,
    getUIConfig, getNavigate,
  } from "../../context.js";
  import { renderMarkdown } from "../../utils/markdown.js";
  import { moveTaskListItem, toggleTaskListItem } from "../../utils/task-list.js";
  import { timeAgo } from "../../utils/time.js";
  import { copyToClipboard } from "../../utils/clipboard.js";
  import EventTimeline from "./EventTimeline.svelte";
  import PRTimelineFilter from "./PRTimelineFilter.svelte";
  import CommentBox from "./CommentBox.svelte";
  import ApproveButton from "./ApproveButton.svelte";
  import ApproveWorkflowsButton from "./ApproveWorkflowsButton.svelte";
  import MergeModal from "./MergeModal.svelte";
  import ReadyForReviewButton from "./ReadyForReviewButton.svelte";
  import ReviewDecisionChip from "./ReviewDecisionChip.svelte";
  import {
    runOpenMerge,
    type PRDetailActionInput,
  } from "./keyboard-actions.js";
  import ActionButton from "../shared/ActionButton.svelte";
  import SelectDropdown from "../shared/SelectDropdown.svelte";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import GitMergeIcon from "@lucide/svelte/icons/git-merge";
  import MonitorUpIcon from "@lucide/svelte/icons/monitor-up";
  import PackagePlusIcon from "@lucide/svelte/icons/package-plus";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import TagsIcon from "@lucide/svelte/icons/tags";
  import XIcon from "@lucide/svelte/icons/x";
  import Chip from "../shared/Chip.svelte";
  import GitHubLabels from "../shared/GitHubLabels.svelte";
  import LabelPicker from "./LabelPicker.svelte";
  import { loadLabelCatalogWithRefresh } from "./labelCatalogRefresh.js";
  import {
    labelPickerCommandMatches,
    OPEN_LABEL_PICKER_EVENT,
    type OpenLabelPickerDetail,
  } from "./labelPickerCommand.js";
  import { nextCatalogLabelNames } from "./labelSelection.js";
  import { floatingPopoverStyle } from "../shared/floatingPosition.js";
  import DiffFilesLayout from "../diff/DiffFilesLayout.svelte";
  import CIStatus from "./CIStatus.svelte";
  import DiffSummaryChip from "./DiffSummaryChip.svelte";
  import CopyItemNumber from "./CopyItemNumber.svelte";
  import { DiffSummaryFilesResult } from "./diff-summary.js";
  import {
    providerItemPath,
    providerRepoPath,
    providerRouteParams,
  } from "../../api/provider-routes.js";
  import { supportsLocked } from "../../api/provider-capabilities.js";
  import { buildDiffSummaryKey } from "./diff-summary-key.js";
  import {
    activePRTimelineFilterCount,
    filterPREvents,
    loadPRTimelineFilter,
    savePRTimelineFilter,
    type PRTimelineFilterState,
  } from "./prTimelineFilter.js";

  const CLEAR_LABELS_PENDING = "__clear-label-selection__";

  const { detail: detailStore, pulls, activity } = getStores();
  const client = getClient();
  const actions = getActions();
  const uiConfig = getUIConfig();
  const navigate = getNavigate();

  const defaultProviderCapabilities: ProviderCapabilities = {
    read_repositories: true,
    read_merge_requests: true,
    read_issues: true,
    read_comments: true,
    read_releases: true,
    read_ci: true,
    read_labels: false,
    comment_mutation: true,
    state_mutation: true,
    merge_mutation: true,
    review_mutation: true,
    workflow_approval: true,
    ready_for_review: true,
    issue_mutation: true,
    label_mutation: false,
  };

  function currentCapabilities(): ProviderCapabilities {
    return detailStore.getDetail()?.repo?.capabilities
      ?? defaultProviderCapabilities;
  }

  interface Props {
    owner: string;
    name: string;
    number: number;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    onPullsRefresh?: () => Promise<void>;
    hideTabs?: boolean;
    hideWorkspaceAction?: boolean;
    hideStaleWhileLoading?: boolean;
    autoSync?: DetailSyncMode;
    workflowApprovalSync?: boolean;
  }

  const {
    owner,
    name,
    number,
    provider,
    platformHost,
    repoPath,
    onPullsRefresh,
    hideTabs = false,
    hideWorkspaceAction = false,
    hideStaleWhileLoading = false,
    autoSync = "background",
    workflowApprovalSync = true,
  }: Props = $props();

  const routeRef = $derived({
    provider,
    platformHost,
    owner,
    name,
    repoPath,
  });
  const labelPickerCommandRef = $derived({
    itemType: "pull" as const,
    provider,
    platformHost,
    owner,
    name,
    repoPath,
    number,
  });

  let activeTab = $state<"conversation" | "files">("conversation");
  let ciExpanded = $state(false);
  let timelineFilter = $state<PRTimelineFilterState>(
    loadPRTimelineFilter(),
  );
  const filteredTimelineEvents = $derived.by(() =>
    filterPREvents(detailStore.getDetail()?.events ?? [], timelineFilter),
  );
  const hasActiveTimelineFilters = $derived(
    activePRTimelineFilterCount(timelineFilter) > 0,
  );

  function ciChecksHavePending(checksJSON: string): boolean {
    if (!checksJSON) return false;
    try {
      const checks = JSON.parse(checksJSON) as Array<{ status?: string }>;
      return checks.some((check) => check.status !== "completed");
    } catch {
      return false;
    }
  }

  function requiredStatusChecksHaveNotPassed(checksJSON: string): boolean {
    if (!checksJSON) return false;
    try {
      const checks = JSON.parse(checksJSON) as Array<{
        required?: boolean;
        status?: string;
        conclusion?: string;
      }>;
      return checks.some((check) =>
        check.required === true &&
        (
          check.status !== "completed" ||
          !["success", "neutral", "skipped"].includes(check.conclusion ?? "")
        ),
      );
    } catch {
      return false;
    }
  }

  function hasWarningLines(
    prState: string,
    mergeableState: string,
    checksJSON: string,
    warnings: readonly string[] | null | undefined,
  ): boolean {
    return (
      (
        prState === "open" &&
        (
          mergeableState === "dirty" ||
          mergeableState === "blocked" ||
          mergeableState === "behind" ||
          requiredStatusChecksHaveNotPassed(checksJSON)
        )
      ) ||
      (warnings?.length ?? 0) > 0
    );
  }
  async function editTimelineComment(
    event: { PlatformID: number | null },
    body: string,
  ): Promise<boolean> {
    if (stalePR) return false;
    if (event.PlatformID === null) return false;
    return detailStore.editComment(owner, name, number, event.PlatformID, body);
  }

  function updateTimelineFilter(next: PRTimelineFilterState): void {
    timelineFilter = next;
    savePRTimelineFilter(next);
  }

  // Mutating actions (close/reopen, kanban state, star, save title/body,
  // workspace creation, etc.) read the (owner, name, number) PROPS, but
  // the visible detail is whatever loadDetail last produced. During a
  // route change those drift apart for the brief window before the new
  // load completes. `stalePR` is true in that window, and every mutation
  // handler short-circuits on it so a click during the transition can't
  // operate on the freshly-routed PR while showing the previous one.
  const stalePR = $derived.by(() => {
    const d = detailStore.getDetail();
    if (d == null) return false;
    return (
      d.repo_owner !== owner ||
      d.repo_name !== name ||
      (d.merge_request?.Number ?? -1) !== number ||
      d.repo?.provider !== provider ||
      d.repo?.platform_host !== platformHost ||
      d.repo?.repo_path !== repoPath
    );
  });

  const shouldAutoRefreshCI = $derived.by(() => {
    const pr = currentPR();
    return Boolean(
      ciExpanded &&
      !stalePR &&
      pr?.State === "open" &&
      ciChecksHavePending(pr.CIChecksJSON),
    );
  });

  $effect(() => {
    if (!shouldAutoRefreshCI) return;
    const refresh = () => {
      void detailStore.refreshPendingCI(owner, name, number, {
        provider,
        platformHost,
        repoPath,
        workflowApprovalSync,
      });
    };
    refresh();
    const interval = setInterval(refresh, 15_000);
    return () => clearInterval(interval);
  });

  $effect(() => {
    const requestOwner = owner;
    const requestName = name;
    const requestNumber = number;
    const requestProvider = provider;
    const requestPlatformHost = platformHost;
    const requestRepoPath = repoPath;
    const requestAutoSync = autoSync;
    const requestWorkflowApprovalSync = workflowApprovalSync;
    untrack(() => {
      void detailStore.loadDetail(
        requestOwner,
        requestName,
        requestNumber,
        {
          sync: requestAutoSync,
          workflowApprovalSync: requestWorkflowApprovalSync,
          provider: requestProvider,
          platformHost: requestPlatformHost,
          repoPath: requestRepoPath,
        },
      );
      detailStore.startDetailPolling(
        requestOwner,
        requestName,
        requestNumber,
        {
          provider: requestProvider,
          platformHost: requestPlatformHost,
          repoPath: requestRepoPath,
        },
      );
    });
    return () => detailStore.stopDetailPolling();
  });

  $effect(() => {
    const handler = (event: Event) => onOpenLabelPickerCommand(event);
    window.addEventListener(OPEN_LABEL_PICKER_EVENT, handler);
    return () => window.removeEventListener(OPEN_LABEL_PICKER_EVENT, handler);
  });

  // Clear modal/edit state on route change so PR A's open modal
  // can't reappear for PR B once `stalePR` clears.
  $effect(() => {
    void owner;
    void name;
    void number;
    showMergeModal = false;
    editingTitle = false;
    editingBody = false;
    titleDraft = "";
    bodyDraft = "";
    // Flush any pending checkbox/reorder save before clearing state.
    // pendingBodySave captures the previous PR's identity at schedule
    // time, so this fires against the correct target even though
    // owner/name/number have already changed.
    flushBodySave();
    clearDragState();
  });

  let copied = $state(false);
  let copyTimeout: ReturnType<typeof setTimeout> | null = null;

  function copyBody(text: string): void {
    void copyToClipboard(text).then((ok) => {
      if (!ok) return;
      copied = true;
      if (copyTimeout !== null) clearTimeout(copyTimeout);
      copyTimeout = setTimeout(() => {
        copied = false;
        copyTimeout = null;
      }, 1500);
    });
  }

  let copiedBranch = $state<string | null>(null);
  let branchCopyTimeout: ReturnType<typeof setTimeout> | null
    = null;

  function copyBranch(text: string): void {
    void copyToClipboard(text).then((ok) => {
      if (!ok) return;
      copiedBranch = text;
      if (branchCopyTimeout !== null) {
        clearTimeout(branchCopyTimeout);
      }
      branchCopyTimeout = setTimeout(() => {
        copiedBranch = null;
        branchCopyTimeout = null;
      }, 1500);
    });
  }

  async function refreshPulls(): Promise<void> {
    if (onPullsRefresh) {
      await onPullsRefresh();
    } else {
      await pulls.loadPulls();
    }
  }

  let stateSubmitting = $state(false);
  let stateError = $state<string | null>(null);

  // Title editing
  let editingTitle = $state(false);
  let titleDraft = $state("");
  let savingTitle = $state(false);

  function currentPR() {
    return detailStore.getDetail()?.merge_request;
  }

  function startEditTitle(): void {
    if (stalePR) return;
    if (!currentCapabilities().state_mutation) return;
    const mr = currentPR();
    if (!mr) return;
    titleDraft = mr.Title;
    editingTitle = true;
  }

  function cancelEditTitle(): void {
    editingTitle = false;
    titleDraft = "";
  }

  function handleStarClick(): void {
    if (stalePR) return;
    const mr = currentPR();
    if (!mr) return;
    void detailStore.toggleDetailPRStar(owner, name, number, mr.Starred);
  }

  async function saveTitle(): Promise<void> {
    if (stalePR) return;
    if (!currentCapabilities().state_mutation) return;
    const mr = currentPR();
    const trimmed = titleDraft.trim();
    if (!trimmed || trimmed === mr?.Title) {
      cancelEditTitle();
      return;
    }
    savingTitle = true;
    try {
      await detailStore.updatePRContent(
        owner, name, number, { title: trimmed },
      );
      editingTitle = false;
      titleDraft = "";
    } catch {
      // Store sets storeError; keep editor open with draft.
    } finally {
      savingTitle = false;
    }
  }

  function onTitleKeydown(e: KeyboardEvent): void {
    if (e.key === "Enter") {
      e.preventDefault();
      void saveTitle();
    } else if (e.key === "Escape") {
      cancelEditTitle();
    }
  }

  // Body editing
  let editingBody = $state(false);
  let bodyDraft = $state("");
  let savingBody = $state(false);

  function startEditBody(): void {
    if (stalePR) return;
    if (!currentCapabilities().state_mutation) return;
    const mr = currentPR();
    if (!mr) return;
    bodyDraft = mr.Body;
    editingBody = true;
  }

  function cancelEditBody(): void {
    editingBody = false;
    bodyDraft = "";
  }

  async function saveBody(): Promise<void> {
    if (stalePR) return;
    if (!currentCapabilities().state_mutation) return;
    const mr = currentPR();
    if (bodyDraft === mr?.Body) {
      cancelEditBody();
      return;
    }
    savingBody = true;
    try {
      await detailStore.updatePRContent(
        owner, name, number, { body: bodyDraft },
      );
      editingBody = false;
      bodyDraft = "";
    } catch {
      // Store sets storeError; keep editor open with draft.
    } finally {
      savingBody = false;
    }
  }

  function onBodyKeydown(e: KeyboardEvent): void {
    if (e.key === "Escape") {
      cancelEditBody();
    }
  }

  async function handleStateChange(
    newState: "open" | "closed",
  ): Promise<void> {
    if (stalePR) return;
    if (!currentCapabilities().state_mutation) return;
    stateSubmitting = true;
    stateError = null;
    try {
      const { error: requestError } = await client.POST(
        providerItemPath("pulls", routeRef, "/github-state"),
        {
          params: { path: { ...providerRouteParams(routeRef), number } },
          body: { state: newState },
        },
      );
      if (requestError) {
        throw new Error(
          requestError.detail
            ?? requestError.title
            ?? "failed to change PR state",
        );
      }
      await detailStore.loadDetail(owner, name, number, {
        provider,
        platformHost,
        repoPath,
      });
      await refreshPulls();
      await activity.loadActivity();
    } catch (err) {
      stateError =
        err instanceof Error ? err.message : String(err);
    } finally {
      stateSubmitting = false;
    }
  }

  type RepoSettings = {
    allowSquash: boolean;
    allowMerge: boolean;
    allowRebase: boolean;
    viewerCanMerge: boolean;
  };

  let repoSettings = $state<RepoSettings | null>(null);
  let repoSettingsRequestID = 0;
  let showMergeModal = $state(false);

  $effect(() => {
    const requestID = ++repoSettingsRequestID;
    repoSettings = null;
    client.GET(providerRepoPath(routeRef), {
      params: { path: providerRouteParams(routeRef) },
    }).then(({ data, error }) => {
      if (requestID !== repoSettingsRequestID) return;
      if (error || !data) return;
      repoSettings = {
        allowSquash: data.AllowSquashMerge,
        allowMerge: data.AllowMergeCommit,
        allowRebase: data.AllowRebaseMerge,
        viewerCanMerge: data.ViewerCanMerge,
      };
    }).catch(() => {
      if (requestID === repoSettingsRequestID) {
        repoSettings = null;
      }
    });
  });

  const workflowApproval = $derived(
    detailStore.getDetail()?.workflow_approval,
  );

  const kanbanOptions: { value: KanbanStatus; label: string }[] = [
    { value: "new", label: "New" },
    { value: "reviewing", label: "Reviewing" },
    { value: "waiting", label: "Waiting" },
    { value: "awaiting_merge", label: "Awaiting Merge" },
  ];

  function onKanbanChange(value: string): void {
    if (stalePR) return;
    void detailStore.updateKanbanState(owner, name, number, value as KanbanStatus);
  }

  function mergeActionLabel(settings: RepoSettings): string {
    if (settings.allowSquash && !settings.allowMerge && !settings.allowRebase) {
      return "Squash and merge";
    }
    if (!settings.allowSquash && settings.allowMerge && !settings.allowRebase) {
      return "Merge";
    }
    if (!settings.allowSquash && !settings.allowMerge && settings.allowRebase) {
      return "Rebase and merge";
    }
    return "Merge";
  }

  function mergeActionHasMenu(settings: RepoSettings): boolean {
    return [settings.allowSquash, settings.allowMerge, settings.allowRebase]
      .filter(Boolean).length > 1;
  }

  function mergeActionShortLabel(settings: RepoSettings): string {
    if (settings.allowSquash && !settings.allowMerge && !settings.allowRebase) {
      return "Squash";
    }
    if (!settings.allowSquash && !settings.allowMerge && settings.allowRebase) {
      return "Rebase";
    }
    return "Merge";
  }

  function hasMergeConflicts(
    pr: { State: string; MergeableState: string },
  ): boolean {
    return pr.State === "open" && pr.MergeableState === "dirty";
  }

  function buildOpenMergeInput(
    pr: { State: string; IsDraft: boolean; MergeableState: string },
    capabilities: ProviderCapabilities,
  ): PRDetailActionInput {
    return {
      pr: {
        State: pr.State,
        IsDraft: pr.IsDraft,
        MergeableState: pr.MergeableState,
      },
      ref: routeRef,
      number,
      viewerCan: {
        approve: false,
        merge: capabilities.merge_mutation,
        markReady: false,
        approveWorkflows: false,
      },
      repoSettings,
      stale: stalePR,
      stores: { detail: detailStore, pulls },
      client,
      setMergeModalOpen: (open: boolean) => { showMergeModal = open; },
      onAfterOpenMerge: closeActionMenu,
    };
  }

  const worktreeLinks = $derived(
    detailStore.getDetail()?.worktree_links ?? [],
  );
  const hasWorktreeLinks = $derived(
    worktreeLinks.length > 0,
  );
  const importAction = $derived(
    (actions.pull ?? []).find(
      (a) => a.id === "import-worktree",
    ),
  );
  const navigateAction = $derived(
    (actions.pull ?? []).find(
      (a) => a.id === "navigate-worktree",
    ),
  );
  const otherActions = $derived(
    (actions.pull ?? []).filter(
      (a) =>
        a.id !== "import-worktree" &&
        a.id !== "navigate-worktree",
    ),
  );
  const labels = $derived(detailStore.getDetail()?.merge_request?.labels ?? []);
  let labelPickerOpen = $state(false);
  let labelCatalog = $state<Label[]>([]);
  let labelCatalogSyncing = $state(false);
  let labelPickerError = $state<string | null>(null);
  let pendingLabel = $state<string | null>(null);
  let labelPickerAnchor = $state<HTMLDivElement>();
  let labelPickerPopover = $state<HTMLDivElement>();
  let labelPickerLaunchedFromActionMenu = $state(false);
  let labelPickerAutofocusFilter = $state(false);
  let labelPickerStyle = $state("");

  const workspace = $derived(detailStore.getDetail()?.workspace);
  let wsCreating = $state(false);
  let wsError = $state<string | null>(null);
  let actionMenuOpen = $state(false);
  let actionMenuWrapEl = $state<HTMLDivElement>();

  function closeActionMenu(): void {
    actionMenuOpen = false;
  }

  function closeLabelPicker(): void {
    labelPickerOpen = false;
    labelPickerError = null;
    pendingLabel = null;
    labelPickerLaunchedFromActionMenu = false;
    labelPickerAutofocusFilter = false;
  }

  function positionLabelPicker(): void {
    if (labelPickerLaunchedFromActionMenu) {
      labelPickerStyle = [
        "left: 50%",
        "top: 50%",
        "width: min(360px, calc(100dvw - 24px))",
        "transform: translate(-50%, -50%)",
        "--label-picker-max-height: min(560px, calc(100dvh - 48px))",
      ].join("; ");
      return;
    }

    if (!labelPickerAnchor) return;
    const popoverHeight = labelPickerPopover?.getBoundingClientRect().height;
    labelPickerStyle = floatingPopoverStyle({
      trigger: labelPickerAnchor.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      ...(popoverHeight !== undefined ? { popoverHeight } : {}),
      align: "end",
      edgeGap: 12,
      maxWidth: 360,
      constrainWidth: true,
    });
  }

  function visibleLabelPickerAnchor(): HTMLDivElement | undefined {
    const anchors = Array.from(document.querySelectorAll<HTMLDivElement>(".label-editor-anchor"));
    return anchors.find((anchor) => {
      const rect = anchor.getBoundingClientRect();
      const style = getComputedStyle(anchor);
      return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0;
    }) ?? anchors[0];
  }

  function onOpenLabelPickerCommand(event: Event): void {
    const detail = (event as CustomEvent<OpenLabelPickerDetail>).detail;
    if (labelPickerCommandMatches(labelPickerCommandRef, detail)) {
      void openLabelPicker();
    }
  }

  async function openLabelPicker(event?: MouseEvent): Promise<void> {
    labelPickerAnchor = (event?.currentTarget as HTMLElement | null)?.closest<HTMLDivElement>(".label-editor-anchor")
      ?? visibleLabelPickerAnchor();
    const launchedFromActionMenu = Boolean(labelPickerAnchor?.closest(".actions-menu-popover"));
    if (event !== undefined && labelPickerOpen) {
      if (launchedFromActionMenu) {
        closeActionMenu();
      }
      closeLabelPicker();
      return;
    }
    labelPickerLaunchedFromActionMenu = launchedFromActionMenu;
    labelPickerAutofocusFilter = event !== undefined && !(window.matchMedia?.("(pointer: coarse)").matches ?? false);
    if (labelPickerLaunchedFromActionMenu) {
      closeActionMenu();
    }
    labelPickerOpen = true;
    labelPickerError = null;
    labelCatalogSyncing = true;
    await tick();
    positionLabelPicker();
    try {
      await loadLabelCatalogWithRefresh({
        isActive: () => labelPickerOpen,
        loadOnce: async () => {
          const { data, error } = await client.GET(
            providerRepoPath(routeRef, "/labels"),
            { params: { path: providerRouteParams(routeRef) } },
          );
          if (error) {
            throw new Error(error.detail ?? error.title ?? "failed to load labels");
          }
          return {
            labels: (data?.labels ?? []) as Label[],
            stale: data?.stale ?? false,
            syncing: data?.syncing ?? false,
          };
        },
        onUpdate: (catalog) => {
          labelCatalog = catalog.labels;
          labelCatalogSyncing = Boolean(catalog.stale || catalog.syncing);
          void tick().then(() => {
            if (labelPickerOpen) positionLabelPicker();
          });
        },
      });
    } catch (err) {
      labelPickerError = err instanceof Error ? err.message : String(err);
    } finally {
      if (labelPickerOpen) labelCatalogSyncing = false;
    }
  }

  $effect(() => {
    if (!labelPickerOpen) return;

    function updatePosition(): void {
      positionLabelPicker();
    }

    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    return () => {
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
  });

  async function toggleLabel(labelName: string): Promise<void> {
    if (pendingLabel !== null) return;
    pendingLabel = labelName;
    labelPickerError = null;
    const nextNames = nextCatalogLabelNames(labels, labelCatalog, labelName);
    try {
      await detailStore.setPullLabels(owner, name, number, nextNames);
    } catch (err) {
      labelPickerError = err instanceof Error ? err.message : String(err);
    } finally {
      pendingLabel = null;
    }
  }

  async function clearLabels(): Promise<void> {
    if (pendingLabel !== null || labels.length === 0) return;
    pendingLabel = CLEAR_LABELS_PENDING;
    labelPickerError = null;
    try {
      await detailStore.setPullLabels(owner, name, number, []);
    } catch (err) {
      labelPickerError = err instanceof Error ? err.message : String(err);
    } finally {
      pendingLabel = null;
    }
  }

  function onActionMenuKeydown(e: KeyboardEvent): void {
    if (actionMenuOpen && e.key === "Escape") {
      actionMenuOpen = false;
    }
  }

  function isLabelPickerControlTarget(target: Node): boolean {
    if (!(target instanceof Element)) return false;
    return Boolean(
      target.closest(".label-editor-anchor")
      || target.closest(".actions-menu-trigger"),
    );
  }

  function onDocumentMousedown(e: MouseEvent): void {
    const target = e.target as Node;
    if (actionMenuOpen && !actionMenuWrapEl?.contains(target)) {
      closeActionMenu();
    }
    if (labelPickerOpen) {
      if (
        !labelPickerPopover?.contains(target)
        && !labelPickerAnchor?.contains(target)
        && !isLabelPickerControlTarget(target)
      ) {
        closeLabelPicker();
      }
    }
  }

  async function createWorkspace(): Promise<void> {
    if (stalePR) return;
    const detail = detailStore.getDetail();
    if (!detail) return;

    wsCreating = true;
    wsError = null;
    try {
      const { data, error: reqError } = await client.POST(
        "/workspaces",
        {
          body: {
            platform_host: detail.platform_host,
            owner: detail.repo_owner,
            name: detail.repo_name,
            mr_number: detail.merge_request.Number,
          },
        },
      );
      if (reqError) {
        throw new Error(
          reqError.detail ?? reqError.title ?? "failed to create workspace",
        );
      }
      if (data?.id) {
        navigate(`/terminal/${data.id}`);
      }
    } catch (err) {
      wsError = err instanceof Error ? err.message : String(err);
    } finally {
      wsCreating = false;
    }
  }

  // Task-list checkbox clicks update the body locally for instant
  // feedback, then debounce a PATCH so a flurry of clicks collapses
  // into a single save. Avoids GitHub-style per-click blocking saves.
  // The target (owner/name/number) AND the body to save are captured
  // when scheduling so a route change before the timer fires can't
  // redirect the save or lose the edit.
  type PendingBodySave = {
    owner: string;
    name: string;
    number: number;
    body: string;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
  };
  let bodySaveTimeout: ReturnType<typeof setTimeout> | null = null;
  let pendingBodySave: PendingBodySave | null = null;
  const BODY_SAVE_DEBOUNCE_MS = 400;

  function scheduleBodySave(body: string): void {
    pendingBodySave = {
      owner, name, number, body,
      provider, platformHost, repoPath,
    };
    if (bodySaveTimeout !== null) clearTimeout(bodySaveTimeout);
    bodySaveTimeout = setTimeout(() => {
      flushBodySave();
    }, BODY_SAVE_DEBOUNCE_MS);
  }

  function flushBodySave(): void {
    if (bodySaveTimeout !== null) {
      clearTimeout(bodySaveTimeout);
      bodySaveTimeout = null;
    }
    const target = pendingBodySave;
    pendingBodySave = null;
    if (target === null) return;
    void detailStore.savePRBodyInBackground(
      target.owner, target.name, target.number, target.body,
      {
        provider: target.provider,
        platformHost: target.platformHost,
        repoPath: target.repoPath,
      },
    );
  }

  function onBodyClick(event: MouseEvent): void {
    const target = event.target as HTMLElement | null;
    if (!target) return;
    if (target.tagName !== "INPUT") return;
    if ((target as HTMLInputElement).type !== "checkbox") return;
    const raw = target.getAttribute("data-task-index");
    if (raw === null) return;
    if (stalePR || !currentCapabilities().state_mutation) {
      event.preventDefault();
      return;
    }
    const index = parseInt(raw, 10);
    if (Number.isNaN(index)) return;
    const mr = currentPR();
    if (!mr) return;
    const newBody = toggleTaskListItem(mr.Body, index);
    if (newBody === mr.Body) return;
    // We manage state ourselves; let the visual flip persist via the
    // optimistic store update rather than the browser's default toggle
    // (which would race with our re-render).
    event.preventDefault();
    detailStore.setLocalPRBody(
      provider, platformHost, owner, name, number, newBody,
    );
    scheduleBodySave(newBody);
  }

  // Drag-to-reorder for task-list items. The handle (rendered by the
  // markdown layer as `<span class="task-drag-handle" draggable>`) is
  // the drag source; the enclosing `<li class="task-list-item">` is
  // the drop target. Drop position relative to the target's vertical
  // midpoint decides before/after placement.
  let dragSourceIndex = $state<number | null>(null);
  let dropTargetIndex = $state<number | null>(null);
  let dropTargetSide = $state<"before" | "after">("before");

  function findTaskItemIndex(el: HTMLElement | null): number | null {
    let cur: HTMLElement | null = el;
    while (cur) {
      if (cur.classList && cur.classList.contains("task-list-item")) {
        const raw = cur.getAttribute("data-task-index");
        if (raw === null) return null;
        const idx = parseInt(raw, 10);
        return Number.isNaN(idx) ? null : idx;
      }
      cur = cur.parentElement;
    }
    return null;
  }

  function onBodyDragStart(event: DragEvent): void {
    if (stalePR || !currentCapabilities().state_mutation) return;
    const target = event.target as HTMLElement | null;
    if (!target?.classList?.contains("task-drag-handle")) return;
    const raw = target.getAttribute("data-task-index");
    if (raw === null) return;
    const idx = parseInt(raw, 10);
    if (Number.isNaN(idx)) return;
    dragSourceIndex = idx;
    if (event.dataTransfer) {
      event.dataTransfer.effectAllowed = "move";
      // Firefox requires a non-empty payload to start a drag.
      event.dataTransfer.setData("text/plain", String(idx));
    }
  }

  function onBodyDragOver(event: DragEvent): void {
    if (dragSourceIndex === null) return;
    const target = event.target as HTMLElement | null;
    const idx = findTaskItemIndex(target);
    if (idx === null) return;
    event.preventDefault(); // allow drop
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    let li: HTMLElement | null = target;
    while (li && !(li.classList && li.classList.contains("task-list-item"))) {
      li = li.parentElement;
    }
    let side: "before" | "after" = "before";
    if (li) {
      const rect = li.getBoundingClientRect();
      side = event.clientY < rect.top + rect.height / 2
        ? "before"
        : "after";
    }
    dropTargetSide = side;
    dropTargetIndex = idx;
    updateDropIndicatorClasses(
      event.currentTarget as HTMLElement,
      idx,
      side,
    );
  }

  function onBodyDragLeave(event: DragEvent): void {
    const related = event.relatedTarget as HTMLElement | null;
    const body = event.currentTarget as HTMLElement;
    if (!related || !body.contains(related)) {
      dropTargetIndex = null;
      clearDropIndicatorClasses(body);
    }
  }

  function updateDropIndicatorClasses(
    root: HTMLElement,
    idx: number,
    side: "before" | "after",
  ): void {
    clearDropIndicatorClasses(root);
    const li = root.querySelector(
      `.task-list-item--interactive[data-task-index="${idx}"]`,
    );
    if (!li) return;
    li.classList.add(
      side === "before" ? "task-drop-before" : "task-drop-after",
    );
  }

  function clearDropIndicatorClasses(root: HTMLElement): void {
    root.querySelectorAll(".task-drop-before").forEach((el) =>
      el.classList.remove("task-drop-before"),
    );
    root.querySelectorAll(".task-drop-after").forEach((el) =>
      el.classList.remove("task-drop-after"),
    );
  }

  function onBodyDrop(event: DragEvent): void {
    const body = event.currentTarget as HTMLElement;
    if (dragSourceIndex === null) {
      clearDragState(body);
      return;
    }
    event.preventDefault();
    const from = dragSourceIndex;
    const to = dropTargetIndex;
    const side = dropTargetSide;
    clearDragState(body);
    if (to === null || to === from) return;
    if (stalePR || !currentCapabilities().state_mutation) return;
    const mr = currentPR();
    if (!mr) return;
    // "before X" with from < X means landing one slot earlier than X
    // after the splice; "after X" means landing on X. Adjust target.
    let target = to;
    if (from < to && side === "before") target = to - 1;
    else if (from > to && side === "after") target = to + 1;
    if (target === from) return;
    const newBody = moveTaskListItem(mr.Body, from, target);
    if (newBody === mr.Body) return;
    detailStore.setLocalPRBody(
      provider, platformHost, owner, name, number, newBody,
    );
    scheduleBodySave(newBody);
  }

  function onBodyDragEnd(event: DragEvent): void {
    clearDragState(event.currentTarget as HTMLElement);
  }

  function clearDragState(root?: HTMLElement | null): void {
    dragSourceIndex = null;
    dropTargetIndex = null;
    dropTargetSide = "before";
    if (root) clearDropIndicatorClasses(root);
  }

  async function loadDiffSummaryFiles(): Promise<DiffSummaryFilesResult> {
    const { data, error } = await client.GET(
      providerItemPath("pulls", routeRef, "/files"),
      {
        params: { path: { ...providerRouteParams(routeRef), number } },
      },
    );
    if (error) {
      throw new Error(
        error.detail ?? error.title ?? "failed to load changed files",
      );
    }
    return new DiffSummaryFilesResult(
      data?.stale ?? true,
      (data?.files ?? []) as DiffFile[],
    );
  }
</script>

<svelte:window onkeydown={onActionMenuKeydown} />
<svelte:document onmousedown={onDocumentMousedown} />

{#if detailStore.isDetailLoading() && (detailStore.getDetail() === null || (stalePR && hideStaleWhileLoading))}
  <div class="state-center"><p class="state-msg">Loading…</p></div>
{:else if detailStore.getDetailError() !== null && (detailStore.getDetail() === null || (stalePR && hideStaleWhileLoading))}
  <div class="state-center"><p class="state-msg state-msg--error">Error: {detailStore.getDetailError()}</p></div>
{:else}
  {@const detail = detailStore.getDetail()}
  {@const staleLoadError = stalePR && detailStore.getDetailError() !== null}
  {#if detail !== null}
    {@const pr = detail.merge_request}
    {@const capabilities = detail.repo?.capabilities ?? defaultProviderCapabilities}
    {@const lockedSupported = supportsLocked(
      detail.repo?.provider ?? provider,
      detail.repo?.platform_host ?? detail.platform_host,
      detail.repo?.owner ?? owner,
      detail.repo?.name ?? name,
    )}
    <div class="pull-detail-wrap">
      {#if staleLoadError}
        <div class="detail-load-error" data-testid="detail-load-error">
          Couldn't load this pull request: {detailStore.getDetailError()}
        </div>
      {/if}
      {#if !hideTabs}
        <div class="detail-tabs">
          <button
            type="button"
            class="detail-tab"
            class:detail-tab--active={activeTab === "conversation"}
            onclick={() => { activeTab = "conversation"; }}
          >
            Conversation
          </button>
          <button
            type="button"
            class="detail-tab"
            class:detail-tab--active={activeTab === "files"}
            onclick={() => { activeTab = "files"; }}
          >
            Files changed
            {#if pr.Additions > 0}
              <span class="files-stat files-stat--add">+{pr.Additions}</span>
            {/if}
            {#if pr.Deletions > 0}
              <span class="files-stat files-stat--del">-{pr.Deletions}</span>
            {/if}
          </button>
        </div>
      {/if}
      {#if !hideTabs && activeTab === "files"}
        <DiffFilesLayout {provider} {platformHost} {owner} {name} {repoPath} {number} />
      {:else}
        <div class="pull-detail">
          <div
            class="pull-detail-content"
            class:pull-detail-content--has-compact-actions={pr.State !== "merged" && !stalePR}
          >
            {#snippet labelActionButton(iconSize = 16)}
              <ActionButton
                class="btn--labels"
                label="Labels"
                shortLabel="Labels"
                size="sm"
                surface="soft"
                tone="neutral"
                disabled={stalePR}
                onclick={openLabelPicker}
              >
                <TagsIcon size={iconSize} aria-hidden="true" />
              </ActionButton>
            {/snippet}

      {#if detailStore.isStaleRefreshing()}
        <div class="refresh-banner">
          <span class="sync-dot"></span>
          Refreshing...
        </div>
      {/if}
      <!-- Header -->
      <div class="detail-header">
        {#if editingTitle}
          <div class="title-edit">
            <!-- svelte-ignore a11y_autofocus -->
            <input
              type="text"
              class="title-edit-input"
              bind:value={titleDraft}
              onkeydown={onTitleKeydown}
              disabled={savingTitle}
              autofocus
            />
            <button
              class="title-edit-save"
              onclick={() => void saveTitle()}
              disabled={savingTitle || !titleDraft.trim()}
            >
              {savingTitle ? "Saving..." : "Save"}
            </button>
            <button
              class="title-edit-cancel"
              onclick={cancelEditTitle}
              disabled={savingTitle}
            >
              Cancel
            </button>
          </div>
        {:else if capabilities.state_mutation}
          <div class="title-line">
            <h2 class="detail-title">{pr.Title}</h2>
            {#if !stalePR}
              <button
                class="edit-title-btn"
                onclick={startEditTitle}
              >Edit</button>
            {/if}
            {#if !uiConfig.hideStar && !stalePR}
              <button
                class="star-btn"
                onclick={handleStarClick}
                title={pr.Starred ? "Unstar" : "Star"}
              >
                {#if pr.Starred}
                  <svg class="star-detail-icon star-detail-icon--active" width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
                    <path d="M8 .25a.75.75 0 01.673.418l1.882 3.815 4.21.612a.75.75 0 01.416 1.279l-3.046 2.97.719 4.192a.75.75 0 01-1.088.791L8 12.347l-3.766 1.98a.75.75 0 01-1.088-.79l.72-4.194L.818 6.374a.75.75 0 01.416-1.28l4.21-.611L7.327.668A.75.75 0 018 .25z"/>
                  </svg>
                {:else}
                  <svg class="star-detail-icon" width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
                    <path d="M8 .25a.75.75 0 01.673.418l1.882 3.815 4.21.612a.75.75 0 01.416 1.279l-3.046 2.97.719 4.192a.75.75 0 01-1.088.791L8 12.347l-3.766 1.98a.75.75 0 01-1.088-.79l.72-4.194L.818 6.374a.75.75 0 01.416-1.28l4.21-.611L7.327.668A.75.75 0 018 .25zm0 2.445L6.615 5.5a.75.75 0 01-.564.41l-3.097.45 2.24 2.184a.75.75 0 01.216.664l-.528 3.084 2.769-1.456a.75.75 0 01.698 0l2.77 1.456-.53-3.084a.75.75 0 01.216-.664l2.24-2.183-3.096-.45a.75.75 0 01-.564-.41L8 2.694z"/>
                  </svg>
                {/if}
              </button>
            {/if}
            <a class="gh-link" href={pr.URL} target="_blank" rel="noopener noreferrer" title="Open on GitHub">
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
                <path d="M6 3H3a1 1 0 0 0-1 1v9a1 1 0 0 0 1 1h9a1 1 0 0 0 1-1v-3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                <path d="M10 2h4v4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                <path d="M8 8L14 2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
              </svg>
            </a>
          </div>
          {#if !stalePR}
            <SelectDropdown
              class="kanban-select kanban-select--header kanban-select--{pr.KanbanStatus.replace('_', '-')}"
              value={pr.KanbanStatus}
              options={kanbanOptions}
              onchange={onKanbanChange}
              title="Change workflow status"
            />
          {/if}
        {/if}
      </div>

      <!-- Meta row -->
      <div class="meta-row">
        <span class="meta-item">{detail.repo_owner}/{detail.repo_name}</span>
        <span class="meta-sep">·</span>
        <CopyItemNumber kind="pull" number={pr.Number} url={pr.URL} />
        <span class="meta-sep">·</span>
        <span class="meta-item">{pr.Author}</span>
        <span class="meta-sep">·</span>
        <span class="meta-item">{timeAgo(pr.CreatedAt)}</span>
        {#if pr.HeadBranch}
          <span class="meta-sep meta-sep--branch">·</span>
          <span class="meta-branch">
            <svg class="branch-icon" width="12" height="12" viewBox="0 0 16 16" fill="currentColor">
              <path d="M11.75 2.5a.75.75 0 100 1.5.75.75 0 000-1.5zm-2.25.75a2.25 2.25 0 113 2.122V6c0 .73-.593 1.322-1.325 1.322H9.457A4.377 4.377 0 006.5 8.579V11.128a2.251 2.251 0 11-1.5 0V4.872a2.251 2.251 0 111.5 0v1.836A5.877 5.877 0 0111.175 5.5h.075V5.372A2.25 2.25 0 019.5 3.25zM4.75 12a.75.75 0 100 1.5.75.75 0 000-1.5zM4 3.25a.75.75 0 111.5 0 .75.75 0 01-1.5 0z"/>
            </svg>
            <button
              class="branch-name-btn"
              class:branch-name-btn--copied={copiedBranch === pr.HeadBranch}
              title={copiedBranch === pr.HeadBranch ? "Copied!" : "Click to copy"}
              onclick={() => copyBranch(pr.HeadBranch)}
            >{pr.HeadBranch}</button>
            <span class="branch-arrow">&rarr;</span>
            <button
              class="branch-name-btn"
              class:branch-name-btn--copied={copiedBranch === pr.BaseBranch}
              title={copiedBranch === pr.BaseBranch ? "Copied!" : "Click to copy"}
              onclick={() => copyBranch(pr.BaseBranch)}
            >{pr.BaseBranch}</button>
          </span>
        {/if}
        {#if detailStore.isDetailSyncing()}
          <span class="meta-sep meta-sep--sync">·</span>
          <span class="sync-indicator" title="Syncing from GitHub">
            <svg class="sync-spinner" width="12" height="12" viewBox="0 0 16 16" fill="none">
              <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="2" stroke-dasharray="28" stroke-dashoffset="8" stroke-linecap="round"/>
            </svg>
            Syncing
          </span>
        {/if}
      </div>

      <div class="chips-row">
        {#if pr.State === "merged"}
          <Chip class="chip--purple">Merged</Chip>
        {:else if pr.State === "closed"}
          <Chip class="chip--red">Closed</Chip>
        {:else if pr.IsDraft}
          <Chip class="chip--amber">Draft</Chip>
        {:else}
          <Chip class="chip--green">Open</Chip>
        {/if}
        {#if pr.IsLocked && lockedSupported}
          <Chip class="chip--amber" title="This pull request is locked">Locked</Chip>
        {/if}
        <CIStatus
          status={pr.CIStatus}
          checksJSON={pr.CIChecksJSON}
          detailLoaded={detailStore.getDetailLoaded()}
          detailSyncing={detailStore.isDetailSyncing()}
          bind:expanded={ciExpanded}
          showPanel={false}
        />
        {#if pr.ReviewDecision}
          <ReviewDecisionChip
            decision={pr.ReviewDecision}
            events={detail.events}
          />
        {/if}
        {#if pr.Additions > 0 || pr.Deletions > 0}
          <DiffSummaryChip
            additions={pr.Additions}
            deletions={pr.Deletions}
            summaryKey={buildDiffSummaryKey(owner, name, number, detail, pr)}
            loadFiles={loadDiffSummaryFiles}
          />
        {/if}
        {#if hasWorktreeLinks}
          <Chip class="chip--teal">Worktree</Chip>
        {/if}
        {#if labels.length > 0}
          <GitHubLabels {labels} mode="full" />
        {/if}
        {#if capabilities.read_labels && capabilities.label_mutation}
          <div class="label-editor-anchor label-editor-anchor--inline">
            {@render labelActionButton()}
          </div>
        {/if}
        {#if labelPickerOpen}
          {#if labelPickerLaunchedFromActionMenu}
            <div class="label-editor-backdrop" aria-hidden="true"></div>
          {/if}
          <div class="label-editor-popover" style={labelPickerStyle} bind:this={labelPickerPopover}>
            <LabelPicker
              catalogLabels={labelCatalog}
              selectedLabels={labels}
              syncing={labelCatalogSyncing}
              {pendingLabel}
              error={labelPickerError}
              autofocusFilter={labelPickerAutofocusFilter}
              ontoggle={toggleLabel}
              onclear={clearLabels}
              onclose={closeLabelPicker}
            />
          </div>
        {/if}
        <CIStatus
          status={pr.CIStatus}
          checksJSON={pr.CIChecksJSON}
          detailLoaded={detailStore.getDetailLoaded()}
          detailSyncing={detailStore.isDetailSyncing()}
          bind:expanded={ciExpanded}
          showButton={false}
        />
      </div>

      {#if !stalePR}
        <SelectDropdown
          class="kanban-select kanban-select--below-chips kanban-select--{pr.KanbanStatus.replace('_', '-')}"
          value={pr.KanbanStatus}
          options={kanbanOptions}
          onchange={onKanbanChange}
          title="Change workflow status"
        />
      {/if}


      <!-- Pull request warnings -->
      {#if !stalePR && hasWarningLines(pr.State, pr.MergeableState, pr.CIChecksJSON, detail.warnings)}
        <div class="merge-warnings" aria-label="Pull request warnings">
          {#if pr.State === "open" && pr.MergeableState === "dirty"}
            <div class="merge-warning-line merge-warning-line--conflict">
              <span>This branch has conflicts that must be resolved before merging.</span>
              <a href={pr.URL} target="_blank" rel="noopener noreferrer">View on GitHub</a>
            </div>
          {/if}
          {#if pr.State === "open" && pr.MergeableState === "blocked"}
            <div class="merge-warning-line">
              <span>Branch protection rules may prevent this merge.</span>
            </div>
          {/if}
          {#if pr.State === "open" && pr.MergeableState === "behind"}
            <div class="merge-warning-line">
              <span>This branch is behind the base branch and may need to be updated.</span>
            </div>
          {/if}
          {#if pr.State === "open" && requiredStatusChecksHaveNotPassed(pr.CIChecksJSON)}
            <div class="merge-warning-line">
              <span>Required status checks have not passed.</span>
            </div>
          {/if}
          {#if detail.warnings && detail.warnings.length > 0}
            {#each detail.warnings as warning (warning)}
              <div class="merge-warning-line">
                <span>{warning}</span>
              </div>
            {/each}
          {/if}
        </div>
      {:else if stalePR && detail.warnings && detail.warnings.length > 0}
        <div class="merge-warnings" aria-label="Pull request warnings">
          {#each detail.warnings as warning (warning)}
            <div class="merge-warning-line">
              <span>{warning}</span>
            </div>
          {/each}
        </div>
      {/if}

      {#snippet primaryActionButtons()}
        {#if pr.State === "open"}
          {#if pr.IsDraft && capabilities.ready_for_review}
            <ReadyForReviewButton
              {owner}
              {name}
              {number}
              {provider}
              {platformHost}
              {repoPath}
              size="sm"
              disabled={stalePR}
              oncompleted={closeActionMenu}
            />
          {/if}
          {#if capabilities.review_mutation}
            <ApproveButton
              {owner}
              {name}
              {number}
              {provider}
              {platformHost}
              {repoPath}
              size="sm"
              disabled={stalePR}
            />
          {/if}
          {#if capabilities.workflow_approval && workflowApproval?.checked && workflowApproval.required}
            <ApproveWorkflowsButton
              {owner}
              {name}
              {number}
              {provider}
              {platformHost}
              {repoPath}
              count={workflowApproval.count ?? 0}
              size="sm"
              disabled={stalePR}
              oncompleted={closeActionMenu}
            />
          {/if}
          {#if repoSettings && capabilities.merge_mutation && repoSettings.viewerCanMerge}
            {@const mergeSettings = repoSettings}
            {@const mergeDisabledByConflicts = hasMergeConflicts(pr)}
            <ActionButton
              class="btn--merge"
              disabled={stalePR || mergeDisabledByConflicts}
              title={mergeDisabledByConflicts
                ? "Resolve merge conflicts before merging"
                : ""}
              onclick={() => {
                if (stalePR) return;
                runOpenMerge(buildOpenMergeInput(pr, capabilities));
              }}
              tone="success"
              surface="solid"
              size="sm"
              label={mergeActionLabel(mergeSettings)}
              shortLabel={mergeActionShortLabel(mergeSettings)}
            >
              <GitMergeIcon size="14" strokeWidth="2.2" aria-hidden="true" />
              {#snippet trailing()}
                {#if mergeActionHasMenu(mergeSettings)}
                  <ChevronDownIcon size="13" strokeWidth="2.2" aria-hidden="true" />
                {/if}
              {/snippet}
            </ActionButton>
          {/if}
          {#if capabilities.state_mutation}
            <ActionButton
              class="btn--close"
              disabled={stateSubmitting || stalePR}
              onclick={() => {
                if (stalePR) return;
                closeActionMenu();
                handleStateChange("closed");
              }}
              tone="danger"
              surface="outline"
              size="sm"
              label={stateSubmitting ? "Closing..." : "Close"}
              shortLabel={stateSubmitting ? "Closing..." : "Close"}
            >
              <XIcon size="14" strokeWidth="2.2" aria-hidden="true" />
            </ActionButton>
          {/if}
        {:else if pr.State === "closed"}
          {#if capabilities.state_mutation}
            <ActionButton
              class="btn--reopen"
              disabled={stateSubmitting || stalePR}
              onclick={() => {
                if (stalePR) return;
                closeActionMenu();
                handleStateChange("open");
              }}
              tone="success"
              surface="solid"
              size="sm"
              label={stateSubmitting ? "Reopening..." : "Reopen"}
              shortLabel={stateSubmitting ? "Reopening..." : "Reopen"}
            >
              <RefreshCwIcon size="14" strokeWidth="2.2" aria-hidden="true" />
            </ActionButton>
          {/if}
        {/if}
      {/snippet}

      {#snippet workspaceActionButton()}
        {#if workspace}
          <ActionButton
            class="btn--workspace"
            disabled={stalePR}
            onclick={() => {
              if (stalePR) return;
              closeActionMenu();
              navigate(`/terminal/${workspace.id}`);
            }}
            tone="info"
            surface="soft"
            size="sm"
            label="Open Workspace"
            shortLabel="Workspace"
          >
            <MonitorUpIcon size="14" strokeWidth="2.2" aria-hidden="true" />
          </ActionButton>
        {:else}
          <ActionButton
            class="btn--workspace"
            disabled={wsCreating || stalePR}
            onclick={() => void createWorkspace()}
            tone="info"
            surface="soft"
            size="sm"
            label={wsCreating ? "Creating..." : "Create Workspace"}
            shortLabel={wsCreating ? "Creating..." : "Create Workspace"}
          >
            <PackagePlusIcon size="14" strokeWidth="2.2" aria-hidden="true" />
          </ActionButton>
        {/if}
      {/snippet}

      <!-- Approve / Merge / Close / Reopen actions -->
      {#if pr.State !== "merged" && !stalePR}
        <div class="primary-actions-wrap">
          <div class="actions-row actions-row--primary">
            {@render primaryActionButtons()}
            {#if !hideWorkspaceAction}
              <div class="primary-workspace-action">
                {@render workspaceActionButton()}
                {#if wsError}
                  <span class="action-error action-error--workspace-compact">{wsError}</span>
                {/if}
              </div>
            {/if}
          </div>
          <div class="actions-menu-wrap" bind:this={actionMenuWrapEl}>
            <button
              type="button"
              class="actions-menu-trigger"
              aria-haspopup="true"
              aria-expanded={actionMenuOpen}
              onclick={() => { actionMenuOpen = !actionMenuOpen; }}
            >
              <span>Actions</span>
              <ChevronDownIcon size="14" strokeWidth="2.2" aria-hidden="true" />
            </button>
            {#if actionMenuOpen}
              <div class="actions-menu-popover">
                {@render primaryActionButtons()}
                {#if capabilities.read_labels && capabilities.label_mutation}
                  <div class="actions-menu-popover__item actions-menu-popover__item--labels label-editor-anchor">
                    {@render labelActionButton(14)}
                  </div>
                {/if}
                {#if !hideWorkspaceAction}
                  <div class="actions-menu-popover__item">
                    {@render workspaceActionButton()}
                  </div>
                  {#if wsError}
                    <span class="action-error">{wsError}</span>
                  {/if}
                {/if}
              </div>
            {/if}
          </div>
          {#if stateError}
            <span class="action-error action-error--state">{stateError}</span>
          {/if}
        </div>
      {/if}

      {#if !hideWorkspaceAction}
        <!-- Workspace actions -->
        <div class="actions-row actions-row--workspace">
          {@render workspaceActionButton()}
          {#if wsError}
            <span class="action-error">{wsError}</span>
          {/if}
        </div>
      {/if}

      {#if !hasWorktreeLinks && importAction}
        <div class="actions-row">
          <ActionButton
            class="btn--embedding-action"
            onclick={() => {
              if (stalePR) return;
              importAction.handler({
                surface: "pull-detail", owner, name, number,
              });
            }}
            disabled={stalePR}
            tone="neutral"
            surface="outline"
            size="sm"
          >
            {importAction.label}
          </ActionButton>
        </div>
      {/if}
      {#if hasWorktreeLinks && navigateAction}
        <div class="actions-row">
          {#each worktreeLinks as link (link.worktree_key)}
            <ActionButton
              class="btn--embedding-action"
              onclick={() => {
                if (stalePR) return;
                navigateAction.handler({
                  surface: "pull-detail", owner, name, number,
                  meta: { worktree_key: link.worktree_key },
                });
              }}
              disabled={stalePR}
              tone="neutral"
              surface="outline"
              size="sm"
            >
              {navigateAction.label}: {link.worktree_key}
            </ActionButton>
          {/each}
        </div>
      {/if}
      {#if otherActions.length > 0}
        <div class="actions-row">
          {#each otherActions as action (action.id)}
            <ActionButton
              class="btn--embedding-action"
              onclick={() => {
                if (stalePR) return;
                action.handler({
                  surface: "pull-detail", owner, name, number,
                });
              }}
              disabled={stalePR}
              tone="neutral"
              surface="outline"
              size="sm"
            >
              {action.label}
            </ActionButton>
          {/each}
        </div>
      {/if}

      {#if showMergeModal && repoSettings && capabilities.merge_mutation && repoSettings.viewerCanMerge && !stalePR && !hasMergeConflicts(pr)}
        {@const d = detailStore.getDetail()!}
        {@const p = d.merge_request}
        <MergeModal
          {owner}
          {name}
          {number}
          {provider}
          {platformHost}
          {repoPath}
          prTitle={p.Title}
          prBody={p.Body}
          prAuthor={p.Author}
          prAuthorDisplayName={p.AuthorDisplayName}
          allowSquash={repoSettings.allowSquash}
          allowMerge={repoSettings.allowMerge}
          allowRebase={repoSettings.allowRebase}
          onclose={() => { showMergeModal = false; }}
          onmerged={() => {
            showMergeModal = false;
            void detailStore.loadDetail(owner, name, number, {
              provider,
              platformHost,
              repoPath,
            });
            void pulls.loadPulls();
            void activity.loadActivity();
          }}
        />
      {/if}

      <!-- PR body -->
      <div class="section body-section">
        <div class="section-header">
          <span class="section-title-inline">Description</span>
          {#if !editingBody && capabilities.state_mutation && !stalePR}
            <button
              class="edit-body-btn"
              onclick={startEditBody}
            >
              Edit
            </button>
          {/if}
        </div>
        {#if editingBody}
          <div class="body-edit">
            <!-- svelte-ignore a11y_autofocus -->
            <textarea
              class="body-edit-textarea"
              bind:value={bodyDraft}
              onkeydown={onBodyKeydown}
              disabled={savingBody}
              autofocus
            ></textarea>
            <div class="body-edit-actions">
              <button
                class="title-edit-save"
                onclick={() => void saveBody()}
                disabled={savingBody}
              >
                {savingBody ? "Saving..." : "Save"}
              </button>
              <button
                class="title-edit-cancel"
                onclick={cancelEditBody}
                disabled={savingBody}
              >
                Cancel
              </button>
            </div>
          </div>
        {:else if pr.Body}
          <div class="inset-box-wrap">
            <button
              class="copy-icon-btn"
              class:copied
              onclick={() => copyBody(pr.Body)}
              title={copied ? "Copied!" : "Copy to clipboard"}
            >
              {#if copied}
                <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
                  <path d="M13.78 4.22a.75.75 0 010 1.06l-7.25 7.25a.75.75 0 01-1.06 0L2.22 9.28a.75.75 0 011.06-1.06L6 10.94l6.72-6.72a.75.75 0 011.06 0z"/>
                </svg>
              {:else}
                <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
                  <path d="M0 6.75C0 5.784.784 5 1.75 5h1.5a.75.75 0 010 1.5h-1.5a.25.25 0 00-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 00.25-.25v-1.5a.75.75 0 011.5 0v1.5A1.75 1.75 0 019.25 16h-7.5A1.75 1.75 0 010 14.25v-7.5z"/>
                  <path d="M5 1.75C5 .784 5.784 0 6.75 0h7.5C15.216 0 16 .784 16 1.75v7.5A1.75 1.75 0 0114.25 11h-7.5A1.75 1.75 0 015 9.25v-7.5zm1.75-.25a.25.25 0 00-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 00.25-.25v-7.5a.25.25 0 00-.25-.25h-7.5z"/>
                </svg>
              {/if}
            </button>
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="inset-box markdown-body"
              class:dragging={dragSourceIndex !== null}
              onclick={onBodyClick}
              ondragstart={onBodyDragStart}
              ondragover={onBodyDragOver}
              ondragleave={onBodyDragLeave}
              ondrop={onBodyDrop}
              ondragend={onBodyDragEnd}
            >{@html renderMarkdown(pr.Body, { provider, platformHost, owner, name, repoPath }, { interactiveTasks: capabilities.state_mutation })}</div>
          </div>
        {:else if capabilities.state_mutation && !stalePR}
          <button
            class="add-description-btn"
            onclick={startEditBody}
          >
            Add a description
          </button>
        {/if}
      </div>

      <!-- Comment box -->
      <div class="section">
        <CommentBox
          {owner}
          {name}
          {number}
          provider={detail.repo.provider}
          platformHost={detail.platform_host}
          repoPath={detail.repo.repo_path}
          disabled={stalePR || !capabilities.comment_mutation}
        />
      </div>

      <!-- Activity -->
      <div class="section">
        <div class="section-title-row">
          <h3 class="section-title">Activity</h3>
          <PRTimelineFilter
            filter={timelineFilter}
            onChange={updateTimelineFilter}
          />
        </div>
        {#if detailStore.getDetailLoaded()}
          <EventTimeline
            events={filteredTimelineEvents}
            {provider}
            {platformHost}
            repoOwner={owner}
            repoName={name}
            {repoPath}
            filtered={hasActiveTimelineFilters}
            showCommitDetails={timelineFilter.showCommitDetails}
            onEditComment={capabilities.comment_mutation && !stalePR
              ? editTimelineComment
              : undefined}
          />
        {:else if detailStore.isDetailSyncing()}
          <div class="loading-placeholder">
            <svg class="sync-spinner" width="14" height="14" viewBox="0 0 16 16" fill="none">
              <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="2" stroke-dasharray="28" stroke-dashoffset="8" stroke-linecap="round"/>
            </svg>
            Loading discussion...
          </div>
        {:else}
          <div class="loading-placeholder">Detail not yet loaded</div>
        {/if}
      </div>
          </div>
        </div>
      {/if}
    </div>
  {/if}
{/if}

<style>
  .state-center {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
  }

  .state-msg {
    font-size: var(--font-size-root);
    color: var(--text-muted);
  }

  .state-msg--error {
    color: var(--accent-red);
  }

  .pull-detail-wrap {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .detail-load-error {
    padding: 6px 16px;
    background: var(--accent-red-soft, color-mix(in srgb, var(--accent-red) 12%, transparent));
    color: var(--accent-red);
    border-bottom: 1px solid var(--border-subtle);
    font-size: var(--font-size-sm);
    flex-shrink: 0;
  }

  .pull-detail {
    padding: 20px 24px;
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    min-width: 0;
    overflow-y: auto;
    overflow-x: hidden;
    width: 100%;
  }

  .pull-detail-content {
    container: pull-detail / inline-size;
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: 100%;
    max-width: 800px;
    margin-inline: auto;
  }

  .label-editor-anchor {
    position: relative;
  }

  .label-editor-popover {
    position: fixed;
    z-index: 60;
  }

  .label-editor-backdrop {
    position: fixed;
    inset: 0;
    z-index: 55;
    background: rgba(128, 128, 128, 0.3);
  }

  .detail-header {
    position: relative;
    display: flex;
    align-items: flex-start;
    gap: 10px;
  }

  .title-line {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    flex: 1;
    min-width: 0;
  }

  .detail-title {
    font-size: var(--font-size-xl);
    font-weight: 600;
    color: var(--text-primary);
    line-height: 1.35;
    min-width: 0;
  }

  .edit-title-btn {
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    padding: 0;
    font-size: var(--font-size-2xs);
    flex-shrink: 0;
    margin-top: 3px;
  }

  .edit-title-btn:hover {
    color: var(--accent-blue);
  }

  .title-edit {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: 1;
  }

  .title-edit-input {
    flex: 1;
    font-size: var(--font-size-lg);
    font-weight: 600;
    font-family: var(--font-sans);
    padding: 4px 8px;
    background: var(--bg-inset);
    border: 1px solid var(--accent-blue);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    outline: none;
  }

  .title-edit-save,
  .title-edit-cancel {
    font-size: var(--font-size-2xs);
    padding: 4px 10px;
    border-radius: var(--radius-sm);
    cursor: pointer;
    white-space: nowrap;
  }

  .title-edit-save {
    background: var(--accent-blue);
    color: #fff;
    border: none;
  }

  .title-edit-save:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .title-edit-cancel {
    background: transparent;
    color: var(--text-secondary);
    border: 1px solid var(--border-default);
  }

  .title-edit-cancel:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .gh-link {
    flex-shrink: 0;
    color: var(--text-muted);
    display: flex;
    align-items: center;
    margin-top: 3px;
    transition: color 0.1s;
  }

  .gh-link:hover {
    color: var(--accent-blue);
    text-decoration: none;
  }

  .star-btn {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    margin-top: 3px;
    cursor: pointer;
    background: none;
    border: none;
    padding: 0;
  }

  .star-detail-icon {
    color: var(--text-muted);
    transition: color 0.1s;
  }

  .star-detail-icon:hover {
    color: var(--accent-amber);
  }

  .star-detail-icon--active {
    color: var(--accent-amber);
  }

  .meta-row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 4px;
  }

  .meta-item {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
  }

  .meta-sep {
    font-size: var(--font-size-sm);
    color: var(--text-muted);
  }

  .sync-indicator {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: var(--font-size-xs);
    color: var(--accent-blue);
  }

  .sync-spinner {
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  .meta-branch {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    font-size: var(--font-size-sm);
  }

  .branch-icon {
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .branch-name-btn {
    position: relative;
    color: var(--text-secondary);
    font-family: "SFMono-Regular", "Consolas", "Liberation Mono", "Menlo", monospace;
    font-size: var(--font-size-sm);
    background: none;
    border: none;
    padding: 1px 4px;
    border-radius: 3px;
    cursor: pointer;
    transition: background 0.15s, color 0.15s;
  }

  .branch-name-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .branch-name-btn--copied {
    color: var(--accent-green);
    background: color-mix(
      in srgb, var(--accent-green) 10%, transparent
    );
  }

  .branch-name-btn--copied::after {
    content: "Copied!";
    position: absolute;
    bottom: calc(100% + 6px);
    left: 50%;
    transform: translateX(-50%);
    font-family: inherit;
    font-size: var(--font-size-2xs);
    font-weight: 600;
    letter-spacing: 0.02em;
    color: #fff;
    background: var(--accent-green);
    padding: 2px 8px;
    border-radius: 4px;
    white-space: nowrap;
    pointer-events: none;
    animation: copied-pop 0.2s ease-out;
  }

  @keyframes copied-pop {
    from {
      opacity: 0;
      transform: translateX(-50%) translateY(4px);
    }
    to {
      opacity: 1;
      transform: translateX(-50%) translateY(0);
    }
  }

  .branch-arrow {
    color: var(--text-muted);
  }

  .chips-row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    min-width: 0;
  }

  .chips-row :global(.btn--labels) {
    min-height: 22px;
    padding: 0 8px;
    border-radius: 8px;
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .chips-row :global(.btn--labels svg) {
    width: 13px;
    height: 13px;
  }

  :global(.kanban-select) {
    min-width: 150px;
  }

  :global(.kanban-select--header) {
    flex-shrink: 0;
    margin-left: auto;
  }

  :global(.kanban-select--below-chips) {
    display: none;
  }

  :global(.kanban-select--new .select-dropdown-trigger) {
    color: var(--kanban-new);
  }

  :global(.kanban-select--reviewing .select-dropdown-trigger) {
    color: var(--accent-amber);
  }

  :global(.kanban-select--waiting .select-dropdown-trigger) {
    color: var(--accent-purple);
  }

  :global(.kanban-select--awaiting-merge .select-dropdown-trigger) {
    color: var(--accent-green);
  }

  @container pull-detail (max-width: 640px) {
    .detail-header {
      flex-wrap: wrap;
    }

    :global(.kanban-select--header) {
      display: none;
    }

    :global(.kanban-select--below-chips) {
      display: block;
      min-width: min(100%, 150px);
      width: fit-content;
    }
  }

  .primary-actions-wrap {
    position: relative;
    min-width: 0;
  }

  .actions-row {
    display: flex;
    align-items: flex-start;
    flex-wrap: wrap;
    gap: 8px;
    min-width: 0;
    max-width: 100%;
  }

  .actions-row :global(.approve-section),
  .actions-row :global(.ready-section),
  .actions-row :global(.workflow-approval-section) {
    min-width: 0;
  }

  .actions-row :global(.action-button) {
    max-width: 100%;
  }

  .primary-workspace-action {
    display: none;
    min-width: 0;
  }

  .actions-row :global(.action-button__label),
  .actions-row :global(.action-button__short-label) {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  @container pull-detail (max-width: 560px) {
    .actions-row--primary :global(.btn--close svg) {
      display: none;
    }
  }

  .actions-menu-wrap {
    display: none;
    position: relative;
    z-index: 65;
  }

  .actions-menu-trigger {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 28px;
    padding: 5px 11px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
    cursor: pointer;
  }

  .actions-menu-trigger:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .actions-menu-popover {
    position: absolute;
    z-index: 20;
    top: calc(100% + 6px);
    left: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: min(240px, calc(100cqw - 48px));
    max-width: calc(100cqw - 48px);
    padding: 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md, 8px);
    background: var(--bg-surface);
    box-shadow: 0 12px 30px rgba(0, 0, 0, 0.18);
  }

  .actions-menu-popover :global(.action-button) {
    width: 100%;
    justify-content: flex-start;
  }

  .actions-menu-popover__item {
    width: 100%;
    min-width: 0;
  }

  .actions-menu-popover__item--labels {
    position: relative;
  }

  .actions-menu-popover__item--labels.label-editor-anchor {
    width: 100%;
  }

  .actions-menu-popover :global(.approve-section),
  .actions-menu-popover :global(.ready-section),
  .actions-menu-popover :global(.workflow-approval-section) {
    width: 100%;
  }

  .actions-menu-popover :global(.approve-section--open) {
    gap: 8px;
  }

  .actions-menu-popover :global(.approve-popover) {
    position: static;
    width: 100%;
    box-shadow: none;
  }

  .actions-menu-popover :global(.approve-actions) {
    flex-wrap: wrap;
  }

  .actions-menu-popover :global(.action-button__short-label) {
    display: none;
  }

  @media (max-width: 640px) {
    .primary-workspace-action {
      display: block;
    }

    .pull-detail-content--has-compact-actions .actions-row.actions-row--workspace {
      display: none;
    }
  }

  @container pull-detail (max-width: 520px) {
    .actions-row--primary :global(.action-button__label),
    .actions-row--workspace :global(.action-button__label) {
      display: none;
    }

    .actions-row--primary :global(.action-button__short-label),
    .actions-row--workspace :global(.action-button__short-label) {
      display: inline;
    }
  }

  @container pull-detail (max-width: 340px) {
    .pull-detail-content .primary-actions-wrap .actions-row--primary {
      display: none;
    }

    .pull-detail-content .primary-actions-wrap .actions-menu-wrap {
      display: block;
    }

    .pull-detail-content--has-compact-actions .label-editor-anchor--inline,
    .pull-detail-content--has-compact-actions .actions-row.actions-row--workspace {
      display: none;
    }
  }

  .action-error {
    font-size: var(--font-size-xs);
    color: var(--accent-red, #d73a49);
  }

  .action-error--state {
    display: block;
    margin-top: 6px;
  }

  .action-error--workspace-compact {
    display: block;
    margin-top: 6px;
  }

  .section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .section-title-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .section-title {
    font-size: var(--font-size-sm);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }

  .section-title-inline {
    font-size: var(--font-size-sm);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }

  .inset-box-wrap {
    position: relative;
  }

  .copy-icon-btn {
    position: absolute;
    top: 6px;
    right: 6px;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    opacity: 0;
    transition: opacity 0.15s, background 0.15s, color 0.15s;
    z-index: 1;
  }

  .inset-box-wrap:hover .copy-icon-btn,
  .copy-icon-btn:focus-visible {
    opacity: 1;
  }

  .copy-icon-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-secondary);
  }

  .copy-icon-btn:active {
    transform: scale(0.92);
  }

  .copy-icon-btn.copied {
    opacity: 1;
    color: var(--accent-green);
    background: color-mix(in srgb, var(--accent-green) 12%, transparent);
  }

  @media (hover: none) {
    .copy-icon-btn {
      opacity: 1;
    }
  }

  .inset-box {
    font-size: var(--font-size-root);
    color: var(--text-primary);
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    padding: 10px 12px;
    word-break: break-word;
    line-height: 1.6;
  }

  .merge-warnings {
    font-size: var(--font-size-sm);
    padding: 8px 12px;
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    color: var(--text-secondary);
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .merge-warning-line {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .merge-warning-line a {
    color: inherit;
    text-decoration: underline;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .merge-warning-line--conflict {
    color: var(--accent-amber);
  }

  .files-stat {
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .files-stat--add {
    color: var(--accent-green);
  }

  .files-stat--del {
    color: var(--accent-red);
  }

  .refresh-banner {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px 12px;
    background: var(--bg-inset);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    margin-bottom: 8px;
  }

  .sync-dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: var(--accent-green);
    animation: pulse 1.5s ease-in-out infinite;
  }

  @keyframes pulse {
    0%, 100% { opacity: 0.4; }
    50% { opacity: 1; }
  }

  .loading-placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 24px 0;
    font-size: var(--font-size-sm);
    color: var(--text-muted);
  }

  .detail-tabs {
    display: flex;
    gap: 0;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
    flex-shrink: 0;
  }

  .detail-tab {
    font-size: var(--font-size-sm);
    font-weight: 500;
    padding: 8px 16px;
    color: var(--text-secondary);
    border-bottom: 2px solid transparent;
    transition: color 0.1s, border-color 0.1s;
    display: flex;
    align-items: center;
    gap: 6px;
    background: none;
    border-top: none;
    border-left: none;
    border-right: none;
    cursor: pointer;
    font-family: inherit;
  }

  .detail-tab:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .detail-tab--active {
    color: var(--text-primary);
    border-bottom-color: var(--accent-blue);
  }

  .edit-body-btn {
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    padding: 0;
    font-size: var(--font-size-2xs);
  }

  .edit-body-btn:hover {
    color: var(--accent-blue);
  }

  .body-edit {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .body-edit-textarea {
    width: 100%;
    min-height: 120px;
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    line-height: 1.5;
    padding: 10px;
    background: var(--bg-inset);
    border: 1px solid var(--accent-blue);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    resize: vertical;
    outline: none;
  }

  .body-edit-actions {
    display: flex;
    gap: 6px;
  }

  .add-description-btn {
    background: none;
    border: 1px dashed var(--border-default);
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    padding: 12px;
    width: 100%;
    cursor: pointer;
    font-size: var(--font-size-xs);
    text-align: center;
  }

  .add-description-btn:hover {
    border-color: var(--accent-blue);
    color: var(--accent-blue);
  }

  @media (max-width: 640px) {
    .pull-detail {
      --detail-mobile-type-xs: var(--mobile-type-xs, var(--font-size-mobile-xs));
      --detail-mobile-type-sm: var(--mobile-type-sm, var(--font-size-mobile-sm));
      --detail-mobile-type-body: var(--mobile-type-body, 1rem);
      --detail-mobile-type-title: var(--mobile-type-title, var(--font-size-mobile-title));
      --detail-mobile-space-xs: 0.5rem;
      --detail-mobile-space-sm: 0.75rem;
      --detail-mobile-space-md: 1rem;
      --detail-mobile-hit-target: 2.85rem;
      padding: var(--detail-mobile-space-md);
      font-size: var(--font-size-mobile-body);
      line-height: 1.5;
    }

    .pull-detail-content {
      gap: var(--detail-mobile-space-md);
      max-width: 100%;
    }

    .detail-header,
    .title-line {
      gap: var(--detail-mobile-space-sm);
    }

    .detail-title {
      font-size: var(--font-size-mobile-title);
      line-height: 1.25;
    }

    .edit-title-btn,
    .edit-body-btn,
    .star-btn,
    .gh-link,
    .copy-icon-btn {
      min-width: var(--detail-mobile-hit-target);
      min-height: var(--detail-mobile-hit-target);
      justify-content: center;
      padding: var(--detail-mobile-space-xs);
      margin-top: 0;
      font-size: var(--font-size-mobile-sm);
    }

    .pull-detail-content .meta-row :global(.copy-number-btn) {
      min-width: 0;
      min-height: 0;
      padding: 0;
      border-radius: 3px;
      font-size: var(--font-size-mobile-sm);
      line-height: 1.35;
    }

    @media (max-width: 480px) {
      .pull-detail-content .meta-row :global(.copy-number-btn) {
        min-width: max(44px, var(--detail-mobile-hit-target));
        min-height: max(44px, var(--detail-mobile-hit-target));
        padding: var(--detail-mobile-space-xs);
        border-radius: var(--radius-sm);
      }
    }

    .meta-row,
    .chips-row,
    .actions-row,
    .body-edit-actions {
      gap: var(--detail-mobile-space-xs);
    }

    .meta-item,
    .meta-sep,
    .meta-branch,
    .branch-name-btn,
    .sync-indicator,
    .section-title,
    .section-title-inline,
    .files-stat,
    .merge-warnings,
    .action-error,
    .refresh-banner,
    .loading-placeholder,
    .detail-tab {
      font-size: var(--font-size-mobile-sm);
      line-height: 1.35;
    }

    .meta-sep--branch,
    .meta-sep--sync {
      display: none;
    }

    .inset-box,
    .body-edit-textarea,
    .title-edit-input,
    .title-edit-save,
    .title-edit-cancel,
    .add-description-btn,
    .detail-load-error,
    :global(.markdown-body) {
      font-size: var(--font-size-mobile-body);
      line-height: 1.55;
    }

    .inset-box {
      padding: var(--detail-mobile-space-sm) var(--detail-mobile-space-md);
      border-radius: 0.75rem;
    }

    :global(.markdown-body pre),
    :global(.markdown-body code) {
      max-width: 100%;
      white-space: pre-wrap;
      overflow-wrap: anywhere;
      word-break: break-word;
    }

    :global(.markdown-body code) {
      font-size: 0.9em;
    }

    .pull-detail :global(.chip),
    .pull-detail :global(.state-chip),
    .pull-detail :global(.status-chip) {
      min-height: calc(var(--detail-mobile-hit-target) * 0.65);
      padding: 0.2rem var(--detail-mobile-space-xs);
      border-radius: 999rem;
      font-size: var(--font-size-mobile-xs);
      line-height: 1.25;
    }

    .actions-row :global(.action-button),
    .actions-menu-trigger,
    .detail-tab,
    .title-edit-save,
    .title-edit-cancel,
    .add-description-btn {
      min-height: var(--detail-mobile-hit-target);
    }

    .copy-icon-btn {
      position: static;
      opacity: 1;
    }
  }
</style>
