// Activity's filter menu: the PR and issue list filters, built from the same
// elements as those lists (shared/list-filter-menus.ts), plus Activity's own
// timeline filters. Activity's extras must not overlap a shared filter.
import type { Snippet } from "svelte";

import type { ActivityItem, ActivitySubject, WorkspaceActivitySubject } from "../api/types.js";
import {
  buildActivityFilterTypes,
  DEFAULT_EVENT_TYPES,
  isActivityItemTypeEnabled,
  type ActivityItemType,
  type ActivityStore,
  type TimeRange,
} from "../stores/activity.svelte.js";
import type { GroupingStore } from "../stores/grouping.svelte.js";
import type { IssuesStore } from "../stores/issues.svelte.js";
import { isBotAuthor as isBotAccount } from "../utils/bot-author.js";
import {
  PULL_ATTRIBUTE_SECTIONS,
  cycleFilterInSections,
  toggleFilterInSections,
  triFilterInSelection,
  type PullAttributeFilter,
} from "../utils/pull-attributes.js";
import { activityItemKey, isClosedOrMergedActivity, isDefaultBranchActivity } from "./activityRows.js";
import { repositoryKeyFromWire, type RepositoryKeyWire } from "../api/repository-key.js";
import {
  commonSection,
  countFilters,
  groupSection,
  hideOrgNameChoice,
  issueFilterSections,
  pullFilterSections,
  resetSections,
  stateSection,
  toggleChoice,
  triChoice,
  type FilterChoice,
  type FilterSection,
  type IssueFilterTarget,
  type PullFilterTarget,
} from "./shared/list-filter-menus.js";

const EVENT_OPTIONS = [
  { value: "comment", label: "Comments", color: "var(--accent-amber)" },
  { value: "review", label: "Reviews", color: "var(--accent-green)" },
  { value: "commit", label: "Commits", color: "var(--accent-teal)" },
  { value: "force_push", label: "Force pushes", color: "var(--accent-red)" },
] satisfies { value: (typeof DEFAULT_EVENT_TYPES)[number]; label: string; color: string }[];

const TIME_RANGES: TimeRange[] = ["24h", "7d", "30d", "90d"];

const ITEM_TYPES: { value: ActivityItemType; label: string }[] = [
  { value: "pr", label: "PRs" },
  { value: "issue", label: "Issues" },
];

// Activity's store keeps Draft and Merge queued in their own saved fields; every
// other PR attribute lives in one list. These names map between the two.
const DRAFT = { require: "draft", exclude: "not_draft" } as const;
const MERGE_QUEUED = { require: "merge_queued", exclude: "not_merge_queued" } as const;
const OWN_FIELD_ATTRIBUTES: readonly PullAttributeFilter[] = [
  DRAFT.require,
  DRAFT.exclude,
  "ready",
  MERGE_QUEUED.require,
  MERGE_QUEUED.exclude,
];

export interface ActivityFilterMenu {
  /** PRs and Issues, which the desktop toolbar also shows as toggles. */
  itemTypes: FilterChoice[];
  /** What both desktop and phone Activity show: item types, time range, and every filter. */
  sections: FilterSection[];
  /**
   * Desktop's sections: `sections` plus the Layout, Group, and Commits choices only the desktop feed has.
   * The phone inbox always shows one card per PR or issue with its newest
   * events, so it has no flat event list, repo grouping, or commit roll-up.
   */
  desktopSections: FilterSection[];
  /** How many filter choices differ from their default, for a badge. */
  filterCount: number;
  /** Turns every filter off. View, time range, grouping, and item types stay. */
  reset: () => void;
}

interface ActivityMenuDeps {
  activity: ActivityStore;
  grouping: GroupingStore;
  issues: IssuesStore;
  /** Content of the Labels row's flyout. */
  labelMenu: Snippet;
  /** Content of the Author row's flyout. */
  authorMenu: Snippet;
}

