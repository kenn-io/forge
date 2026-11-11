import { buildActivityFilterTypes, type ActivityStore, type DEFAULT_EVENT_TYPES } from "../stores/activity.svelte.js";
import type { GroupingStore } from "../stores/grouping.svelte.js";
import { cycleTriFilter, triFilterRow, type TriFilter } from "../utils/tri-filter.js";

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

/** Phone choices for a filter that can be required or excluded. */
export const ACTIVITY_TRI_FILTER_OPTIONS = [
  { value: "any", label: "Any" },
  { value: "require", label: "Yes" },
  { value: "exclude", label: "No" },
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

  const triFilters = [
    {
      id: "involves-me",
      label: "Involves me",
      get: activity.getInvolvesMe,
      set: activity.setInvolvesMe,
    },
    {
      id: "unassigned",
      label: "Unassigned",
      get: activity.getUnassigned,
      set: activity.setUnassigned,
    },
  ].map(({ id, label, get, set }): ActivityVisibilityFilter => {
    const value = get();
    const choose = (next: TriFilter) => {
      set(next);
      activity.loadActivity();
    };
    return {
      id,
      label,
      ...triFilterRow(value),
      tri: { value, set: choose },
      reset: () => set(undefined),
      onSelect: () => choose(cycleTriFilter(get())),
    };
  });

  const toggles: {
    id: string;
    label: string;
    mobileLabel?: string;
    active: boolean;
    setActive: (active: boolean) => void;
    refresh?: () => void;
  }[] = [
    {
      id: "hide-default-branch",
      label: "Hide default-branch activity",
      mobileLabel: "Hide branch",
      active: activity.getHideDefaultBranchActivity(),
      setActive: activity.setHideDefaultBranchActivity,
      refresh: applyFilters,
    },
    {
      id: "hide-closed-merged",
      label: "Hide closed/merged",
      active: activity.getHideClosedMerged(),
      setActive: activity.setHideClosedMerged,
      refresh: activity.loadActivity,
    },
    {
      id: "hide-bots",
      label: "Hide bots",
      active: activity.getHideBots(),
      setActive: activity.setHideBots,
      refresh: activity.loadActivity,
    },
    {
      id: "hide-merge-queued",
      label: "Hide queued for merge",
      active: activity.getHideMergeQueued(),
      setActive: activity.setHideMergeQueued,
      refresh: activity.loadActivity,
    },
    {
      id: "hide-drafts",
      label: "Hide drafts",
      active: activity.getHideDrafts(),
      setActive: activity.setHideDrafts,
      refresh: activity.loadActivity,
    },
    {
      id: "hide-org-name",
      label: "Hide org name",
      mobileLabel: "Hide org",
      active: grouping.getHideOrgName(),
      setActive: grouping.setHideOrgName,
    },
  ];

  return {
    applyFilters,
    events,
    visibility: [
      ...triFilters,
      ...toggles.map(({ refresh, setActive, ...filter }): ActivityVisibilityFilter => ({
        ...filter,
        excluded: false,
        reset: () => setActive(false),
        onSelect: () => {
          setActive(!filter.active);
          refresh?.();
        },
      })),
    ],
  };
}
