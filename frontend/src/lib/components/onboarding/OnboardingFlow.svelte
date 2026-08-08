<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import type { Attachment } from "svelte/attachments";
  import {
    Button,
    Checkbox,
    SearchInput,
    Spinner,
    StatusDot,
    formatRelativeTime,
  } from "@kenn-io/kit-ui";
  import AlertTriangleIcon from "@lucide/svelte/icons/alert-triangle";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import CircleDashedIcon from "@lucide/svelte/icons/circle-dashed";
  import GitPullRequestIcon from "@lucide/svelte/icons/git-pull-request";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import TerminalIcon from "@lucide/svelte/icons/terminal";

  import {
    listUserRepositories,
    type DiscoveredUserRepository,
  } from "../../api/project-intake.ts";
  import type { PullRequest } from "../../api/types.js";
  import { createPullRequestWorkspace } from "../../api/onboarding.ts";
  import { bulkAddRepos } from "../../api/settings.ts";
  import { buildProviderPullRequestRoute } from "../../routes.js";
  import { navigate } from "../../stores/router.svelte.ts";
  import { resolveToolingStatus } from "../../stores/tooling-status.svelte.ts";
  import type { StoreInstances } from "../../types.js";
  import ProviderReadinessStep from "./ProviderReadinessStep.svelte";

  type Phase = "repos" | "sync" | "pulls" | "workspace";
  type StepId = "auth" | Phase;

  interface Props {
    stores: StoreInstances;
    iconSrc: string;
    onStart: () => void;
    onDismiss: () => void;
    onComplete: () => void;
  }

  const steps: Array<{ id: StepId; label: string }> = [
    { id: "auth", label: "Code forge" },
    { id: "repos", label: "Choose repos" },
    { id: "sync", label: "First sync" },
    { id: "pulls", label: "Open a PR" },
    { id: "workspace", label: "Start workspace" },
  ];

  let { stores, iconSrc, onStart, onDismiss, onComplete }: Props = $props();
  let phase = $state<Phase>(
    untrack(() => stores.settings.hasConfiguredRepos()) ? "sync" : "repos",
  );
  let hadConfiguredRepos = untrack(() => stores.settings.hasConfiguredRepos());
  let headingEl: HTMLHeadingElement | undefined;

  let repositories = $state.raw<DiscoveredUserRepository[]>([]);
  let repositoryFilter = $state("");
  let selectedRepositories = $state<string[]>([]);
  let repositoriesLoading = $state(false);
  let repositoryError = $state<string | null>(null);
  let providerConfirmed = $state(false);
  let ghVerified = $state(false);
  let toolingRetrying = $state(false);
  let toolingRetryError = $state<string | null>(null);
  let repoLoadStarted = false;

  let configureBusy = $state(false);
  let configureError = $state<string | null>(null);
  let syncStartIssued = $state(false);
  let syncFinishing = $state(false);
  let syncError = $state<string | null>(null);

  let availablePulls = $state.raw<PullRequest[]>([]);
  let selectedPull = $state<PullRequest | null>(null);
  let pullsLoading = $state(false);
  let pullsError = $state<string | null>(null);
  let workspaceBusy = $state(false);
  let workspaceError = $state<string | null>(null);

  const tooling = $derived(resolveToolingStatus());
  const gh = $derived(tooling?.gh);
  const hasConfiguredRepos = $derived(stores.settings.hasConfiguredRepos());
  const ghReady = $derived(
    hasConfiguredRepos
      || ghVerified
      || (gh?.available === true && gh.authenticated === true),
  );
  const activeStep = $derived<StepId>(
    hasConfiguredRepos || providerConfirmed ? phase : "auth",
  );
  const syncState = $derived(stores.sync.getSyncState());
  const visibleRepositories = $derived.by(() => {
    const query = repositoryFilter.trim().toLowerCase();
    if (!query) return repositories;
    return repositories.filter((repository) =>
      repository.name_with_owner.toLowerCase().includes(query),
    );
  });
  const selectedRepositoryRows = $derived(
    repositories.filter((repository) =>
      selectedRepositories.includes(repository.name_with_owner),
    ),
  );

  const captureHeading: Attachment<HTMLHeadingElement> = (node) => {
    headingEl = node;
    return () => {
      if (headingEl === node) headingEl = undefined;
    };
  };

  function errorMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }

  function selectedCountLabel(): string {
    return `${selectedRepositories.length} ${
      selectedRepositories.length === 1 ? "repository" : "repositories"
    }`;
  }

  function stepState(id: StepId): "done" | "active" | "next" {
    const order = steps.map((step) => step.id);
    const stepIndex = order.indexOf(id);
    const activeIndex = order.indexOf(activeStep);
    return stepIndex < activeIndex
      ? "done"
      : stepIndex === activeIndex
        ? "active"
        : "next";
  }

  async function moveTo(next: Phase): Promise<void> {
    phase = next;
    await tick();
    headingEl?.focus();
  }

  async function loadRepositories(): Promise<boolean> {
    if (repositoriesLoading) return false;
    repoLoadStarted = true;
    repositoriesLoading = true;
    repositoryError = null;
    try {
      repositories = await listUserRepositories({
        provider: "github",
        platformHost: gh?.host || "github.com",
      });
      const available = new Set(
        repositories.map((repository) => repository.name_with_owner),
      );
      selectedRepositories = selectedRepositories.filter((name) =>
        available.has(name),
      );
      return true;
    } catch (error) {
      repositoryError = errorMessage(error);
      return false;
    } finally {
      repositoriesLoading = false;
    }
  }

  function retryRepositoryLoad(): void {
    repoLoadStarted = false;
    void loadRepositories();
  }

  function toggleRepository(name: string, checked: boolean): void {
    selectedRepositories = checked
      ? [...selectedRepositories, name]
      : selectedRepositories.filter((candidate) => candidate !== name);
  }

  function splitRepositoryPath(repoPath: string): {
    owner: string;
    name: string;
  } {
    const parts = repoPath.split("/").filter(Boolean);
    return {
      owner: parts.slice(0, -1).join("/"),
      name: parts.at(-1) ?? "",
    };
  }

  async function configureRepositories(): Promise<void> {
    if (configureBusy || selectedRepositoryRows.length === 0) return;
    configureBusy = true;
    configureError = null;
    try {
      const settings = await bulkAddRepos(
        selectedRepositoryRows.map((repository) => {
          const identity = splitRepositoryPath(repository.name_with_owner);
          return {
            provider: repository.provider,
            host: repository.platform_host,
            owner: identity.owner,
            name: identity.name,
            repo_path: repository.name_with_owner,
          };
        }),
      );
      stores.settings.setConfiguredRepos(settings.repos);
      await moveTo("sync");
      void startSync();
    } catch (error) {
      configureError = errorMessage(error);
    } finally {
      configureBusy = false;
    }
  }

  async function startSync(): Promise<void> {
    if (syncStartIssued) return;
    syncStartIssued = true;
    syncError = null;
    try {
      await stores.sync.triggerSync();
      const state = stores.sync.getSyncState();
      if (!state?.running) {
        if (state?.last_error) syncError = state.last_error;
        else if (state?.last_run_at) await finishSync();
      }
    } catch (error) {
      syncError = errorMessage(error);
    }
  }

  function retrySync(): void {
    syncStartIssued = false;
    syncError = null;
    void startSync();
  }

  async function loadAvailablePulls(): Promise<void> {
    pullsLoading = true;
    pullsError = null;
    try {
      await stores.pulls.loadPulls();
      const loadError = stores.pulls.getError();
      if (loadError) {
        pullsError = loadError;
        availablePulls = [];
        selectedPull = null;
      } else {
        availablePulls = stores.pulls
          .getPulls()
          .filter((pull) => pull.State === "open")
          .slice(0, 8);
        selectedPull = availablePulls[0] ?? null;
      }
    } catch (error) {
      pullsError = errorMessage(error);
    } finally {
      pullsLoading = false;
    }
  }

  async function finishSync(): Promise<void> {
    if (syncFinishing) return;
    syncFinishing = true;
    await loadAvailablePulls();
    syncFinishing = false;
    await moveTo("pulls");
  }

  async function retryPullLoad(): Promise<void> {
    await loadAvailablePulls();
  }

  function selectPull(pull: PullRequest): void {
    selectedPull = pull;
    workspaceError = null;
  }

  function pullRoute(pull: PullRequest): string {
    return buildProviderPullRequestRoute({
      provider: pull.repo.provider,
      platformHost: pull.repo.platform_host,
      repoPath: pull.repo.repo_path,
      number: pull.Number,
    });
  }

  function openPullView(pull: PullRequest): void {
    onDismiss();
    navigate(pullRoute(pull));
  }

  function finishWithoutPull(): void {
    onDismiss();
    navigate("/pulls");
  }

  function configureAnotherProvider(): void {
    navigate("/settings");
  }

  async function continueWithGitHub(): Promise<void> {
    providerConfirmed = true;
    await tick();
    headingEl?.focus();
  }

  function openSelectedPullView(): void {
    if (selectedPull) openPullView(selectedPull);
  }

  async function startWorkspace(): Promise<void> {
    const pull = selectedPull;
    if (!pull || workspaceBusy) return;
    workspaceBusy = true;
    workspaceError = null;
    try {
      const workspace = pull.workspace?.id
        ? { id: pull.workspace.id, status: pull.workspace.status }
        : await createPullRequestWorkspace(pull);
      onComplete();
      navigate(`/terminal/${encodeURIComponent(workspace.id)}`);
    } catch (error) {
      workspaceError = errorMessage(error);
    } finally {
      workspaceBusy = false;
    }
  }

  async function retryTooling(): Promise<void> {
    if (toolingRetrying) return;
    toolingRetrying = true;
    toolingRetryError = null;
    const verified = await loadRepositories();
    if (verified) {
      ghVerified = true;
      providerConfirmed = true;
      await tick();
      headingEl?.focus();
    }
    else toolingRetryError = repositoryError;
    toolingRetrying = false;
  }

  function ciLabel(pull: PullRequest): string {
    const status = pull.CIStatus.trim().toLowerCase();
    if (status === "success" || status === "passed") return "CI passed";
    if (status === "failure" || status === "failed" || status === "error") {
      return "CI failed";
    }
    if (pull.ReviewDecision) return pull.ReviewDecision.replaceAll("_", " ");
    return "Open";
  }

  $effect(() => {
    if (phase !== "repos" || !providerConfirmed || !ghReady || repoLoadStarted) return;
    void loadRepositories();
  });

  $effect(() => {
    const configured = hasConfiguredRepos;
    const becameConfigured = configured && !hadConfiguredRepos;
    hadConfiguredRepos = configured;
    if (!becameConfigured || phase !== "repos") return;
    void moveTo("sync").then(startSync);
  });

  onMount(() => {
    onStart();
    const unsubscribe = stores.sync.subscribeSyncComplete(() => {
      if (phase !== "sync") return;
      const state = stores.sync.getSyncState();
      if (state?.last_error) syncError = state.last_error;
      else void finishSync();
    });
    if (phase === "sync") void startSync();
    return unsubscribe;
  });
