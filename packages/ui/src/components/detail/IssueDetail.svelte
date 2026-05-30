<script lang="ts">
  import { tick, untrack } from "svelte";
  import { providerItemPath, providerRepoPath, providerRouteParams } from "../../api/provider-routes.js";
  import type { Label, ProviderCapabilities } from "../../api/types.js";
  import {
    getStores, getClient, getActions,
    getUIConfig, getNavigate,
  } from "../../context.js";
  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";
  import type { IssueDetailSyncMode } from "../../stores/issues.svelte.js";
  import { renderMarkdown } from "../../utils/markdown.js";
  import { moveTaskListItem, toggleTaskListItem } from "../../utils/task-list.js";
  import { timeAgo } from "../../utils/time.js";
  import { copyToClipboard } from "../../utils/clipboard.js";
  import EventTimeline from "./EventTimeline.svelte";
  import IssueCommentBox from "./IssueCommentBox.svelte";
  import ActionButton from "../shared/ActionButton.svelte";
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
  import CopyItemNumber from "./CopyItemNumber.svelte";
  import MonitorUpIcon from "@lucide/svelte/icons/monitor-up";
  import PackagePlusIcon from "@lucide/svelte/icons/package-plus";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import TagsIcon from "@lucide/svelte/icons/tags";
  import XIcon from "@lucide/svelte/icons/x";

  const CLEAR_LABELS_PENDING = "__clear-label-selection__";

  const { issues, activity } = getStores();
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
    thread_reply: false,
    thread_resolve: false,
    review_draft_mutation: false,
    review_thread_resolution: false,
    read_review_threads: false,
    native_multiline_ranges: false,
    supported_review_actions: [],
  };

  function currentCapabilities(): ProviderCapabilities {
    return issues.getIssueDetail()?.repo?.capabilities
      ?? defaultProviderCapabilities;
  }

  interface Props {
    owner: string;
    name: string;
    number: number;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    hideStaleWhileLoading?: boolean;
    autoSync?: IssueDetailSyncMode;
  }

  const {
    owner,
    name,
    number,
    provider,
    platformHost,
    repoPath,
    hideStaleWhileLoading = false,
    autoSync = "background",
  }: Props = $props();

  const routeRef = $derived({
    provider,
    platformHost,
    owner,
    name,
    repoPath,
  });
  const labelPickerCommandRef = $derived({
    itemType: "issue" as const,
    provider,
    platformHost,
    owner,
    name,
    repoPath,
    number,
  });

  // See PullDetail.svelte: while a route change is in flight, the
  // displayed issue may briefly belong to the previous route. Mutating
  // actions (state change, workspace create, etc.) read the props,
  // which point at the new route — so they must be gated until the
  // displayed issue catches up.
  const staleIssue = $derived.by(() => {
    const d = issues.getIssueDetail();
    if (d == null) return false;
    if (
      d.repo_owner !== owner ||
      d.repo_name !== name ||
      (d.issue?.Number ?? -1) !== number
    ) {
      return true;
    }
    return d.repo?.provider !== provider
      || d.repo?.repo_path !== repoPath
      || d.repo?.platform_host !== platformHost;
  });

  async function editTimelineComment(
    event: { PlatformID: number | null },
    body: string,
  ): Promise<boolean> {
    if (staleIssue) return false;
    if (event.PlatformID === null) return false;
    return issues.editIssueComment(owner, name, number, event.PlatformID, body);
  }

  $effect(() => {
    const requestOwner = owner;
    const requestName = name;
    const requestNumber = number;
    const requestProvider = provider;
    const requestPlatformHost = platformHost;
    const requestRepoPath = repoPath;
    const requestAutoSync = autoSync;
    untrack(() => {
      void issues.loadIssueDetail(
        requestOwner,
        requestName,
        requestNumber,
        {
          sync: requestAutoSync,
          provider: requestProvider,
          platformHost: requestPlatformHost,
          repoPath: requestRepoPath,
        },
      );
      issues.startIssueDetailPolling(
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
    return () => issues.stopIssueDetailPolling();
  });

  $effect(() => {
    const handler = (event: Event) => onOpenLabelPickerCommand(event);
    window.addEventListener(OPEN_LABEL_PICKER_EVENT, handler);
    return () => window.removeEventListener(OPEN_LABEL_PICKER_EVENT, handler);
  });

  // Clear conflict/error state on route change so issue A's
  // dialogs can't bleed into issue B's view.
  $effect(() => {
    void owner;
    void name;
    void number;
    branchConflict = null;
    workspaceCreating = false;
    workspaceError = null;
    stateError = null;
    labelPickerOpen = false;
    labelPickerError = null;
    pendingLabel = null;
  });

  let copied = $state(false);
  let copyTimeout: ReturnType<typeof setTimeout> | null = null;
  let labelPickerOpen = $state(false);
  let labelCatalog = $state<Label[]>([]);
  let labelCatalogSyncing = $state(false);
  let labelPickerError = $state<string | null>(null);
  let pendingLabel = $state<string | null>(null);
  let labelPickerAnchor = $state<HTMLDivElement>();
  let labelPickerPopover = $state<HTMLDivElement>();
  let labelPickerAutofocusFilter = $state(false);
  let labelPickerStyle = $state("");

  function closeLabelPicker(): void {
    labelPickerOpen = false;
    labelPickerError = null;
    pendingLabel = null;
    labelPickerAutofocusFilter = false;
  }

  function positionLabelPicker(): void {
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

  function onOpenLabelPickerCommand(event: Event): void {
    const detail = (event as CustomEvent<OpenLabelPickerDetail>).detail;
    if (labelPickerCommandMatches(labelPickerCommandRef, detail)) {
      void openLabelPicker();
    }
  }

  async function openLabelPicker(event?: MouseEvent): Promise<void> {
    labelPickerAnchor = (event?.currentTarget as HTMLElement | null)?.closest<HTMLDivElement>(".label-editor-anchor")
      ?? labelPickerAnchor;
    if (event !== undefined && labelPickerOpen) {
      closeLabelPicker();
      return;
    }
    labelPickerAutofocusFilter = event !== undefined && !(window.matchMedia?.("(pointer: coarse)").matches ?? false);
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
    const currentLabels = issues.getIssueDetail()?.issue.labels ?? [];
    pendingLabel = labelName;
    labelPickerError = null;
    const nextNames = nextCatalogLabelNames(currentLabels, labelCatalog, labelName);
    try {
      await issues.setIssueLabels(owner, name, number, nextNames);
    } catch (err) {
      labelPickerError = err instanceof Error ? err.message : String(err);
    } finally {
      pendingLabel = null;
    }
  }

  async function clearLabels(): Promise<void> {
    if (pendingLabel !== null) return;
    const currentLabels = issues.getIssueDetail()?.issue.labels ?? [];
    if (currentLabels.length === 0) return;
    pendingLabel = CLEAR_LABELS_PENDING;
    labelPickerError = null;
    try {
      await issues.setIssueLabels(owner, name, number, []);
    } catch (err) {
      labelPickerError = err instanceof Error ? err.message : String(err);
    } finally {
      pendingLabel = null;
    }
  }

  function onDocumentMousedown(e: MouseEvent): void {
    if (!labelPickerOpen) return;
    const target = e.target as Node;
    if (!labelPickerPopover?.contains(target) && !labelPickerAnchor?.contains(target)) {
      closeLabelPicker();
    }
  }

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

  function handleStarClick(): void {
    if (staleIssue) return;
    const detail = issues.getIssueDetail();
    if (!detail) return;
    void issues.toggleIssueStar(
      owner,
      name,
      number,
      detail.issue.Starred,
    );
  }

  let stateSubmitting = $state(false);
  let stateError = $state<string | null>(null);

  async function handleStateChange(
    newState: "open" | "closed",
  ): Promise<void> {
    if (staleIssue) return;
    if (!currentCapabilities().state_mutation) return;
    stateSubmitting = true;
    stateError = null;
    try {
      const { error: requestError } = await client.POST(
        providerItemPath("issues", routeRef, "/github-state"),
        {
          params: { path: { ...providerRouteParams(routeRef), number } },
          body: { state: newState },
        },
      );
      if (requestError) {
        throw new Error(
          requestError.detail
            ?? requestError.title
            ?? "failed to change issue state",
        );
      }
      await issues.loadIssueDetail(
        owner,
        name,
        number,
        { provider, platformHost, repoPath },
      );
      await issues.loadIssues();
      await activity.loadActivity();
    } catch (err) {
      stateError =
        err instanceof Error ? err.message : String(err);
    } finally {
      stateSubmitting = false;
    }
  }

  let workspaceCreating = $state(false);
  let workspaceError = $state<string | null>(null);
  const ISSUE_WORKSPACE_BRANCH_CONFLICT_TYPE =
    "urn:middleman:error:issue-workspace-branch-conflict";

  type APIErrorDetail = {
    location?: string;
    value?: unknown;
  };

  type APIError = {
    type?: string;
    title?: string;
    detail?: string;
    errors?: APIErrorDetail[] | null;
  };

  type BranchConflictState = {
    existingBranch: string;
    suggestedBranch: string;
    branchInput: string;
    error: string | null;
  };

  let branchConflict = $state<BranchConflictState | null>(
    null,
  );

  $effect(() => {
    if (branchConflict == null) return;
    return untrack(() => pushModalFrame("issue-detail-confirm", []));
  });
  const workspace = $derived(
    issues.getIssueDetail()?.workspace,
  );

  function issueWorkspaceBranch(): string {
    return `middleman/issue-${number}`;
  }

  function branchConflictValue(
    error: APIError,
    location: string,
  ): string | null {
    const value = error.errors?.find(
      (entry) => entry.location === location,
    )?.value;
    return typeof value === "string" && value
      ? value
      : null;
  }

  function parseBranchConflict(
    error: APIError | undefined,
  ): BranchConflictState | null {
    if (!error) {
      return null;
    }

    const existingBranch =
      branchConflictValue(error, "body.git_head_ref")
      ?? "";
    const suggestedBranch =
      branchConflictValue(
        error,
        "body.suggested_git_head_ref",
      )
      ?? "";
    const isTypedConflict =
      error.type === ISSUE_WORKSPACE_BRANCH_CONFLICT_TYPE;
    if (
      !isTypedConflict
      && (!existingBranch || !suggestedBranch)
    ) {
      return null;
    }

    return {
      existingBranch:
        existingBranch || issueWorkspaceBranch(),
      suggestedBranch:
        suggestedBranch
        || `${existingBranch || issueWorkspaceBranch()}-2`,
      branchInput:
        suggestedBranch
        || `${existingBranch || issueWorkspaceBranch()}-2`,
      error: null,
    };
  }

  type CreateWorkspaceOptions = {
    gitHeadRef?: string;
    reuseExistingBranch?: boolean;
    fromConflictDialog?: boolean;
  };

  async function createWorkspace(
    options: CreateWorkspaceOptions = {},
  ): Promise<void> {
    if (staleIssue) return;
    const detail = issues.getIssueDetail();
    if (!detail) return;

    if (!options.fromConflictDialog) {
      branchConflict = null;
    } else if (
      branchConflict
      && options.gitHeadRef?.trim() === ""
    ) {
      branchConflict.error =
        "Branch name cannot be empty.";
      return;
    }

    workspaceCreating = true;
    workspaceError = null;
    if (branchConflict) {
      branchConflict.error = null;
    }
    try {
      const { data, error: requestError } = await client.POST(
        providerItemPath("issues", routeRef, "/workspace"),
        {
          params: {
            path: {
              ...providerRouteParams(routeRef),
              number,
            },
          },
          body: {
            ...(options.gitHeadRef
              ? {
                  git_head_ref:
                    options.gitHeadRef.trim(),
                }
              : {}),
            ...(options.reuseExistingBranch
              ? {
                  reuse_existing_branch: true,
                }
              : {}),
          },
        },
      );
      if (requestError) {
        const conflict = parseBranchConflict(
          requestError as APIError,
        );
        if (conflict) {
          branchConflict = conflict;
          return;
        }

        const message =
          requestError.detail
          ?? requestError.title
          ?? "failed to create workspace";
        if (options.fromConflictDialog && branchConflict) {
          branchConflict.error = message;
          return;
        }
        throw new Error(
          message,
        );
      }
      if (data?.id) {
        navigate(`/terminal/${data.id}`);
      }
    } catch (err) {
      workspaceError =
        err instanceof Error ? err.message : String(err);
    } finally {
      workspaceCreating = false;
    }
  }

  function closeBranchConflictDialog(): void {
    if (workspaceCreating) return;
    branchConflict = null;
  }

  // Task-list checkbox clicks update the body locally for instant
  // feedback, then debounce a PATCH so a flurry of clicks collapses
  // into a single save. Target and body are captured at schedule
  // time so a route change before the timer fires can't redirect
  // the save to a different issue or lose the edit.
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
    void issues.saveIssueBodyInBackground(
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
    if (staleIssue || !currentCapabilities().state_mutation) {
      event.preventDefault();
      return;
    }
    const index = parseInt(raw, 10);
    if (Number.isNaN(index)) return;
    const detail = issues.getIssueDetail();
    if (!detail) return;
    const newBody = toggleTaskListItem(detail.issue.Body, index);
    if (newBody === detail.issue.Body) return;
    event.preventDefault();
    issues.setLocalIssueBody(
      provider, platformHost, owner, name, number, newBody,
    );
    scheduleBodySave(newBody);
  }

  // Drag-to-reorder for task-list items. See PullDetail.svelte for the
  // mirror implementation — the only difference is the store getter.
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
    if (staleIssue || !currentCapabilities().state_mutation) return;
    const target = event.target as HTMLElement | null;
    if (!target?.classList?.contains("task-drag-handle")) return;
    const raw = target.getAttribute("data-task-index");
    if (raw === null) return;
    const idx = parseInt(raw, 10);
    if (Number.isNaN(idx)) return;
    dragSourceIndex = idx;
    if (event.dataTransfer) {
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", String(idx));
    }
  }

  function onBodyDragOver(event: DragEvent): void {
    if (dragSourceIndex === null) return;
    const target = event.target as HTMLElement | null;
    const idx = findTaskItemIndex(target);
    if (idx === null) return;
    event.preventDefault();
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
    if (staleIssue || !currentCapabilities().state_mutation) return;
    const detail = issues.getIssueDetail();
    if (!detail) return;
    let target = to;
    if (from < to && side === "before") target = to - 1;
    else if (from > to && side === "after") target = to + 1;
    if (target === from) return;
    const newBody = moveTaskListItem(detail.issue.Body, from, target);
    if (newBody === detail.issue.Body) return;
    issues.setLocalIssueBody(
      provider, platformHost, owner, name, number, newBody,
    );
    scheduleBodySave(newBody);
  }

  function onBodyDragEnd(event: DragEvent): void {
    clearDragState(event.currentTarget as HTMLElement);
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

  function clearDragState(root?: HTMLElement | null): void {
    dragSourceIndex = null;
    dropTargetIndex = null;
    dropTargetSide = "before";
    if (root) clearDropIndicatorClasses(root);
  }

  // Drop any pending checkbox save when navigating to a different
  // issue so a stale toggle doesn't land on the new target. The
  // pending save still fires against the originally-captured target
  // so a fast click + navigate sequence persists.
  $effect(() => {
    void owner;
    void name;
    void number;
    flushBodySave();
    clearDragState();
  });
</script>

<svelte:document onmousedown={onDocumentMousedown} />

{#if issues.isIssueDetailLoading() && (issues.getIssueDetail() === null || (staleIssue && hideStaleWhileLoading))}
  <div class="state-center"><p class="state-msg">Loading...</p></div>
{:else if issues.getIssueDetailError() !== null && (issues.getIssueDetail() === null || (staleIssue && hideStaleWhileLoading))}
  <div class="state-center"><p class="state-msg state-msg--error">Error: {issues.getIssueDetailError()}</p></div>
{:else}
  {@const detail = issues.getIssueDetail()}
  {@const staleLoadError = staleIssue && issues.getIssueDetailError() !== null}
  {#if detail !== null}
    {@const issue = detail.issue}
    {@const labels = issue.labels ?? []}
    {@const capabilities = detail.repo?.capabilities ?? defaultProviderCapabilities}
    <div class="issue-detail">
      <div class="issue-detail-content">
      {#if staleLoadError}
        <div class="detail-load-error" data-testid="detail-load-error">
          Couldn't load this issue: {issues.getIssueDetailError()}
        </div>
      {/if}
      {#if issues.isIssueStaleRefreshing()}
        <div class="refresh-banner">
          <span class="sync-dot"></span>
          Refreshing...
        </div>
      {/if}
      <!-- Header -->
      <div class="detail-header">
        <h2 class="detail-title">{issue.Title}</h2>
        {#if !uiConfig.hideStar && !staleIssue}
          <button
            class="star-btn"
            onclick={handleStarClick}
            title={issue.Starred ? "Unstar" : "Star"}
          >
            {#if issue.Starred}
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
        <a class="gh-link" href={issue.URL} target="_blank" rel="noopener noreferrer" title="Open on GitHub">
          <svg width="14" height="14" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
            <path d="M6 3H3a1 1 0 0 0-1 1v9a1 1 0 0 0 1 1h9a1 1 0 0 0 1-1v-3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            <path d="M10 2h4v4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            <path d="M8 8L14 2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
          </svg>
        </a>
      </div>

      <!-- Meta row -->
      <div class="meta-row">
        <span class="meta-item">{detail.repo_owner}/{detail.repo_name}</span>
        <span class="meta-sep">·</span>
        <CopyItemNumber kind="issue" number={issue.Number} url={issue.URL} />
        <span class="meta-sep">·</span>
        <span class="meta-item">{issue.Author}</span>
        {#if issue.assignees && issue.assignees.length > 0}
          <span class="meta-sep">·</span>
          <span class="meta-item">Assigned: {issue.assignees.join(", ")}</span>
        {/if}
        <span class="meta-sep">·</span>
        <span class="meta-item">{timeAgo(issue.CreatedAt)}</span>
        <span class="meta-sep">·</span>
        <Chip size="sm" class={`issue-state-chip chip--${issue.State}`}>
          {issue.State === "open" ? "Open" : "Closed"}
        </Chip>
        {#if labels.length > 0 || (capabilities.read_labels && capabilities.label_mutation)}
          <span class="meta-sep">·</span>
          {#if labels.length > 0}
            <GitHubLabels {labels} mode="full" />
          {/if}
          {#if capabilities.read_labels && capabilities.label_mutation}
            <div class="label-editor-anchor" bind:this={labelPickerAnchor}>
              <ActionButton
                class="btn--labels"
                label="Labels"
                shortLabel="Labels"
                size="sm"
                surface="soft"
                tone="neutral"
                disabled={staleIssue}
                onclick={openLabelPicker}
              >
                <TagsIcon size="16" aria-hidden="true" />
              </ActionButton>
              {#if labelPickerOpen}
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
            </div>
          {/if}
        {/if}
        {#if issues.isIssueDetailSyncing()}
          <span class="meta-sep">·</span>
          <span class="sync-indicator" title="Syncing from GitHub">
            <svg class="sync-spinner" width="12" height="12" viewBox="0 0 16 16" fill="none">
              <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="2" stroke-dasharray="28" stroke-dashoffset="8" stroke-linecap="round"/>
            </svg>
            Syncing
          </span>
        {/if}
      </div>


      <!-- Issue body -->
      {#if issue.Body}
        <div class="section body-section">
          <div class="section-header">
            <span class="section-title-inline">Description</span>
          </div>
          <div class="inset-box-wrap">
            <button
              class="copy-icon-btn"
              class:copied
              onclick={() => copyBody(issue.Body)}
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
            >{@html renderMarkdown(issue.Body, { provider, platformHost, owner, name, repoPath }, { interactiveTasks: capabilities.state_mutation })}</div>
          </div>
        </div>
      {/if}

      <!-- Actions -->
      <div class="actions-row">
        {#if workspace}
          <ActionButton
            class="btn--workspace"
            disabled={staleIssue}
            onclick={() => {
              if (staleIssue) return;
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
            disabled={workspaceCreating || staleIssue}
            onclick={() => void createWorkspace()}
            tone="info"
            surface="soft"
            size="sm"
            label={workspaceCreating ? "Creating..." : "Create Workspace"}
            shortLabel={workspaceCreating ? "Creating..." : "Create Workspace"}
          >
            <PackagePlusIcon size="14" strokeWidth="2.2" aria-hidden="true" />
          </ActionButton>
        {/if}
        {#if issue.State === "open" && capabilities.state_mutation}
          <ActionButton
            class="btn--close"
            disabled={stateSubmitting || staleIssue}
            onclick={() => {
              if (staleIssue) return;
              handleStateChange("closed");
            }}
            tone="danger"
            surface="outline"
            size="sm"
            label={stateSubmitting ? "Closing..." : "Close issue"}
            shortLabel={stateSubmitting ? "Closing..." : "Close"}
          >
            <XIcon size="14" strokeWidth="2.2" aria-hidden="true" />
          </ActionButton>
        {:else if capabilities.state_mutation}
          <ActionButton
            class="btn--reopen"
            disabled={stateSubmitting || staleIssue}
            onclick={() => {
              if (staleIssue) return;
              handleStateChange("open");
            }}
            tone="success"
            surface="solid"
            size="sm"
            label={stateSubmitting ? "Reopening..." : "Reopen issue"}
            shortLabel={stateSubmitting ? "Reopening..." : "Reopen"}
          >
            <RefreshCwIcon size="14" strokeWidth="2.2" aria-hidden="true" />
          </ActionButton>
        {/if}
        {#if workspaceError}
          <span class="action-error">{workspaceError}</span>
        {/if}
        {#each actions.issue ?? [] as action (action.id)}
          <ActionButton
            class="btn--embedding-action"
            onclick={() => {
              if (staleIssue) return;
              action.handler({
                surface: "issue-detail", owner, name, number,
              });
            }}
            disabled={staleIssue}
            tone="neutral"
            surface="outline"
            size="sm"
          >
            {action.label}
          </ActionButton>
        {/each}
        {#if stateError}
          <span class="action-error">{stateError}</span>
        {/if}
      </div>

      <!-- Comment box -->
      <div class="section">
        <IssueCommentBox
          {owner}
          {name}
          {number}
          provider={detail.repo.provider}
          platformHost={detail.platform_host}
          repoPath={detail.repo.repo_path}
          disabled={staleIssue || !capabilities.comment_mutation}
        />
      </div>

      <!-- Activity -->
      <div class="section">
        <h3 class="section-title">Activity</h3>
        {#if issues.getIssueDetailLoaded()}
          <EventTimeline
            events={detail.events ?? []}
            {provider}
            {platformHost}
            repoOwner={owner}
            repoName={name}
            {repoPath}
            onEditComment={capabilities.comment_mutation && !staleIssue
              ? editTimelineComment
              : undefined}
          />
        {:else if issues.isIssueDetailSyncing()}
          <div class="loading-placeholder">
            <svg class="sync-spinner" width="14" height="14" viewBox="0 0 16 16" fill="none">
              <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="2" stroke-dasharray="28" stroke-dashoffset="8" stroke-linecap="round"/>
            </svg>
            Loading comments...
          </div>
        {:else}
          <div class="loading-placeholder">Detail not yet loaded</div>
        {/if}
      </div>
      </div>
    </div>

    {#if branchConflict}
      {@const conflict = branchConflict}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class="modal-overlay"
        onclick={closeBranchConflictDialog}
        onkeydown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            closeBranchConflictDialog();
          }
        }}
      >
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="modal"
          role="dialog"
          aria-modal="true"
          aria-labelledby="issue-workspace-branch-conflict-title"
          tabindex="-1"
          onclick={(event) => event.stopPropagation()}
        >
          <div class="modal-header">
            <h3
              id="issue-workspace-branch-conflict-title"
              class="modal-title"
            >
              Branch Name Conflict
            </h3>
            <button
              class="modal-close"
              onclick={closeBranchConflictDialog}
              title="Cancel (Esc)"
              disabled={workspaceCreating}
            >
              <svg
                width="16"
                height="16"
                viewBox="0 0 16 16"
                fill="currentColor"
              >
                <path d="M3.72 3.72a.75.75 0 011.06 0L8 6.94l3.22-3.22a.75.75 0 111.06 1.06L9.06 8l3.22 3.22a.75.75 0 11-1.06 1.06L8 9.06l-3.22 3.22a.75.75 0 01-1.06-1.06L6.94 8 3.72 4.78a.75.75 0 010-1.06z"/>
              </svg>
            </button>
          </div>

          <div class="modal-body">
            <p class="modal-copy">
              The branch <code>{conflict.existingBranch}</code> already exists locally.
            </p>

            <div class="branch-conflict-option">
              <div>
                <div class="branch-conflict-heading">
                  Reuse the existing branch
                </div>
                <div class="branch-conflict-copy">
                  Reopen the workspace on the branch that is already present in the local clone.
                </div>
              </div>
              <ActionButton
                class="btn btn--primary"
                onclick={() => void createWorkspace({
                  gitHeadRef: conflict.existingBranch,
                  reuseExistingBranch: true,
                  fromConflictDialog: true,
                })}
                disabled={workspaceCreating}
                tone="neutral"
                surface="outline"
                size="sm"
              >
                {workspaceCreating ? "Creating..." : "Use Existing Branch"}
              </ActionButton>
            </div>

            <div class="field">
              <label
                class="field-label"
                for="issue-workspace-branch-name"
              >
                New branch name
              </label>
              <input
                id="issue-workspace-branch-name"
                class="field-input"
                type="text"
                bind:value={conflict.branchInput}
                oninput={() => {
                  if (branchConflict) {
                    branchConflict.error = null;
                  }
                }}
              />
              <p class="field-hint">
                Suggested: <code>{conflict.suggestedBranch}</code>
              </p>
            </div>

            {#if conflict.error}
              <p class="merge-error">{conflict.error}</p>
            {/if}
          </div>

          <div class="modal-footer">
            <ActionButton
              class="btn btn--secondary"
              onclick={closeBranchConflictDialog}
              disabled={workspaceCreating}
              tone="neutral"
              surface="outline"
            >
              Cancel
            </ActionButton>
            <ActionButton
              class="btn btn--primary btn--green"
              onclick={() => void createWorkspace({
                gitHeadRef: conflict.branchInput,
                fromConflictDialog: true,
              })}
              disabled={workspaceCreating}
              tone="success"
              surface="solid"
            >
              {workspaceCreating ? "Creating..." : "Create New Branch"}
            </ActionButton>
          </div>
        </div>
      </div>
    {/if}
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

  .issue-detail {
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

  .issue-detail-content {
    container: issue-detail / inline-size;
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
    z-index: 20;
  }

  .detail-header {
    display: flex;
    align-items: flex-start;
    gap: 10px;
  }

  .detail-title {
    font-size: var(--font-size-xl);
    font-weight: 600;
    color: var(--text-primary);
    line-height: 1.35;
    flex: 1;
    min-width: 0;
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

  .meta-row :global(.btn--labels) {
    min-height: 18px;
    padding: 0 6px;
    border-radius: 8px;
    font-size: var(--font-size-2xs);
    font-weight: 600;
  }

  .meta-row :global(.btn--labels svg) {
    width: 12px;
    height: 12px;
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

  .actions-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 0;
  }

  .action-error {
    font-size: var(--font-size-xs);
    color: var(--accent-red, #d73a49);
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

  .detail-load-error {
    padding: 6px 16px;
    background: var(--accent-red-soft, color-mix(in srgb, var(--accent-red) 12%, transparent));
    color: var(--accent-red);
    border-bottom: 1px solid var(--border-subtle);
    font-size: var(--font-size-sm);
    flex-shrink: 0;
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

  .modal-overlay {
    position: fixed;
    inset: 0;
    background: var(--overlay-bg);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
    animation: fade-in 0.12s ease-out;
  }

  @keyframes fade-in {
    from { opacity: 0; }
    to { opacity: 1; }
  }

  .modal {
    width: min(560px, 92vw);
    background: var(--bg-surface);
    border: 1px solid var(--border-muted);
    border-radius: 12px;
    box-shadow: var(--shadow-lg);
    overflow: hidden;
  }

  .modal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border-muted);
  }

  .modal-title {
    margin: 0;
    font-size: var(--font-size-lg);
    font-weight: 600;
    color: var(--text-primary);
  }

  .modal-close {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border: 1px solid transparent;
    border-radius: 8px;
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
  }

  .modal-close:hover:not(:disabled) {
    background: var(--bg-inset);
    color: var(--text-primary);
  }

  .modal-close:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .modal-body {
    padding: 16px;
    display: grid;
    gap: 14px;
  }

  .modal-copy {
    margin: 0;
    font-size: var(--font-size-root);
    color: var(--text-secondary);
    line-height: 1.5;
  }

  .branch-conflict-option {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    padding: 12px;
    border: 1px solid var(--border-muted);
    border-radius: 10px;
    background: var(--bg-inset);
  }

  .branch-conflict-heading {
    font-size: var(--font-size-root);
    font-weight: 600;
    color: var(--text-primary);
    margin-bottom: 4px;
  }

  .branch-conflict-copy {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: 1.5;
  }

  .field {
    display: grid;
    gap: 6px;
  }

  .field-label {
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
  }

  .field-input {
    width: 100%;
    min-width: 0;
    padding: 9px 11px;
    border: 1px solid var(--border-muted);
    border-radius: 8px;
    background: var(--bg-canvas);
    color: var(--text-primary);
    font-size: var(--font-size-root);
  }

  .field-hint {
    margin: 0;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .merge-error {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--accent-red, #d73a49);
  }

  .modal-footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 14px 16px;
    border-top: 1px solid var(--border-muted);
  }

  @media (max-width: 640px) {
    .issue-detail {
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

    .issue-detail-content {
      gap: var(--detail-mobile-space-md);
      max-width: 100%;
    }

    .detail-header {
      gap: var(--detail-mobile-space-sm);
    }

    .detail-title {
      font-size: var(--font-size-mobile-title);
      line-height: 1.25;
    }

    .star-btn,
    .gh-link,
    .copy-icon-btn,
    .meta-row :global(.copy-number-btn) {
      min-width: var(--detail-mobile-hit-target);
      min-height: var(--detail-mobile-hit-target);
      justify-content: center;
      padding: var(--detail-mobile-space-xs);
      margin-top: 0;
    }

    .meta-row {
      gap: var(--detail-mobile-space-xs);
    }

    .meta-item,
    .meta-sep,
    .sync-indicator,
    .section-title,
    .section-title-inline,
    .action-error,
    .refresh-banner,
    .loading-placeholder {
      font-size: var(--font-size-mobile-sm);
      line-height: 1.35;
    }

    .inset-box,
    .modal-copy,
    .branch-conflict-heading,
    .branch-conflict-copy,
    .field-label,
    .field-input,
    .field-hint,
    .merge-error,
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

    .issue-detail :global(.chip),
    .issue-detail :global(.state-chip),
    .issue-detail :global(.status-chip) {
      min-height: calc(var(--detail-mobile-hit-target) * 0.65);
      padding: 0.2rem var(--detail-mobile-space-xs);
      border-radius: 999rem;
      font-size: var(--font-size-mobile-xs);
      line-height: 1.25;
    }

    .actions-row {
      gap: var(--detail-mobile-space-sm);
    }

    .actions-row :global(.action-button),
    .modal-close,
    .field-input {
      min-height: var(--detail-mobile-hit-target);
      font-size: var(--font-size-mobile-sm);
    }

    .copy-icon-btn {
      position: static;
      opacity: 1;
    }
  }
</style>
