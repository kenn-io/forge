// The one definition of the PR, issue, and Activity filter menus. Each filter
// is an element built once here and bound to whichever list shows it, so the
// PR list, the issue list, and Activity offer the same filters on desktop and
// phone. Activity is built from the PR and issue elements plus its own extras:
// a filter added to `pullFilterSections` or `issueFilterSections` reaches
// Activity without further work, and its target type fails to compile until
// Activity can apply it. App.list-filter-parity checks every surface in the browser.
import type { FilterDropdownItem, FilterDropdownSection } from "@kenn-io/kit-ui";
import type { Snippet } from "svelte";

import type { GroupingStore } from "../../stores/grouping.svelte.js";
import type { IssuesStore } from "../../stores/issues.svelte.js";
import type { PullsStore } from "../../stores/pulls.svelte.js";
import { labelFilterSummary, type LabelFilters } from "../../utils/label-filter.js";
import { triFilterInSelection, type PullAttributeFilter } from "../../utils/pull-attributes.js";
import { cycleTriFilter, triFilterRow, type TriFilter } from "../../utils/tri-filter.js";

/** A filter row. The fields beyond kit's tell the phone renderer which control to use. */
export interface FilterChoice extends FilterDropdownItem {
  /** A three-way filter; the phone renders it as Any, Yes, and No. */
  tri?: { value: TriFilter; set: (value: TriFilter) => void };
  /** Turns the row off. Rows without it, such as pick-one options, reset with their section. */
  reset?: () => void;
  /** The row is on by default, like Activity's event types; turning it off is the filter. */
  defaultOn?: boolean;
}

export interface FilterSection extends FilterDropdownSection {
  items: FilterChoice[];
  /**
   * The items are mutually exclusive, so the phone renders one select. A
   * required section always has one choice on and resets to `defaultId`; an
   * optional one offers Any, which runs `clear`.
   */
  pick?: { defaultId?: string; clear?: () => void };
}

/** A pick-one section the desktop sidebar also shows as toolbar toggles. */
export interface ToolbarSection extends FilterSection {
  items: (FilterChoice & { shortLabel: string })[];
}

export interface ListFilterMenu {
  state: ToolbarSection;
  group: ToolbarSection;
  /** The Starred row, which the desktop search bar also shows as a star button. */
  starred: FilterChoice;
  /** State and group. */
  listSections: ToolbarSection[];
  /** Every other filter and view option. */
  filterSections: FilterSection[];
  /** Every section, for a surface with a single filter menu. */
  sections: FilterSection[];
  /** Whether any choice differs from the default view. */
  changed: boolean;
  /** How many filterSections choices are on, for a badge. */
  filterCount: number;
  /** Restores the default view. */
  reset: () => void;
  /** Turns off every filterSections choice. */
  clearFilters: () => void;
}

interface Value<T> {
  get: () => T;
  set: (value: T) => void;
}

export type ListState = "open" | "closed" | "all";

/** What every list must expose for the filters the PR list, issue list, and Activity share. */
export interface ListFilterTarget {
  state: Value<string>;
  starred: Value<boolean>;
  involvesMe: Value<TriFilter>;
  unassigned: Value<TriFilter>;
  labels: { filters: LabelFilters; clear: () => void; menu: Snippet };
  /** Reloads the list after a filter the server applies changes. */
  reload: () => void;
}

/** What a list must expose to offer the PR filters. Activity implements it too. */
export interface PullFilterTarget extends ListFilterTarget {
  attributes: {
    get: () => readonly PullAttributeFilter[];
    toggle: (filter: PullAttributeFilter) => void;
    cycle: (filter: PullAttributeFilter, opposite: PullAttributeFilter) => void;
    /** Replaces the selection in one step; `changed` names the filter that moved. */
    set: (next: PullAttributeFilter[], changed: PullAttributeFilter) => void;
    clear: () => void;
  };
}

