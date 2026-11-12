import type { ActivityItem, ActivitySubject, WorkspaceActivitySubject } from "../api/types.js";
import { buildActivityFilterTypes, type ActivityStore, type DEFAULT_EVENT_TYPES } from "../stores/activity.svelte.js";
import type { GroupingStore } from "../stores/grouping.svelte.js";
import {
  BROWSER_ONLY_PULL_ATTRIBUTES,
  triFilterInSelection,
  type PullAttributeFilter,
} from "../utils/pull-attributes.js";
import { cycleTriFilter, triFilterRow, type TriFilter } from "../utils/tri-filter.js";
import { isClosedOrMergedActivity, isDefaultBranchActivity } from "./activityRows.js";

const EVENT_OPTIONS = [
  { value: "comment", label: "Comments", color: "var(--accent-amber)" },
  { value: "review", label: "Reviews", color: "var(--accent-green)" },
  { value: "commit", label: "Commits", color: "var(--accent-teal)" },
  { value: "force_push", label: "Force pushes", color: "var(--accent-red)" },
] satisfies { value: (typeof DEFAULT_EVENT_TYPES)[number]; label: string; color: string }[];

interface ActivityVisibilityFilter {
  id: string;
  label: string;
  mobileLabel?: string;
  active: boolean;
  /** The filter drops its matches; only a filter with `tri` can be excluded. */
  excluded: boolean;
  /** Set for a filter that can require or exclude its matches. `onSelect` cycles it. */
  tri?: { value: TriFilter; set: (value: TriFilter) => void };
  reset: () => void;
  onSelect: () => void;
}

/** A pick-one choice: each option is a row on desktop and a select option on phones. */
interface ActivityChoiceFilter<T extends string> {
  id: string;
  label: string;
  value: T;
  /** The value Reset filters restores; any other value counts as an active filter. */
  defaultValue: T;
  options: { value: T; label: string }[];
  set: (value: T) => void;
  /** Chooses an option, or turns the filter off when an option with an off value is picked again. */
  choose: (value: T) => void;
  /** Phone select choices, including an off choice when the desktop rows have none. */
  selectOptions: { value: string; label: string }[];
  select: (value: string) => void;
}

function choiceFilter<T extends string>(
  choice: Omit<ActivityChoiceFilter<T>, "value" | "choose" | "selectOptions" | "select"> & { get: () => T },
  offLabel?: string,
): ActivityChoiceFilter<T> {
  return {
    ...choice,
    value: choice.get(),
    choose: (value) => choice.set(offLabel !== undefined && choice.get() === value ? choice.defaultValue : value),
    selectOptions:
      offLabel === undefined ? choice.options : [{ value: choice.defaultValue, label: offLabel }, ...choice.options],
    select: (value) =>
      choice.set(choice.options.find((option) => option.value === value)?.value ?? choice.defaultValue),
  };
}

/** Phone choices for a filter that can be required or excluded. */
export const ACTIVITY_TRI_FILTER_OPTIONS = [
  { value: "any", label: "Any" },
  { value: "require", label: "Yes" },
  { value: "exclude", label: "No" },
];

// Labels and order match the pull request list's filter menu.
const STATE_OPTIONS: { value: "open" | "closed" | "all"; label: string }[] = [
  { value: "open", label: "Open" },
  { value: "closed", label: "Closed" },
  { value: "all", label: "All states" },
];

const CI_OPTIONS: { value: "any" | "passed_ci" | "failed_ci"; label: string }[] = [
  { value: "passed_ci", label: "Passing CI" },
  { value: "failed_ci", label: "Failed CI" },
];

/** The pull request list's conditions, in its order. They apply only to pull request activity. */
const PULL_CONDITIONS: { id: string; value: PullAttributeFilter; opposite: PullAttributeFilter; label: string }[] = [
  { id: "approved", value: "approved", opposite: "not_approved", label: "Approved" },
  { id: "merge-conflicts", value: "merge_conflicts", opposite: "no_merge_conflicts", label: "Merge conflicts" },
];
const PULL_BROWSER_CONDITIONS: typeof PULL_CONDITIONS = [
  { id: "has-workspace", value: "has_workspace", opposite: "no_workspace", label: "Has workspace" },
  { id: "from-fork", value: "fork", opposite: "not_fork", label: "From fork" },
];

