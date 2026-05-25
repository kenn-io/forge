<script lang="ts">
  import FilterDropdown from "../shared/FilterDropdown.svelte";
  import {
    activePRTimelineFilterCount,
    DEFAULT_PR_TIMELINE_FILTER,
    type PRTimelineFilterState,
  } from "./prTimelineFilter.js";

  interface Props {
    filter: PRTimelineFilterState;
    onChange: (filter: PRTimelineFilterState) => void;
  }

  let { filter, onChange }: Props = $props();

  const activeCount = $derived(activePRTimelineFilterCount(filter));

  function update(patch: Partial<PRTimelineFilterState>): void {
    onChange({ ...filter, ...patch });
  }

  const sections = $derived.by(() => [
    {
      title: "Content",
      items: [
        {
          id: "messages",
          label: "Messages",
          active: filter.showMessages,
          color: "var(--accent-blue)",
          onSelect: () => update({ showMessages: !filter.showMessages }),
        },
        {
          id: "replies",
          label: "Replies",
          active: filter.showReplies,
          color: "var(--accent-purple)",
          onSelect: () => update({ showReplies: !filter.showReplies }),
        },
        {
          id: "commit-details",
          label: "Commit details",
          active: filter.showCommitDetails,
          color: "var(--accent-green)",
          onSelect: () =>
            update({ showCommitDetails: !filter.showCommitDetails }),
        },
        {
          id: "events",
          label: "Events",
          active: filter.showEvents,
          color: "var(--accent-amber)",
          onSelect: () => update({ showEvents: !filter.showEvents }),
        },
        {
          id: "force-pushes",
          label: "Force pushes",
          active: filter.showForcePushes,
          color: "var(--accent-red)",
          onSelect: () =>
            update({ showForcePushes: !filter.showForcePushes }),
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
          onSelect: () => update({ hideBots: !filter.hideBots }),
        },
      ],
    },
  ]);
</script>

<FilterDropdown
  label="Filters"
  active={activeCount > 0}
  badgeCount={activeCount}
  title="Filter PR activity"
  {sections}
  minWidth="220px"
  align="end"
  {...activeCount > 0
    ? {
        resetLabel: "Show all",
        onReset: () => onChange(DEFAULT_PR_TIMELINE_FILTER),
      }
    : {}}
/>
