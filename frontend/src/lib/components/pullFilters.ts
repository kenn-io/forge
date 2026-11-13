import type { PullsStore } from "../stores/pulls.svelte.js";
import { triFilterInSelection, type PullAttributeFilter } from "../utils/pull-attributes.js";
import { cycleTriFilter, triFilterRow, type TriFilter } from "../utils/tri-filter.js";

/**
 * The pull request filters the PR lists and Activity share: State, Involves me, Unassigned, the
 * pull request conditions, and CI. Each view keeps the values in its own store; this module owns
 * the names, order, and behavior, so a change here reaches every desktop and phone filter menu.
 */

/** An on/off or three-state filter: a menu row on desktop, a toggle or choice on phones. */
export interface FilterRow {
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

/** A pick-one choice: each option is a row on desktop and a select option on phones. */
export interface ChoiceFilter<T extends string> {
  id: string;
  label: string;
  value: T;
  /** The value Reset filters restores; any other value counts as an active filter. */
  defaultValue: T;
  options: { value: T; label: string }[];
  set: (value: T) => void;
  /** Chooses an option, or turns the filter off when an option with an off value is picked again. */
  choose: (value: T) => void;
  /** Phone select choices, including an off choice when the desktop rows have none. */
  selectOptions: { value: string; label: string }[];
  select: (value: string) => void;
}

export function choiceFilter<T extends string>(
  choice: Omit<ChoiceFilter<T>, "value" | "choose" | "selectOptions" | "select"> & { get: () => T },
  offLabel?: string,
): ChoiceFilter<T> {
  return {
    ...choice,
    value: choice.get(),
    choose: (value) => choice.set(offLabel !== undefined && choice.get() === value ? choice.defaultValue : value),
    selectOptions:
      offLabel === undefined ? choice.options : [{ value: choice.defaultValue, label: offLabel }, ...choice.options],
    select: (value) =>
      choice.set(choice.options.find((option) => option.value === value)?.value ?? choice.defaultValue),
  };
}

/** The desktop menu rows for a pick-one choice. */
export function choiceItems<T extends string>(choice: ChoiceFilter<T>) {
  return choice.options.map((option) => ({
    id: `${choice.id}-${option.value}`,
    label: option.label,
    active: choice.value === option.value,
    onSelect: () => choice.choose(option.value),
  }));
}

/** Phone choices for a filter that can be required or excluded. */
export const TRI_FILTER_OPTIONS = [
  { value: "any", label: "Any" },
  { value: "require", label: "Yes" },
  { value: "exclude", label: "No" },
];

interface FilterSource<T> {
  get: () => T;
  set: (value: T) => void;
}

export type TriFilterSource = FilterSource<TriFilter>;

/** The source with `after`, such as a reload, run after each change. */
export function afterSet<T>(source: FilterSource<T>, after: () => void): FilterSource<T> {
  return {
    get: source.get,
    set: (value) => {
      source.set(value);
      after();
    },
  };
}

export function triFilter(id: string, label: string, source: TriFilterSource): FilterRow {
  const value = source.get();
  return {
    id,
    label,
    ...triFilterRow(value),
    tri: { value, set: source.set },
    reset: () => source.set(undefined),
    onSelect: () => source.set(cycleTriFilter(source.get())),
  };
}

export type PullStateFilter = "open" | "closed" | "all";

const STATE_OPTIONS: { value: PullStateFilter; label: string }[] = [
  { value: "open", label: "Open" },
  { value: "closed", label: "Closed" },
  { value: "all", label: "All states" },
];

export type PullCIFilter = "any" | "passed_ci" | "failed_ci";

const CI_OPTIONS: { value: PullCIFilter; label: string }[] = [
  { value: "passed_ci", label: "Passing CI" },
  { value: "failed_ci", label: "Failed CI" },
];

export interface PullCondition {
  id: string;
  label: string;
  value: PullAttributeFilter;
  opposite: PullAttributeFilter;
}

/** The pull request conditions, in menu order. */
export const PULL_CONDITIONS: readonly PullCondition[] = [
  { id: "draft", label: "Draft", value: "draft", opposite: "not_draft" },
  { id: "approved", label: "Approved", value: "approved", opposite: "not_approved" },
  { id: "merge-conflicts", label: "Merge conflicts", value: "merge_conflicts", opposite: "no_merge_conflicts" },
  { id: "merge-queued", label: "Merge queued", value: "merge_queued", opposite: "not_merge_queued" },
  { id: "has-workspace", label: "Has workspace", value: "has_workspace", opposite: "no_workspace" },
  { id: "from-fork", label: "From fork", value: "fork", opposite: "not_fork" },
];

/** A condition kept as a pair of attributes in a selection list. */
export function attributeConditionSource(
  get: () => readonly PullAttributeFilter[],
  set: (next: PullAttributeFilter[]) => void,
  { value, opposite }: PullCondition,
): TriFilterSource {
  return {
    get: () => triFilterInSelection(get(), value, opposite),
    set: (next) => {
      const others = get().filter((filter) => filter !== value && filter !== opposite);
      set(next === undefined ? others : [...others, next === "require" ? value : opposite]);
    },
  };
}

/** CI kept as one attribute in a selection list. */
export function attributeCISource(
  get: () => readonly PullAttributeFilter[],
  set: (next: PullAttributeFilter[]) => void,
): FilterSource<PullCIFilter> {
  return {
    get: () =>
      get().find((filter): filter is "passed_ci" | "failed_ci" => filter === "passed_ci" || filter === "failed_ci") ??
      "any",
    set: (value) => {
      const others = get().filter((filter) => filter !== "passed_ci" && filter !== "failed_ci");
      set(value === "any" ? others : [...others, value]);
    },
  };
}

/** Where a view keeps the shared pull request filters, and what a change reloads. */
export interface PullFilterSource {
  state: FilterSource<PullStateFilter> & { defaultValue: PullStateFilter };
  involvesMe: TriFilterSource;
  unassigned: TriFilterSource;
  condition: (condition: PullCondition) => TriFilterSource;
  ci: FilterSource<PullCIFilter>;
}

export function pullFilterOptions(source: PullFilterSource) {
  return {
    state: choiceFilter<PullStateFilter>({ id: "state", label: "State", options: STATE_OPTIONS, ...source.state }),
    involvement: [
      triFilter("involves-me", "Involves me", source.involvesMe),
      triFilter("unassigned", "Unassigned", source.unassigned),
    ],
    conditions: PULL_CONDITIONS.map((condition) =>
      triFilter(condition.id, condition.label, source.condition(condition)),
    ),
    ci: choiceFilter<PullCIFilter>(
      { id: "ci", label: "CI", options: CI_OPTIONS, defaultValue: "any", ...source.ci },
      "Any",
    ),
  };
}

/**
 * The shared filters over the pull request list store. `reload` runs after a State, Involves me, or
 * Unassigned change; the store reloads on its own when a condition or CI change alters the request.
 */
export function pullListFilterOptions(pulls: PullsStore, reload: () => void) {
  const attributes = [pulls.getAttributeFilters, pulls.setAttributeFilters] as const;
  return pullFilterOptions({
    state: {
      ...afterSet<PullStateFilter>(
        {
          get: () => {
            const state = pulls.getFilterState();
            return state === "closed" || state === "all" ? state : "open";
          },
          set: pulls.setFilterState,
        },
        reload,
      ),
      defaultValue: "open",
    },
    involvesMe: afterSet({ get: pulls.getInvolvesMe, set: pulls.setInvolvesMe }, reload),
    unassigned: afterSet({ get: pulls.getUnassigned, set: pulls.setUnassigned }, reload),
    // The store reloads itself when a server-matched condition changes the request.
    condition: (condition) => attributeConditionSource(...attributes, condition),
    ci: attributeCISource(...attributes),
  });
}

export type PullFilters = ReturnType<typeof pullFilterOptions>;
