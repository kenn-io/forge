import { buildActivityFilterTypes, type ActivityStore, type DEFAULT_EVENT_TYPES } from "../stores/activity.svelte.js";
import type { GroupingStore } from "../stores/grouping.svelte.js";

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
  color: string;
  active: boolean;
  setActive: (active: boolean) => void;
  onSelect: () => void;
}

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

  const visibility: (Omit<ActivityVisibilityFilter, "onSelect"> & { refresh?: () => void })[] = [
    {
      id: "involves-me",
      label: "Involves me",
      color: "var(--accent-blue)",
      active: activity.getInvolvesMe(),
      setActive: activity.setInvolvesMe,
      refresh: activity.loadActivity,
    },
    {
      id: "unassigned",
      label: "Unassigned",
      color: "var(--accent-blue)",
      active: activity.getUnassigned(),
      setActive: activity.setUnassigned,
      refresh: activity.loadActivity,
    },
    {
      id: "hide-default-branch",
      label: "Hide default-branch activity",
      mobileLabel: "Hide branch",
      color: "var(--accent-teal)",
      active: activity.getHideDefaultBranchActivity(),
      setActive: activity.setHideDefaultBranchActivity,
      refresh: applyFilters,
    },
    {
      id: "hide-closed-merged",
      label: "Hide closed/merged",
      color: "var(--accent-red)",
      active: activity.getHideClosedMerged(),
      setActive: activity.setHideClosedMerged,
      refresh: activity.loadActivity,
    },
    {
      id: "hide-bots",
      label: "Hide bots",
      color: "var(--accent-purple)",
      active: activity.getHideBots(),
      setActive: activity.setHideBots,
      refresh: activity.loadActivity,
    },
    {
      id: "hide-merge-queued",
      label: "Hide queued for merge",
      color: "var(--accent-green)",
      active: activity.getHideMergeQueued(),
      setActive: activity.setHideMergeQueued,
      refresh: activity.loadActivity,
    },
    {
      id: "hide-drafts",
      label: "Hide drafts",
      color: "var(--accent-amber)",
      active: activity.getHideDrafts(),
      setActive: activity.setHideDrafts,
      refresh: activity.loadActivity,
    },
    {
      id: "hide-org-name",
      label: "Hide org name",
      mobileLabel: "Hide org",
      color: "var(--accent-blue)",
      active: grouping.getHideOrgName(),
      setActive: grouping.setHideOrgName,
    },
  ];

  return {
    applyFilters,
    events,
    visibility: visibility.map(({ refresh, ...filter }): ActivityVisibilityFilter => ({
      ...filter,
      onSelect: () => {
        filter.setActive(!filter.active);
        refresh?.();
      },
    })),
  };
}