/** Activity's filter menu. Read it inside a `$derived` so it tracks the stores. */
export function activityFilterMenu({
  activity,
  grouping,
  issues,
  labelMenu,
  authorMenu,
}: ActivityMenuDeps): ActivityFilterMenu {
  const reload = () => {
    activity.syncToURL();
    activity.loadActivity();
  };
  // Event types and default-branch activity are sent as activity types.
  const applyTypes = () => {
    activity.setActivityFilterTypes(
      buildActivityFilterTypes(
        activity.getEnabledItemTypes(),
        activity.getEnabledEvents(),
        activity.getHideDefaultBranchActivity(),
        activity.getShowNotifications(),
      ),
    );
    reload();
  };

  const getAttributes = (): PullAttributeFilter[] => {
    const draft = activity.getDraft();
    const queued = activity.getMergeQueued();
    return [
      ...(draft ? [DRAFT[draft]] : []),
      ...(queued ? [MERGE_QUEUED[queued]] : []),
      ...activity.getPullAttributeFilters(),
    ];
  };
  const setAttributes = (next: PullAttributeFilter[]) => {
    const before = getAttributes();
    const tri = (pair: { require: PullAttributeFilter; exclude: PullAttributeFilter }) =>
      next.includes(pair.require) ? "require" : next.includes(pair.exclude) ? "exclude" : undefined;
    // Setting Draft or Merge queued overrides its saved default, so touch only the one that changed.
    const draft = tri(DRAFT);
    if (draft !== activity.getDraft()) activity.setDraft(draft);
    const queued = tri(MERGE_QUEUED);
    if (queued !== activity.getMergeQueued()) activity.setMergeQueued(queued);
    activity.setPullAttributeFilters(next.filter((filter) => !OWN_FIELD_ATTRIBUTES.includes(filter)));
    if ([...before, ...next].some((filter) => before.includes(filter) !== next.includes(filter))) reload();
  };

  // Activity applies every PR and issue list filter. A filter added to those
  // targets fails to compile here until Activity can apply it too.
  const target = {
    state: {
      get: activity.getActivityState,
      set: (value: string) => activity.setActivityState(value === "open" || value === "closed" ? value : "all"),
    },
    starred: { get: activity.getStarred, set: activity.setStarred },
    involvesMe: { get: activity.getInvolvesMe, set: activity.setInvolvesMe },
    unassigned: { get: activity.getUnassigned, set: activity.setUnassigned },
    labels: { filters: activity.getLabelFilters(), clear: activity.clearLabelFilters, menu: labelMenu },
    attributes: {
      get: getAttributes,
      toggle: (filter: PullAttributeFilter) =>
        setAttributes(toggleFilterInSections(PULL_ATTRIBUTE_SECTIONS, getAttributes(), filter)),
      cycle: (filter: PullAttributeFilter, opposite: PullAttributeFilter) =>
        setAttributes(cycleFilterInSections(PULL_ATTRIBUTE_SECTIONS, getAttributes(), filter, opposite)),
      set: (next: PullAttributeFilter[]) => setAttributes(next),
      clear: () => setAttributes([]),
    },
    referencedByPR: issues.canFilterReferencedByPR()
      ? { get: activity.getReferencedByPR, set: activity.setReferencedByPR }
      : null,
    hideBotAuthors: { get: activity.getHideBotAuthoredIssues, set: activity.setHideBotAuthoredIssues },
    reload,
  } satisfies PullFilterTarget & IssueFilterTarget;

  const itemTypes = ITEM_TYPES.map(({ value, label }) => ({
    id: `items-${value}`,
    label,
    active: activity.getEnabledItemTypes().has(value),
    defaultOn: true,
    onSelect: () => {
      const next = new Set(activity.getEnabledItemTypes());
      if (next.has(value)) next.delete(value);
      else next.add(value);
      activity.setEnabledItemTypes(next);
      applyTypes();
    },
  }));

  const items: FilterSection = { title: "Items", items: itemTypes };
  const timeRange: FilterSection = {
    title: "Time range",
    pick: { defaultId: `range-${activity.getTimeRangeDefault()}` },
    items: TIME_RANGES.map((range) => ({
      id: `range-${range}`,
      label: range,
      active: activity.getTimeRange() === range,
      closeOnSelect: true,
      onSelect: () => {
        activity.setTimeRange(range);
        reload();
      },
    })),
  };
  const desktopLayout: FilterSection[] = [
    {
      title: "Layout",
      pick: { defaultId: `view-${activity.getViewModeDefault()}` },
      items: (["flat", "threaded"] as const).map((mode) => ({
        id: `view-${mode}`,
        label: mode === "flat" ? "Flat" : "Threaded",
        active: activity.getViewMode() === mode,
        closeOnSelect: true,
        onSelect: () => {
          activity.setViewMode(mode);
          reload();
        },
      })),
    },
    ...(activity.getViewMode() === "threaded" ? [groupSection(grouping)] : []),
    {
      title: "Commits",
      items: [
        toggleChoice(
          "roll-up-commits",
          "Roll up commits",
          { get: activity.getRollUpCommits, set: activity.setRollUpCommits },
          activity.syncToURL,
        ),
      ],
    },
  ];

  const author = activity.getActivityAuthor();
  const common = commonSection(target);
  const filterSections: FilterSection[] = [
    stateSection(target, "all"),
    {
      ...common,
      items: [
        {
          id: "author",
          label: "Author",
          detail: author || "Anyone",
          active: Boolean(author),
          submenu: authorMenu,
          reset: () => activity.setActivityAuthor(undefined),
          onSelect: () => {},
        },
        ...common.items,
      ],
    },
    ...pullFilterSections(target),
    ...issueFilterSections(target),
    {
      title: "Event types",
      items: [
        ...EVENT_OPTIONS.map(({ value, label, color }) => ({
          id: value,
          label,
          color,
          active: activity.getEnabledEvents().has(value),
          defaultOn: true,
          reset: () => activity.setEnabledEvents(new Set(DEFAULT_EVENT_TYPES)),
          onSelect: () => {
            const next = new Set(activity.getEnabledEvents());
            // An empty selection hides filterable events while PR commits and
            // Notifications keep their own selections.
            if (next.has(value)) next.delete(value);
            else next.add(value);
            activity.setEnabledEvents(next);
            applyTypes();
          },
        })),
        {
          id: "notification",
          label: "Notifications",
          color: "var(--accent-blue)",
          active: activity.getShowNotifications(),
          defaultOn: true,
          reset: () => activity.setShowNotifications(true),
          onSelect: () => {
            activity.setShowNotifications(!activity.getShowNotifications());
            applyTypes();
          },
        },
      ],
    },
    {
      title: "Visibility",
      items: [
        // Bots is who wrote each event; Hide bot-authored issues is who opened the issue.
        triChoice("bots", "Bots", { get: activity.getBots, set: activity.setBots }, reload),
        toggleChoice(
          "hide-default-branch",
          "Hide default-branch activity",
          { get: activity.getHideDefaultBranchActivity, set: activity.setHideDefaultBranchActivity },
          applyTypes,
        ),
      ],
    },
  ];
  const view: FilterSection = { title: "View", items: [hideOrgNameChoice(grouping)] };

  return {
    itemTypes,
    // The filters come first, then layout and the time range, then View, as in the PR list menu.
    sections: [items, ...filterSections, timeRange, view],
    // Desktop shows the item types as toolbar toggles, so its menu leaves them out.
    desktopSections: [...filterSections, ...desktopLayout, timeRange, view],
    filterCount: countFilters([...filterSections, view]),
    reset: () => {
      resetSections([...filterSections, view]);
      applyTypes();
    },
  };
}

