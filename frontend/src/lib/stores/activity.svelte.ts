import { Effect, Result } from "effect";
import type { AppRuntime } from "../app/runtime.js";
import type { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import { executeGeneratedApiRequest, type GeneratedApi } from "../api/generated-api.js";
import { retryIdempotentRead } from "../api/retry-policy.js";
import type {
  ActivityItem,
  ActivityAuthorsParams,
  ActivityParams,
  ActivityResponse,
  ActivitySettings,
  ActivitySubject,
  NotificationBulkResponse,
  WorkspaceActivitySubject,
} from "../api/types.js";
import { activityItemKey } from "../components/activityRows.js";
import { ActivityWorkflow } from "./activity-workflow.js";
import { showFlash } from "./flash.svelte.js";
import { createListPaging } from "./list-paging.svelte.js";
import { ProviderMutations, providerMutationFailureMessage } from "./ordered-mutations.js";
import type { TriFilter } from "../utils/tri-filter.js";
import {
  PULL_ATTRIBUTE_SECTIONS,
  cycleFilterInSections,
  toggleFilterInSections,
  type PullAttributeFilter,
} from "../utils/pull-attributes.js";
import { readInvolvesMeFilter, writeInvolvesMeFilter } from "./involves-me-filter.js";
import { readUnassignedFilter, writeUnassignedFilter } from "./unassigned-filter.js";
import { repositoryKeyFromWire, repositoryKeyToWire } from "../api/repository-key.js";
import { parseRepoFilterSelection, providerQualifiedRepoFilterValue } from "../utils/repo-filter-values.js";
import { cycleLabelFilter as cycleLabel, labelFilterParams, type LabelFilters } from "../utils/label-filter.js";

export type TimeRange = "24h" | "7d" | "30d" | "90d";
export type ViewMode = "flat" | "threaded";
export type ActivityItemType = "pr" | "issue";
export type ActivityAPIItemType = ActivityItemType | "repo";

export const DEFAULT_ACTIVITY_ITEM_TYPES = ["pr", "issue"] as const;
export const DEFAULT_EVENT_TYPES = ["comment", "review", "commit", "force_push"] as const;
const PR_TIMELINE_EVENT_TYPES = ["comment", "review", "force_push"] as const;
const ISSUE_TIMELINE_EVENT_TYPES = ["comment"] as const;
const NO_ACTIVITY_FILTER_TYPE = "none";
const ACTIVITY_SAFETY_CAP = 5000; // Matches itemapi.ActivitySafetyCap.
const ACTIVITY_ITEM_TYPES_PARAM = "item_types";
const ACTIVITY_EVENT_TYPES_PARAM = "event_types";

// Default-branch force pushes and PR force pushes share a filter. Commits are
// intentionally different: the Commits filter controls only top-level branch
// activity, while commits inside PR timelines are always part of the PR.
const BRANCH_TYPE_FOR_EVENT: Partial<Record<string, string>> = {
  commit: "default_branch_commit",
  force_push: "default_branch_force_push",
};

export function buildActivityFilterTypes(
  enabledItemTypes: ReadonlySet<ActivityItemType>,
  enabledEvents: ReadonlySet<string>,
  hideDefaultBranchActivity: boolean,
  showNotifications = true,
): string[] {
  const allSelected =
    enabledItemTypes.size === DEFAULT_ACTIVITY_ITEM_TYPES.length &&
    enabledEvents.size === DEFAULT_EVENT_TYPES.length &&
    !hideDefaultBranchActivity &&
    showNotifications;
  // An empty list means "no type filter" — the backend returns every
  // activity_type, notifications included. Only short-circuit when the
  // notification toggle is also at its default, otherwise fall through
  // to build the explicit list that omits "notification".
  if (allSelected) return [];

  const types: string[] = [];
  if (enabledItemTypes.size === 0) types.push(NO_ACTIVITY_FILTER_TYPE);
  // Opening rows are timeline events, not parent anchors. Authoritative parent
  // summaries are projected separately, so opening rows follow only the
  // timeline events that can occur on that item kind: an issue timeline has
  // comments alone, so Reviews or Force pushes must not toggle issue opening
  // rows. The Commits toggle controls top-level default-branch commits only,
  // so it does not opt into PR or issue opening rows either.
  if (enabledItemTypes.has("pr") && PR_TIMELINE_EVENT_TYPES.some((eventType) => enabledEvents.has(eventType))) {
    types.push("new_pr");
  }
  if (enabledItemTypes.has("issue") && ISSUE_TIMELINE_EVENT_TYPES.some((eventType) => enabledEvents.has(eventType))) {
    types.push("new_issue");
  }
  if (!hideDefaultBranchActivity) {
    for (const evt of DEFAULT_EVENT_TYPES) {
      const branchType = BRANCH_TYPE_FOR_EVENT[evt];
      if (branchType && enabledEvents.has(evt)) types.push(branchType);
    }
  }
  for (const evt of DEFAULT_EVENT_TYPES) {
    if (evt === "commit") {
      if (enabledItemTypes.has("pr")) types.push(evt);
    } else if (enabledEvents.has(evt)) {
      types.push(evt);
    }
  }
  if (showNotifications) types.push("notification");
  return types.length > 0 ? types : [NO_ACTIVITY_FILTER_TYPE];
}

export function buildActivityItemTypeFilter(enabledItemTypes: ReadonlySet<ActivityItemType>): ActivityAPIItemType[] {
  return [...DEFAULT_ACTIVITY_ITEM_TYPES.filter((itemType) => enabledItemTypes.has(itemType)), "repo"];
}

export function isActivityItemTypeEnabled(itemType: string, enabledItemTypes: ReadonlySet<ActivityItemType>): boolean {
  if (itemType !== "pr" && itemType !== "issue") return true;
  return enabledItemTypes.has(itemType);
}

function readSelectedFilters<T extends string>(value: string | null, defaults: readonly T[]): Set<T> {
  if (value === null) return new Set(defaults);
  const selected = value === NO_ACTIVITY_FILTER_TYPE ? [] : value.split(",");
  return new Set(defaults.filter((candidate) => selected.includes(candidate)));
}

function writeSelectedFilters<T extends string>(
  searchParams: URLSearchParams,
  name: string,
  selected: ReadonlySet<T>,
  available: readonly T[],
  defaults: readonly T[],
  preserve: boolean,
): void {
  const ordered = available.filter((candidate) => selected.has(candidate));
  if (!preserve && ordered.length === defaults.length && defaults.every((value) => selected.has(value))) {
    searchParams.delete(name);
  } else {
    searchParams.set(name, ordered.length > 0 ? ordered.join(",") : NO_ACTIVITY_FILTER_TYPE);
  }
}

function readLegacyFilterSelections(value: string): {
  itemTypes: Set<ActivityItemType>;
  events: Set<string>;
} {
  const types = value ? value.split(",") : [];
  if (types.length === 0) {
    return {
      itemTypes: new Set(DEFAULT_ACTIVITY_ITEM_TYPES),
      events: new Set(DEFAULT_EVENT_TYPES),
    };
  }

  const encodedEmptyEventDefaultScope = types.length === 1 && types[0] === "notification";
  const itemTypes = encodedEmptyEventDefaultScope
    ? new Set(DEFAULT_ACTIVITY_ITEM_TYPES)
    : new Set(
        DEFAULT_ACTIVITY_ITEM_TYPES.filter((itemType) => types.includes(itemType === "pr" ? "new_pr" : "new_issue")),
      );
  const events = encodedEmptyEventDefaultScope
    ? new Set<string>()
    : new Set(DEFAULT_EVENT_TYPES.filter((eventType) => types.includes(eventType)));
  return { itemTypes, events };
}

// Activity item ids are "<source>:<source_id>"; notification rows use
// the "ntf" source whose source_id is the notification's DB id.
export function notificationDbId(activityItemId: string): number | null {
  const prefix = "ntf:";
  if (!activityItemId.startsWith(prefix)) return null;
  const id = Number(activityItemId.slice(prefix.length));
  return Number.isInteger(id) && id > 0 ? id : null;
}

const RANGE_MS: Record<TimeRange, number> = {
  "24h": 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000,
  "30d": 30 * 24 * 60 * 60 * 1000,
  "90d": 90 * 24 * 60 * 60 * 1000,
};

interface OwnedActivityResponse {
  readonly response: ActivityResponse;
  readonly startedAt: number;
}

interface ActivityReadResponse extends OwnedActivityResponse {
  readonly scope: string;
  readonly repo: string | undefined;
  readonly filterScope: string;
  readonly subjectDepth?: number;
  readonly partialFailure?: ApiProblemError | TransientTransportError;
}

interface CachedActivitySnapshot {
  readonly repo: string | undefined;
  readonly filterScope: string;
  readonly items: ActivityItem[];
  readonly itemActivity: ActivitySubject[];
  readonly workspaceActivity: WorkspaceActivitySubject[];
  readonly capped: boolean;
  readonly itemActivityCapped: boolean;
}

type ActivityPollProjection =
  | { readonly mode: "append"; readonly result: ActivityReadResponse }
  | { readonly mode: "replace"; readonly result: ActivityReadResponse };

export interface ActivityStoreOptions {
  runtime: AppRuntime;
  getGlobalRepo?: () => string | undefined;
  getBasePath?: () => string;
}

function apiErrorMessage(error: { detail?: string; title?: string }, fallback: string): string {
  return error.detail ?? error.title ?? fallback;
}

function readErrorMessage(
  error: ApiProblemError | TransientTransportError,
  fallback = "failed to load activity",
): string {
  if (error._tag === "ApiProblemError") {
    return apiErrorMessage(error.problem, fallback);
  }
  return "Could not reach Kenn Forge";
}

export function createActivityStore(opts: ActivityStoreOptions) {
  const runtime = opts.runtime;
  const getGlobalRepo = opts.getGlobalRepo ?? (() => undefined);
  const getBasePath = opts.getBasePath ?? (() => "/");

  // --- state ---

  let items = $state.raw<ActivityItem[]>([]);
  let itemActivity = $state.raw<ActivitySubject[]>([]);
  let workspaceActivity = $state.raw<WorkspaceActivitySubject[]>([]);
  let loading = $state(false);
  let storeError = $state<string | null>(null);
  let capped = $state(false);
  let itemActivityCapped = $state(false);
  let subjectCursor = $state("");
  let requestedSubjectDepth = $state(1);
  let displayedSubjectDepth = $state(1);
  let olderItemsPage: { cursor: string; depth: number } | undefined;
  let subjectPageScope: string | undefined;
  let activityEventCursor = $state("");
  let loadedThreadKeys = $state.raw<ReadonlySet<string>>(new Set());
  let loadingThreadKeys = $state.raw<ReadonlySet<string>>(new Set());
  let failedThreadKeys = $state.raw<ReadonlySet<string>>(new Set());
  let threadLoadError = $state<string | null>(null);
  let filterTypes = $state<string[]>([]);
  let searchQuery = $state<string | undefined>(undefined);
  let authorFilter = $state<string | undefined>(undefined);
  let authorCandidates = $state.raw<string[] | null>(null);
  let authorsLoading = $state(false);
  let authorsError = $state<string | null>(null);
  let timeRange = $state<TimeRange>("7d");
  let viewMode = $state<ViewMode>("flat");
  let timeRangeDefault: TimeRange = "7d";
  let viewModeDefault: ViewMode = "flat";
  let itemTypesDefault: readonly ActivityItemType[] = DEFAULT_ACTIVITY_ITEM_TYPES;
  let eventTypesDefault: readonly string[] = DEFAULT_EVENT_TYPES;
  let rollUpCommitsDefault = false;
  let hideDefaultBranchDefault = false;
  let showNotificationsDefault = true;
  const selectedFilters = new Set<string>();
  let defaultsHydrated = false;
  let timeRangeSelected = false;
  let viewModeSelected = false;
  let collapseThreads = $state(false);
  let rollUpCommits = $state(false);
  let collapseThreadsDefault = false;
  let fullEventProjectionRequired = false;
  let activityPageLimit: number | undefined;
  const flatPaging = createListPaging();
  let expandOverrides = $state<Set<string>>(new Set());
  let pagedActivityGeneration = 0;
  let childProjectionInvalidationGeneration = 0;
  let projectedChildInvalidationGeneration = 0;
  const threadRequestTokens = new Map<string, symbol>();
  let bulkRequestToken: symbol | undefined;
  let authorRequestVersion = 0;
  let authorScopeKey: string | null = null;
  let authorCandidatesScopeKey: string | null = null;
  let pollCount = 0;
  let pollingStarted = false;
  let retainedFeedStale = false;
  let snapshotScope: string | undefined;
  let snapshotFilterScope: string | undefined;
  let displayedRepo: string | undefined;
  const recentSnapshots = new Map<string, CachedActivitySnapshot>();
  const AUTHORITATIVE_REFRESH_EVERY = 4;
  let activityLifecycleTick = 0;
  const notificationStateOwnership = new Map<string, { readonly tick: number; readonly state: string }>();

  let hideClosedMerged = $state(false);
  let hideClosedMergedOverride: boolean | undefined;
  let hideBots = $state(false);
  let hideBotsOverride: boolean | undefined;
  // Labels filter loaded rows in the browser, like the PR list's, so they are not saved.
  let labelFilters = $state.raw<LabelFilters>(new Map());
  let hideDrafts = $state(false);
  let hideDraftsOverride: boolean | undefined;
  let hideMergeQueued = $state(false);
  let hideMergeQueuedOverride: boolean | undefined;
  // Keeping only queued merges is a passing view, so unlike hiding them it is not saved.
  let onlyMergeQueuedChoice = $state(false);
  // A restored URL or saved default can turn hiding on after the choice; hiding then wins.
  const onlyMergeQueued = $derived(onlyMergeQueuedChoice && !hideMergeQueued);
  // Keeping only closed items, bot activity, or drafts are passing views too; their hide choices are saved.
  let onlyClosedMergedChoice = $state(false);
  const onlyClosedMerged = $derived(onlyClosedMergedChoice && !hideClosedMerged);
  let onlyBotsChoice = $state(false);
  const onlyBots = $derived(onlyBotsChoice && !hideBots);
  let onlyDraftsChoice = $state(false);
  const onlyDrafts = $derived(onlyDraftsChoice && !hideDrafts);
  // The pull request list's other conditions. They apply only to pull request activity and are not saved.
  let pullAttributeFilters = $state<PullAttributeFilter[]>([]);
  const serverPullAttributes = $derived([...(onlyDrafts ? ["draft"] : []), ...pullAttributeFilters]);
  // A thread's parent already passed the feed's workspace check. A spoke forwards thread requests
  // to the hub, which does not know the spoke's workspaces, so they leave the workspace attributes out.
  const threadPullAttributes = $derived(
    serverPullAttributes.filter((attribute) => attribute !== "has_workspace" && attribute !== "no_workspace"),
  );
  let useWorkspaceActivityForRecency = $state(false);
  let hideDefaultBranchActivity = $state(false);
  let enabledItemTypes = $state<Set<ActivityItemType>>(new Set(DEFAULT_ACTIVITY_ITEM_TYPES));
  let enabledEvents = $state<Set<string>>(new Set(DEFAULT_EVENT_TYPES));
  let showNotifications = $state(true);
  // Filters Activity shares with the PR and issue lists that the lists keep per session.
  let starred = $state(false);
  let referencedByPR = $state<TriFilter>(undefined);
  let hideBotAuthoredIssues = $state(false);
  let involvesMe = $state(readInvolvesMeFilter("activity"));
  let unassigned = $state(readUnassignedFilter("activity"));
  let initialized = false;

  // --- reads ---

  function getActivityItems(): ActivityItem[] {
    return items;
  }
  function getItemActivity(): ActivitySubject[] {
    return itemActivity;
  }
  function getWorkspaceActivity(): WorkspaceActivitySubject[] {
    return workspaceActivity;
  }
  function isActivityLoading(): boolean {
    return loading;
  }
  function getActivityError(): string | null {
    return storeError;
  }
  function getThreadLoadError(): string | null {
    return threadLoadError;
  }
  function isActivityCapped(): boolean {
    return capped;
  }
  function isItemActivityCapped(): boolean {
    return itemActivityCapped;
  }
  function canLoadOlderItems(): boolean {
    return subjectCursor !== "";
  }
  function isLoadingOlderItems(): boolean {
    return requestedSubjectDepth > displayedSubjectDepth;
  }
  function loadOlderItems(): void {
    if (!subjectCursor || isLoadingOlderItems()) return;
    requestedSubjectDepth += 1;
    const params = buildParams();
    const scope = activityProjectionScope(params);
    const generation = childProjectionInvalidationGeneration;
    const page = { cursor: subjectCursor, depth: requestedSubjectDepth };
    olderItemsPage = page;
    runtime.runCommand(loadActivityEffect(params, generation, scope, page), {
      operation: "load older activity items",
      safeContext: {},
      onFailure: () => {},
    });
  }
  function canLoadMoreActivity(): boolean {
    return (
      viewMode === "flat" &&
      activityPageLimit === undefined &&
      (flatPaging.isLoadingMore() || (capped && flatPaging.getLimit() < ACTIVITY_SAFETY_CAP))
    );
  }
  function isLoadingMoreActivity(): boolean {
    return flatPaging.isLoadingMore();
  }
  function loadMoreActivity(): void {
    if (!flatPaging.loadMore(canLoadMoreActivity())) return;
    loadActivity();
  }
  function getActivityEventCursor(): string {
    return activityEventCursor;
  }
  function getActivityFilterTypes(): string[] {
    return filterTypes;
  }
  function getActivitySearch(): string | undefined {
    return searchQuery;
  }
  function getActivityAuthor(): string | undefined {
    return authorFilter;
  }
  function getActivityAuthors(): string[] {
    const selected = authorFilter;
    const candidates = authorCandidates ?? [];
    if (!selected) return candidates;
    const selectedIndex = candidates.findIndex((candidate) => candidate.toLowerCase() === selected.toLowerCase());
    if (selectedIndex < 0) return [selected, ...candidates];
    if (candidates[selectedIndex] === selected) return candidates;
    const withSelected = [...candidates];
    withSelected[selectedIndex] = selected;
    return withSelected;
  }
  function isActivityAuthorsLoading(): boolean {
    return authorsLoading && authorCandidates === null;
  }
  function getActivityAuthorsError(): string | null {
    return authorsError;
  }
  function getTimeRange(): TimeRange {
    return timeRange;
  }
  function getViewMode(): ViewMode {
    return viewMode;
  }
  function getCollapseThreads(): boolean {
    return collapseThreads;
  }
  function getRollUpCommits(): boolean {
    return rollUpCommits;
  }
  function isThreadItemExpanded(key: string): boolean {
    return expandOverrides.has(key) ? collapseThreads : !collapseThreads;
  }
  function getHideClosedMerged(): boolean {
    return hideClosedMerged;
  }
  /** Open hides closed and merged items, Closed keeps only them, and All shows both. */
  function getActivityState(): "open" | "closed" | "all" {
    if (onlyClosedMerged) return "closed";
    return hideClosedMerged ? "open" : "all";
  }
  function getBots(): TriFilter {
    if (onlyBots) return "require";
    return hideBots ? "exclude" : undefined;
  }
  function getDraft(): TriFilter {
    if (onlyDrafts) return "require";
    return hideDrafts ? "exclude" : undefined;
  }
  function getPullAttributeFilters(): PullAttributeFilter[] {
    return pullAttributeFilters;
  }
  function getLabelFilters(): LabelFilters {
    return labelFilters;
  }

  // The server applies label filters before its limit, so each change reloads.
  function cycleLabelFilter(name: string): void {
    labelFilters = cycleLabel(labelFilters, name);
    invalidatePagedActivityRequests();
    loadActivity();
  }

  function clearLabelFilters(): void {
    if (labelFilters.size === 0) return;
    labelFilters = new Map();
    invalidatePagedActivityRequests();
    loadActivity();
  }

  function getHideBots(): boolean {
    return hideBots;
  }
  function getHideDrafts(): boolean {
    return hideDrafts;
  }
  function getHideMergeQueued(): boolean {
    return hideMergeQueued;
  }
  /** Required keeps only pull requests queued or set to merge after CI; excluded hides them. */
  function getMergeQueued(): TriFilter {
    if (onlyMergeQueued) return "require";
    return hideMergeQueued ? "exclude" : undefined;
  }
  function getUseWorkspaceActivityForRecency(): boolean {
    return useWorkspaceActivityForRecency;
  }
  function getHideDefaultBranchActivity(): boolean {
    return hideDefaultBranchActivity;
  }
  function getEnabledItemTypes(): Set<ActivityItemType> {
    return enabledItemTypes;
  }
  function getEnabledEvents(): Set<string> {
    return enabledEvents;
  }
  function getShowNotifications(): boolean {
    return showNotifications;
  }
  function getTimeRangeDefault(): TimeRange {
    return timeRangeDefault;
  }
  function getViewModeDefault(): ViewMode {
    return viewModeDefault;
  }
  function getStarred(): boolean {
    return starred;
  }
  function getReferencedByPR(): TriFilter {
    return referencedByPR;
  }
  function getHideBotAuthoredIssues(): boolean {
    return hideBotAuthoredIssues;
  }
  function getInvolvesMe(): TriFilter {
    return involvesMe;
  }
  function getUnassigned(): TriFilter {
    return unassigned;
  }

  // --- writes ---

  function setActivityFilterTypes(types: string[]): void {
    filterTypes = types;
    invalidatePagedActivityRequests();
  }
  function setActivitySearch(q: string | undefined): void {
    searchQuery = q;
    invalidatePagedActivityRequests();
  }
  function setActivityAuthor(author: string | undefined): void {
    authorFilter = author?.trim() || undefined;
    invalidatePagedActivityRequests();
  }
  function setTimeRange(range_: TimeRange): void {
    timeRangeSelected = true;
    timeRange = range_;
    invalidatePagedActivityRequests();
  }
  function setViewMode(mode: ViewMode): void {
    viewModeSelected = true;
    viewMode = mode;
    invalidatePagedActivityRequests();
  }
  function setFullEventProjectionRequired(required: boolean): void {
    fullEventProjectionRequired = required;
    invalidatePagedActivityRequests();
  }
  function setActivityPageLimit(limit: number | undefined): void {
    if (activityPageLimit === limit) return;
    activityPageLimit = limit;
    invalidatePagedActivityRequests();
  }
  function setRollUpCommits(value: boolean): void {
    selectedFilters.add("rollup_commits");
    rollUpCommits = value;
  }
  function collapseAllThreads(): void {
    collapseThreads = true;
    expandOverrides = new Set();
    syncToURL();
    loadActivity();
  }
  function expandAllThreads(): void {
    collapseThreads = false;
    expandOverrides = new Set();
    syncToURL();
    loadBulkActivity();
  }
  function toggleThreadItem(key: string): void {
    // Per-item overrides are session-only and intentionally not synced to the
    // URL; only collapse-all/expand-all persist via collapseThreads.
    const next = new Set(expandOverrides);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    expandOverrides = next;
    if (isThreadItemExpanded(key)) loadThreadEvents(key);
  }
  function setHideClosedMerged(v: boolean): void {
    hideClosedMerged = v;
    hideClosedMergedOverride = v;
    syncToURL();
    invalidatePagedActivityRequests();
  }
  function setHideBots(v: boolean): void {
    hideBots = v;
    hideBotsOverride = v;
    invalidatePagedActivityRequests();
  }
  function setHideMergeQueued(v: boolean): void {
    hideMergeQueued = v;
    hideMergeQueuedOverride = v;
    syncToURL();
    invalidatePagedActivityRequests();
  }
  function setMergeQueued(value: TriFilter): void {
    onlyMergeQueuedChoice = value === "require";
    setHideMergeQueued(value === "exclude");
  }
  function setActivityState(state: "open" | "closed" | "all"): void {
    onlyClosedMergedChoice = state === "closed";
    setHideClosedMerged(state === "open");
  }
  function setBots(value: TriFilter): void {
    onlyBotsChoice = value === "require";
    setHideBots(value === "exclude");
  }
  function setDraft(value: TriFilter): void {
    onlyDraftsChoice = value === "require";
    setHideDrafts(value === "exclude");
  }
  function applyPullAttributeFilters(next: PullAttributeFilter[]): void {
    pullAttributeFilters = next;
    invalidatePagedActivityRequests();
  }
  /** Selects one option of a pick-one pair such as Passing CI and Failed CI. */
  function togglePullAttributeFilter(filter: PullAttributeFilter): void {
    applyPullAttributeFilters(toggleFilterInSections(PULL_ATTRIBUTE_SECTIONS, pullAttributeFilters, filter));
  }
  /** Cycles a condition through off, required, and its opposite. */
  function cyclePullAttributeFilter(filter: PullAttributeFilter, opposite: PullAttributeFilter): void {
    applyPullAttributeFilters(cycleFilterInSections(PULL_ATTRIBUTE_SECTIONS, pullAttributeFilters, filter, opposite));
  }
  function setPullAttributeFilters(next: PullAttributeFilter[]): void {
    const changed =
      next.length !== pullAttributeFilters.length || next.some((filter) => !pullAttributeFilters.includes(filter));
    pullAttributeFilters = next;
    if (changed) invalidatePagedActivityRequests();
  }
  function setHideDrafts(v: boolean): void {
    hideDrafts = v;
    hideDraftsOverride = v;
    invalidatePagedActivityRequests();
  }
  function setHideDefaultBranchActivity(v: boolean): void {
    selectedFilters.add("hide_branch");
    hideDefaultBranchActivity = v;
    invalidatePagedActivityRequests();
  }
  function setEnabledItemTypes(itemTypes: Set<ActivityItemType>): void {
    selectedFilters.add("item_types");
    enabledItemTypes = itemTypes;
    invalidatePagedActivityRequests();
  }
  function setEnabledEvents(events: Set<string>): void {
    selectedFilters.add("event_types");
    enabledEvents = events;
    invalidatePagedActivityRequests();
  }
  function setShowNotifications(v: boolean): void {
    selectedFilters.add("notif");
    showNotifications = v;
    invalidatePagedActivityRequests();
  }
  function setStarred(value: boolean): void {
    starred = value;
    invalidatePagedActivityRequests();
  }
  function setReferencedByPR(value: TriFilter): void {
    referencedByPR = value;
    invalidatePagedActivityRequests();
  }
  function setHideBotAuthoredIssues(value: boolean): void {
    hideBotAuthoredIssues = value;
    invalidatePagedActivityRequests();
  }
  function setInvolvesMe(value: TriFilter): void {
    involvesMe = value;
    invalidatePagedActivityRequests();
    writeInvolvesMeFilter("activity", value);
  }
  function setUnassigned(value: TriFilter): void {
    unassigned = value;
    invalidatePagedActivityRequests();
    writeUnassignedFilter("activity", value);
  }
  // --- hydration ---

  function hydrateDefaults(activity: ActivitySettings): void {
    timeRangeDefault = activity.time_range;
    viewModeDefault = activity.view_mode;
    itemTypesDefault = DEFAULT_ACTIVITY_ITEM_TYPES.filter((value) => activity.item_types.includes(value));
    eventTypesDefault = activity.event_types;
    rollUpCommitsDefault = activity.roll_up_commits;
    hideDefaultBranchDefault = activity.hide_default_branch;
    showNotificationsDefault = !activity.hide_notifications;
    enabledItemTypes = new Set(itemTypesDefault);
    enabledEvents = new Set(eventTypesDefault);
    rollUpCommits = rollUpCommitsDefault;
    hideDefaultBranchActivity = hideDefaultBranchDefault;
    showNotifications = showNotificationsDefault;
    defaultsHydrated = true;
    viewMode = activity.view_mode;
    timeRange = activity.time_range;
    hideClosedMerged = activity.hide_closed;
    hideBots = hideBotsOverride ?? activity.hide_bots;
    hideDrafts = hideDraftsOverride ?? activity.hide_drafts;
    hideMergeQueued = hideMergeQueuedOverride ?? activity.hide_merge_queued;
    useWorkspaceActivityForRecency = activity.use_workspace_activity_for_recency;
    collapseThreadsDefault = activity.collapse_threads;
    collapseThreads = activity.collapse_threads;
    expandOverrides = new Set();
    rebuildFilterTypes();
    if (initialized) {
      syncFromURL();
      // Once a settings reload makes the live state match the new default,
      // drop the now-redundant collapsed param so a later default change is
      // not shadowed by a stale override.
      if (collapseThreads === collapseThreadsDefault) {
        deleteCollapsedParam();
      }
    }
  }

  function initializeFromMount(): void {
    const previousScope = pagedActivityScopeKey();
    syncFromURL();
    if (previousScope !== pagedActivityScopeKey()) invalidatePagedActivityRequests();
    initialized = true;
    syncToURL();
  }

  // --- internals ---

  function computeSince(): string {
    return new Date(Date.now() - RANGE_MS[timeRange]).toISOString();
  }

  function activityPageQuery(params: ActivityParams): ActivityParams {
    return { ...params, since: timeRange };
  }

  function buildParams(): ActivityParams {
    const since = computeSince();
    const p: ActivityParams = { since };
    p.projection =
      activityPageLimit !== undefined || (viewMode === "threaded" && collapseThreads && !fullEventProjectionRequired)
        ? "collapsed"
        : "full";
    if (activityPageLimit !== undefined) p.limit = activityPageLimit;
    const repo = getGlobalRepo();
    if (repo) p.repo = repo;
    if (filterTypes.length > 0) p.types = filterTypes;
    const itemTypes = buildActivityItemTypeFilter(enabledItemTypes);
    if (itemTypes.length < DEFAULT_ACTIVITY_ITEM_TYPES.length + 1) {
      p.item_types = itemTypes;
    }
    if (searchQuery) p.search = searchQuery;
    if (authorFilter) p.author = authorFilter;
    if (involvesMe === "require") p.involves_me = true;
    if (involvesMe === "exclude") p.not_involves_me = true;
    if (unassigned === "require") p.unassigned = true;
    if (unassigned === "exclude") p.assigned = true;
    if (hideClosedMerged) p.hide_closed_merged = true;
    if (hideBots) p.hide_bots = true;
    if (hideDrafts) p.hide_drafts = true;
    if (hideMergeQueued) p.hide_merge_queued = true;
    if (onlyMergeQueued) p.merge_queued = true;
    if (onlyClosedMerged) p.closed_merged = true;
    if (onlyBots) p.bots = true;
    if (serverPullAttributes.length > 0) p.attributes = serverPullAttributes.join(",");
    if (starred) p.starred = true;
    if (referencedByPR === "require") p.referenced_by_pr = true;
    if (referencedByPR === "exclude") p.not_referenced_by_pr = true;
    if (hideBotAuthoredIssues) p.hide_bot_authored_issues = true;
    Object.assign(p, labelFilterParams(labelFilters));
    if (hideDefaultBranchActivity) p.hide_default_branch = true;
    if (activityPageLimit === undefined && viewMode === "flat") {
      return { ...flatPaging.withLimit(activityPageQuery(p)), since };
    }
    return p;
  }

  function shouldUseCollapsedAuthoritativeProjection(): boolean {
    return activityPageLimit !== undefined || (viewMode === "threaded" && !fullEventProjectionRequired);
  }

  function activityProjectionScope(params: ActivityParams): string {
    return JSON.stringify([
      timeRange,
      params.projection ?? "full",
      params.repo ?? "",
      params.types ?? [],
      params.item_types ?? [],
      params.search ?? "",
      params.author ?? "",
      params.involves_me ?? false,
      params.not_involves_me ?? false,
      params.unassigned ?? false,
      params.assigned ?? false,
      params.hide_closed_merged ?? false,
      params.hide_bots ?? false,
      params.hide_drafts ?? false,
      params.hide_merge_queued ?? false,
      params.merge_queued ?? false,
      params.closed_merged ?? false,
      params.bots ?? false,
      params.starred ?? false,
      params.referenced_by_pr ?? false,
      params.not_referenced_by_pr ?? false,
      params.hide_bot_authored_issues ?? false,
      params.labels ?? [],
      params.exclude_labels ?? [],
      params.attributes ?? "",
      params.hide_default_branch ?? false,
      params.limit ?? 0,
    ]);
  }

  function pagedActivityScopeKey(): string {
    const params = buildParams();
    return JSON.stringify([activityProjectionScope(params), params.projection]);
  }

  function rememberActivitySnapshot(): void {
    if (snapshotScope === undefined || snapshotFilterScope === undefined) return;
    recentSnapshots.delete(snapshotScope);
    recentSnapshots.set(snapshotScope, {
      repo: displayedRepo,
      filterScope: snapshotFilterScope,
      items,
      itemActivity,
      workspaceActivity,
      capped,
      itemActivityCapped,
    });
    if (recentSnapshots.size > 8) recentSnapshots.delete(recentSnapshots.keys().next().value!);
  }

  function restoreRepositoryActivity(params: ActivityParams): void {
    rememberActivitySnapshot();
    if (displayedRepo === params.repo) return;
    const { repo, ...unscopedParams } = params;
    const selected = new Set(parseRepoFilterSelection(repo));
    const filterScope = activityProjectionScope(unscopedParams);
    const cached =
      recentSnapshots.get(pagedActivityScopeKey()) ??
      [...recentSnapshots.values()].reverse().find((entry) => {
        if (entry.filterScope !== filterScope) return false;
        const cachedRepos = parseRepoFilterSelection(entry.repo);
        return (
          cachedRepos.length === 0 || (selected.size > 0 && [...selected].every((value) => cachedRepos.includes(value)))
        );
      });
    const matchesRepo = (item: ActivityItem | ActivitySubject | WorkspaceActivitySubject): boolean => {
      if (selected.size === 0) return true;
      const value = providerQualifiedRepoFilterValue({
        provider: item.repo?.provider,
        platformHost: item.repo?.platform_host ?? item.platform_host,
        repoPath: item.repo?.repo_path,
      });
      return value !== null && selected.has(value);
    };
    displayedRepo = repo;
    items = cached?.items.filter(matchesRepo) ?? [];
    itemActivity = cached?.itemActivity.filter(matchesRepo) ?? [];
    workspaceActivity = cached?.workspaceActivity.filter(matchesRepo) ?? [];
    capped = cached?.capped ?? false;
    itemActivityCapped = cached?.itemActivityCapped ?? false;
    // Cached rows are a preview. Only the response for this selection may
    // establish cursor and child-page authority or satisfy ensureActivityLoaded.
    snapshotScope = undefined;
    snapshotFilterScope = undefined;
    activityEventCursor = "";
    loadedThreadKeys = new Set();
  }

  function ownsForegroundSnapshot(generation: number, scope: string): boolean {
    return generation === pagedActivityGeneration && scope === activityProjectionScope(buildParams());
  }

  function invalidatePagedActivityRequests(): void {
    pagedActivityGeneration += 1;
    childProjectionInvalidationGeneration += 1;
    const scope = activityProjectionScope({ ...buildParams(), projection: "full" });
    if (subjectPageScope !== scope) {
      requestedSubjectDepth = 1;
      displayedSubjectDepth = 1;
      subjectCursor = "";
      subjectPageScope = scope;
    }
    loading = false;
    threadRequestTokens.clear();
    loadingThreadKeys = new Set();
    failedThreadKeys = new Set();
    threadLoadError = null;
    bulkRequestToken = undefined;
  }

  // The author list gains a name only when someone new authors an item in the
  // scope, so it is read once per repository and time range: on a fresh Activity
  // load, a scope change, or a retry after a failed read. Polls and provider
  // events do not reread it.
  function loadActivityAuthorsEffect(force = false) {
    return Effect.suspend(() => {
      const repo = getGlobalRepo();
      const scopeKey = `${repo ?? ""}\0${timeRange}`;
      if (!force && scopeKey === authorScopeKey) return Effect.void;

      // Refreshes and retries keep the picker usable until its repo or range changes.
      if (scopeKey !== authorCandidatesScopeKey) {
        authorCandidates = null;
        authorCandidatesScopeKey = scopeKey;
      }
      authorScopeKey = scopeKey;
      const version = ++authorRequestVersion;
      authorsLoading = true;
      authorsError = null;
      const query: ActivityAuthorsParams = { since: computeSince(), ...(repo ? { repo } : {}) };
      return executeGeneratedApiRequest("GET /activity/authors", (client, signal) =>
        client.ActivityService.listActivityAuthors(query, { signal }),
      ).pipe(
        retryIdempotentRead,
        Effect.tap((response) =>
          Effect.sync(() => {
            if (version !== authorRequestVersion) return;
            authorCandidates = response.authors ?? [];
            authorsLoading = false;
          }),
        ),
        Effect.catch((failure) =>
          Effect.sync(() => {
            if (version !== authorRequestVersion) return;
            authorsError = readErrorMessage(failure, "failed to load activity authors");
            authorsLoading = false;
            authorScopeKey = null;
          }),
        ),
        Effect.onInterrupt(() =>
          Effect.sync(() => {
            if (version !== authorRequestVersion || scopeKey !== authorScopeKey) return;
            authorsLoading = false;
            authorScopeKey = null;
          }),
        ),
      );
    });
  }

  function loadActivityAuthors(force = false): void {
    runtime.runCommand(loadActivityAuthorsEffect(force), {
      operation: "load activity authors",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function activityRead(params: ActivityParams) {
    const scope = pagedActivityScopeKey();
    // Background reads may request collapsed summaries while the retained
    // display includes expanded children. Cache by the logical display scope.
    const { repo, ...unscopedParams } = buildParams();
    const filterScope = activityProjectionScope(unscopedParams);
    return Effect.sync(() => ++activityLifecycleTick).pipe(
      Effect.flatMap((startedAt) =>
        executeGeneratedApiRequest("GET /activity", (client, signal) =>
          client.ActivityService.listActivity(params, { signal }),
        ).pipe(
          retryIdempotentRead,
          Effect.map((response) => ({ response, startedAt, scope, repo, filterScope })),
        ),
      ),
    );
  }

  function appendUnseen<T extends ActivitySubject | WorkspaceActivitySubject>(
    into: T[],
    seen: Set<string | undefined>,
    incoming: readonly T[] | undefined,
  ): void {
    for (const subject of incoming ?? []) {
      const key = stableParentKey(subject);
      if (seen.has(key)) continue;
      seen.add(key);
      into.push(subject);
    }
  }

  function activityReadPages(params: ActivityParams, depth: number) {
    return Effect.gen(function* () {
      const scope = activityProjectionScope({ ...params, projection: "full" });
      const first = yield* activityRead(params);
      const subjects = [...(first.response.item_activity ?? [])];
      const seen = new Set(subjects.map(stableParentKey));
      const workspaces = [...(first.response.workspace_activity ?? [])];
      const seenWorkspaces = new Set(workspaces.map(stableParentKey));
      let cursor = first.response.next_subject_cursor ?? "";
      let subjectsCapped = first.response.item_activity_capped ?? false;
      let partialFailure: ApiProblemError | TransientTransportError | undefined;
      let page = 1;
      for (; cursor && page < Math.max(depth, scope === subjectPageScope ? displayedSubjectDepth : 1); page++) {
        const nextResult = yield* Effect.result(activityRead({ ...params, subject_before: cursor }));
        if (Result.isFailure(nextResult)) {
          partialFailure = nextResult.failure;
          break;
        }
        const next = nextResult.success;
        appendUnseen(subjects, seen, next.response.item_activity);
        appendUnseen(workspaces, seenWorkspaces, next.response.workspace_activity);
        cursor = next.response.next_subject_cursor ?? "";
        subjectsCapped = next.response.item_activity_capped ?? false;
      }
      return {
        ...first,
        subjectDepth: page,
        ...(partialFailure ? { partialFailure } : {}),
        response: {
          ...first.response,
          item_activity: subjects,
          workspace_activity: workspaces,
          next_subject_cursor: cursor,
          item_activity_capped: subjectsCapped,
        },
      };
    });
  }

  function projectOwnedNotificationStates(result: OwnedActivityResponse, completeSnapshot = true): ActivityItem[] {
    const projected = (result.response.items ?? []).map((item) => {
      const owned = notificationStateOwnership.get(item.id);
      if (owned === undefined) return item;
      if (owned.tick > result.startedAt) return { ...item, item_state: owned.state };
      notificationStateOwnership.delete(item.id);
      return item;
    });
    if (completeSnapshot) {
      const present = new Set(projected.map((item) => item.id));
      for (const [id, owned] of notificationStateOwnership) {
        if (owned.tick <= result.startedAt && !present.has(id)) notificationStateOwnership.delete(id);
      }
    }
    return projected;
  }

  function projectOwnedActivityPages(results: readonly OwnedActivityResponse[]): ActivityItem[] {
    const seen = new Set<string>();
    const projected: ActivityItem[] = [];
    for (const result of results) {
      for (const item of projectOwnedNotificationStates(result, false)) {
        if (seen.has(item.id)) continue;
        seen.add(item.id);
        projected.push(item);
      }
    }
    return projected;
  }

  function stableParentKey(item: ActivityItem | ActivitySubject | WorkspaceActivitySubject): string | undefined {
    const repo = item.repo;
    if (!repo) return undefined;
    const repositoryKey = repositoryKeyFromWire(repo);
    if (!repositoryKey || (item.item_type !== "pr" && item.item_type !== "issue")) return undefined;
    return activityItemKey({
      provider: repo.provider,
      platformHost: repo.platform_host,
      repositoryKey,
      owner: repo.owner,
      name: repo.name,
      repoPath: repo.repo_path,
      itemType: item.item_type,
      itemNumber: item.item_number,
    });
  }

  function reconcileItemsWithParentSubjects(subjects: ActivitySubject[]): void {
    const subjectsByKey = new Map<string, ActivitySubject>();
    for (const subject of subjects) {
      const key = stableParentKey(subject);
      if (key) subjectsByKey.set(key, subject);
    }
    items = items.map((item) => {
      const key = stableParentKey(item);
      const subject = key ? subjectsByKey.get(key) : undefined;
      if (!subject) return item;
      const {
        workspace: _staleWorkspace,
        labels: _staleLabels,
        head_repo_kind: _staleHeadRepoKind,
        ...retainedEvent
      } = item;
      return {
        ...retainedEvent,
        ...(item.activity_type === "notification" ? { activity_url: subject.item_url } : {}),
        item_author: subject.item_author || item.item_author || "",
        item_last_activity_at: subject.activity_at,
        item_state: item.activity_type === "notification" ? item.item_state : subject.item_state,
        item_title: subject.item_title,
        item_url: subject.item_url,
        platform_host: subject.repo.platform_host,
        repo_owner: subject.repo.owner,
        repo_name: subject.repo.name,
        repo: subject.repo,
        ...(item.activity_type === "notification" ? { subject_state: subject.item_state } : {}),
        ...(subject.workspace ? { workspace: subject.workspace } : {}),
        // The parent's current labels and fork kind, so a filter never acts on a retained event's old ones.
        ...(subject.labels ? { labels: subject.labels } : {}),
        ...(subject.head_repo_kind ? { head_repo_kind: subject.head_repo_kind } : {}),
      };
    });
  }

  function projectActivitySubjects(response: ActivityResponse, params: ActivityParams, depth: number): void {
    flatPaging.apply(activityPageQuery(params));
    const subjects = response.item_activity ?? [];
    reconcileItemsWithParentSubjects(subjects);
    itemActivity = subjects;
    workspaceActivity = response.workspace_activity ?? [];
    itemActivityCapped = response.item_activity_capped ?? false;
    subjectCursor = response.next_subject_cursor ?? "";
    displayedSubjectDepth = depth;
    requestedSubjectDepth = subjectCursor ? Math.max(requestedSubjectDepth, depth) : depth;
  }

  function projectActivitySnapshot(result: ActivityReadResponse, params: ActivityParams): void {
    snapshotScope = result.scope;
    snapshotFilterScope = result.filterScope;
    displayedRepo = result.repo;
    items = projectOwnedNotificationStates(result);
    projectActivitySubjects(result.response, params, result.subjectDepth ?? 1);
    capped = result.response.capped;
    activityEventCursor = result.response.event_cursor ?? "";
    loadedThreadKeys = new Set();
    loadingThreadKeys = new Set();
    failedThreadKeys = new Set();
    threadLoadError = null;
    projectedChildInvalidationGeneration = childProjectionInvalidationGeneration;
    loading = false;
    storeError = null;
    if (params.projection === "collapsed" && activityPageLimit === undefined) {
      for (const subject of itemActivity) {
        const key = stableParentKey(subject);
        if (key && isThreadItemExpanded(key)) loadThreadEvents(key);
      }
    } else if (
      params.projection === "full" &&
      viewMode === "threaded" &&
      !collapseThreads &&
      capped &&
      activityEventCursor !== ""
    ) {
      loadBulkActivity();
    }
  }

  function projectAuthoritativeActivitySnapshot(result: ActivityReadResponse, params: ActivityParams): void {
    if (viewMode === "flat" && activityPageLimit === undefined) {
      const eventCursor = result.response.event_cursor ?? activityEventCursor;
      projectActivitySnapshot(result, params);
      activityEventCursor = eventCursor;
      return;
    }
    snapshotScope = result.scope;
    snapshotFilterScope = result.filterScope;
    displayedRepo = result.repo;
    const reconcileCollapsedThreads = shouldUseCollapsedAuthoritativeProjection();
    const autoLoadExpandedThreadEvents = activityPageLimit === undefined;
    const childProjectionStale =
      reconcileCollapsedThreads && projectedChildInvalidationGeneration !== childProjectionInvalidationGeneration;
    const noMatchingEvents = !result.partialFailure && result.response.event_cursor === "";
    const received = projectOwnedNotificationStates(result);
    const receivedIDs = new Set(received.map((item) => item.id));
    const receivedSubjectKeys = new Set(
      (result.response.item_activity ?? [])
        .map((subject) => stableParentKey(subject))
        .filter((key): key is string => key !== undefined),
    );
    const previousSubjects = new Map(itemActivity.map((subject) => [stableParentKey(subject), subject] as const));
    const changedThreadKeys = new Set<string>();
    const newExpandedThreadKeys = new Set<string>();
    let restartBulkActivity = false;
    const parentFilterActive =
      searchQuery !== undefined ||
      authorFilter !== undefined ||
      involvesMe !== undefined ||
      unassigned !== undefined ||
      hideClosedMerged ||
      hideBots ||
      hideDrafts ||
      hideMergeQueued ||
      onlyMergeQueued ||
      onlyClosedMerged ||
      onlyBots ||
      serverPullAttributes.length > 0 ||
      starred ||
      referencedByPR !== undefined ||
      hideBotAuthoredIssues ||
      labelFilters.size > 0 ||
      enabledItemTypes.size !== DEFAULT_ACTIVITY_ITEM_TYPES.length;
    const refreshFilteredBulk =
      !result.partialFailure &&
      autoLoadExpandedThreadEvents &&
      !collapseThreads &&
      parentFilterActive &&
      !!result.response.item_activity_capped &&
      (result.response.event_cursor ?? activityEventCursor) !== "";
    const absentThreadKeys = new Set<string>();
    if (
      reconcileCollapsedThreads &&
      (!result.response.item_activity_capped || parentFilterActive || (result.subjectDepth ?? 1) > 1)
    ) {
      for (const key of previousSubjects.keys()) {
        if (key !== undefined && !receivedSubjectKeys.has(key)) absentThreadKeys.add(key);
      }
      loadedThreadKeys = new Set([...loadedThreadKeys].filter((key) => !absentThreadKeys.has(key)));
      loadingThreadKeys = new Set([...loadingThreadKeys].filter((key) => !absentThreadKeys.has(key)));
      failedThreadKeys = new Set([...failedThreadKeys].filter((key) => !absentThreadKeys.has(key)));
      if (failedThreadKeys.size === 0) threadLoadError = null;
      for (const key of absentThreadKeys) threadRequestTokens.delete(key);
    }
    for (const subject of result.response.item_activity ?? []) {
      const key = stableParentKey(subject);
      const previous = key ? previousSubjects.get(key) : undefined;
      if (
        key &&
        previous &&
        (previous.activity_at !== subject.activity_at ||
          previous.event_ledger_revision !== subject.event_ledger_revision)
      ) {
        changedThreadKeys.add(key);
      }
      if (reconcileCollapsedThreads && autoLoadExpandedThreadEvents && key && !previous && isThreadItemExpanded(key)) {
        newExpandedThreadKeys.add(key);
      }
    }
    const previousSubjectKeys = [...previousSubjects.keys()].filter((key): key is string => key !== undefined);
    const unchangedBulk =
      bulkRequestToken !== undefined &&
      (result.response.event_cursor ?? activityEventCursor) === activityEventCursor &&
      changedThreadKeys.size === 0 &&
      previousSubjectKeys.length === receivedSubjectKeys.size &&
      previousSubjectKeys.every((key) => receivedSubjectKeys.has(key));
    restartBulkActivity = refreshFilteredBulk && !unchangedBulk;
    const dropCachedThreads = childProjectionStale || noMatchingEvents;
    const retainedThreadItems = dropCachedThreads
      ? []
      : items.filter((item) => {
          const key = stableParentKey(item);
          if (
            result.response.item_activity_capped &&
            (!collapseThreads || (!parentFilterActive && (result.subjectDepth ?? 1) === 1))
          ) {
            return (
              (item.item_type === "pr" || item.item_type === "issue") &&
              !receivedIDs.has(item.id) &&
              (key === undefined ||
                (!changedThreadKeys.has(key) &&
                  !(parentFilterActive && !refreshFilteredBulk && absentThreadKeys.has(key))))
            );
          }
          return (
            key !== undefined &&
            receivedSubjectKeys.has(key) &&
            !receivedIDs.has(item.id) &&
            !changedThreadKeys.has(key)
          );
        });
    items = [...received, ...retainedThreadItems];
    projectActivitySubjects(result.response, params, result.subjectDepth ?? 1);
    capped = result.response.capped;
    activityEventCursor = result.response.event_cursor ?? activityEventCursor;
    if (childProjectionStale) {
      projectedChildInvalidationGeneration = childProjectionInvalidationGeneration;
      loadedThreadKeys = new Set();
      loadingThreadKeys = new Set();
      failedThreadKeys = new Set();
      threadLoadError = null;
      threadRequestTokens.clear();
      bulkRequestToken = undefined;
      if (autoLoadExpandedThreadEvents && !collapseThreads) {
        restartBulkActivity = true;
      } else if (autoLoadExpandedThreadEvents) {
        for (const key of receivedSubjectKeys) {
          if (isThreadItemExpanded(key)) loadThreadEvents(key);
        }
      }
    } else if (reconcileCollapsedThreads && (changedThreadKeys.size > 0 || newExpandedThreadKeys.size > 0)) {
      restartBulkActivity ||=
        autoLoadExpandedThreadEvents &&
        ((changedThreadKeys.size > 0 && bulkRequestToken !== undefined) ||
          (!collapseThreads && newExpandedThreadKeys.size > 0));
      if (!restartBulkActivity) {
        const reloadThreadKeys = new Set([...changedThreadKeys, ...newExpandedThreadKeys]);
        loadedThreadKeys = new Set([...loadedThreadKeys].filter((key) => !reloadThreadKeys.has(key)));
        loadingThreadKeys = new Set([...loadingThreadKeys].filter((key) => !reloadThreadKeys.has(key)));
        failedThreadKeys = new Set([...failedThreadKeys].filter((key) => !reloadThreadKeys.has(key)));
        if (failedThreadKeys.size === 0) threadLoadError = null;
        for (const key of reloadThreadKeys) threadRequestTokens.delete(key);
        if (autoLoadExpandedThreadEvents) {
          for (const key of reloadThreadKeys) {
            if (isThreadItemExpanded(key)) loadThreadEvents(key);
          }
        }
      }
    }
    if (noMatchingEvents) {
      bulkRequestToken = undefined;
      restartBulkActivity = false;
    }
    loading = bulkRequestToken !== undefined;
    storeError = null;
    if (restartBulkActivity) loadBulkActivity();
  }

  function projectReadSnapshot(
    result: ActivityReadResponse,
    params: ActivityParams,
    authoritative: boolean,
  ): Effect.Effect<void, ApiProblemError | TransientTransportError, GeneratedApi> {
    return Effect.gen(function* () {
      const failure = result.partialFailure;
      let projected = result;
      const sameScope = result.scope === snapshotScope;
      if (
        !failure &&
        sameScope &&
        result.response.next_subject_cursor &&
        (result.subjectDepth ?? 1) < displayedSubjectDepth
      ) {
        const refreshed = yield* activityReadPages({ ...params, since: computeSince() }, displayedSubjectDepth);
        if (result.scope !== snapshotScope) return;
        return yield* projectReadSnapshot(refreshed, params, authoritative);
      }
      if (failure && sameScope) {
        const subjects = [...(result.response.item_activity ?? [])];
        const workspaces = [...(result.response.workspace_activity ?? [])];
        appendUnseen(subjects, new Set(subjects.map(stableParentKey)), itemActivity);
        appendUnseen(workspaces, new Set(workspaces.map(stableParentKey)), workspaceActivity);
        projected = {
          ...result,
          subjectDepth: displayedSubjectDepth,
          response: {
            ...result.response,
            item_activity: subjects,
            workspace_activity: workspaces,
            next_subject_cursor: subjectCursor || result.response.next_subject_cursor || "",
            item_activity_capped: subjectCursor ? itemActivityCapped : (result.response.item_activity_capped ?? false),
          },
        };
      }
      if (authoritative || (failure && sameScope)) projectAuthoritativeActivitySnapshot(projected, params);
      else projectActivitySnapshot(projected, params);
      if (failure) {
        storeError = readErrorMessage(failure);
        return yield* Effect.fail(failure);
      }
    });
  }

  function projectOlderActivityPage(
    result: ActivityReadResponse,
    params: ActivityParams,
    cursor: string,
    depth: number,
  ): void {
    if (displayedSubjectDepth >= depth) return;
    if (subjectCursor !== cursor) {
      requestedSubjectDepth = displayedSubjectDepth;
      if (subjectCursor) loadOlderItems();
      return;
    }
    const subjects = [...itemActivity];
    appendUnseen(subjects, new Set(subjects.map(stableParentKey)), result.response.item_activity);
    const added = subjects.slice(itemActivity.length);
    itemActivity = subjects;
    reconcileItemsWithParentSubjects(added);
    const workspaces = [...workspaceActivity];
    appendUnseen(workspaces, new Set(workspaces.map(stableParentKey)), result.response.workspace_activity);
    workspaceActivity = workspaces;
    itemActivityCapped = result.response.item_activity_capped ?? false;
    subjectCursor = result.response.next_subject_cursor ?? "";
    displayedSubjectDepth = depth;
    if (params.projection === "collapsed" && activityPageLimit === undefined) {
      for (const subject of added) {
        const key = stableParentKey(subject);
        if (key && isThreadItemExpanded(key)) loadThreadEvents(key);
      }
    }
  }

  function loadActivityProgram(
    params: ActivityParams,
    owner: "foreground" | "poll" = "foreground",
    acceptsProjection: () => boolean = () => true,
    page?: { cursor: string; depth: number },
  ) {
    return Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const scope = pagedActivityScopeKey();
      const read = Effect.suspend(() =>
        page
          ? activityRead({ ...params, subject_before: page.cursor })
          : activityReadPages(params, requestedSubjectDepth),
      );
      const project = (result: ActivityReadResponse) =>
        Effect.suspend(() => {
          if (!acceptsProjection()) return Effect.void;
          if (page) return Effect.sync(() => projectOlderActivityPage(result, params, page.cursor, page.depth));
          return projectReadSnapshot(result, params, false);
        });
      const clearLoading = Effect.sync(() => {
        if (!page && acceptsProjection()) loading = false;
      });
      yield* page
        ? workflow.pageRead(scope, read, project, clearLoading)
        : owner === "foreground"
          ? workflow.load(scope, read, project, clearLoading)
          : workflow.pollSnapshotRead(scope, read, project, clearLoading);
    });
  }

  function loadActivityEffect(
    params: ActivityParams,
    generation: number,
    scope: string,
    page?: { cursor: string; depth: number },
  ) {
    const ownsProjection = () =>
      page
        ? generation === childProjectionInvalidationGeneration && scope === activityProjectionScope(buildParams())
        : ownsForegroundSnapshot(generation, scope);
    const depth = requestedSubjectDepth;
    return Effect.sync(() => ownsProjection()).pipe(
      Effect.flatMap((current) => {
        if (!current) return Effect.void;
        if (!page) loading = true;
        storeError = null;
        return loadActivityProgram(params, "foreground", ownsProjection, page);
      }),
      Effect.tapError((failure) =>
        Effect.sync(() => {
          if (!ownsProjection()) return;
          storeError = readErrorMessage(failure);
          if (!page) loading = false;
          if (requestedSubjectDepth === depth) requestedSubjectDepth = displayedSubjectDepth;
          flatPaging.rollback(activityPageQuery(params));
        }),
      ),
      Effect.ensuring(
        Effect.sync(() => {
          if (
            page &&
            generation === childProjectionInvalidationGeneration &&
            olderItemsPage === page &&
            requestedSubjectDepth === depth &&
            displayedSubjectDepth < depth
          ) {
            requestedSubjectDepth = displayedSubjectDepth;
          }
        }),
      ),
    );
  }

  function reconcileActivityEffect() {
    return Effect.suspend(() => {
      retainedFeedStale = false;
      const params = buildParams();
      const scope = pagedActivityScopeKey();
      if (shouldUseCollapsedAuthoritativeProjection()) params.projection = "collapsed";
      const read = Effect.suspend(() => activityReadPages(params, requestedSubjectDepth));
      const project = (result: ActivityReadResponse) => projectReadSnapshot(result, params, true);
      return Effect.gen(function* () {
        const workflow = yield* ActivityWorkflow;
        yield* Effect.all([workflow.reconcileRead(scope, read, project), loadActivityAuthorsEffect()], {
          concurrency: "unbounded",
          discard: true,
        });
      });
    });
  }

  function refreshAgentStatusEffect() {
    return Effect.gen(function* () {
      if (![items, itemActivity, workspaceActivity].some((rows) => rows.some((item) => item.workspace))) return;
      const response = yield* executeGeneratedApiRequest("refresh Activity agent status", (client, signal) =>
        client.WorkspacesService.listWorkspaces({ signal }),
      );
      const states = new Map(response.workspaces.map((workspace) => [workspace.id, workspace.agent_state]));
      const update = <T extends { workspace?: ActivityItem["workspace"] }>(item: T): T => {
        if (!item.workspace) return item;
        const agentState = states.get(item.workspace.id);
        return item.workspace.agent_state === agentState
          ? item
          : { ...item, workspace: { ...item.workspace, agent_state: agentState } };
      };
      const updateRows = <T extends { workspace?: ActivityItem["workspace"] }>(rows: T[]): T[] => {
        const next = rows.map(update);
        return next.some((row, index) => row !== rows[index]) ? next : rows;
      };
      items = updateRows(items);
      itemActivity = updateRows(itemActivity);
      workspaceActivity = updateRows(workspaceActivity);
    });
  }

  function loadThreadEvents(key: string, loadAllPages = true): void {
    if (loadedThreadKeys.has(key) || loadingThreadKeys.has(key)) return;
    const subject = itemActivity.find((candidate) => stableParentKey(candidate) === key);
    const repositoryKey = repositoryKeyFromWire(subject?.repo);
    if (!subject || !repositoryKey || (subject.item_type !== "pr" && subject.item_type !== "issue")) return;
    const itemType: "pr" | "issue" = subject.item_type === "pr" ? "pr" : "issue";

    const requestGeneration = pagedActivityGeneration;
    const requestScope = pagedActivityScopeKey();
    const requestToken = Symbol(key);
    const initialThreadItemIDs = new Set(items.filter((item) => stableParentKey(item) === key).map((item) => item.id));
    threadRequestTokens.set(key, requestToken);
    loadingThreadKeys = new Set([...loadingThreadKeys, key]);
    failedThreadKeys = new Set([...failedThreadKeys].filter((candidate) => candidate !== key));
    if (failedThreadKeys.size === 0) threadLoadError = null;
    const snapshotCursor = activityEventCursor;
    const baseQuery = {
      provider: subject.repo.provider,
      platform_host: subject.repo.platform_host,
      ...repositoryKeyToWire(repositoryKey),
      item_type: itemType,
      item_number: subject.item_number,
      since: computeSince(),
      ...(snapshotCursor ? { at_or_before: snapshotCursor } : {}),
      limit: loadAllPages ? 100 : 10,
      ...(filterTypes.length > 0 ? { types: [...filterTypes] } : {}),
      ...(searchQuery ? { search: searchQuery } : {}),
      ...(unassigned === "require" ? { unassigned: true } : {}),
      ...(hideClosedMerged ? { hide_closed_merged: true } : {}),
      ...(hideBots ? { hide_bots: true } : {}),
      ...(hideDrafts ? { hide_drafts: true } : {}),
      ...(hideMergeQueued ? { hide_merge_queued: true } : {}),
      ...(onlyMergeQueued ? { merge_queued: true } : {}),
      ...(onlyClosedMerged ? { closed_merged: true } : {}),
      ...(onlyBots ? { bots: true } : {}),
      ...(threadPullAttributes.length > 0 ? { attributes: threadPullAttributes.join(",") } : {}),
      ...(starred ? { starred: true } : {}),
      ...(referencedByPR === "require" ? { referenced_by_pr: true } : {}),
      ...(referencedByPR === "exclude" ? { not_referenced_by_pr: true } : {}),
      ...(hideBotAuthoredIssues ? { hide_bot_authored_issues: true } : {}),
      ...labelFilterParams(labelFilters),
      ...(hideDefaultBranchActivity ? { hide_default_branch: true } : {}),
    };
    const isCurrentRequest = () =>
      requestGeneration === pagedActivityGeneration &&
      requestScope === pagedActivityScopeKey() &&
      threadRequestTokens.get(key) === requestToken;
    const readAllPages = Effect.gen(function* () {
      let before = "";
      const pageResults: OwnedActivityResponse[] = [];
      while (true) {
        const query = {
          ...baseQuery,
          ...(before ? { before } : {}),
        };
        const startedAt = yield* Effect.sync(() => ++activityLifecycleTick);
        const response = yield* executeGeneratedApiRequest("GET /activity/thread-events", (client, signal) =>
          client.ActivityService.listActivityThreadEvents(query, { signal }),
        ).pipe(retryIdempotentRead);
        if (!(yield* Effect.sync(isCurrentRequest))) return;
        pageResults.push({ response, startedAt });
        if (!loadAllPages) break;
        const next = response.next_cursor ?? "";
        if (next === "") break;
        before = next;
      }
      yield* Effect.sync(() => {
        if (!isCurrentRequest()) return;
        const replacement = projectOwnedActivityPages(pageResults);
        const replacementIDs = new Set(replacement.map((item) => item.id));
        items = [
          ...items.filter((item) => {
            if (stableParentKey(item) !== key) return true;
            return !initialThreadItemIDs.has(item.id) && !replacementIDs.has(item.id);
          }),
          ...replacement,
        ];
        reconcileItemsWithParentSubjects(itemActivity);
      });
    });
    runtime.runCommand(
      readAllPages.pipe(
        Effect.tap(() =>
          Effect.sync(() => {
            if (!isCurrentRequest()) return;
            loadedThreadKeys = new Set([...loadedThreadKeys, key]);
          }),
        ),
        Effect.tapError((failure) =>
          Effect.sync(() => {
            if (!isCurrentRequest()) return;
            failedThreadKeys = new Set([...failedThreadKeys, key]);
            threadLoadError = readErrorMessage(failure, "could not load activity thread");
          }),
        ),
        Effect.ensuring(
          Effect.sync(() => {
            if (threadRequestTokens.get(key) !== requestToken) return;
            threadRequestTokens.delete(key);
            loadingThreadKeys = new Set([...loadingThreadKeys].filter((candidate) => candidate !== key));
          }),
        ),
      ),
      {
        operation: "load activity thread",
        safeContext: { itemType: subject.item_type, itemNumber: subject.item_number },
        onFailure: () => {},
      },
    );
  }

  function loadThreadPreview(key: string): void {
    loadThreadEvents(key, false);
  }

  function retryFailedThreadLoads(): void {
    const retryKeys = [...failedThreadKeys].filter((key) => isThreadItemExpanded(key));
    failedThreadKeys = new Set();
    threadLoadError = null;
    for (const key of retryKeys) loadThreadEvents(key);
  }

  function loadBulkActivity(): void {
    if (activityEventCursor === "") {
      loadActivity();
      return;
    }
    pagedActivityGeneration += 1;
    threadRequestTokens.clear();
    loadingThreadKeys = new Set();
    const requestGeneration = pagedActivityGeneration;
    const requestScope = pagedActivityScopeKey();
    const requestToken = Symbol("bulk activity");
    bulkRequestToken = requestToken;
    const initialItemIDs = new Set(items.map((item) => item.id));
    const snapshotCursor = activityEventCursor;
    const baseParams = buildParams();
    baseParams.projection = "events";
    baseParams.limit = 500;
    baseParams.at_or_before = snapshotCursor;
    const isCurrentRequest = () =>
      requestGeneration === pagedActivityGeneration &&
      requestScope === pagedActivityScopeKey() &&
      bulkRequestToken === requestToken;
    loading = true;
    storeError = null;
    const program = Effect.gen(function* () {
      let before = "";
      const pageResults: OwnedActivityResponse[] = [];
      while (true) {
        const params = { ...baseParams, ...(before !== "" ? { before } : {}) };
        const result = yield* activityRead(params);
        if (!(yield* Effect.sync(isCurrentRequest))) return;
        pageResults.push(result);
        const next = result.response.next_cursor ?? "";
        if (next === "") break;
        before = next;
      }
      yield* Effect.sync(() => {
        if (!isCurrentRequest()) return;
        const replacement = projectOwnedActivityPages(pageResults);
        const replacementIDs = new Set(replacement.map((item) => item.id));
        items = [
          ...items.filter((item) => !initialItemIDs.has(item.id) && !replacementIDs.has(item.id)),
          ...replacement,
        ];
        reconcileItemsWithParentSubjects(itemActivity);
      });
    }).pipe(
      Effect.tapError((failure) =>
        Effect.sync(() => {
          if (!isCurrentRequest()) return;
          storeError = readErrorMessage(failure, "could not load full activity");
        }),
      ),
      Effect.ensuring(
        Effect.sync(() => {
          if (!isCurrentRequest()) return;
          bulkRequestToken = undefined;
          loading = false;
        }),
      ),
    );
    runtime.runCommand(program, {
      operation: "expand all activity",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function ensureActivityLoaded(): void {
    if (snapshotScope !== pagedActivityScopeKey()) {
      loadActivity(true);
    } else if (retainedFeedStale) {
      runtime.runCommand(reconcileActivityEffect(), {
        operation: "reconcile stale activity on return",
        safeContext: {},
        onFailure: () => {},
      });
    }
    startActivityPolling();
  }

  // Provider changes while the Activity page is hidden mark the retained feed
  // instead of reading it; the page reconciles it when it is shown again.
  function markActivityStale(): void {
    if (initialized) retainedFeedStale = true;
  }

  function loadActivity(forceAuthors = false): void {
    retainedFeedStale = false;
    invalidatePagedActivityRequests();
    const generation = pagedActivityGeneration;
    const params = buildParams();
    restoreRepositoryActivity(params);
    const scope = activityProjectionScope(params);
    loadActivityAuthors(forceAuthors || authorsLoading);
    runtime.runCommand(loadActivityEffect(params, generation, scope), {
      operation: "load activity",
      safeContext: {},
      onFailure: (failure) => {
        if (!ownsForegroundSnapshot(generation, scope)) return;
        storeError = readErrorMessage(failure);
        loading = false;
      },
    });
  }

  function refreshActivityProgram(params: ActivityParams) {
    return Effect.gen(function* () {
      const scope = pagedActivityScopeKey();
      if (shouldUseCollapsedAuthoritativeProjection()) params.projection = "collapsed";
      const workflow = yield* ActivityWorkflow;
      yield* workflow.pollSnapshotRead(
        scope,
        Effect.suspend(() => activityReadPages(params, requestedSubjectDepth)),
        (result) => projectReadSnapshot(result, params, true),
        Effect.sync(() => {
          loading = false;
        }),
      );
    });
  }

  // Mark a notification feed row as seen: queues the GitHub read
  // propagation backend-side and flips the row to read locally so the
  // unread affordance clears without waiting for the next sync. The
  // activity item id for a notification is "ntf:<db id>".
  function projectNotificationState(id: string, state: string): void {
    const update = (item: ActivityItem): ActivityItem => (item.id === id ? { ...item, item_state: state } : item);
    items = items.map(update);
    for (const [scope, snapshot] of recentSnapshots) {
      recentSnapshots.set(scope, { ...snapshot, items: snapshot.items.map(update) });
    }
  }

  function markNotificationSeen(item: ActivityItem): void {
    const id = notificationDbId(item.id);
    if (id === null) return;
    const mutationTick = ++activityLifecycleTick;
    const baseline = item.item_state;
    let acknowledged = false;
    const apply = (state: string) =>
      Effect.sync(() => {
        notificationStateOwnership.set(item.id, { tick: mutationTick, state });
        projectNotificationState(item.id, state);
      });
    const program = Effect.gen(function* () {
      const mutations = yield* ProviderMutations;
      const commit = executeGeneratedApiRequest<NotificationBulkResponse>(
        "POST mark notification read",
        (client, signal) => client.ActivityService.markNotificationsRead({ ids: [id] }, { signal }),
      ).pipe(
        Effect.map((response) => {
          acknowledged = [...(response.succeeded ?? []), ...(response.queued ?? [])].includes(id);
          return acknowledged ? "read" : baseline;
        }),
      );
      yield* mutations.submit({
        key: `notification\u0000${encodeURIComponent(String(id))}\u0000seen`,
        baseline,
        optimistic: "read",
        apply,
        commit,
        refreshOnStale: Effect.succeed(baseline),
      });
      if (acknowledged) {
        const acknowledgementTick = ++activityLifecycleTick;
        notificationStateOwnership.set(item.id, { tick: acknowledgementTick, state: "read" });
        projectNotificationState(item.id, "read");
      } else {
        notificationStateOwnership.delete(item.id);
        showFlash("Failed to mark notification as read.", { tone: "danger" });
      }
    });
    runtime.runCommand(program, {
      operation: "mark notification read",
      safeContext: { notificationId: id },
      onFailure: (failure) => {
        showFlash(providerMutationFailureMessage(failure, "failed to mark notification as read"), { tone: "danger" });
      },
    });
  }

  // Keeps polled rows inside the time range and, in the flat feed, inside the
  // loaded page, so Load more only ever extends what is shown.
  function liveActivityPage(next: ActivityItem[]): ActivityItem[] {
    const cutoff = new Date(Date.now() - RANGE_MS[timeRange]);
    const live = next.filter((item) => new Date(item.created_at) >= cutoff);
    if (viewMode !== "flat" || activityPageLimit !== undefined || live.length <= flatPaging.getLimit()) return live;
    capped = true;
    return live.slice(0, flatPaging.getLimit());
  }

  const pollNewItems = Effect.suspend(() => {
    if (loading) return Effect.void;
    pollCount += 1;
    const params = buildParams();
    if (items.length === 0 && itemActivity.length === 0 && workspaceActivity.length === 0) {
      loading = true;
      return loadActivityProgram(params, "poll").pipe(Effect.andThen(loadActivityAuthorsEffect()));
    }
    if (pollCount % AUTHORITATIVE_REFRESH_EVERY === 0) {
      return refreshActivityProgram(params).pipe(Effect.andThen(loadActivityAuthorsEffect()));
    }
    if (activityEventCursor === "") return Effect.void;
    const scope = pagedActivityScopeKey();
    const authoritativeParams = { ...params };
    if (shouldUseCollapsedAuthoritativeProjection()) authoritativeParams.projection = "collapsed";
    params.projection = "events";
    params.limit = 500;
    params.after = activityEventCursor;
    return Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const projectPoll = ({ mode, result }: ActivityPollProjection) =>
        Effect.suspend(() => {
          if (mode === "replace") {
            return projectReadSnapshot(result, authoritativeParams, true);
          }
          const existingIds = new Set(items.map((item) => item.id));
          const newItems = projectOwnedNotificationStates(result, false).filter((item) => !existingIds.has(item.id));
          activityEventCursor = result.response.event_cursor ?? activityEventCursor;
          const nextItems = liveActivityPage(newItems.length > 0 ? [...newItems, ...items] : items);
          if (newItems.length > 0 || nextItems.length !== items.length) items = nextItems;
          return Effect.void;
        });
      yield* workflow.pollRead(
        scope,
        activityRead(params),
        (result) => {
          if (!result.response.capped) return projectPoll({ mode: "append", result });
          const replacementParams = buildParams();
          const replacementScope = pagedActivityScopeKey();
          if (shouldUseCollapsedAuthoritativeProjection()) replacementParams.projection = "collapsed";
          return workflow.pollSnapshotRead(
            replacementScope,
            Effect.suspend(() => activityReadPages(replacementParams, requestedSubjectDepth)),
            (replacement) => projectPoll({ mode: "replace", result: replacement }),
          );
        },
        Effect.sync(() => {
          loading = false;
        }),
      );
    });
  }).pipe(
    Effect.catch(() => Effect.void),
    Effect.asVoid,
  );

  function startActivityPolling(): void {
    if (pollingStarted) return;
    pollingStarted = true;
    const program = Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      yield* workflow.poll(pollNewItems, "15 seconds");
    }).pipe(
      Effect.ensuring(
        Effect.sync(() => {
          pollingStarted = false;
        }),
      ),
    );
    runtime.runCommand(program, {
      operation: "poll activity",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function stopActivityPolling(): void {
    const program = Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      yield* workflow.stopPolling;
    });
    runtime.runCommand(program, {
      operation: "stop activity polling",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function rebuildFilterTypes(): void {
    filterTypes = buildActivityFilterTypes(
      enabledItemTypes,
      enabledEvents,
      hideDefaultBranchActivity,
      showNotifications,
    );
  }

  function applyCollapsedFromURL(): void {
    const sp = new URLSearchParams(window.location.search);
    if (!sp.has("collapsed")) return;
    const v = sp.get("collapsed");
    if (v === "1") collapseThreads = true;
    else if (v === "0") collapseThreads = false;
  }

  function deleteCollapsedParam(): void {
    const sp = new URLSearchParams(window.location.search);
    if (!sp.has("collapsed")) return;
    sp.delete("collapsed");
    const qs = sp.toString();
    const path = window.location.pathname || getBasePath();
    history.replaceState(null, "", path + (qs ? `?${qs}` : ""));
  }

  function syncFromURL(): void {
    const sp = new URLSearchParams(window.location.search);
    const legacySelections = sp.has("types") ? readLegacyFilterSelections(sp.get("types") ?? "") : undefined;
    enabledItemTypes = sp.has(ACTIVITY_ITEM_TYPES_PARAM)
      ? readSelectedFilters(sp.get(ACTIVITY_ITEM_TYPES_PARAM), DEFAULT_ACTIVITY_ITEM_TYPES)
      : (legacySelections?.itemTypes ?? new Set(itemTypesDefault));
    enabledEvents = sp.has(ACTIVITY_EVENT_TYPES_PARAM)
      ? readSelectedFilters(sp.get(ACTIVITY_EVENT_TYPES_PARAM), DEFAULT_EVENT_TYPES)
      : (legacySelections?.events ?? new Set(eventTypesDefault));
    if (sp.has("search")) searchQuery = sp.get("search") ?? undefined;
    authorFilter = sp.get("author")?.trim() || undefined;
    if (sp.has("range")) {
      const rangeParam = sp.get("range");
      if (rangeParam && rangeParam in RANGE_MS) timeRange = rangeParam as TimeRange;
    }
    if (sp.has("view")) {
      const viewParam = sp.get("view");
      if (viewParam === "flat" || viewParam === "threaded") viewMode = viewParam;
    }
    rollUpCommits = sp.has("rollup_commits") ? sp.get("rollup_commits") === "1" : rollUpCommitsDefault;
    hideDefaultBranchActivity = sp.has("hide_branch") ? sp.get("hide_branch") === "1" : hideDefaultBranchDefault;
    showNotifications = sp.has("notif") ? sp.get("notif") !== "0" : showNotificationsDefault;
    const hideClosedParam = sp.get("hide_closed");
    hideClosedMergedOverride = hideClosedParam === "1" ? true : hideClosedParam === "0" ? false : undefined;
    if (hideClosedMergedOverride !== undefined) hideClosedMerged = hideClosedMergedOverride;
    const hideQueuedParam = sp.get("hide_merge_queued");
    hideMergeQueuedOverride = hideQueuedParam === "1" ? true : hideQueuedParam === "0" ? false : undefined;
    if (hideMergeQueuedOverride !== undefined) hideMergeQueued = hideMergeQueuedOverride;
    applyCollapsedFromURL();
    rebuildFilterTypes();
  }

  function syncToURL(): void {
    const sp = new URLSearchParams(window.location.search);
    rebuildFilterTypes();
    const legacySelections = sp.has("types");
    sp.delete("types");
    const preserve = (name: string) => !defaultsHydrated && (selectedFilters.has(name) || sp.has(name));
    writeSelectedFilters(
      sp,
      ACTIVITY_ITEM_TYPES_PARAM,
      enabledItemTypes,
      DEFAULT_ACTIVITY_ITEM_TYPES,
      itemTypesDefault,
      preserve(ACTIVITY_ITEM_TYPES_PARAM) || (!defaultsHydrated && legacySelections),
    );
    writeSelectedFilters(
      sp,
      ACTIVITY_EVENT_TYPES_PARAM,
      enabledEvents,
      DEFAULT_EVENT_TYPES,
      eventTypesDefault,
      preserve(ACTIVITY_EVENT_TYPES_PARAM) || (!defaultsHydrated && legacySelections),
    );
    if (searchQuery) sp.set("search", searchQuery);
    else sp.delete("search");
    if (authorFilter) sp.set("author", authorFilter);
    else sp.delete("author");
    if (hideClosedMergedOverride !== undefined) sp.set("hide_closed", hideClosedMergedOverride ? "1" : "0");
    else sp.delete("hide_closed");
    if (hideMergeQueuedOverride !== undefined) sp.set("hide_merge_queued", hideMergeQueuedOverride ? "1" : "0");
    else sp.delete("hide_merge_queued");
    if (timeRange !== timeRangeDefault || (!defaultsHydrated && (timeRangeSelected || sp.has("range"))))
      sp.set("range", timeRange);
    else sp.delete("range");
    if (viewMode !== viewModeDefault || (!defaultsHydrated && (viewModeSelected || sp.has("view"))))
      sp.set("view", viewMode);
    else sp.delete("view");
    for (const [name, value, defaultValue] of [
      ["rollup_commits", rollUpCommits, rollUpCommitsDefault],
      ["hide_branch", hideDefaultBranchActivity, hideDefaultBranchDefault],
      ["notif", showNotifications, showNotificationsDefault],
    ] as const) {
      if (value !== defaultValue || preserve(name)) sp.set(name, value ? "1" : "0");
      else sp.delete(name);
    }
    if (collapseThreads !== collapseThreadsDefault) {
      sp.set("collapsed", collapseThreads ? "1" : "0");
    } else {
      sp.delete("collapsed");
    }
    const qs = sp.toString();
    const path = window.location.pathname || getBasePath();
    const url = path + (qs ? `?${qs}` : "");
    history.replaceState(null, "", url);
  }

  return {
    getActivityItems,
    getItemActivity,
    getWorkspaceActivity,
    isActivityLoading,
    getActivityError,
    getThreadLoadError,
    isActivityCapped,
    isItemActivityCapped,
    canLoadOlderItems,
    isLoadingOlderItems,
    loadOlderItems,
    canLoadMoreActivity,
    isLoadingMoreActivity,
    loadMoreActivity,
    getActivityEventCursor,
    getActivityFilterTypes,
    getActivitySearch,
    getActivityAuthor,
    getActivityAuthors,
    isActivityAuthorsLoading,
    getActivityAuthorsError,
    getTimeRange,
    getViewMode,
    getCollapseThreads,
    getRollUpCommits,
    isThreadItemExpanded,
    getHideClosedMerged,
    getHideBots,
    getLabelFilters,
    cycleLabelFilter,
    clearLabelFilters,
    getHideDrafts,
    getHideMergeQueued,
    getMergeQueued,
    getActivityState,
    getTimeRangeDefault,
    getViewModeDefault,
    getStarred,
    getReferencedByPR,
    getHideBotAuthoredIssues,
    setStarred,
    setReferencedByPR,
    setHideBotAuthoredIssues,
    getBots,
    getDraft,
    getPullAttributeFilters,
    getUseWorkspaceActivityForRecency,
    getHideDefaultBranchActivity,
    getEnabledItemTypes,
    getEnabledEvents,
    getShowNotifications,
    getInvolvesMe,
    getUnassigned,
    markActivityStale,
    setActivityFilterTypes,
    setActivitySearch,
    setActivityAuthor,
    setTimeRange,
    setViewMode,
    setFullEventProjectionRequired,
    setActivityPageLimit,
    setRollUpCommits,
    collapseAllThreads,
    expandAllThreads,
    toggleThreadItem,
    retryFailedThreadLoads,
    loadThreadPreview,
    setHideClosedMerged,
    setHideBots,
    setHideDrafts,
    setHideMergeQueued,
    setMergeQueued,
    setActivityState,
    setBots,
    setDraft,
    togglePullAttributeFilter,
    cyclePullAttributeFilter,
    setPullAttributeFilters,
    setHideDefaultBranchActivity,
    setEnabledItemTypes,
    setEnabledEvents,
    setShowNotifications,
    setInvolvesMe,
    setUnassigned,
    hydrateDefaults,
    initializeFromMount,
    loadActivityAuthors,
    ensureActivityLoaded,
    loadActivity,
    loadActivityEffect,
    reconcileActivityEffect,
    refreshAgentStatusEffect,
    markNotificationSeen,
    startActivityPolling,
    stopActivityPolling,
    syncFromURL,
    syncToURL,
  };
}

export type ActivityStore = ReturnType<typeof createActivityStore>;