/** What a list must expose to offer the issue filters. Activity implements it too. */
export interface IssueFilterTarget extends ListFilterTarget {
  /** Null when the synced providers cannot say which issues a PR references. */
  referencedByPR: Value<TriFilter> | null;
  hideBotAuthors: Value<boolean>;
}

const LIST_STATES = [
  { value: "open", label: "Open", shortLabel: "Open" },
  { value: "closed", label: "Closed", shortLabel: "Closed" },
  { value: "all", label: "All states", shortLabel: "All" },
] as const;

export function toggleChoice(id: string, label: string, value: Value<boolean>, after?: () => void): FilterChoice {
  const active = value.get();
  return {
    id,
    label,
    active,
    reset: () => {
      if (value.get()) value.set(false);
    },
    onSelect: () => {
      // Read the store, not the value this row was built with, so quick repeated clicks step correctly.
      value.set(!value.get());
      after?.();
    },
  };
}

export function triChoice(id: string, label: string, value: Value<TriFilter>, after?: () => void): FilterChoice {
  const current = value.get();
  const choose = (next: TriFilter) => {
    value.set(next);
    after?.();
  };
  return {
    id,
    label,
    ...triFilterRow(current),
    tri: { value: current, set: choose },
    reset: () => value.set(undefined),
    onSelect: () => choose(cycleTriFilter(value.get())),
  };
}

function stateSection(target: ListFilterTarget, defaultState: ListState = "open"): ToolbarSection {
  const current = target.state.get();
  return {
    title: "State",
    pick: { defaultId: `state-${defaultState}` },
    items: LIST_STATES.map((state) => ({
      id: `state-${state.value}`,
      label: state.label,
      shortLabel: state.shortLabel,
      active: current === state.value,
      onSelect: () => {
        target.state.set(state.value);
        target.reload();
      },
    })),
  };
}

/** Repository grouping. Short labels name the desktop toolbar toggles. */
export function groupSection(
  grouping: GroupingStore,
  shortLabels: { byRepo: string; flat: string } = { byRepo: "Repo", flat: "All" },
): ToolbarSection {
  return {
    title: "Group",
    pick: { defaultId: "group-byRepo" },
    items: [
      { id: "group-byRepo", label: "By repo", shortLabel: shortLabels.byRepo, byRepo: true },
      { id: "group-flat", label: "Flat list", shortLabel: shortLabels.flat, byRepo: false },
    ].map(({ id, label, shortLabel, byRepo }) => ({
      id,
      label,
      shortLabel,
      active: grouping.getGroupByRepo() === byRepo,
      onSelect: () => grouping.setGroupByRepo(byRepo),
    })),
  };
}

export function hideOrgNameChoice(grouping: GroupingStore): FilterChoice {
  return toggleChoice("hide-org-name", "Hide org name", {
    get: grouping.getHideOrgName,
    set: grouping.setHideOrgName,
  });
}

/** Labels, Starred, Involves me, and Unassigned: the filters every list offers. */
function commonSection(target: ListFilterTarget): FilterSection {
  const labels = target.labels.filters;
  return {
    title: "Filters",
    items: [
      {
        id: "labels",
        label: "Labels",
        detail: labelFilterSummary(labels),
        active: labels.size > 0,
        submenu: target.labels.menu,
        reset: target.labels.clear,
        onSelect: () => {},
      },
      toggleChoice("starred", "Starred", target.starred, target.reload),
      triChoice("involves-me", "Involves me", target.involvesMe, target.reload),
      triChoice("unassigned", "Unassigned", target.unassigned, target.reload),
    ],
  };
}