const BOT_SUFFIXES = ["[bot]", "-bot", "bot"];

/** Whether an event author is a bot, for Activity's Bots filter. */
export function isBotAuthor(author: string): boolean {
  const lower = author.toLowerCase();
  return BOT_SUFFIXES.some((suffix) => lower.endsWith(suffix));
}

interface ActivityRowFacts {
  itemType: string;
  closedOrMerged: boolean;
  author: string;
  itemAuthor: string;
  provider: string | undefined;
  hasWorkspace: boolean;
  headRepoKind: string | undefined;
}

/**
 * Applies the filters the browser checks on a loaded row. The server applies
 * the same filters to what it returns; the browser repeats them so rows that
 * arrive through live updates or workspace activity obey them, and applies
 * the ones only the browser can match (Has workspace, From fork). Labels are
 * left to the server, which matches them with full Unicode case folding. Desktop and
 * phone Activity both call these. Pull request conditions leave issue and
 * repository rows alone.
 */
function activityRowVisible(activity: ActivityStore, row: ActivityRowFacts): boolean {
  if (!isActivityItemTypeEnabled(row.itemType, activity.getEnabledItemTypes())) return false;
  const state = activity.getActivityState();
  if (state !== "all" && row.closedOrMerged !== (state === "closed")) return false;
  const bots = activity.getBots();
  if (bots !== undefined && isBotAuthor(row.author) !== (bots === "require")) return false;
  if (row.itemType === "issue") {
    return !(activity.getHideBotAuthoredIssues() && isBotAccount(row.itemAuthor, row.provider));
  }
  if (row.itemType !== "pr") return true;
  const filters = activity.getPullAttributeFilters();
  const workspace = triFilterInSelection(filters, "has_workspace", "no_workspace");
  if (workspace !== undefined && row.hasWorkspace !== (workspace === "require")) return false;
  const fork = triFilterInSelection(filters, "fork", "not_fork");
  if (fork !== undefined && (row.headRepoKind === "fork") !== (fork === "require")) return false;
  return true;
}