</script>

<section class="onboarding" aria-label="Kenn Forge first-run setup">
  <header class="onboarding-bar">
    <div class="wordmark">
      <img src={iconSrc} alt="" aria-hidden="true" />
      <span>kenn-forge</span>
    </div>
    <button type="button" class="quiet-action" onclick={onDismiss}>
      I’ll do this later
    </button>
  </header>

  <div class="onboarding-layout">
    <aside class="progress" aria-label="Setup progress">
      <p class="progress-label">SETUP</p>
      <ol>
        {#each steps as step, index (step.id)}
          {@const state = stepState(step.id)}
          <li
            class:active={state === "active"}
            class:done={state === "done"}
            aria-label={`${step.label}: ${state === "done" ? "complete" : state === "active" ? "current" : "upcoming"}`}
            aria-current={state === "active" ? "step" : undefined}
          >
            <span class="step-icon" aria-hidden="true">
              {#if state === "done"}
                <CircleCheckIcon size={16} />
              {:else if state === "active"}
                <CircleDashedIcon size={16} />
              {:else}
                <span>{index + 1}</span>
              {/if}
            </span>
            <span>{step.label}</span>
          </li>
        {/each}
      </ol>

      <div class="identity" class:identity-ready={ghReady || hasConfiguredRepos}>
        {#if hasConfiguredRepos}
          <CircleCheckIcon size={16} aria-hidden="true" />
          <div><strong>Code forge configured</strong><span>Repositories ready</span></div>
        {:else if tooling === undefined}
          <Spinner size={14} label="Checking code forge tooling" />
          <div><strong>Checking code forge</strong><span>Local tooling</span></div>
        {:else if ghReady}
          <CircleCheckIcon size={16} aria-hidden="true" />
          <div>
            <strong>Code forge available</strong>
            <span>{gh?.host || "github.com"}{gh?.user ? ` · @${gh.user}` : ""}</span>
          </div>
        {:else}
          <AlertTriangleIcon size={16} aria-hidden="true" />
          <div>
            <strong>Code forge setup needed</strong>
            <span>Choose a provider to continue</span>
          </div>
        {/if}
      </div>
    </aside>

    <main class="onboarding-main">
      {#if activeStep === "auth"}
        <div class="step-heading">
          <p class="step-number">STEP 1 OF 5</p>
          <h1 {@attach captureHeading} tabindex="-1">Connect a code forge</h1>
          <p>Choose how Kenn Forge should reach the repositories you maintain. Credentials stay on this host.</p>
        </div>
        <ProviderReadinessStep
          {tooling}
          retrying={toolingRetrying}
          retryError={toolingRetryError}
          onContinueGitHub={() => void continueWithGitHub()}
          onCheckAgain={() => void retryTooling()}
          onOpenSettings={configureAnotherProvider}
        />
      {:else if phase === "repos"}
        <div class="step-heading">
          <p class="step-number">STEP 2 OF 5</p>
          <h1 {@attach captureHeading} tabindex="-1">Choose the repositories you maintain</h1>
          <p>Kenn Forge found these through your existing <code>gh</code> session. Start small; you can add more later.</p>
        </div>

        <label class="repo-search">
          <span>Filter repositories</span>
          <SearchInput
            bind:value={repositoryFilter}
            block
            autofocus
            ariaLabel="Filter repositories"
            placeholder="owner or repository"
          />
        </label>

        <div class="repo-list" aria-label="GitHub repositories" aria-busy={repositoriesLoading}>
          {#if repositoriesLoading}
            <div class="center-state" role="status"><Spinner size={16} /> Loading repositories</div>
          {:else if repositoryError}
            <div class="center-state center-state--error" role="alert">
              <span>{repositoryError}</span>
              <Button size="sm" onclick={retryRepositoryLoad}>Retry</Button>
            </div>
          {:else}
            {#each visibleRepositories as repository (repository.name_with_owner)}
              {@const selected = selectedRepositories.includes(repository.name_with_owner)}
              <Checkbox
                class={selected ? "repo-row selected" : "repo-row"}
                checked={selected}
                onchange={(checked) => toggleRepository(repository.name_with_owner, checked)}
              >
                <span class="repo-copy">
                  <strong>{repository.name_with_owner}</strong>
                  <span>{repository.default_branch ? `Default branch: ${repository.default_branch}` : "GitHub repository"}</span>
                </span>
              </Checkbox>
            {/each}
            {#if visibleRepositories.length === 0}
              <p class="no-results">
                {repositories.length === 0
                  ? "No repositories are available to this gh account."
                  : `No repositories match “${repositoryFilter}”.`}
              </p>
            {/if}
          {/if}
        </div>

        {#if configureError}
          <p class="inline-error" role="alert">{configureError}</p>
        {/if}
        <div class="step-actions">
          <span>{selectedCountLabel()} selected</span>
          <div class="repo-actions">
            <Button onclick={configureAnotherProvider}>Configure repositories in Settings</Button>
            <Button
              tone="info"
              surface="solid"
              disabled={selectedRepositories.length === 0 || configureBusy}
              onclick={() => void configureRepositories()}
            >
              {#if configureBusy}<Spinner size={13} />{/if}
              Configure {selectedCountLabel()}
            </Button>
          </div>
        </div>
      {:else if phase === "sync"}
        <div class="step-heading compact-heading">
          <p class="step-number">STEP 3 OF 5</p>
          <h1 {@attach captureHeading} tabindex="-1">First sync is underway</h1>
          <p>Open pull requests arrive first. History and activity continue filling in afterward.</p>
        </div>

        <div class="sync-board" aria-live="polite">
          <div class="sync-summary">
            <span class="sync-glyph"><RefreshCwIcon size={18} /></span>
            <div>
              <strong>{syncState?.running ? "Syncing configured repositories" : "Preparing first sync"}</strong>
              <span>{syncState?.progress || "Pull requests, issues, CI, and activity"}</span>
            </div>
            <StatusDot
              status={syncError ? "stale" : syncState?.running ? "working" : "idle"}
              label={syncError ? "Sync failed" : syncState?.running ? "Syncing" : "Starting"}
              size={7}
            />
          </div>
          <div class="progress-track" aria-hidden="true"><span class:complete={!syncState?.running}></span></div>
          <div class="sync-detail">
            <span>Current repository</span>
            <strong>{syncState?.current_repo || "Waiting for sync worker"}</strong>
          </div>
        </div>

        {#if syncError}
          <div class="recovery" role="alert">
            <div><strong>Sync needs attention</strong><span>{syncError}</span></div>
            <Button size="sm" onclick={retrySync}>Retry sync</Button>
          </div>
        {:else}
          <p class="sync-note">This step advances as soon as the first sync completes.</p>
        {/if}
      {:else if phase === "pulls"}
        <div class="step-heading compact-heading">
          <p class="step-number">STEP 4 OF 5</p>
          <h1 {@attach captureHeading} tabindex="-1">Open a pull request</h1>
          <p>Choose one synced item to continue into its isolated workspace.</p>
        </div>

        <div class="pulls-shell" aria-busy={pullsLoading}>
          <div class="pulls-title-row"><span>Open pull requests</span><span>{availablePulls.length}</span></div>
          {#if pullsLoading}
            <div class="center-state" role="status"><Spinner size={16} /> Loading pull requests</div>
          {:else if pullsError}
            <div class="center-state center-state--error" role="alert">
              <span>{pullsError}</span>
              <Button size="sm" onclick={() => void retryPullLoad()}>Retry</Button>
            </div>
          {:else}
            {#each availablePulls as pull (pull.ID)}
              <button
                class="pull-row"
                class:selected-pull={selectedPull?.ID === pull.ID}
                type="button"
                aria-pressed={selectedPull?.ID === pull.ID}
                onclick={() => selectPull(pull)}
              >
                <GitPullRequestIcon size={16} aria-hidden="true" />
                <span>
                  <strong>{pull.Title}</strong>
                  <small>{pull.repo.repo_path} #{pull.Number} · updated {formatRelativeTime(pull.UpdatedAt)}</small>
                </span>
                <span class:ci-ok={ciLabel(pull) === "CI passed"}>{ciLabel(pull)}</span>
              </button>
            {/each}
            {#if availablePulls.length === 0}
              <div class="center-state empty-pulls">
                <GitPullRequestIcon size={20} aria-hidden="true" />
                <strong>No open pull requests yet</strong>
                <span>The repositories are configured and will keep syncing in the background.</span>
              </div>
            {/if}
          {/if}
        </div>

        <div class="step-actions step-actions--end">
          {#if selectedPull}
            <Button tone="info" surface="solid" onclick={() => void moveTo("workspace")}>
              Continue with PR #{selectedPull.Number}
            </Button>
          {:else if !pullsLoading && !pullsError}
            <Button tone="info" surface="solid" onclick={finishWithoutPull}>Open pull request view</Button>
          {/if}
        </div>
      {:else}
        <div class="step-heading compact-heading">
          <p class="step-number">STEP 5 OF 5</p>
          <h1 {@attach captureHeading} tabindex="-1">Start your first workspace</h1>
          <p>Kenn Forge creates an isolated worktree and keeps its terminal beside the pull request.</p>
        </div>

        {#if selectedPull}
          <div class="workspace-preview">
            <div class="workspace-header">
              <div class="terminal-mark"><TerminalIcon size={18} /></div>
              <div>
                <strong>{selectedPull.repo.repo_path} · PR #{selectedPull.Number}</strong>
                <span>{selectedPull.HeadBranch}</span>
              </div>
              <span class="ready-label">
                {selectedPull.workspace?.status?.toUpperCase() || "TO CREATE"}
              </span>
            </div>
            <div class="workspace-context">
              <span>Pull request</span>
              <strong>{selectedPull.Title}</strong>
              <span>Workspace branch</span>
              <code>{selectedPull.HeadBranch}</code>
            </div>
            {#if workspaceError}
              <p class="inline-error workspace-error" role="alert">{workspaceError}</p>
            {/if}
            <div class="launch-actions">
              <Button
                tone="info"
                surface="solid"
                disabled={workspaceBusy}
                onclick={() => void startWorkspace()}
              >
                {#if workspaceBusy}<Spinner size={13} />{:else}<TerminalIcon size={14} aria-hidden="true" />{/if}
                {selectedPull.workspace?.id ? "Open workspace" : "Create workspace"}
              </Button>
              <Button onclick={openSelectedPullView}>Open PR first</Button>
            </div>
          </div>
        {/if}
      {/if}
    </main>
  </div>
</section>

<style>
  .onboarding {
    min-height: 100dvh;
    display: grid;
    grid-template-rows: 48px minmax(0, 1fr);
    color: var(--text-primary);
    background: var(--bg-primary);
  }

  .onboarding-bar {
    padding: 0 var(--space-6);
    display: flex;
    align-items: center;
    justify-content: space-between;
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-surface);
  }

  .wordmark {
    display: inline-flex;
    align-items: center;
    gap: var(--space-3);
    font-size: var(--font-size-md);
    font-weight: 650;
  }

  .wordmark img {
    width: 22px;
    height: 22px;
  }

  .quiet-action {
    padding: var(--space-2);
    border: 0;
    background: none;
    color: var(--text-muted);
    cursor: pointer;
    font: inherit;
    font-size: var(--font-size-sm);
  }

  .quiet-action:hover {
    color: var(--text-primary);
  }

  .quiet-action:focus-visible,
  .pull-row:focus-visible {
    outline: var(--focus-ring);
    outline-offset: 2px;
  }

  .onboarding-layout {
    min-height: 0;
    display: grid;
    grid-template-columns: 220px minmax(0, 1fr);
  }

  .progress {
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding: var(--space-7) var(--space-6);
    border-right: 1px solid var(--border-muted);
    background: var(--bg-inset);
  }

  .progress-label,
  .step-number {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    letter-spacing: 0.09em;
  }

  .progress ol {
    display: grid;
    gap: var(--space-5);
    margin: var(--space-6) 0 0;
    padding: 0;
    list-style: none;
  }

  .progress li {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .progress li.active {
    color: var(--text-primary);
    font-weight: 650;
  }

  .progress li.done {
    color: var(--text-secondary);
  }

  .step-icon {
    width: 18px;
    height: 18px;
    display: grid;
    flex: 0 0 auto;
    place-items: center;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .done .step-icon,
  .identity-ready {
    color: var(--accent-green);
  }

  .active .step-icon {
    color: var(--accent-blue);
  }

  .identity {
    margin-top: auto;
    display: flex;
    gap: var(--space-3);
    align-items: flex-start;
    color: var(--accent-yellow);
  }

  .identity div {
    min-width: 0;
    display: grid;
    gap: var(--space-1);
  }

  .identity strong,
  .identity span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-xs);
  }

  .identity strong {
    color: var(--text-secondary);
  }

  .identity span {
    color: var(--text-muted);
  }

  .onboarding-main {
    width: min(100%, 780px);
    margin: 0 auto;
    padding: var(--space-8) var(--space-7);
    box-sizing: border-box;
    overflow: auto;
  }

  .step-heading {
    display: grid;
    gap: var(--space-3);
    margin-bottom: var(--space-6);
  }

  .compact-heading {
    margin-bottom: var(--space-7);
  }

  .step-heading h1 {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-2xl);
    line-height: 1.2;
    letter-spacing: -0.025em;
  }

  .step-heading h1:focus {
    outline: none;
  }

  .step-heading p:last-child {
    max-width: 65ch;
    margin: 0;
    color: var(--text-secondary);
    line-height: 1.55;
  }

  code {
    font-family: var(--font-mono);
  }

  .recovery {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-5);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-surface);
  }

  .recovery div {
    display: grid;
    gap: var(--space-2);
  }

  .recovery strong {
    color: var(--text-primary);
  }

  .recovery span {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .repo-search {
    display: grid;
    gap: var(--space-3);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .repo-list,
  .pulls-shell,
  .sync-board,
  .workspace-preview {
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-surface);
  }

  .repo-list {
    max-height: 330px;
    margin-top: var(--space-5);
    overflow-y: auto;
  }

  .repo-list :global(.kit-checkbox.repo-row) {
    width: 100%;
    min-height: 54px;
    box-sizing: border-box;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--space-4);
    align-items: center;
    padding: var(--space-4) var(--space-5);
    border-bottom: 1px solid var(--border-muted);
  }

  .repo-list :global(.kit-checkbox.repo-row:last-child) {
    border-bottom: 0;
  }

  .repo-list :global(.kit-checkbox.repo-row.selected) {
    background: color-mix(in srgb, var(--accent-blue) 8%, var(--bg-surface));
  }

  .repo-list :global(.repo-row .kit-checkbox__label) {
    min-width: 0;
  }

  .repo-copy {
    min-width: 0;
    display: grid;
    gap: var(--space-2);
  }

  .repo-copy strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
  }

  .repo-copy span,
  .no-results,
  .sync-note,
  .inline-error {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .center-state {
    min-height: 120px;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    padding: var(--space-7);
    box-sizing: border-box;
    color: var(--text-muted);
    text-align: center;
  }

  .center-state--error,
  .empty-pulls {
    flex-direction: column;
  }

  .empty-pulls strong {
    color: var(--text-primary);
  }

  .empty-pulls span {
    max-width: 48ch;
    font-size: var(--font-size-sm);
  }

  .no-results {
    margin: 0;
    padding: var(--space-7);
    text-align: center;
  }

  .inline-error {
    margin: var(--space-4) 0 0;
    color: var(--accent-red);
  }

  .step-actions {
    margin-top: var(--space-5);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-5);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .step-actions--end {
    justify-content: flex-end;
  }

  .repo-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: var(--space-3);
  }

  .sync-summary {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    gap: var(--space-4);
    align-items: center;
    padding: var(--space-5);
  }

  .sync-glyph,
  .terminal-mark {
    width: 34px;
    height: 34px;
    display: grid;
    place-items: center;
    border-radius: var(--radius-md);
    background: color-mix(in srgb, var(--accent-blue) 12%, var(--bg-inset));
    color: var(--accent-blue);
  }

  .sync-summary div,
  .workspace-header > div:nth-child(2) {
    min-width: 0;
    display: grid;
    gap: var(--space-2);
  }

  .sync-summary span,
  .workspace-header span,
  .workspace-context > span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .progress-track {
    height: 3px;
    margin: 0 var(--space-5) var(--space-4);
    overflow: hidden;
    border-radius: 999px;
    background: var(--bg-inset);
  }

  .progress-track span {
    width: 58%;
    height: 100%;
    display: block;
    background: var(--accent-blue);
  }

  .progress-track span.complete {
    width: 100%;
  }

  .sync-detail {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--space-4);
    padding: var(--space-4) var(--space-5);
    border-top: 1px solid var(--border-muted);
    font-size: var(--font-size-sm);
  }

  .sync-detail span {
    color: var(--text-muted);
  }

  .sync-detail strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
  }

  .sync-note {
    margin: var(--space-4) 0 0;
    text-align: center;
  }

  .recovery {
    margin-top: var(--space-5);
    justify-content: space-between;
    border-color: color-mix(in srgb, var(--accent-red) 32%, var(--border-default));
  }

  .pulls-title-row {
    display: flex;
    justify-content: space-between;
    padding: var(--space-4) var(--space-5);
    border-bottom: 1px solid var(--border-muted);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
  }

  .pull-row {
    width: 100%;
    min-height: 62px;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    gap: var(--space-4);
    align-items: center;
    padding: var(--space-4) var(--space-5);
    border: 0;
    border-bottom: 1px solid var(--border-muted);
    background: transparent;
    color: var(--text-secondary);
    text-align: left;
    cursor: pointer;
    font: inherit;
  }

  .pull-row:last-child {
    border-bottom: 0;
  }

  .selected-pull {
    background: color-mix(in srgb, var(--accent-blue) 8%, var(--bg-surface));
    color: var(--accent-blue);
  }

  .pull-row > span:first-of-type {
    min-width: 0;
    display: grid;
    gap: var(--space-2);
  }

  .pull-row strong,
  .pull-row small {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pull-row strong {
    color: var(--text-primary);
  }

  .pull-row small,
  .pull-row > span:last-child {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .pull-row .ci-ok {
    color: var(--accent-green);
  }

  .workspace-header {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    gap: var(--space-4);
    align-items: center;
    padding: var(--space-5);
    border-bottom: 1px solid var(--border-muted);
  }

  .workspace-header strong,
  .workspace-header span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .ready-label {
    color: var(--accent-green) !important;
    font-weight: 800;
    letter-spacing: 0.08em;
  }

  .workspace-context {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--space-4);
    align-items: baseline;
    padding: var(--space-5);
  }

  .workspace-context strong,
  .workspace-context code {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .workspace-context code {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .workspace-error {
    margin: 0;
    padding: 0 var(--space-5) var(--space-4);
  }

  .launch-actions {
    display: flex;
    gap: var(--space-4);
    padding: 0 var(--space-5) var(--space-5);
  }

  @media (max-width: 760px) {
    .onboarding {
      min-height: 100dvh;
    }

    .onboarding-layout {
      grid-template-columns: 1fr;
      grid-template-rows: auto minmax(0, 1fr);
    }

    .progress {
      padding: var(--space-4);
      border-right: 0;
      border-bottom: 1px solid var(--border-muted);
    }

    .progress ol {
      grid-template-columns: repeat(5, 1fr);
      gap: var(--space-2);
      margin-top: 0;
    }

    .progress li {
      justify-content: center;
    }

    .progress li > span:last-child,
    .identity,
    .progress-label {
      display: none;
    }

    .onboarding-main {
      padding: var(--space-7) var(--space-5);
    }

    .step-actions,
    .repo-actions,
    .launch-actions,
    .recovery {
      align-items: stretch;
      flex-direction: column;
    }

    .pull-row {
      grid-template-columns: auto minmax(0, 1fr);
    }

    .pull-row > span:last-child {
      display: none;
    }
  }
</style>