// Every selected filter must match. CI and Roborev are pick-one pairs; a condition row
// cycles through off, required, and its opposite.
const PULL_CONDITIONS: { value: PullAttributeFilter; opposite: PullAttributeFilter; label: string }[] = [
  { value: "draft", opposite: "not_draft", label: "Draft" },
  { value: "approved", opposite: "not_approved", label: "Approved" },
  { value: "merge_conflicts", opposite: "no_merge_conflicts", label: "Merge conflicts" },
  { value: "merge_queued", opposite: "not_merge_queued", label: "Merge queued" },
  { value: "has_workspace", opposite: "no_workspace", label: "Has workspace" },
  { value: "fork", opposite: "not_fork", label: "From fork" },
];

const PULL_PICK_ONE: { title: string; options: { value: PullAttributeFilter; label: string }[] }[] = [
  {
    title: "CI",
    options: [
      { value: "passed_ci", label: "Passing CI" },
      { value: "failed_ci", label: "Failed CI" },
    ],
  },
  {
    title: "Roborev",
    options: [
      { value: "roborev_findings", label: "Roborev findings" },
      { value: "roborev_clean", label: "No roborev findings" },
    ],
  },
];

/** The PR filters. The PR list and Activity both show exactly these. */
export function pullFilterSections(target: PullFilterTarget): FilterSection[] {
  const { attributes } = target;
  const selected = attributes.get();
  return [
    {
      title: "Pull requests",
      items: PULL_CONDITIONS.map((condition) => {
        const value = triFilterInSelection(selected, condition.value, condition.opposite);
        const set = (next: TriFilter) => {
          const others = attributes
            .get()
            .filter((filter) => filter !== condition.value && filter !== condition.opposite);
          const chosen = next === "require" ? [condition.value] : next === "exclude" ? [condition.opposite] : [];
          attributes.set([...others, ...chosen], condition.value);
        };
        return {
          id: condition.value === "has_workspace" ? "has-workspace" : `pr-${condition.value}`,
          label: condition.label,
          ...triFilterRow(value),
          tri: { value, set },
          reset: () => set(undefined),
          onSelect: () => attributes.cycle(condition.value, condition.opposite),
        };
      }),
    },
    ...PULL_PICK_ONE.map((section) => ({
      title: section.title,
      pick: {
        clear: () => {
          for (const option of section.options)
            if (attributes.get().includes(option.value)) attributes.toggle(option.value);
        },
      },
      items: section.options.map((option) => ({
        id: `pr-${option.value}`,
        label: option.label,
        active: selected.includes(option.value),
        onSelect: () => attributes.toggle(option.value),
      })),
    })),
  ];
}

/** The issue filters. The issue list and Activity both show exactly these. */
export function issueFilterSections(target: IssueFilterTarget): FilterSection[] {
  return [
    {
      title: "Issues",
      items: [
        ...(target.referencedByPR
          ? [triChoice("referenced-by-pr", "Referenced by PR", target.referencedByPR, target.reload)]
          : []),
        toggleChoice("hide-bot-authored-issues", "Hide bot-authored issues", target.hideBotAuthors, target.reload),
      ],
    },
  ];
}

/** Rows that count toward the badge: every filter row that is on, and every non-default pick-one choice. */
export function countFilters(sections: FilterSection[]): number {
  return sections.flatMap((section) =>
    section.pick?.defaultId !== undefined
      ? section.items.filter((item) => item.active && item.id !== section.pick?.defaultId)
      : section.items.filter((item) => item.excluded || item.active !== (item.defaultOn ?? false)),
  ).length;
}

/** Turns every row and optional pick-one section off, and returns required sections to their default. */
export function resetSections(sections: FilterSection[]): void {
  for (const section of sections) {
    if (section.pick?.defaultId !== undefined) {
      const fallback = section.items.find((item) => item.id === section.pick?.defaultId);
      if (fallback && !fallback.active) fallback.onSelect();
    } else if (section.pick) {
      section.pick.clear?.();
    } else {
      for (const item of section.items) item.reset?.();
    }
  }
}