export function activityItemVisible(activity: ActivityStore, item: ActivityItem): boolean {
  if (activity.getHideDefaultBranchActivity() && isDefaultBranchActivity(item)) return false;
  return activityRowVisible(activity, {
    itemType: item.item_type,
    closedOrMerged: isClosedOrMergedActivity(item),
    author: item.author,
    itemAuthor: item.item_author ?? "",
    provider: item.repo?.provider,
    hasWorkspace: item.workspace !== undefined,
    headRepoKind: item.head_repo_kind,
  });
}

/** Whether Activity's filters keep a loaded PR or issue. */
export function activitySubjectVisible(
  activity: ActivityStore,
  subject: ActivitySubject | WorkspaceActivitySubject,
): boolean {
  return activityRowVisible(activity, {
    itemType: subject.item_type,
    closedOrMerged: subject.item_state === "closed" || subject.item_state === "merged",
    author: subject.item_author ?? "",
    itemAuthor: subject.item_author ?? "",
    provider: subject.repo?.provider,
    hasWorkspace: subject.workspace !== undefined,
    headRepoKind: subject.head_repo_kind,
  });
}

type LabeledSubject = Pick<
  ActivityItem,
  "repo_owner" | "repo_name" | "platform_host" | "item_type" | "item_number" | "labels"
> & {
  repo?:
    | (RepositoryKeyWire & { provider?: string; platform_host?: string; repo_path?: string | undefined })
    | undefined;
};

// Keyed by stable repository identity, so a workspace row still carrying a repository's old
// owner/name matches the hub's rows for the same item.
function labelSubjectKey(subject: LabeledSubject): string | undefined {
  if (!subject.item_type) return undefined;
  return activityItemKey({
    provider: subject.repo?.provider ?? "",
    platformHost: subject.repo?.platform_host ?? subject.platform_host,
    repositoryKey: repositoryKeyFromWire(subject.repo),
    owner: subject.repo_owner,
    name: subject.repo_name,
    repoPath: subject.repo?.repo_path,
    itemType: subject.item_type,
    itemNumber: subject.item_number,
  });
}

/**
 * Labels for every loaded PR or issue. A spoke sends workspace rows without
 * labels, so a workspace row takes its item's labels from the provider rows.
 * Desktop and phone Activity both offer label choices from this.
 */
export function activityLabelIndex(activity: ActivityStore): {
  /** One entry per PR or issue, so a label's count is items, not events. */
  subjects: { labels?: ActivityItem["labels"] }[];
} {
  // Parents last, so their labels win.
  const provider = new Map<string, ActivityItem["labels"]>();
  for (const subject of [...activity.getActivityItems(), ...activity.getItemActivity()]) {
    const key = labelSubjectKey(subject);
    if (key) provider.set(key, subject.labels);
  }
  const labelsFor = (subject: WorkspaceActivitySubject) => {
    if (subject.labels) return subject.labels;
    const key = labelSubjectKey(subject);
    return key ? provider.get(key) : undefined;
  };
  const subjects = new Map<string, { labels?: ActivityItem["labels"] }>();
  for (const [key, labels] of provider) subjects.set(key, { labels });
  for (const subject of activity.getWorkspaceActivity()) {
    const key = labelSubjectKey(subject);
    if (key && !subjects.has(key)) subjects.set(key, { labels: labelsFor(subject) });
  }
  return { subjects: [...subjects.values()] };
}
