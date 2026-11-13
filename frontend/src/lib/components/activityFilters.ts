import type { ActivityItem, ActivitySubject, WorkspaceActivitySubject } from "../api/types.js";
import { buildActivityFilterTypes, type ActivityStore, type DEFAULT_EVENT_TYPES } from "../stores/activity.svelte.js";
import type { GroupingStore } from "../stores/grouping.svelte.js";
import { BROWSER_ONLY_PULL_ATTRIBUTES, triFilterInSelection } from "../utils/pull-attributes.js";
import {
  afterSet,
  attributeCISource,
  attributeConditionSource,
  pullFilterOptions,
  triFilter,
  type FilterRow,
} from "./pullFilters.js";
import { isClosedOrMergedActivity, isDefaultBranchActivity } from "./activityRows.js";

const EVENT_OPTIONS = [
  { value: "comment", label: "Comments", color: "var(--accent-amber)" },
  { value: "review", label: "Reviews", color: "var(--accent-green)" },
  { value: "commit", label: "Commits", color: "var(--accent-teal)" },
  { value: "force_push", label: "Force pushes", color: "var(--accent-red)" },
] satisfies { value: (typeof DEFAULT_EVENT_TYPES)[number]; label: string; color: string }[];

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

  const reload = () => activity.loadActivity();
  const attributes = [activity.getPullAttributeFilters, activity.setPullAttributeFilters] as const;
  const pull = pullFilterOptions({
    state: {
      ...afterSet({ get: activity.getActivityState, set: activity.setActivityState }, reload),
      defaultValue: "all",
    },
    involvesMe: afterSet({ get: activity.getInvolvesMe, set: activity.setInvolvesMe }, reload),
    unassigned: afterSet({ get: activity.getUnassigned, set: activity.setUnassigned }, reload),
    // Draft and Merge queued have their own Activity settings, so they live outside the attribute list.
    condition: (condition) => {
      if (condition.value === "draft") return afterSet({ get: activity.getDraft, set: activity.setDraft }, reload);
      if (condition.value === "merge_queued") {
        return afterSet({ get: activity.getMergeQueued, set: activity.setMergeQueued }, reload);
      }
      const source = attributeConditionSource(...attributes, condition);
      return BROWSER_ONLY_PULL_ATTRIBUTES.includes(condition.value) ? source : afterSet(source, reload);
    },
    ci: afterSet(attributeCISource(...attributes), reload),
  });

  const bots = triFilter("bots", "Bots", afterSet({ get: activity.getBots, set: activity.setBots }, reload));
  const hideDefaultBranch: FilterRow = {
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
  };
  // The desktop menu's Filter order, which the phone sheet follows.
  const conditions: FilterRow[] = [...pull.involvement, bots, ...pull.conditions, hideDefaultBranch];

  const view: FilterRow[] = [
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
    pull,
    bots,
    hideDefaultBranch,
    view,
    /** Every on/off and three-state filter, for counts, reset, and the phone sheet. */
    visibility: [...conditions, ...view],
    /** Pick-one filters that differ from their default. */
    activeChoiceCount: [pull.state, pull.ci].filter((choice) => choice.value !== choice.defaultValue).length,
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
