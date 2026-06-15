export type {
  MiddlemanClient,
  Action,
  ActionContext,
  ActionRegistry,
  NavigateEvent,
  NavigateCallback,
  MiddlemanEvent,
  EventCallback,
  PrepareRouteCallback,
  HostStateAccessors,
  StoreInstances,
  UIConfig,
  SidebarAccessors,
  PullsStore,
  IssuesStore,
  DetailStore,
  ActivityStore,
  SyncStore,
  DiffStore,
  DiffReviewDraftStore,
  GroupingStore,
  CollapsedReposStore,
  SettingsStore,
  EventsStore,
  DaemonStore,
  JobsStore,
  ReviewStore,
  LogStore,
} from "./types.js";

export {
  getStores,
  getClient,
  getActions,
  getNavigate,
  getEventCallback,
  getPrepareRoute,
  getUIConfig,
  getSidebar,
  getHostState,
  getRoborevClient,
} from "./context.js";
export {
  buildFocusIssueRoute,
  buildFocusListRoute,
  buildFocusPullRequestRoute,
  buildIssueRoute,
  buildPullRequestFilesRoute,
  buildPullRequestRoute,
  buildRoutedItemRoute,
} from "./routes.js";
export { supportsLocked } from "./api/provider-capabilities.js";
export { firstUnavailableGate, operationGate } from "./components/detail/operation-gates.js";
export type { OperationGate } from "./components/detail/operation-gates.js";
export { providerDefaultHost } from "./api/provider-routes.js";
export { DEFAULT_MODE_VISIBILITY, DEFAULT_TERMINAL_SETTINGS } from "./api/types.js";
export type {
  FocusListRouteRef,
  IssueRouteRef,
  NumberedRouteItemRef,
  PullRequestRouteRef,
  RepositoryRouteRef,
  RoutedItemRef,
} from "./routes.js";

// Store factories
export { createPullsStore } from "./stores/pulls.svelte.js";
export { createIssuesStore } from "./stores/issues.svelte.js";
export { createDetailStore } from "./stores/detail.svelte.js";
export { createActivityStore } from "./stores/activity.svelte.js";
export { createSyncStore } from "./stores/sync.svelte.js";
export { createDiffStore } from "./stores/diff.svelte.js";
export { createDiffReviewDraftStore } from "./stores/diff-review-draft.svelte.js";
export { createGroupingStore } from "./stores/grouping.svelte.js";
export { classifyPR, groupByWorkflow, workflowGroupOrder, workflowGroupLabels } from "./stores/workflow.svelte.js";
export type { WorkflowGroup, WorkflowGroupEntry } from "./stores/workflow.svelte.js";
export { createCollapsedReposStore } from "./stores/collapsedRepos.svelte.js";
export { createSettingsStore } from "./stores/settings.svelte.js";
export { createEventsStore } from "./stores/events.svelte.js";
export { createDaemonStore } from "./stores/roborev/daemon.svelte.js";
export { createJobsStore } from "./stores/roborev/jobs.svelte.js";
export { createReviewStore } from "./stores/roborev/review.svelte.js";
export { createLogStore } from "./stores/roborev/log.svelte.js";

// Provider and views
export { default as Provider } from "./Provider.svelte";
export { default as PRListView } from "./views/PRListView.svelte";
export { default as IssueListView } from "./views/IssueListView.svelte";
export { default as ActivityFeedView } from "./views/ActivityFeedView.svelte";
export { default as MobileActivityView } from "./views/MobileActivityView.svelte";
export { default as KanbanBoardView } from "./views/KanbanBoardView.svelte";
export { default as ReviewsView } from "./views/ReviewsView.svelte";
export { default as FocusListView } from "./views/FocusListView.svelte";
export { default as ActionButton } from "./components/shared/ActionButton.svelte";
export { default as CommentEditor } from "./components/detail/CommentEditor.svelte";
export { default as Chip } from "./components/shared/Chip.svelte";
export { default as ItemKindChip } from "./components/shared/ItemKindChip.svelte";
export { default as ItemStateChip } from "./components/shared/ItemStateChip.svelte";
export { default as CollapsibleResizableSidebar } from "./components/shared/CollapsibleResizableSidebar.svelte";
export { default as LeftSidebarToggle } from "./components/shared/LeftSidebarToggle.svelte";
export { default as DiffStats } from "./components/shared/DiffStats.svelte";
export { default as FilterDropdown } from "./components/shared/FilterDropdown.svelte";
export { default as SelectDropdown } from "./components/shared/SelectDropdown.svelte";
export { default as SplitResizeHandle } from "./components/shared/SplitResizeHandle.svelte";
export type { SplitResizeEvent } from "./components/shared/split-resize.js";
export { default as TabbedPanelTree } from "./components/shared/TabbedPanelTree.svelte";
export {
  clearActiveTabbedPanelDrag,
  readTabbedPanelTabDrag,
  startTabbedPanelTabDrag,
} from "./components/shared/tabbed-panel-drag.js";
export type { TabbedPanelTabDragPayload } from "./components/shared/tabbed-panel-drag.js";
export {
  activateTabbedPanelTab,
  appendTabbedPanelTabToLeaf,
  clampTabbedPanelRatio,
  collectTabbedPanelTabKeys,
  createTabbedPanelLeaf,
  findTabbedPanelLeafByTab,
  firstTabbedPanelLeaf,
  moveTabbedPanelTabBefore,
  normalizeTabbedPanelTree,
  splitTabbedPanelTabIntoLeaf,
  tabbedPanelSplitEdgeFromPoint,
  tabbedPanelSplitPlacementForEdge,
  updateTabbedPanelSplitRatio,
} from "./components/shared/tabbed-panel-layout.js";
export type {
  TabbedPanelDescriptor,
  TabbedPanelDirection,
  TabbedPanelLeaf,
  TabbedPanelNode,
  TabbedPanelSplit,
  TabbedPanelSplitEdge,
} from "./components/shared/tabbed-panel-layout.js";
export { default as WorkspaceRightSidebar } from "./components/workspace/WorkspaceRightSidebar.svelte";
export { default as WorkspaceDiffPanel } from "./components/workspace/WorkspaceDiffPanel.svelte";
export { default as DiffSidebar } from "./components/diff/DiffSidebar.svelte";
export { default as DiffToolbar } from "./components/diff/DiffToolbar.svelte";
export { default as DiffView } from "./components/diff/DiffView.svelte";
export { default as KbdBadge } from "./components/keyboard/KbdBadge.svelte";
