// The one definition of the PR and issue list filter menus. The desktop
// sidebars and every focus and phone list render these menus; a surface must
// not add, drop, or rename a filter on its own. App.list-filter-parity checks
// that every surface shows the same items and narrows the list the same way.
import type { FilterDropdownItem, FilterDropdownSection } from "@kenn-io/kit-ui";
import type { Snippet } from "svelte";

import type { GroupingStore } from "../../stores/grouping.svelte.js";
import type { IssuesStore } from "../../stores/issues.svelte.js";
import type { PullsStore } from "../../stores/pulls.svelte.js";
import { labelFilterSummary, type LabelFilters } from "../../utils/label-filter.js";
import { triFilterInSelection, type PullAttributeFilter } from "../../utils/pull-attributes.js";
import { cycleTriFilter, triFilterRow, type TriFilter } from "../../utils/tri-filter.js";

/** A pick-one section the desktop sidebar also shows as toolbar toggles. */
export interface ToolbarSection extends FilterDropdownSection {
  items: (FilterDropdownItem & { shortLabel: string })[];
}

export interface ListFilterMenu {
  state: ToolbarSection;
  group: ToolbarSection;
  /** The Starred row, which the desktop search bar also shows as a star button. */
  starred: FilterDropdownItem;
  /** State and group. */
  listSections: ToolbarSection[];
  /** Every other filter and view option. */
  filterSections: FilterDropdownSection[];
  /** Every section, for a surface with a single filter menu. */
  sections: FilterDropdownSection[];
  /** Whether any choice differs from the default view. */
  changed: boolean;
  /** How many filterSections choices are on, for a badge. */
  filterCount: number;
  /** Restores the default view. */
  reset: () => void;
  /** Turns off every filterSections choice. */
  clearFilters: () => void;
}

interface MenuDeps {
  grouping: GroupingStore;
  /** Content of the Labels row's flyout. */
  labelMenu: Snippet;
  /** Reloads the list with the surface's own query parameters. */
  reload: () => void;
}

const LIST_STATES = [
  { value: "open", label: "Open", shortLabel: "Open" },
  { value: "closed", label: "Closed", shortLabel: "Closed" },
  { value: "all", label: "All states", shortLabel: "All" },
] as const;

function stateSection(current: string, select: (state: string) => void): ToolbarSection {
  return {
    title: "State",
    items: LIST_STATES.map((state) => ({
      id: `state-${state.value}`,
      label: state.label,
      shortLabel: state.shortLabel,
      active: current === state.value,
      onSelect: () => select(state.value),
    })),
  };
}

function labelsItem(filters: LabelFilters, labelMenu: Snippet) {
  return {
    id: "labels",
    label: "Labels",
    detail: labelFilterSummary(filters),
    active: filters.size > 0,
    submenu: labelMenu,
    onSelect: () => {},
  };
}

function starredItem(starred: boolean, set: (value: boolean) => void, reload: () => void): FilterDropdownItem {
  return {
    id: "starred",
    label: "Starred",
    active: starred,
    onSelect: () => {
      set(!starred);
      reload();
    },
  };
}

function triItem(id: string, label: string, value: TriFilter, set: (value: TriFilter) => void, reload: () => void) {
  return {
    id,
    label,
    ...triFilterRow(value),
    onSelect: () => {
      set(cycleTriFilter(value));
      reload();
    },
  };
}

function hideOrgNameItem(grouping: GroupingStore) {
  return {
    id: "hide-org-name",
    label: "Hide org name",
    active: grouping.getHideOrgName(),
    onSelect: () => grouping.setHideOrgName(!grouping.getHideOrgName()),
  };
}

// Every selected filter must match. Review and CI are pick-one pairs of opposites;
// a condition row cycles through off, required, and its opposite.
const PULL_CONDITIONS: { value: PullAttributeFilter; opposite: PullAttributeFilter; label: string }[] = [
  { value: "approved", opposite: "not_approved", label: "Approved" },
  { value: "merge_conflicts", opposite: "no_merge_conflicts", label: "Merge conflicts" },
  { value: "merge_queued", opposite: "not_merge_queued", label: "Merge queued" },
  { value: "has_workspace", opposite: "no_workspace", label: "Has workspace" },
];

const PULL_PICK_ONE: { title: string; options: { value: PullAttributeFilter; label: string }[] }[] = [
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
];