function listMenu(
  state: ToolbarSection,
  group: ToolbarSection,
  starred: FilterChoice,
  filters: FilterSection[],
  view: FilterSection,
  reload: () => void,
): ListFilterMenu {
  const listSections = [state, group];
  const filterSections = [...filters, view];
  const clearFilters = () => {
    resetSections(filterSections);
    reload();
  };
  const filterCount = countFilters(filterSections);
  return {
    state,
    group,
    starred,
    listSections,
    filterSections,
    // The filters come first, then layout, then View, as in the Activity menu.
    sections: [state, ...filters, group, view],
    changed: filterCount > 0 || countFilters(listSections) > 0,
    filterCount,
    reset: () => {
      resetSections(listSections);
      clearFilters();
    },
    clearFilters,
  };
}

interface MenuDeps {
  grouping: GroupingStore;
  /** Content of the Labels row's flyout. */
  labelMenu: Snippet;
  /** Reloads the list with the surface's own query parameters. */
  reload: () => void;
}

/** The PR list filter menu. Read it inside a `$derived` so it tracks the stores. */
export function pullListFilterMenu({
  pulls,
  grouping,
  labelMenu,
  reload,
}: MenuDeps & { pulls: PullsStore }): ListFilterMenu {
  const target: PullFilterTarget = {
    state: { get: pulls.getFilterState, set: pulls.setFilterState },
    starred: { get: pulls.getFilterStarred, set: pulls.setFilterStarred },
    involvesMe: { get: pulls.getInvolvesMe, set: pulls.setInvolvesMe },
    unassigned: { get: pulls.getUnassigned, set: pulls.setUnassigned },
    labels: { filters: pulls.getLabelFilters(), clear: pulls.clearLabelFilters, menu: labelMenu },
    attributes: {
      get: pulls.getAttributeFilters,
      toggle: pulls.toggleAttributeFilter,
      cycle: pulls.cycleAttributeFilter,
      set: pulls.setAttributeFilters,
      clear: pulls.clearAttributeFilters,
    },
    reload,
  };
  const common = commonSection(target);
  const starred = common.items.find((item) => item.id === "starred")!;
  return listMenu(
    stateSection(target),
    groupSection(grouping),
    starred,
    [common, ...pullFilterSections(target)],
    {
      title: "View",
      items: [
        // Stacks arrange PR rows; Activity rows are events, so it has no stack layout.
        toggleChoice("stack-tree", "Stack tree", { get: pulls.getStackTree, set: pulls.setStackTree }),
        hideOrgNameChoice(grouping),
      ],
    },
    reload,
  );
}

/** The issue list filter menu. Read it inside a `$derived` so it tracks the stores. */
export function issueListFilterMenu({
  issues,
  grouping,
  labelMenu,
  reload,
}: MenuDeps & { issues: IssuesStore }): ListFilterMenu {
  const target: IssueFilterTarget = {
    state: { get: issues.getIssueFilterState, set: issues.setIssueFilterState },
    starred: { get: issues.getIssueFilterStarred, set: issues.setIssueFilterStarred },
    involvesMe: { get: issues.getInvolvesMe, set: issues.setInvolvesMe },
    unassigned: { get: issues.getUnassigned, set: issues.setUnassigned },
    labels: { filters: issues.getLabelFilters(), clear: issues.clearLabelFilters, menu: labelMenu },
    referencedByPR: issues.canFilterReferencedByPR()
      ? { get: issues.getReferencedByPR, set: issues.setReferencedByPR }
      : null,
    hideBotAuthors: { get: issues.getHideBots, set: issues.setHideBots },
    reload,
  };
  const common = commonSection(target);
  const starred = common.items.find((item) => item.id === "starred")!;
  return listMenu(
    stateSection(target),
    groupSection(grouping, { byRepo: "By Repo", flat: "All" }),
    starred,
    [common, ...issueFilterSections(target)],
    { title: "View", items: [hideOrgNameChoice(grouping)] },
    reload,
  );
}

export { commonSection, stateSection };
