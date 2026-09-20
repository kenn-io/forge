<script lang="ts">
  import {
    Button,
    Chip,
    FilterDropdown,
    ScrollBox,
    SearchInput,
    SplitResizeHandle,
    type SplitResizeEvent,
  } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { onMount, untrack } from "svelte";
  import type { Attachment } from "svelte/attachments";

  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { workflowRepositoryKey, type WorkflowRepositoryRef } from "../../stores/workflow-actions.svelte.js";
  import { apiErrorMessage } from "../../api/runtime.js";
  import { getStores } from "../../context.js";
  import {
    normalizeSummaries,
    repoKey,
    repoStateKey,
    type RepoSummaryCard,
  } from "../repositories/repoSummary.js";
  import {
    RepoSummaryWorkflow,
    type RepoSummaryReadError,
  } from "../repositories/repo-summary-workflow.js";
  import { getGlobalRepo, parseRepoFilterValue } from "../../stores/filter.svelte.js";
  import GroupedSidebarSection from "../shared/GroupedSidebarSection.svelte";
  import {
    actionsPaneBounds,
    clampPaneWidth,
    fitPaneWidths,
    loadPaneWidths,
    savePaneWidths,
    type ActionsPane,
  } from "./actions-pane-widths.js";
  import { workflowStatusPresentation } from "./workflow-run-status.js";
  import WorkflowDispatchForm, { type WorkflowDispatchRequest } from "./WorkflowDispatchForm.svelte";
  import {
    workflowActionsErrorMessage,
    workflowDispatchPresentation,
  } from "./workflow-dispatch-presentation.js";
  import WorkflowRunList from "./WorkflowRunList.svelte";

  const runtime = getAppRuntime();
  const { workflowActions, events } = getStores();

  let summaries = $state.raw<RepoSummaryCard[]>([]);
  let summariesLoading = $state(true);
  let summariesError = $state<string | null>(null);
  let summaryExecution: AppExecution<void, never> | null = null;
  let selectedRepositoryKey = $state<string | null>(null);

  const filteredSummaries = $derived.by(() => {
    const selected = parseRepoFilterValue(getGlobalRepo());
    if (selected.length === 0) return summaries;
    const selectedKeys = new Set(selected);
    return summaries.filter((summary) => selectedKeys.has(repoStateKey(summary)));
  });
  const capableSummaries = $derived(filteredSummaries.filter(supportsWorkflowActions));
  const unsupportedSummaries = $derived(filteredSummaries.filter((summary) => !supportsWorkflowActions(summary)));
  const selectedSummary = $derived(
    capableSummaries.find((summary) => repoStateKey(summary) === selectedRepositoryKey)
      ?? capableSummaries[0]
      ?? null,
  );
  const selectedRef = $derived(selectedSummary ? workflowRef(selectedSummary) : null);
  const snapshot = $derived(selectedRef ? workflowActions.getSnapshot(selectedRef) : null);
  const catalog = $derived(snapshot?.catalog ?? null);
  const selectedWorkflow = $derived(snapshot?.selectedWorkflow ?? null);
  const showRepositoryRail = $derived(capableSummaries.length > 1 || unsupportedSummaries.length > 0);

  type RailSort = "name" | "activity";
  const railSortOptions: { value: RailSort; label: string }[] = [
    { value: "name", label: "Name" },
    { value: "activity", label: "Recent activity" },
  ];
  let railSort = $state<RailSort>("name");
  let railSearch = $state("");
  let collapsedGroups = $state<Record<string, boolean>>({});

  const railSortLabel = $derived(railSortOptions.find((option) => option.value === railSort)?.label ?? "Name");

  const railSortSections = $derived([
    {
      title: "Sorting",
      items: railSortOptions.map((option) => ({
        id: option.value,
        label: option.label,
        active: railSort === option.value,
        closeOnSelect: true,
        onSelect: () => { railSort = option.value; },
      })),
    },
  ]);

  function railOrder(list: RepoSummaryCard[]): RepoSummaryCard[] {
    const query = railSearch.trim().toLowerCase();
    const matches = query === ""
      ? list
      : list.filter((summary) => repoKey(summary).toLowerCase().includes(query));
    return [...matches].sort((a, b) =>
      (railSort === "activity"
        ? (b.most_recent_activity_at ?? "").localeCompare(a.most_recent_activity_at ?? "")
        : 0)
      || a.name.localeCompare(b.name));
  }

  function ownerLabel(summary: RepoSummaryCard): string {
    return repoKey(summary).slice(0, -(summary.name.length + 1));
  }

  const railRepositories = $derived(railOrder(capableSummaries));
  const railUnsupported = $derived(railOrder(unsupportedSummaries));
  // Owners only earn a group header when there is more than one to tell apart.
  const railGroups = $derived.by(() => {
    const groups: { label: string; items: RepoSummaryCard[] }[] = [];
    for (const summary of railRepositories) {
      const label = ownerLabel(summary);
      const group = groups.find((candidate) => candidate.label === label);
      if (group) group.items.push(summary);
      else groups.push({ label, items: [summary] });
    }
    return groups.sort((a, b) => a.label.localeCompare(b.label));
  });
  const groupByOwner = $derived(new Set(capableSummaries.map(ownerLabel)).size > 1);

  // Only repositories whose runs were loaded this session have a known state.
  function latestRunStatus(summary: RepoSummaryCard) {
    const ref = workflowRef(summary);
    const latest = ref ? workflowActions.getRuns(ref)[0] : undefined;
    return latest ? workflowStatusPresentation(latest.status, latest.conclusion) : null;
  }

  const sortedWorkflows = $derived(
    [...(catalog?.workflows ?? [])].sort((a, b) => Number(b.available) - Number(a.available)),
  );

  let requestedWidths = $state(loadPaneWidths());
  let layoutWidth = $state(0);
  let resizeStartWidth = 0;
  const visiblePanes = $derived<ActionsPane[]>(
    showRepositoryRail ? ["rail", "catalog", "dispatch"] : ["catalog", "dispatch"],
  );
  const paneWidths = $derived(fitPaneWidths(requestedWidths, layoutWidth, visiblePanes));

  function startResize(pane: ActionsPane): void {
    resizeStartWidth = paneWidths[pane];
  }

  function resize(pane: ActionsPane, event: SplitResizeEvent): void {
    requestedWidths[pane] = clampPaneWidth(pane, resizeStartWidth + event.delta);
  }

  function endResize(): void {
    requestedWidths = { ...paneWidths };
    savePaneWidths(requestedWidths);
  }

  function supportsWorkflowActions(summary: RepoSummaryCard): boolean {
    const capabilities = summary.repo.capabilities;
    return !!summary.repo.platform_repo_id
      && capabilities.read_workflows
      && capabilities.read_workflow_runs
      && capabilities.workflow_dispatch;
  }

  function workflowRef(summary: RepoSummaryCard): WorkflowRepositoryRef | null {
    const platformRepoId = summary.repo.platform_repo_id;
    if (!platformRepoId) return null;
    return {
      platformRepoId,
      provider: summary.repo.provider,
      platformHost: summary.repo.platform_host,
      owner: summary.repo.owner,
      name: summary.repo.name,
      repoPath: summary.repo.repo_path,
    };
  }

  function summaryFailureMessage(failure: RepoSummaryReadError): string {
    if (failure._tag === "ApiProblemError") {
      return apiErrorMessage(failure.problem, "Could not load repositories for Actions.");
    }
    return failure.cause instanceof Error
      ? failure.cause.message
      : "Could not load repositories for Actions.";
  }

  function loadSummaries(): void {
    summaryExecution?.interrupt();
    summariesLoading = true;
    summariesError = null;
    summaryExecution = runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* RepoSummaryWorkflow;
        return normalizeSummaries(yield* workflow.read);
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) => Effect.sync(() => {
            summariesError = summaryFailureMessage(failure);
            summariesLoading = false;
          }),
          onSuccess: (loaded) => Effect.sync(() => {
            summaries = loaded;
            summariesLoading = false;
          }),
        }),
      ),
      {
        operation: "actions.repositories.read",
        safeContext: { surface: "actions" },
        onFailure: () => undefined,
      },
    );
  }

  function selectRepository(summary: RepoSummaryCard): void {
    selectedRepositoryKey = repoStateKey(summary);
  }

  function selectWorkflow(workflowId: string): void {
    if (!selectedRef) return;
    workflowActions.selectWorkflow(selectedRef, workflowId);
  }

  function submitWorkflow(request: WorkflowDispatchRequest): void {
    if (!selectedRef || !selectedWorkflow) return;
    workflowActions.dispatch({
      ref: selectedRef,
      workflowId: selectedWorkflow.id,
      expectedDefinitionSha: selectedWorkflow.definition_sha,
      dispatchRef: request.ref,
      inputs: request.inputs,
    });
  }

  function reloadWorkflowCatalog(): void {
    if (!selectedRef || !selectedWorkflow) return;
    workflowActions.refreshCatalog(selectedRef, selectedWorkflow.id);
  }

  function newDispatchCycle(): void {
    if (!selectedRef || !selectedWorkflow) return;
    workflowActions.newDispatchCycle(selectedRef, selectedWorkflow.id);
  }

  function expandRun(runId: string): void {
    if (!selectedRef) return;
    workflowActions.loadJobs(selectedRef, runId);
  }

  onMount(() => {
    loadSummaries();
    return () => {
      summaryExecution?.interrupt();
    };
  });

  function loadSelectedCatalog(ref: WorkflowRepositoryRef | null): Attachment {
    return () => {
      if (!ref) return;
      untrack(() => workflowActions.loadCatalog(ref));
      return events.subscribeWorkspaceEvents((event) => {
        if (event.type !== "workflow_runs_changed") return;
        const identity = {
          provider: event.payload.provider,
          platformHost: event.payload.platform_host,
          platformRepoId: event.payload.platform_repo_id,
        };
        if (workflowRepositoryKey(identity) === workflowRepositoryKey(ref)) workflowActions.refreshRuns(ref);
      });
    };
  }

