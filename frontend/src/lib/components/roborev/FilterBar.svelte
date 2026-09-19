<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getStores } from "../../context.js";
  import { Checkbox, FilterDropdown, SearchInput } from "@kenn-io/kit-ui";
  import RepoTreePicker from "./RepoTreePicker.svelte";

  interface Props {
    disabled?: boolean;
    onRepositoryChange?: () => void;
  }
  let { disabled = false, onRepositoryChange }: Props = $props();

  const stores = getStores();
  const jobsStore = stores.roborevJobs;
  const runtime = getAppRuntime();

  const statusOptions = [
    { value: "", label: "All statuses" },
    { value: "queued", label: "Queued" },
    { value: "running", label: "Running" },
    { value: "done", label: "Done" },
    { value: "failed", label: "Failed" },
    { value: "canceled", label: "Canceled" },
  ];

  let searchValue = $state(
    jobsStore?.getFilterSearch() ?? "",
  );
  let searchExecution: AppExecution<void, never> | undefined;

  function setStatusFilter(value: string): void {
    jobsStore?.setFilter("status", value || undefined);
  }

  const statusDetail = $derived.by(() => {
    const current = jobsStore?.getFilterStatus() ?? "";
    if (current === "") return undefined;
    return statusOptions.find(
      (opt) => opt.value === current,
    )?.label;
  });

  const statusSections = $derived.by(() => [
    {
      items: statusOptions.map((opt) => ({
        id: opt.value || "all-statuses",
        label: opt.label,
        active:
          (jobsStore?.getFilterStatus() ?? "") === opt.value,
        color:
          opt.value === "queued"
            ? "var(--accent-amber)"
            : opt.value === "running"
              ? "var(--accent-blue)"
              : opt.value === "done"
                ? "var(--accent-green)"
                : opt.value === "failed"
                  ? "var(--accent-red)"
                  : opt.value === "canceled"
                    ? "var(--text-muted)"
                    : "var(--accent-blue)",
        closeOnSelect: true,
        onSelect: () => setStatusFilter(opt.value),
      })),
    },
  ]);

  function onSearchInput(value: string): void {
    searchValue = value;
    searchExecution?.interrupt();
    searchExecution = runtime.runCommand(
      Effect.sleep("300 millis").pipe(
        Effect.andThen(
          Effect.sync(() => {
            jobsStore?.setFilter("search", value || undefined);
          }),
        ),
      ),
      {
        operation: "apply Roborev search filter",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  onDestroy(() => searchExecution?.interrupt());

  function onHideClosedChange(checked: boolean): void {
    jobsStore?.setFilter("hideClosed", checked);
  }

  function onShowAutoDesignChange(checked: boolean): void {
    jobsStore?.setFilter("showAutoDesign", checked);
  }
</script>

<div class="filter-bar">
  <div class:filter-disabled={disabled}>
    <RepoTreePicker onChange={onRepositoryChange} />
  </div>

  <FilterDropdown
    label="Status"
    active={(jobsStore?.getFilterStatus() ?? "") !== ""}
    showBadge={false}
    sections={statusSections}
    title="Filter reviews by status"
    minWidth="170px"
    {disabled}
    {...statusDetail ? { detail: statusDetail } : {}}
  />

  <div class="search-wrap">
    <SearchInput
      bind:value={searchValue}
      size="sm"
      block
      placeholder="Search by ref..."
      ariaLabel="Search by ref"
      oninput={onSearchInput}
      {disabled}
    />
  </div>

  <Checkbox
    class="filter-checkbox"
    checked={jobsStore?.getFilterHideClosed() ?? false}
    label="Hide closed"
    onchange={onHideClosedChange}
    {disabled}
  />

  <Checkbox
    class="filter-checkbox"
    checked={jobsStore?.getFilterShowAutoDesign() ?? false}
    label="Show auto-design"
    onchange={onShowAutoDesignChange}
    {disabled}
  />

</div>

<style>
  .filter-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-surface);
    flex-shrink: 0;
    flex-wrap: wrap;
  }

  .search-wrap {
    min-width: 140px;
    flex: 1;
    max-width: 220px;
  }

  :global(.filter-checkbox) {
    gap: var(--space-2);
    white-space: nowrap;
    user-select: none;
  }

  :global(.filter-checkbox .kit-checkbox__label) {
    color: var(--text-secondary);
  }

  .filter-disabled {
    pointer-events: none;
    opacity: 0.5;
  }
</style>
