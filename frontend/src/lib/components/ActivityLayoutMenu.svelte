<script lang="ts">
  import { FilterDropdown, type FilterDropdownSection } from "@kenn-io/kit-ui";
  import { getStores } from "../context.js";
  import type { TimeRange, ViewMode } from "../stores/activity.svelte.js";

  // Activity's own layout choices, kept out of the shared filter menu: flat or threaded rows,
  // threaded grouping, and the time range.
  interface Props {
    onOpenChange: (open: boolean) => void;
  }

  const { onOpenChange }: Props = $props();
  const { activity, grouping } = getStores();

  const TIME_RANGES: { value: TimeRange; label: string }[] = [
    { value: "24h", label: "24h" },
    { value: "7d", label: "7d" },
    { value: "30d", label: "30d" },
    { value: "90d", label: "90d" },
  ];

  function setViewMode(mode: ViewMode): void {
    activity.setViewMode(mode);
    activity.syncToURL();
    activity.loadActivity();
  }

  function setTimeRange(range: TimeRange): void {
    activity.setTimeRange(range);
    activity.syncToURL();
    activity.loadActivity();
  }

  const detail = $derived(`${activity.getViewMode() === "flat" ? "Flat" : "Threaded"} · ${activity.getTimeRange()}`);

  const sections = $derived<FilterDropdownSection[]>([
    {
      title: "Layout",
      items: (["flat", "threaded"] as const).map((mode) => ({
        id: `view-${mode}`,
        label: mode === "flat" ? "Flat" : "Threaded",
        active: activity.getViewMode() === mode,
        closeOnSelect: true,
        onSelect: () => setViewMode(mode),
      })),
    },
    ...(activity.getViewMode() === "threaded"
      ? [
          {
            title: "Group",
            items: [
              {
                id: "group-by-repo",
                label: "By repo",
                active: grouping.getGroupByRepo(),
                closeOnSelect: true,
                onSelect: () => grouping.setGroupByRepo(true),
              },
              {
                id: "group-all",
                label: "All",
                active: !grouping.getGroupByRepo(),
                closeOnSelect: true,
                onSelect: () => grouping.setGroupByRepo(false),
              },
            ],
          },
        ]
      : []),
    {
      title: "Time range",
      items: TIME_RANGES.map((range) => ({
        id: `range-${range.value}`,
        label: range.label,
        active: activity.getTimeRange() === range.value,
        closeOnSelect: true,
        onSelect: () => setTimeRange(range.value),
      })),
    },
  ]);
</script>

<FilterDropdown
  label="Layout"
  title="Activity layout and time range"
  {detail}
  icon="sort"
  showBadge={false}
  {sections}
  minWidth="160px"
  {onOpenChange}
/>
