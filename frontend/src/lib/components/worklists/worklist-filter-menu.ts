// The worklist Filters menu, shared by the desktop rail's dropdown and the
// phone list's inline panel so both offer the same filters.
import type { Snippet } from "svelte";

import type { GroupingStore } from "../../stores/grouping.svelte.js";
import { labelFilterSummary } from "../../utils/label-filter.js";
import { cycleFilterInSections, toggleFilterInSections, triFilterInSelection } from "../../utils/pull-attributes.js";
import { triFilterRow, type TriFilter } from "../../utils/tri-filter.js";
import { hideOrgNameChoice, type FilterSection } from "../shared/list-filter-menus.js";
import { WORKLIST_FILTER_SECTIONS, type WorklistFilters, type WorklistItemFilter } from "./worklist-items.js";

// Every selected filter must match. Review, CI, and Conflicts are pick-one pairs of opposites;
// an Items row cycles through off, required, and its opposite.
const ITEM_CONDITIONS: { value: WorklistItemFilter; opposite: WorklistItemFilter; label: string }[] = [
  { value: "involves_me", opposite: "not_involves_me", label: "Involves me" },
  { value: "approved", opposite: "not_approved", label: "Approved" },
  { value: "has_workspace", opposite: "no_workspace", label: "Has workspace" },
  // Excluded means the head's review found nothing; a head with no review matches neither.
  { value: "roborev_findings", opposite: "roborev_clean", label: "Roborev findings" },
];

const PICK_ONE: { title: string; options: { value: WorklistItemFilter; label: string }[] }[] = [
  {
    title: "Review",
    options: [
      { value: "draft", label: "Draft" },
      { value: "ready", label: "Ready for review" },
    ],
  },
  {
    title: "CI",
    options: [
      { value: "passed_ci", label: "Passing CI" },
      { value: "failed_ci", label: "Failed CI" },
    ],
  },
  {
    title: "Conflicts",
    options: [
      { value: "merge_conflicts", label: "Merge conflicts" },
      { value: "no_merge_conflicts", label: "No merge conflicts" },
    ],
  },
];

export interface WorklistFilterMenu {
  sections: FilterSection[];
  /** How many filters are on, for a badge. */
  count: number;
  clear: () => void;
}

/** The Filters menu over `filters`, which it changes in place. */
export function worklistFilterMenu(
  filters: WorklistFilters,
  grouping: GroupingStore,
  labelMenu: Snippet,
): WorklistFilterMenu {
  const without = (dropped: readonly WorklistItemFilter[]) => filters.items.filter((item) => !dropped.includes(item));

  const sections: FilterSection[] = [
    {
      title: "Items",
      items: [
        {
          id: "worklist-labels",
          label: "Labels",
          detail: labelFilterSummary(filters.labels),
          active: filters.labels.size > 0,
          submenu: labelMenu,
          onSelect: () => {},
        },
        ...ITEM_CONDITIONS.map((condition) => {
          const value = triFilterInSelection(filters.items, condition.value, condition.opposite);
          const set = (next: TriFilter) => {
            const chosen = next === "require" ? [condition.value] : next === "exclude" ? [condition.opposite] : [];
            filters.items = [...without([condition.value, condition.opposite]), ...chosen];
          };
          return {
            id: `worklist-${condition.value}`,
            label: condition.label,
            ...triFilterRow(value),
            tri: { value, set },
            onSelect: () =>
              (filters.items = cycleFilterInSections(
                WORKLIST_FILTER_SECTIONS,
                filters.items,
                condition.value,
                condition.opposite,
              )),
          };
        }),
      ],
    },
    ...PICK_ONE.map((group) => ({
      title: group.title,
      pick: { clear: () => (filters.items = without(group.options.map((option) => option.value))) },
      items: group.options.map((option) => ({
        id: `worklist-${option.value}`,
        label: option.label,
        active: filters.items.includes(option.value),
        onSelect: () => (filters.items = toggleFilterInSections(WORKLIST_FILTER_SECTIONS, filters.items, option.value)),
      })),
    })),
    { title: "View", items: [hideOrgNameChoice(grouping)] },
  ];

  return {
    sections,
    count: filters.items.length + filters.labels.size + Number(grouping.getHideOrgName()),
    clear: () => {
      filters.items = [];
      filters.labels = new Map();
      grouping.setHideOrgName(false);
    },
  };
}
