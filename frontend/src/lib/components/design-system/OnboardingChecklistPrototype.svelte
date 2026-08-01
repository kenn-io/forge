<script lang="ts">
  import { Card, Checkbox, IconButton } from "@kenn-io/kit-ui";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import CircleIcon from "@lucide/svelte/icons/circle";
  import CommandIcon from "@lucide/svelte/icons/command";
  import GitPullRequestIcon from "@lucide/svelte/icons/git-pull-request";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import TerminalIcon from "@lucide/svelte/icons/terminal";

  const repositoryOptions = ["acme/forge", "acme/docs", "acme/runtime"];
  const pullRequests = [
    {
      number: 248,
      title: "Keep workspace activity across reloads",
      repo: "acme/forge",
      meta: "CI passed · review requested",
    },
    {
      number: 91,
      title: "Clarify repository setup in quickstart",
      repo: "acme/docs",
      meta: "2 approvals · ready to merge",
    },
  ];

  let choosingRepositories = $state(false);
  let selectedRepositories = $state<string[]>(["acme/forge"]);
  let syncStarted = $state(false);
  let openedPull = $state<number | null>(null);
  let workspaceCreated = $state(false);

  function toggleRepository(repository: string): void {
    selectedRepositories = selectedRepositories.includes(repository)
      ? selectedRepositories.filter((candidate) => candidate !== repository)
      : [...selectedRepositories, repository];
  }

  function startSync(): void {
    if (selectedRepositories.length === 0) return;
    syncStarted = true;
    choosingRepositories = false;
  }
</script>

