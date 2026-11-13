<script lang="ts">
  import type { ComponentProps, Snippet } from "svelte";
  import { FilterDropdown, type FilterDropdownItem, type FilterDropdownSection } from "@kenn-io/kit-ui";
  import { choiceItems, type PullFilters } from "./pullFilters.js";

  // The filter menu the PR lists and Activity share. Sections run State, a view's own sections,
  // Filter (Labels, Involves me, Unassigned, the pull request conditions), CI, then View. Layout
  // choices such as grouping and time range belong in each view's own menu, outside this one.
  interface Props extends Omit<ComponentProps<typeof FilterDropdown>, "sections"> {
    filters: PullFilters;
    /** Off when the view shows State as its own control. */
    showState?: boolean;
    /** The Labels row, for a view whose rows apply label filters. */
    labels?: { summary: string; active: boolean; menu: Snippet };
    /** Sections that follow State, such as Activity's event types. */
    sectionsAfterState?: FilterDropdownSection[];
    /** Filter rows before Labels, after Unassigned, and after the conditions. */
    rowsBefore?: FilterDropdownItem[];
    rowsAfterInvolvement?: FilterDropdownItem[];
    rowsAfter?: FilterDropdownItem[];
    view?: FilterDropdownItem[];
  }

  const {
    filters,
    showState = true,
    labels,
    sectionsAfterState = [],
    rowsBefore = [],
    rowsAfterInvolvement = [],
    rowsAfter = [],
    view = [],
    ...dropdown
  }: Props = $props();

  const sections = $derived<FilterDropdownSection[]>([
    ...(showState ? [{ title: "State", items: choiceItems(filters.state) }] : []),
    ...sectionsAfterState,
    {
      title: "Filter",
      items: [
        ...rowsBefore,
        ...(labels
          ? [{
              id: "labels",
              label: "Labels",
              detail: labels.summary,
              active: labels.active,
              submenu: labels.menu,
              onSelect: () => {},
            }]
          : []),
        ...filters.involvement,
        ...rowsAfterInvolvement,
        ...filters.conditions,
        ...rowsAfter,
      ],
    },
    { title: "CI", items: choiceItems(filters.ci) },
    ...(view.length > 0 ? [{ title: "View", items: view }] : []),
  ]);
</script>

<FilterDropdown {...dropdown} {sections} />