/** The PR list filter menu. Read it inside a `$derived` so it tracks the stores. */
export function pullListFilterMenu({
  pulls,
  grouping,
  labelMenu,
  reload,
}: MenuDeps & { pulls: PullsStore }): ListFilterMenu {
  const attributes = pulls.getAttributeFilters();
  const state = stateSection(pulls.getFilterState(), (value) => {
    pulls.setFilterState(value);
    reload();
  });
  const group: ToolbarSection = {
    title: "Group",
    items: [
      { id: "group-byRepo", label: "By repo", shortLabel: "Repo", value: "byRepo" as const },
      { id: "group-flat", label: "Flat list", shortLabel: "All", value: "flat" as const },
    ].map(({ id, label, shortLabel, value }) => ({
      id,
      label,
      shortLabel,
      active: grouping.getGroupingMode() === value,
      onSelect: () => grouping.setGroupingMode(value),
    })),
  };
  const listSections = [state, group];
  const starred = starredItem(pulls.getFilterStarred(), pulls.setFilterStarred, reload);
  const filterSections: FilterDropdownSection[] = [
    {
      title: "PR",
      items: [
        labelsItem(pulls.getLabelFilters(), labelMenu),
        starred,
        triItem("involves-me", "Involves me", pulls.getInvolvesMe(), pulls.setInvolvesMe, reload),
        triItem("unassigned", "Unassigned", pulls.getUnassigned(), pulls.setUnassigned, reload),
        ...PULL_CONDITIONS.map((condition) => ({
          id: condition.value === "has_workspace" ? "has-workspace" : `pr-${condition.value}`,
          label: condition.label,
          ...triFilterRow(triFilterInSelection(attributes, condition.value, condition.opposite)),
          onSelect: () => pulls.cycleAttributeFilter(condition.value, condition.opposite),
        })),
      ],
    },
    ...PULL_PICK_ONE.map((section) => ({
      title: section.title,
      items: section.options.map((option) => ({
        id: `pr-${option.value}`,
        label: option.label,
        active: attributes.includes(option.value),
        onSelect: () => pulls.toggleAttributeFilter(option.value),
      })),
    })),
    {
      title: "View",
      items: [
        {
          id: "stack-tree",
          label: "Stack tree",
          active: pulls.getStackTree(),
          onSelect: () => pulls.setStackTree(!pulls.getStackTree()),
        },
        hideOrgNameItem(grouping),
      ],
    },
  ];

  function clearFilters(): void {
    pulls.clearLocalFilters();
    pulls.setFilterStarred(false);
    pulls.setStackTree(false);
    grouping.setHideOrgName(false);
    reload();
  }

  const filterCount =
    pulls.getLocalFilterCount() +
    Number(pulls.getFilterStarred()) +
    Number(pulls.getStackTree()) +
    Number(grouping.getHideOrgName());
  return {
    state,
    group,
    starred,
    listSections,
    filterSections,
    sections: [...listSections, ...filterSections],
    changed: filterCount > 0 || pulls.getFilterState() !== "open" || grouping.getGroupingMode() !== "byRepo",
    filterCount,
    reset: () => {
      pulls.setFilterState("open");
      grouping.setGroupingMode("byRepo");
      clearFilters();
    },
    clearFilters,
  };
}

/** The issue list filter menu. Read it inside a `$derived` so it tracks the stores. */
export function issueListFilterMenu({
  issues,
  grouping,
  labelMenu,
  reload,
}: MenuDeps & { issues: IssuesStore }): ListFilterMenu {
  const state = stateSection(issues.getIssueFilterState(), (value) => {
    issues.setIssueFilterState(value);
    reload();
  });
  const group: ToolbarSection = {
    title: "Group",
    items: [
      { id: "group-byRepo", label: "By repo", shortLabel: "By Repo", byRepo: true },
      { id: "group-flat", label: "Flat list", shortLabel: "All", byRepo: false },
    ].map(({ id, label, shortLabel, byRepo }) => ({
      id,
      label,
      shortLabel,
      active: grouping.getGroupByRepo() === byRepo,
      onSelect: () => grouping.setGroupByRepo(byRepo),
    })),
  };
  const listSections = [state, group];
  const starred = starredItem(issues.getIssueFilterStarred(), issues.setIssueFilterStarred, reload);
  const filterSections: FilterDropdownSection[] = [
    {
      title: "Issue",
      items: [
        labelsItem(issues.getLabelFilters(), labelMenu),
        starred,
        triItem("involves-me", "Involves me", issues.getInvolvesMe(), issues.setInvolvesMe, reload),
        triItem("unassigned", "Unassigned", issues.getUnassigned(), issues.setUnassigned, reload),
        ...(issues.canFilterReferencedByPR()
          ? [
              triItem(
                "referenced-by-pr",
                "Referenced by PR",
                issues.getReferencedByPR(),
                issues.setReferencedByPR,
                reload,
              ),
            ]
          : []),
        {
          id: "hide-bot-authored-issues",
          label: "Hide bot-authored issues",
          active: issues.getHideBots(),
          onSelect: () => issues.setHideBots(!issues.getHideBots()),
        },
      ],
    },
    {
      title: "View",
      items: [hideOrgNameItem(grouping)],
    },
  ];

  function clearFilters(): void {
    issues.clearLabelFilters();
    issues.setIssueFilterStarred(false);
    issues.setInvolvesMe(undefined);
    issues.setUnassigned(undefined);
    issues.setReferencedByPR(undefined);
    // Hide bots is a saved server setting; skip the write when it is already off.
    if (issues.getHideBots()) issues.setHideBots(false);
    grouping.setHideOrgName(false);
    reload();
  }

  const filterCount =
    issues.getLabelFilters().size +
    Number(issues.getIssueFilterStarred()) +
    Number(issues.getInvolvesMe() !== undefined) +
    Number(issues.getUnassigned() !== undefined) +
    Number(issues.getReferencedByPR() !== undefined) +
    Number(issues.getHideBots()) +
    Number(grouping.getHideOrgName());
  return {
    state,
    group,
    starred,
    listSections,
    filterSections,
    sections: [...listSections, ...filterSections],
    changed: filterCount > 0 || issues.getIssueFilterState() !== "open" || !grouping.getGroupByRepo(),
    filterCount,
    reset: () => {
      issues.setIssueFilterState("open");
      grouping.setGroupByRepo(true);
      clearFilters();
    },
    clearFilters,
  };
}