<section class="shell" aria-label="In-shell activation checklist prototype">
  <header class="topbar">
    <div class="brand"><span>k</span> kenn-forge</div>
    <nav aria-label="Mock primary navigation">
      <button type="button">Activity</button>
      <button type="button" class="active">PRs</button>
      <button type="button">Issues</button>
      <button type="button">Workspaces</button>
    </nav>
    <div class="topbar-actions">
      {#if syncStarted}
        <span class="syncing"><RefreshCwIcon size={13} /> Syncing</span>
      {/if}
      <IconButton size="sm" ariaLabel="Command palette"><CommandIcon size={15} /></IconButton>
    </div>
  </header>

  <div class="shell-body">
    <aside class="activation-rail">
      <div class="rail-heading">
        <p>GET STARTED</p>
        <span>{workspaceCreated ? "5 of 5" : syncStarted ? `${openedPull ? 4 : 3} of 5` : "1 of 5"}</span>
      </div>
      <h2>Your first useful session</h2>
      <p class="rail-copy">Set up in context. You can leave and resume anytime.</p>

      <ol class="checklist">
        <li class="complete">
          <CircleCheckIcon size={17} aria-hidden="true" />
          <div><strong>GitHub connected</strong><span>@maintainer via gh</span></div>
        </li>
        <li class:complete={syncStarted} class:current={!syncStarted}>
          {#if syncStarted}<CircleCheckIcon size={17} />{:else}<CircleIcon size={17} />{/if}
          <div>
            <strong>Choose repositories</strong>
            <span>{syncStarted ? `${selectedRepositories.length} tracked` : "Nothing tracked yet"}</span>
          </div>
        </li>
        <li class:complete={syncStarted} class:current={syncStarted && openedPull === null}>
          {#if syncStarted}<CircleCheckIcon size={17} />{:else}<CircleIcon size={17} />{/if}
          <div><strong>Run first sync</strong><span>{syncStarted ? "Open PRs ready" : "Starts automatically"}</span></div>
        </li>
        <li class:complete={openedPull !== null} class:current={syncStarted && openedPull === null}>
          {#if openedPull !== null}<CircleCheckIcon size={17} />{:else}<CircleIcon size={17} />{/if}
          <div><strong>Open a pull request</strong><span>{openedPull ? `PR #${openedPull}` : "See review context"}</span></div>
        </li>
        <li class:complete={workspaceCreated} class:current={openedPull !== null && !workspaceCreated}>
          {#if workspaceCreated}<CircleCheckIcon size={17} />{:else}<CircleIcon size={17} />{/if}
          <div><strong>Create a workspace</strong><span>{workspaceCreated ? "Shell ready" : "Worktree + terminal"}</span></div>
        </li>
      </ol>

      {#if !syncStarted}
        <button type="button" class="rail-primary" onclick={() => { choosingRepositories = true; }}>
          Choose repositories
        </button>
      {:else if openedPull === null}
        <p class="rail-hint">Open any pull request to continue.</p>
      {:else if !workspaceCreated}
        <p class="rail-hint">The workspace action is highlighted in the detail pane.</p>
      {:else}
        <div class="activation-complete">
          <CircleCheckIcon size={17} />
          <span>Activation complete</span>
        </div>
      {/if}

      <button type="button" class="dismiss">Hide this checklist</button>
    </aside>

    <main class="product-surface">
      {#if choosingRepositories}
        <section class="repo-chooser" aria-labelledby="checklist-repo-title">
          <p class="surface-kicker">REPOSITORIES</p>
          <h2 id="checklist-repo-title">Select repositories</h2>
          <p>Showing recently updated repositories from <code>gh api user/repos</code>.</p>
          <div class="choice-list">
            {#each repositoryOptions as repository (repository)}
              <Checkbox
                class="choice-row"
                checked={selectedRepositories.includes(repository)}
                onchange={() => toggleRepository(repository)}
              >
                <span><strong>{repository}</strong><small>Updated this week</small></span>
              </Checkbox>
            {/each}
          </div>
          <div class="chooser-actions">
            <button type="button" class="secondary" onclick={() => { choosingRepositories = false; }}>Cancel</button>
            <button type="button" class="primary" disabled={selectedRepositories.length === 0} onclick={startSync}>
              Add {selectedRepositories.length} and start sync
            </button>
          </div>
        </section>
      {:else if !syncStarted}
        <section class="empty-prs">
          <div class="empty-icon"><GitPullRequestIcon size={22} /></div>
          <h2>No repositories configured</h2>
          <p>
            Pull requests will appear here after you choose repositories.
            Kenn Forge can use the GitHub session already on this machine.
          </p>
          <div class="detected-row">
            <CircleCheckIcon size={15} />
            <span><strong>gh ready</strong> · github.com · @maintainer</span>
          </div>
          <button type="button" class="primary" onclick={() => { choosingRepositories = true; }}>
            <PlusIcon size={14} /> Add repositories
          </button>
        </section>
      {:else}
        <div class="pr-layout">
          <section class="pr-list" aria-label="Pull requests">
            <div class="list-toolbar">
              <div><strong>Pull requests</strong><span>{pullRequests.length}</span></div>
              <span class="sync-progress"><RefreshCwIcon size={12} /> First sync · 68%</span>
            </div>
            {#each pullRequests as pull (pull.number)}
              <button
                type="button"
                class:selected={openedPull === pull.number}
                class="pr-row"
                onclick={() => { openedPull = pull.number; workspaceCreated = false; }}
              >
                <GitPullRequestIcon size={15} aria-hidden="true" />
                <span>
                  <strong>{pull.title}</strong>
                  <small>{pull.repo} #{pull.number} · {pull.meta}</small>
                </span>
              </button>
            {/each}
          </section>

          <section class="pr-detail" aria-label="Pull request detail">
            {#if openedPull === null}
              <div class="detail-empty">
                <GitPullRequestIcon size={22} />
                <strong>Pick a pull request</strong>
                <span>Review context and workspace actions appear here.</span>
              </div>
            {:else}
              {@const pull = pullRequests.find((candidate) => candidate.number === openedPull)!}
              <div class="detail-header">
                <span class="detail-repo">{pull.repo} · #{pull.number}</span>
                <h3>{pull.title}</h3>
                <p>Open · maintainer wants to merge 4 commits into main</p>
              </div>
              <div class="detail-tabs"><span class="active">Overview</span><span>Files 6</span><span>Commits 4</span></div>
              <div class="detail-summary">
                <Card level="default" padding="sm" class="detail-summary-card">
                  <span>CI</span><strong class="ok">All checks passed</strong>
                </Card>
                <Card level="default" padding="sm" class="detail-summary-card">
                  <span>Review</span><strong>Requested from you</strong>
                </Card>
              </div>
              <div class="workspace-callout">
                <div>
                  <TerminalIcon size={17} />
                  <span><strong>{workspaceCreated ? "Workspace ready" : "Continue locally"}</strong><small>{workspaceCreated ? "Shell is available beside this PR" : "Create an isolated worktree for this branch"}</small></span>
                </div>
                <button type="button" class="primary" onclick={() => { workspaceCreated = true; }}>
                  {workspaceCreated ? "Open workspace" : "Create workspace"}
                </button>
              </div>
            {/if}
          </section>
        </div>
      {/if}
    </main>
  </div>
</section>

<style>
  .shell {
    min-height: 610px;
    color: var(--text-primary);
    background: var(--bg-primary);
    font-family: var(--font-sans, system-ui, sans-serif);
  }

  button { font: inherit; }
  button { cursor: pointer; }
  button:focus-visible { outline: 2px solid var(--accent-blue); outline-offset: 2px; }

  .topbar {
    min-height: 46px;
    display: grid;
    grid-template-columns: minmax(160px, 1fr) auto minmax(160px, 1fr);
    align-items: center;
    padding: 0 var(--space-4);
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-surface);
  }

  .brand { display: flex; align-items: center; gap: var(--space-3); font-weight: 700; font-size: var(--font-size-sm); }
  .brand > span { display: grid; width: 21px; height: 21px; place-items: center; border-radius: var(--radius-sm); background: var(--accent-blue); color: var(--bg-primary); font-family: var(--font-mono); }
  .topbar nav { display: flex; align-self: stretch; }
  .topbar nav button { padding: 0 var(--space-5); border: 0; border-bottom: 2px solid transparent; background: none; color: var(--text-muted); font-size: var(--font-size-sm); }
  .topbar nav button.active { border-bottom-color: var(--accent-blue); color: var(--text-primary); font-weight: 650; }
  .topbar-actions { display: flex; align-items: center; justify-content: flex-end; gap: var(--space-4); }
  .topbar-actions :global(.kit-icon-button) { color: var(--text-muted); }
  .syncing { display: inline-flex; align-items: center; gap: var(--space-2); color: var(--accent-blue); font-size: var(--font-size-xs); }

  .shell-body { min-height: 564px; display: grid; grid-template-columns: 264px minmax(0, 1fr); }
  .activation-rail { display: flex; flex-direction: column; padding: var(--space-6) var(--space-5); border-right: 1px solid var(--border-muted); background: var(--bg-inset); }
  .rail-heading { display: flex; justify-content: space-between; align-items: center; color: var(--text-muted); font-size: var(--font-size-xs); font-weight: 700; letter-spacing: .08em; }
  .rail-heading p { margin: 0; }
  .rail-heading span { font-family: var(--font-mono); letter-spacing: 0; }
  .activation-rail h2 { margin: var(--space-4) 0 0; font-size: var(--font-size-xl); line-height: 1.2; }
  .rail-copy { margin: var(--space-3) 0 0; color: var(--text-muted); font-size: var(--font-size-sm); line-height: 1.45; }

  .checklist { display: grid; gap: var(--space-4); margin: var(--space-6) 0 0; padding: 0; list-style: none; }
  .checklist li { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: var(--space-3); color: var(--text-muted); }
  .checklist li.current { color: var(--accent-blue); }
  .checklist li.complete { color: var(--accent-green); }
  .checklist div { display: grid; gap: var(--space-1); }
  .checklist strong { color: var(--text-secondary); font-size: var(--font-size-sm); }
  .checklist .current strong { color: var(--text-primary); }
  .checklist span { color: var(--text-muted); font-size: var(--font-size-xs); }
  .rail-primary { min-height: 32px; margin-top: var(--space-6); border: 1px solid var(--accent-blue); border-radius: var(--radius-md); background: var(--accent-blue); color: var(--bg-primary); font-weight: 650; }
  .rail-hint { margin: var(--space-6) 0 0; padding: var(--space-4); border: 1px solid var(--border-muted); border-radius: var(--radius-md); color: var(--text-secondary); font-size: var(--font-size-xs); line-height: 1.45; }
  .dismiss { margin-top: auto; border: 0; background: none; color: var(--text-muted); font-size: var(--font-size-xs); }
  .activation-complete { margin-top: var(--space-6); display: flex; align-items: center; gap: var(--space-3); color: var(--accent-green); font-size: var(--font-size-sm); font-weight: 650; }

  .product-surface { min-width: 0; display: grid; }
  .empty-prs,
  .repo-chooser { width: min(100% - 48px, 600px); align-self: center; justify-self: center; box-sizing: border-box; }
  .empty-prs { display: grid; justify-items: center; gap: var(--space-4); text-align: center; }
  .empty-icon { display: grid; width: 44px; height: 44px; place-items: center; border: 1px solid var(--border-default); border-radius: 50%; color: var(--text-muted); background: var(--bg-surface); }
  .empty-prs h2,
  .repo-chooser h2 { margin: 0; font-size: var(--font-size-2xl); }
  .empty-prs > p,
  .repo-chooser > p { max-width: 56ch; margin: 0; color: var(--text-secondary); line-height: 1.5; }
  .detected-row { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-3) var(--space-4); border: 1px solid color-mix(in srgb, var(--accent-green) 30%, var(--border-default)); border-radius: var(--radius-md); color: var(--accent-green); background: color-mix(in srgb, var(--accent-green) 6%, var(--bg-primary)); font-size: var(--font-size-sm); }
  .detected-row span { color: var(--text-secondary); }

  .primary,
  .secondary { min-height: 32px; display: inline-flex; align-items: center; justify-content: center; gap: var(--space-3); padding: 0 var(--space-5); border-radius: var(--radius-md); font-weight: 650; }
  .primary { border: 1px solid var(--accent-blue); background: var(--accent-blue); color: var(--bg-primary); }
  .primary:disabled { opacity: .45; cursor: not-allowed; }
  .secondary { border: 1px solid var(--border-default); background: var(--bg-surface); color: var(--text-primary); }
  .surface-kicker { margin-bottom: var(--space-3) !important; color: var(--accent-blue) !important; font-size: var(--font-size-xs); font-weight: 700; letter-spacing: .08em; }
  .repo-chooser h2 { margin-bottom: var(--space-3); }
  .choice-list { margin-top: var(--space-6); border: 1px solid var(--border-default); border-radius: var(--radius-lg); background: var(--bg-surface); }
  .choice-list :global(.kit-checkbox.choice-row) { width: 100%; box-sizing: border-box; min-height: 54px; display: grid; grid-template-columns: auto 1fr; gap: var(--space-4); align-items: center; padding: var(--space-3) var(--space-5); border-bottom: 1px solid var(--border-muted); cursor: pointer; }
  .choice-list :global(.choice-row:last-child) { border-bottom: 0; }
  .choice-list :global(.choice-row .kit-checkbox__label) { min-width: 0; }
  .choice-list span { display: grid; gap: var(--space-1); }
  .choice-list strong { font-family: var(--font-mono); font-size: var(--font-size-sm); }
  .choice-list small { color: var(--text-muted); }
  .chooser-actions { margin-top: var(--space-5); display: flex; justify-content: flex-end; gap: var(--space-4); }

  .pr-layout { min-width: 0; display: grid; grid-template-columns: minmax(230px, 38%) minmax(0, 1fr); }
  .pr-list { min-width: 0; border-right: 1px solid var(--border-muted); background: var(--bg-inset); }
  .list-toolbar { min-height: 44px; display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); padding: 0 var(--space-4); border-bottom: 1px solid var(--border-muted); }
  .list-toolbar div { display: flex; align-items: center; gap: var(--space-3); font-size: var(--font-size-sm); }
  .list-toolbar div span { color: var(--text-muted); font-family: var(--font-mono); }
  .sync-progress { display: inline-flex; align-items: center; gap: var(--space-2); color: var(--accent-blue); font-size: var(--font-size-xs); }
  .pr-row { width: 100%; min-height: 70px; display: grid; grid-template-columns: auto minmax(0, 1fr); gap: var(--space-3); align-items: start; padding: var(--space-4); border: 0; border-bottom: 1px solid var(--border-muted); background: transparent; color: var(--text-muted); text-align: left; }
  .pr-row.selected { background: color-mix(in srgb, var(--accent-blue) 9%, var(--bg-primary)); color: var(--accent-blue); }
  .pr-row > span { min-width: 0; display: grid; gap: var(--space-2); }
  .pr-row strong { overflow: hidden; color: var(--text-primary); font-size: var(--font-size-sm); text-overflow: ellipsis; white-space: nowrap; }
  .pr-row small { color: var(--text-muted); font-size: var(--font-size-xs); line-height: 1.4; }

  .pr-detail { min-width: 0; background: var(--bg-primary); }
  .detail-empty { height: 100%; display: grid; place-content: center; justify-items: center; gap: var(--space-3); color: var(--text-muted); text-align: center; }
  .detail-empty strong { color: var(--text-secondary); }
  .detail-empty span { font-size: var(--font-size-sm); }
  .detail-header { padding: var(--space-6); }
  .detail-repo { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--font-size-xs); }
  .detail-header h3 { max-width: 28ch; margin: var(--space-3) 0; font-size: var(--font-size-2xl); line-height: 1.25; }
  .detail-header p { margin: 0; color: var(--text-muted); font-size: var(--font-size-sm); }
  .detail-tabs { display: flex; gap: var(--space-6); padding: 0 var(--space-6); border-bottom: 1px solid var(--border-muted); color: var(--text-muted); font-size: var(--font-size-sm); }
  .detail-tabs span { padding: 0 0 var(--space-4); border-bottom: 2px solid transparent; }
  .detail-tabs .active { border-bottom-color: var(--accent-blue); color: var(--text-primary); }
  .detail-summary { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-4); padding: var(--space-6); }
  .detail-summary :global(.detail-summary-card .kit-card__body) { display: grid; gap: var(--space-2); }
  .detail-summary span { color: var(--text-muted); font-size: var(--font-size-xs); }
  .detail-summary strong { font-size: var(--font-size-sm); }
  .detail-summary .ok { color: var(--accent-green); }
  .workspace-callout { margin: 0 var(--space-6); display: flex; align-items: center; justify-content: space-between; gap: var(--space-5); padding: var(--space-4); border: 1px solid color-mix(in srgb, var(--accent-blue) 32%, var(--border-default)); border-radius: var(--radius-lg); background: color-mix(in srgb, var(--accent-blue) 7%, var(--bg-primary)); }
  .workspace-callout > div { display: flex; align-items: center; gap: var(--space-3); color: var(--accent-blue); }
  .workspace-callout span { display: grid; gap: var(--space-1); }
  .workspace-callout strong { color: var(--text-primary); font-size: var(--font-size-sm); }
  .workspace-callout small { color: var(--text-muted); }

  @media (max-width: 760px) {
    .topbar { grid-template-columns: 1fr auto; }
    .topbar nav { display: none; }
    .shell-body { grid-template-columns: 1fr; }
    .activation-rail { border-right: 0; border-bottom: 1px solid var(--border-muted); }
    .checklist { grid-template-columns: repeat(5, 1fr); gap: var(--space-3); }
    .checklist li { display: flex; justify-content: center; }
    .checklist li div,
    .rail-copy,
    .rail-primary,
    .rail-hint,
    .activation-complete,
    .dismiss { display: none; }
    .pr-layout { grid-template-columns: 1fr; }
    .pr-list { border-right: 0; border-bottom: 1px solid var(--border-muted); }
    .pr-detail { min-height: 360px; }
  }
</style>