</script>

{#snippet repositoryRow(summary: RepoSummaryCard)}
  {@const selected = summary === selectedSummary}
  {@const latest = latestRunStatus(summary)}
  <button
    type="button"
    class="rail-row"
    class:selected
    aria-current={selected ? "true" : undefined}
    title={repoKey(summary)}
    onclick={() => selectRepository(summary)}
  >
    <span class="rail-row__name">{summary.name}</span>
    {#if !groupByOwner}<small class="rail-row__owner">{ownerLabel(summary)}</small>{/if}
    {#if latest}
      <span class="rail-row__status" data-tone={latest.tone} title={`Latest run: ${latest.label}`}>
        <span class="kit-sr-only">Latest run: {latest.label}</span>
      </span>
    {/if}
  </button>
{/snippet}

{#snippet paneHandle(pane: ActionsPane, label: string)}
  <SplitResizeHandle
    class="actions-resize-handle"
    ariaLabel={label}
    orientation="horizontal"
    ariaValueMin={actionsPaneBounds[pane].min}
    ariaValueMax={actionsPaneBounds[pane].max}
    ariaValueNow={paneWidths[pane]}
    onResizeStart={() => startResize(pane)}
    onResize={(event) => resize(pane, event)}
    onResizeEnd={endResize}
  />
{/snippet}

<section
  class="actions-page"
  aria-labelledby="actions-title"
  {@attach loadSelectedCatalog(selectedRef)}
>
  <header class="actions-page__header">
    <h1 id="actions-title">Actions</h1>
    {#if selectedSummary}
      <span class="actions-page__selection" title={selectedSummary.repo.repo_path}>
        {repoKey(selectedSummary)}
      </span>
    {/if}
  </header>

  {#if summariesLoading}
    <div class="actions-page__state" role="status">Loading workflow repositories…</div>
  {:else if summariesError}
    <div class="actions-page__state actions-page__state--error" role="alert">
      <span>{summariesError}</span>
      <Button size="sm" surface="soft" onclick={loadSummaries}>Retry</Button>
    </div>
  {:else if filteredSummaries.length === 0}
    <div class="actions-page__state">No repositories match the current repository filter.</div>
  {:else if capableSummaries.length === 0}
    <div class="actions-page__state">
      <strong>Workflow Actions unavailable</strong>
      <span>No repositories in the current filter support workflow catalogs, runs, and dispatch.</span>
      <ul class="unsupported-list">
        {#each unsupportedSummaries as summary (repoStateKey(summary))}
          <li><strong>{repoKey(summary)}</strong> does not support workflow Actions.</li>
        {/each}
      </ul>
    </div>
  {:else}
    <div
      class="actions-layout"
      class:actions-layout--with-rail={showRepositoryRail}
      bind:clientWidth={layoutWidth}
      style:--actions-rail-width={`${paneWidths.rail}px`}
      style:--actions-catalog-width={`${paneWidths.catalog}px`}
      style:--actions-dispatch-width={`${paneWidths.dispatch}px`}
    >
      {#if showRepositoryRail}
        <nav class="repository-rail" aria-label="Actions repositories">
          <header class="pane-heading">
            <h2>Repos</h2>
            <Chip size="xs" tone="muted" uppercase={false}>{capableSummaries.length}</Chip>
            <span class="pane-heading__tools">
              <FilterDropdown
                label={railSortLabel}
                showBadge={false}
                sections={railSortSections}
                title="Sort repositories"
                minWidth="180px"
                align="end"
                icon="sort"
              />
            </span>
          </header>
          <div class="rail-search">
            <SearchInput
              bind:value={railSearch}
              size="sm"
              block
              placeholder="Search repositories..."
              ariaLabel="Search Actions repositories"
            />
          </div>
          <ScrollBox label="Actions repositories">
            <div class="repository-list">
              {#if groupByOwner}
                {#each railGroups as group (group.label)}
                  <GroupedSidebarSection
                    label={group.label}
                    count={group.items.length}
                    collapsed={collapsedGroups[group.label] === true}
                    onclick={() => { collapsedGroups[group.label] = collapsedGroups[group.label] !== true; }}
                  >
                    {#each group.items as summary (repoStateKey(summary))}
                      {@render repositoryRow(summary)}
                    {/each}
                  </GroupedSidebarSection>
                {/each}
              {:else}
                {#each railRepositories as summary (repoStateKey(summary))}
                  {@render repositoryRow(summary)}
                {/each}
              {/if}
              {#if railRepositories.length === 0 && railUnsupported.length === 0}
                <p class="pane-state">No repositories match "{railSearch.trim()}".</p>
              {/if}
              {#if railUnsupported.length > 0}
                <GroupedSidebarSection
                  label="No workflow support"
                  count={railUnsupported.length}
                  collapsed={collapsedGroups.unsupported === true}
                  onclick={() => { collapsedGroups.unsupported = collapsedGroups.unsupported !== true; }}
                >
                  {#each railUnsupported as summary (repoStateKey(summary))}
                    <div
                      class="rail-row rail-row--unsupported"
                      aria-label={`${repoKey(summary)} does not support workflow Actions`}
                      title={`${repoKey(summary)} does not support workflow Actions`}
                      role="note"
                    >
                      <span class="rail-row__name">{summary.name}</span>
                      <small class="rail-row__owner">{ownerLabel(summary)}</small>
                    </div>
                  {/each}
                </GroupedSidebarSection>
              {/if}
            </div>
          </ScrollBox>
        </nav>
        {@render paneHandle("rail", "Resize Actions repositories")}
      {/if}

      <section class="workflow-catalog" aria-labelledby="workflow-list-title">
        <header class="pane-heading">
          <h2 id="workflow-list-title">Workflows</h2>
          {#if catalog}<Chip size="xs" tone="muted" uppercase={false}>{catalog.workflows?.length ?? 0}</Chip>{/if}
        </header>
        <ScrollBox label="Manual workflows">
          {#if snapshot?.loading.catalog || !snapshot}
            <p class="pane-state" role="status">Loading workflows…</p>
          {:else if snapshot.error && !catalog}
            <div class="pane-state pane-state--error" role="alert">
              <p>Could not load workflows.</p>
              <Button size="sm" surface="soft" onclick={() => selectedRef && workflowActions.loadCatalog(selectedRef)}>Retry workflows</Button>
            </div>
          {:else if sortedWorkflows.length === 0}
            <p class="pane-state">No manual workflows are available.</p>
          {:else}
            <ul class="workflow-list">
              {#each sortedWorkflows as workflow (workflow.id)}
                <li>
                  <button
                    type="button"
                    class="rail-row workflow-row"
                    class:selected={selectedWorkflow?.id === workflow.id}
                    class:workflow-row--unavailable={!workflow.available}
                    aria-current={selectedWorkflow?.id === workflow.id ? "true" : undefined}
                    onclick={() => selectWorkflow(workflow.id)}
                  >
                    <span class="rail-row__name">{workflow.name}</span>
                    <code class="workflow-row__path" title={workflow.path}>{workflow.path}</code>
                    {#if !workflow.available}
                      <span class="workflow-row__flag" title={workflow.unavailable_reason || "Unavailable"}>
                        <Chip size="xs" tone="muted" uppercase={false}>Unavailable</Chip>
                      </span>
                    {/if}
                  </button>
                </li>
              {/each}
            </ul>
          {/if}
        </ScrollBox>
      </section>
      {@render paneHandle("catalog", "Resize workflow list")}

      <div class="workflow-workspace">
        <section class="dispatch-pane" aria-labelledby="dispatch-title">
          <header class="pane-heading"><h2 id="dispatch-title">Dispatch</h2></header>
          <ScrollBox label="Workflow dispatch form">
            <div class="pane-content">
              {#if selectedWorkflow && selectedRef}
                {#key workflowRepositoryKey(selectedRef)}
                  <WorkflowDispatchForm
                    workflow={selectedWorkflow}
                    environments={snapshot?.catalog?.environments ?? []}
                    initialRef={snapshot?.catalog?.repo.default_branch ?? ""}
                    operation={snapshot?.catalog?.repo.operations?.dispatch_workflow}
                    state={workflowDispatchPresentation(snapshot, selectedWorkflow.id)}
                    onsubmit={submitWorkflow}
                    reloading={snapshot?.loading.catalog ?? false}
                    onreload={reloadWorkflowCatalog}
                    onnewcycle={newDispatchCycle}
                  />
                {/key}
              {:else}
                <p class="pane-state pane-state--flush">Select a workflow to configure a manual run.</p>
              {/if}
            </div>
          </ScrollBox>
        </section>
        {@render paneHandle("dispatch", "Resize dispatch form")}

        <section class="runs-pane" aria-labelledby="runs-title">
          <header class="pane-heading">
            <h2 id="runs-title">Recent runs</h2>
            {#if snapshot}<Chip size="xs" tone="muted" uppercase={false}>{snapshot.runs.length}</Chip>{/if}
          </header>
          <ScrollBox label="Recent workflow runs">
            {#if snapshot?.error}
              <div class="workflow-data-error" role="alert">
                <strong>Workflow data may be stale.</strong>
                <span>{workflowActionsErrorMessage(snapshot.error, "Workflow data could not be refreshed.")}</span>
              </div>
            {/if}
            {#if snapshot?.loading.runs && snapshot.runs.length === 0}
              <p class="pane-state" role="status">Loading runs…</p>
            {:else if !snapshot?.error && (snapshot?.runs.length ?? 0) === 0}
              <p class="pane-state">No recent workflow runs.</p>
            {:else if selectedRef && snapshot}
              <WorkflowRunList
                runs={snapshot.runs}
                jobs={snapshot.jobs}
                jobErrors={snapshot.jobErrors}
                loadingJobs={snapshot.loading.jobs}
                onexpand={expandRun}
              />
              {#if snapshot.runsPage.nextCursor && !snapshot.runsPage.exhausted}
                <div class="runs-pagination">
                  <Button
                    size="sm"
                    surface="soft"
                    disabled={snapshot.runsPage.loadingMore}
                    onclick={() => workflowActions.loadMoreRuns(selectedRef)}
                  >
                    {snapshot.runsPage.loadingMore ? "Loading more runs…" : "Load more runs"}
                  </Button>
                </div>
              {/if}
            {/if}
          </ScrollBox>
        </section>
      </div>
    </div>
  {/if}
</section>

<style>
  .actions-page {
    flex: 1;
    min-height: 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
    background: var(--bg-primary);
  }

  .actions-page__header {
    min-height: 36px;
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-5);
    padding: 0 var(--space-5);
    border-block-end: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  h1,
  h2,
  p {
    margin: 0;
  }

  h1 {
    font-size: var(--font-size-md);
    font-weight: 650;
    line-height: 1.25;
  }

  .actions-page__selection {
    max-width: min(50vw, 480px);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .actions-page__state {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--space-4);
    padding: var(--space-7);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .actions-page__state--error,
  .pane-state--error {
    color: var(--status-danger-text, var(--text-danger));
  }

  .unsupported-list {
    display: grid;
    gap: var(--space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .actions-layout,
  .workflow-workspace {
    flex: 1;
    min-height: 0;
    min-width: 0;
    display: flex;
  }

  .repository-rail,
  .workflow-catalog,
  .dispatch-pane,
  .runs-pane {
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    background: var(--bg-surface);
  }

  .repository-rail {
    flex: 0 0 var(--actions-rail-width);
    background: var(--sidebar-list-bg);
  }

  .workflow-catalog {
    flex: 0 0 var(--actions-catalog-width);
    background: var(--sidebar-list-bg);
  }

  .dispatch-pane {
    flex: 0 0 var(--actions-dispatch-width);
  }

  .runs-pane {
    flex: 1 1 0;
  }

  .pane-heading {
    min-height: 34px;
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: 0 var(--space-4) 0 var(--space-5);
    border-block-end: 1px solid var(--border-default);
    background: var(--bg-inset);
  }

  .pane-heading h2 {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 650;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }

  .pane-heading__tools {
    margin-inline-start: auto;
    display: inline-flex;
  }

  .rail-search {
    flex: 0 0 auto;
    padding: var(--space-3) var(--space-4);
    border-block-end: 1px solid var(--sidebar-list-border-muted);
  }

  .repository-list,
  .workflow-list {
    display: grid;
  }

  .workflow-list {
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .workflow-list li {
    min-width: 0;
  }

  .rail-row {
    box-sizing: border-box;
    width: 100%;
    min-width: 0;
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: 0 var(--space-3);
    padding: var(--space-3) var(--space-4) var(--space-3) var(--space-5);
    border: 0;
    border-block-end: 1px solid var(--sidebar-list-border-muted);
    background: var(--sidebar-row-bg);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: start;
  }

  button.rail-row {
    cursor: pointer;
  }

  button.rail-row:hover,
  button.rail-row.selected:hover {
    background: var(--sidebar-row-hover-bg);
  }

  button.rail-row.selected {
    background: var(--bg-row-selected);
    box-shadow: inset var(--chrome-active-accent-width) 0 0 var(--accent-blue);
  }

  .rail-row__name,
  .rail-row__owner,
  .workflow-row__path {
    grid-column: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .rail-row__name {
    font-weight: 550;
  }

  .rail-row__owner,
  .workflow-row__path {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .workflow-row__path {
    font-family: var(--font-mono);
  }

  .rail-row__status,
  .workflow-row__flag {
    grid-column: 2;
    grid-row: 1;
    display: inline-flex;
  }

  .workflow-row__flag {
    grid-row: 1 / span 2;
  }

  .rail-row__status {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--text-muted);
  }

  .rail-row__status[data-tone="success"] { background: var(--accent-green); }
  .rail-row__status[data-tone="danger"] { background: var(--accent-red); }
  .rail-row__status[data-tone="warning"] { background: var(--accent-amber); }
  .rail-row__status[data-tone="info"] { background: var(--accent-blue); }

  .rail-row--unsupported .rail-row__name,
  .workflow-row--unavailable .rail-row__name {
    color: var(--text-secondary);
    font-weight: 450;
  }

  .pane-content {
    padding: var(--space-5);
  }

  .pane-state {
    padding: var(--space-5);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .pane-state--flush {
    padding: 0;
  }

  .pane-state--error {
    display: grid;
    justify-items: start;
    gap: var(--space-3);
  }

  .runs-pagination {
    display: flex;
    justify-content: center;
    padding: var(--space-4);
  }

  .workflow-data-error {
    display: grid;
    gap: var(--space-1);
    margin: var(--space-4) var(--space-5);
    padding: var(--space-3) var(--space-4);
    border: 1px solid color-mix(in srgb, var(--accent-red) 45%, var(--border-default));
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent-red) 8%, var(--bg-surface));
    color: var(--status-danger-text, var(--text-danger));
    font-size: var(--font-size-sm);
  }

  /* Below the split threshold the panes stack, so the dividers stand down. */
  @media (max-width: 900px) {
    .actions-layout {
      display: grid;
      grid-template-columns: 200px minmax(0, 1fr);
      grid-template-rows: minmax(0, 1fr);
    }

    .actions-layout :global(.actions-resize-handle) {
      display: none;
    }

    .repository-rail,
    .workflow-catalog {
      border-inline-end: 1px solid var(--border-default);
    }

    .actions-layout--with-rail {
      grid-template-rows: minmax(150px, 0.7fr) minmax(0, 1.3fr);
    }

    .actions-layout--with-rail .repository-rail {
      grid-row: 1 / 3;
    }

    .actions-layout--with-rail .workflow-catalog {
      border-inline-end: 0;
      border-block-end: 1px solid var(--border-default);
    }

    .workflow-workspace {
      flex-direction: column;
    }

    .dispatch-pane {
      flex: 0 1 auto;
      max-height: 45%;
      border-block-end: 1px solid var(--border-default);
    }
  }

  @media (max-width: 640px) {
    .actions-page__header {
      padding: 0 var(--space-4);
    }

    .actions-layout {
      display: flex;
      flex-direction: column;
      overflow: auto;
    }

    .repository-rail,
    .workflow-catalog,
    .dispatch-pane,
    .runs-pane {
      flex: 0 0 auto;
      max-height: none;
      min-height: 200px;
      border-inline-end: 0;
      border-block-end: 1px solid var(--border-default);
    }

    .repository-rail,
    .workflow-catalog {
      max-height: 40vh;
    }

    .workflow-workspace {
      display: contents;
    }

    .dispatch-pane,
    .runs-pane {
      min-height: 260px;
    }
  }
</style>