export function activityFilterOptions(activity: ActivityStore, grouping: GroupingStore) {
  function applyFilters(): void {
    activity.setActivityFilterTypes(
      buildActivityFilterTypes(
        activity.getEnabledItemTypes(),
        activity.getEnabledEvents(),
        activity.getHideDefaultBranchActivity(),
        activity.getShowNotifications(),
      ),
    );
    activity.syncToURL();
    activity.loadActivity();
  }
  const events = [
    ...EVENT_OPTIONS.map(({ value, label, color }) => ({
      id: value,
      label,
      color,
      active: activity.getEnabledEvents().has(value),
      onSelect: () => {
        const next = new Set(activity.getEnabledEvents());
        // An empty selection hides filterable events while PR commits and
        // Notifications retain their independent selections.
        if (next.has(value)) next.delete(value);
        else next.add(value);
        activity.setEnabledEvents(next);
        applyFilters();
      },
    })),
    {
      id: "notification",
      label: "Notifications",
      color: "var(--accent-blue)",
      active: activity.getShowNotifications(),
      onSelect: () => {
        activity.setShowNotifications(!activity.getShowNotifications());
        applyFilters();
      },
    },
  ];

  const state = choiceFilter<"open" | "closed" | "all">({
    id: "state",
    label: "State",
    get: () => activity.getActivityState(),
    defaultValue: "all",
    options: STATE_OPTIONS,
    set: (value) => {
      activity.setActivityState(value);
      activity.loadActivity();
    },
  });

  const ci = choiceFilter<"any" | "passed_ci" | "failed_ci">(
    {
      id: "ci",
      label: "CI",
      get: () =>
        activity.getPullAttributeFilters().find((filter) => filter === "passed_ci" || filter === "failed_ci") ?? "any",
      defaultValue: "any",
      options: CI_OPTIONS,
      set: (value) => {
        const next = activity
          .getPullAttributeFilters()
          .filter((filter) => filter !== "passed_ci" && filter !== "failed_ci");
        activity.setPullAttributeFilters(value === "any" ? next : [...next, value]);
        activity.loadActivity();
      },
    },
    "Any",
  );

  function triFilter(id: string, label: string, get: () => TriFilter, set: (value: TriFilter) => void, reload = true) {
    const value = get();
    const choose = (next: TriFilter) => {
      set(next);
      if (reload) activity.loadActivity();
    };
    return {
      id,
      label,
      ...triFilterRow(value),
      tri: { value, set: choose },
      reset: () => set(undefined),
      onSelect: () => choose(cycleTriFilter(get())),
    } satisfies ActivityVisibilityFilter;
  }

  function pullCondition({ id, value, opposite, label }: (typeof PULL_CONDITIONS)[number]) {
    const get = () => triFilterInSelection(activity.getPullAttributeFilters(), value, opposite);
    const set = (next: TriFilter) => {
      const others = activity.getPullAttributeFilters().filter((filter) => filter !== value && filter !== opposite);
      activity.setPullAttributeFilters(
        next === undefined ? others : [...others, next === "require" ? value : opposite],
      );
    };
    return triFilter(id, label, get, set, !BROWSER_ONLY_PULL_ATTRIBUTES.includes(value));
  }

  const conditions: ActivityVisibilityFilter[] = [
    triFilter("involves-me", "Involves me", activity.getInvolvesMe, activity.setInvolvesMe),
    triFilter("unassigned", "Unassigned", activity.getUnassigned, activity.setUnassigned),
    triFilter("bots", "Bots", activity.getBots, activity.setBots),
    triFilter("draft", "Draft", activity.getDraft, activity.setDraft),
    ...PULL_CONDITIONS.map(pullCondition),
    triFilter("merge-queued", "Merge queued", activity.getMergeQueued, activity.setMergeQueued),
    ...PULL_BROWSER_CONDITIONS.map(pullCondition),
    {
      id: "hide-default-branch",
      label: "Hide default-branch activity",
      mobileLabel: "Hide branch",
      active: activity.getHideDefaultBranchActivity(),
      excluded: false,
      reset: () => activity.setHideDefaultBranchActivity(false),
      onSelect: () => {
        activity.setHideDefaultBranchActivity(!activity.getHideDefaultBranchActivity());
        applyFilters();
      },
    },
  ];

  const view: ActivityVisibilityFilter[] = [
    {
      id: "hide-org-name",
      label: "Hide org name",
      mobileLabel: "Hide org",
      active: grouping.getHideOrgName(),
      excluded: false,
      reset: () => grouping.setHideOrgName(false),
      onSelect: () => grouping.setHideOrgName(!grouping.getHideOrgName()),
    },
  ];

  return {
    applyFilters,
    events,
    state,
    conditions,
    ci,
    view,
    /** Every on/off and three-state filter, for counts, reset, and the phone sheet. */
    visibility: [...conditions, ...view],
    /** Pick-one filters that differ from their default. */
    activeChoiceCount: [state, ci].filter((choice) => choice.value !== choice.defaultValue).length,
    resetChoices: () => {
      activity.setActivityState("all");
      activity.setPullAttributeFilters([]);
    },
  };
}

const BOT_SUFFIXES = ["[bot]", "-bot", "bot"];

export function isBotAuthor(author: string): boolean {
  const lower = author.toLowerCase();
  return BOT_SUFFIXES.some((suffix) => lower.endsWith(suffix));
}

interface ActivityRowFacts {
  itemType: string;
  closedOrMerged: boolean;
  author: string;
  hasWorkspace: boolean;
  headRepoKind: string | undefined;
}

/**
 * Applies the filters the browser can check on a loaded row: State and Bots, so a change shows
 * before the next load, and Has workspace and From fork, which only the browser matches. Pull
 * request conditions leave issue and repository rows alone.
 */
function activityRowVisible(activity: ActivityStore, row: ActivityRowFacts): boolean {
  const state = activity.getActivityState();
  if (state !== "all" && row.closedOrMerged !== (state === "closed")) return false;
  const bots = activity.getBots();
  if (bots !== undefined && isBotAuthor(row.author) !== (bots === "require")) return false;
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
    hasWorkspace: item.workspace !== undefined,
    headRepoKind: item.head_repo_kind,
  });
}

export function activitySubjectVisible(
  activity: ActivityStore,
  subject: ActivitySubject | WorkspaceActivitySubject,
): boolean {
  return activityRowVisible(activity, {
    itemType: subject.item_type,
    closedOrMerged: subject.item_state === "closed" || subject.item_state === "merged",
    author: subject.item_author ?? "",
    hasWorkspace: subject.workspace !== undefined,
    headRepoKind: subject.head_repo_kind,
  });
}
