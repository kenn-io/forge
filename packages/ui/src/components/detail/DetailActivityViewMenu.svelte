<script lang="ts">
  import FilterDropdown from "../shared/FilterDropdown.svelte";
  import type { DetailActivityViewMode } from "../../stores/detail-activity-view.svelte.js";
  import {
    activePRTimelineFilterCount,
    DEFAULT_PR_TIMELINE_FILTER,
    type PRTimelineFilterState,
  } from "./prTimelineFilter.js";

  interface Props {
    viewMode: DetailActivityViewMode;
    onViewChange: (mode: DetailActivityViewMode) => void;
    filter?: PRTimelineFilterState;
    onFilterChange?: (filter: PRTimelineFilterState) => void;
  }

  let { viewMode, onViewChange, filter, onFilterChange }: Props = $props();

  const activeFilterCount = $derived(
    filter ? activePRTimelineFilterCount(filter) : 0,
  );
  const currentViewDetail = $derived(
    viewMode === "compact" ? "Compact" : "Normal",
  );
  const hasPRFilters = $derived(
    filter !== undefined && onFilterChange !== undefined,
  );

  function updateFilter(patch: Partial<PRTimelineFilterState>): void {
    if (!filter || !onFilterChange) return;
    onFilterChange({ ...filter, ...patch });
  }

  function resetFilter(): void {
    onFilterChange?.(DEFAULT_PR_TIMELINE_FILTER);
  }

  const sections = $derived.by(() => [
    {
      title: "Layout",
      items: [
        {
          id: "view-normal",
          label: "Normal",
          active: viewMode === "normal",
          closeOnSelect: true,
          onSelect: () => onViewChange("normal"),
        },
        {
          id: "view-compact",
          label: "Compact",
          active: viewMode === "compact",
          closeOnSelect: true,
          onSelect: () => onViewChange("compact"),
        },
      ],
    },
    ...(hasPRFilters && filter
      ? [
          {
            title: "Content",
            items: [
              {
                id: "messages",
                label: "Messages",
                active: filter.showMessages,
                color: "var(--accent-blue)",
                onSelect: () =>
                  updateFilter({ showMessages: !filter.showMessages }),
              },
              {
                id: "replies",
                label: "Replies",
                active: filter.showReplies,
                color: "var(--accent-purple)",
                onSelect: () =>
                  updateFilter({ showReplies: !filter.showReplies }),
              },
              {
                id: "commit-details",
                label: "Commit details",
                active: filter.showCommitDetails,
                color: "var(--accent-green)",
                onSelect: () =>
                  updateFilter({
                    showCommitDetails: !filter.showCommitDetails,
                  }),
              },
              {
                id: "events",
                label: "Events",
                active: filter.showEvents,
                color: "var(--accent-amber)",
                onSelect: () =>
                  updateFilter({ showEvents: !filter.showEvents }),
              },
              {
                id: "force-pushes",
                label: "Force pushes",
                active: filter.showForcePushes,
                color: "var(--accent-red)",
                onSelect: () =>
                  updateFilter({ showForcePushes: !filter.showForcePushes }),
              },
            ],
          },
          {
            title: "Visibility",
            items: [
              {
                id: "hide-bots",
                label: "Hide bot activity",
                active: filter.hideBots,
                color: "var(--accent-purple)",
                onSelect: () =>
                  updateFilter({ hideBots: !filter.hideBots }),
              },
            ],
          },
        ]
      : []),
  ]);
</script>

<FilterDropdown
  label="View"
  detail={currentViewDetail}
  active={viewMode === "compact" || activeFilterCount > 0}
  badgeCount={activeFilterCount}
  title={hasPRFilters
    ? "View and filter activity"
    : "View activity"}
  {sections}
  minWidth="220px"
  align="end"
  {...activeFilterCount > 0
    ? {
        resetLabel: "Show all",
        onReset: resetFilter,
      }
    : {}}
/>
