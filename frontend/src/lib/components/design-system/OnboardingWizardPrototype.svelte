<script lang="ts">
  import { Card, Checkbox, SearchInput } from "@kenn-io/kit-ui";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import CircleDashedIcon from "@lucide/svelte/icons/circle-dashed";
  import GitPullRequestIcon from "@lucide/svelte/icons/git-pull-request";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import TerminalIcon from "@lucide/svelte/icons/terminal";

  type Phase = "repos" | "sync" | "pulls" | "workspace";

  interface Repository {
    name: string;
    description: string;
    private: boolean;
    updated: string;
  }

  const repositories: Repository[] = [
    {
      name: "acme/forge",
      description: "Maintainer console and workspace runtime",
      private: false,
      updated: "12 min ago",
    },
    {
      name: "acme/docs",
      description: "Product documentation and examples",
      private: false,
      updated: "2 hr ago",
    },
    {
      name: "acme/runtime",
      description: "Local execution and build tooling",
      private: true,
      updated: "Yesterday",
    },
    {
      name: "octo-labs/relay",
      description: "Event relay used across services",
      private: true,
      updated: "4 days ago",
    },
  ];

  const steps = [
    { id: "auth", label: "GitHub ready" },
    { id: "repos", label: "Choose repos" },
    { id: "sync", label: "First sync" },
    { id: "pulls", label: "Open a PR" },
    { id: "workspace", label: "Start workspace" },
  ] as const;

  let phase = $state<Phase>("repos");
  let filter = $state("");
  let selected = $state<string[]>(["acme/docs"]);

  const visibleRepositories = $derived(
    repositories.filter((repository) =>
      repository.name.toLowerCase().includes(filter.trim().toLowerCase()),
    ),
  );

  function toggleRepository(name: string): void {
    selected = selected.includes(name)
      ? selected.filter((candidate) => candidate !== name)
      : [...selected, name];
  }

  function stepState(id: (typeof steps)[number]["id"]): "done" | "active" | "next" {
    const order = ["auth", "repos", "sync", "pulls", "workspace"];
    const active = phase === "pulls" ? "pulls" : phase;
    const stepIndex = order.indexOf(id);
    const activeIndex = order.indexOf(active);
    return stepIndex < activeIndex ? "done" : stepIndex === activeIndex ? "active" : "next";
  }

  function repositoryCountLabel(): string {
    return `${selected.length} ${selected.length === 1 ? "repository" : "repositories"}`;
  }
</script>

<section class="wizard" aria-label="Focused setup wizard prototype">
  <header class="wizard-bar">
    <div class="wordmark"><span class="mark">k</span> kenn-forge</div>
    <button type="button" class="quiet-action">I’ll do this later</button>
  </header>

  <div class="wizard-layout">
    <aside class="progress" aria-label="Setup progress">
      <p class="progress-label">SETUP</p>
      <ol>
        {#each steps as step, index (step.id)}
          {@const state = stepState(step.id)}
          <li class:active={state === "active"} class:done={state === "done"}>
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

      <div class="identity">
        <CircleCheckIcon size={16} aria-hidden="true" />
        <div>
          <strong>gh authenticated</strong>
          <span>github.com · @maintainer</span>
        </div>
      </div>
    </aside>

    <main class="wizard-main">
      {#if phase === "repos"}
        <div class="step-heading">
          <p class="step-number">STEP 2 OF 5</p>
          <h2>Choose the repositories you maintain</h2>
          <p>
            Kenn Forge found these through your existing <code>gh</code>
            session. Start small; you can add more later.
          </p>
        </div>

        <label class="repo-search">
          <span>Filter repositories</span>
          <SearchInput
            class="prototype-search"
            bind:value={filter}
            block
            ariaLabel="Filter repositories"
            placeholder="owner or repository"
            keys={["⌘", "K"]}
          />
        </label>

        <div class="repo-list" aria-label="GitHub repositories">
          {#each visibleRepositories as repository (repository.name)}
            <Checkbox
              class={selected.includes(repository.name) ? "repo-row selected" : "repo-row"}
              checked={selected.includes(repository.name)}
              onchange={() => toggleRepository(repository.name)}
            >
              <span class="repo-copy">
                <strong>{repository.name}</strong>
                <span>{repository.description}</span>
              </span>
              <span class="repo-meta">
                {repository.private ? "Private" : "Public"}<br />
                {repository.updated}
              </span>
            </Checkbox>
          {/each}
          {#if visibleRepositories.length === 0}
            <p class="no-results">No repositories match “{filter}”.</p>
          {/if}
        </div>

        <div class="step-actions">
          <span>{repositoryCountLabel()} selected</span>
          <button
            type="button"
            class="primary-action"
            disabled={selected.length === 0}
            onclick={() => { phase = "sync"; }}
          >
            Configure {repositoryCountLabel()}
          </button>
        </div>
      {:else if phase === "sync"}
        <div class="step-heading compact-heading">
          <p class="step-number">STEP 3 OF 5</p>
          <h2>First sync is underway</h2>
          <p>
            The app is already useful while history fills in. Open pull
            requests arrive first.
          </p>
        </div>

        <Card level="raised" padding="none" class="sync-board">
          <div class="sync-summary">
            <span class="sync-glyph"><RefreshCwIcon size={18} /></span>
            <div>
              <strong>Syncing {repositoryCountLabel()}</strong>
              <span>Pull requests · issues · CI · activity</span>
            </div>
            <span class="sync-percent">68%</span>
          </div>
          <div class="progress-track"><span></span></div>
          {#each selected as repository, index (repository)}
            <div class="sync-row">
              <CircleCheckIcon size={15} aria-hidden="true" />
              <span>{repository}</span>
              <span>{index === selected.length - 1 ? "Fetching activity" : "Pull requests ready"}</span>
            </div>
          {/each}
        </Card>

        <div class="first-result">
          <div>
            <span class="result-label">READY NOW</span>
            <strong>3 open pull requests found</strong>
            <p>You do not need to wait for the full sync.</p>
          </div>
          <button type="button" class="primary-action" onclick={() => { phase = "pulls"; }}>
            Show pull requests
          </button>
        </div>
      {:else if phase === "pulls"}
        <div class="step-heading compact-heading">
          <p class="step-number">STEP 4 OF 5</p>
          <h2>Open a pull request</h2>
          <p>Pick one real item to learn the review and workspace surfaces.</p>
        </div>

        <Card level="raised" padding="none" class="pulls-shell">
          <div class="pulls-title-row">
            <span>Open pull requests</span><span>3</span>
          </div>
          <button class="pull-row selected-pull" type="button" onclick={() => { phase = "workspace"; }}>
            <GitPullRequestIcon size={16} aria-hidden="true" />
            <span>
              <strong>Keep workspace activity across reloads</strong>
              <small>acme/forge #248 · updated 18 min ago</small>
            </span>
            <span class="ci-ok">CI passed</span>
          </button>
          <button class="pull-row" type="button">
            <GitPullRequestIcon size={16} aria-hidden="true" />
            <span>
              <strong>Clarify quickstart repository setup</strong>
              <small>acme/docs #91 · updated 2 hr ago</small>
            </span>
            <span>Review</span>
          </button>
        </Card>
        <p class="action-hint">Open PR #248 to continue to a workspace.</p>
      {:else}
        <div class="step-heading compact-heading">
          <p class="step-number">STEP 5 OF 5</p>
          <h2>Your first workspace is ready</h2>
          <p>
            Kenn Forge will create an isolated worktree for PR #248 and keep
            the terminal beside its review context.
          </p>
        </div>

        <Card level="raised" padding="none" class="workspace-preview">
          <div class="workspace-header">
            <div class="terminal-mark"><TerminalIcon size={18} /></div>
            <div>
              <strong>acme/forge · PR #248</strong>
              <span>maintain-workspace-activity</span>
            </div>
            <span class="ready-label">READY</span>
          </div>
          <div class="workspace-path">
            <span>Worktree</span>
            <code>~/.kenn/forge/worktrees/acme/forge/pr-248</code>
          </div>
          <div class="launch-actions">
            <button type="button" class="primary-action">
              <TerminalIcon size={14} aria-hidden="true" /> Launch shell
            </button>
            <button type="button" class="secondary-action">Open PR first</button>
          </div>
        </Card>

        <button type="button" class="restart" onclick={() => { phase = "repos"; }}>
          Replay prototype
        </button>
      {/if}
    </main>
  </div>
</section>

<style>
  .wizard {
    min-height: 610px;
    color: var(--text-primary);
    background: var(--bg-primary);
    font-family: var(--font-sans, system-ui, sans-serif);
  }

  .wizard-bar {
    height: 48px;
    padding: 0 var(--space-5);
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

  .mark {
    display: grid;
    width: 22px;
    height: 22px;
    place-items: center;
    border-radius: var(--radius-sm);
    background: var(--accent-blue);
    color: var(--bg-primary);
    font-family: var(--font-mono);
    font-weight: 800;
  }

  button {
    font: inherit;
  }

  button:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 2px;
  }

  .quiet-action,
  .restart {
    border: 0;
    background: none;
    color: var(--text-muted);
    cursor: pointer;
    font-size: var(--font-size-sm);
  }

  .wizard-layout {
    min-height: 562px;
    display: grid;
    grid-template-columns: 210px minmax(0, 1fr);
  }

  .progress {
    display: flex;
    flex-direction: column;
    padding: var(--space-7) var(--space-5);
    border-right: 1px solid var(--border-muted);
    background: var(--bg-inset);
  }

  .progress-label,
  .step-number,
  .result-label {
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
    display: grid;
    width: 18px;
    height: 18px;
    place-items: center;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .done .step-icon {
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
    color: var(--accent-green);
  }

  .identity div {
    display: grid;
    gap: var(--space-1);
  }

  .identity strong {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .identity span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .wizard-main {
    width: min(100%, 720px);
    margin: 0 auto;
    padding: 42px var(--space-7) var(--space-7);
    box-sizing: border-box;
  }

  .step-heading {
    display: grid;
    gap: var(--space-3);
    margin-bottom: var(--space-6);
  }

  .compact-heading {
    margin-bottom: var(--space-7);
  }

  .step-heading h2 {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-2xl);
    line-height: 1.2;
    letter-spacing: -0.025em;
  }

  .step-heading p:last-child {
    max-width: 62ch;
    margin: 0;
    color: var(--text-secondary);
    line-height: 1.55;
  }

  code {
    font-family: var(--font-mono);
  }

  .repo-search {
    display: grid;
    gap: var(--space-3);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .repo-list {
    max-height: 265px;
    margin-top: var(--space-5);
    overflow: auto;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
  }

  .repo-list :global(.kit-checkbox.repo-row) {
    width: 100%;
    box-sizing: border-box;
    min-height: 58px;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--space-4);
    align-items: center;
    padding: var(--space-4) var(--space-5);
    border-bottom: 1px solid var(--border-muted);
    cursor: pointer;
  }

  .repo-list :global(.kit-checkbox.repo-row:last-child) {
    border-bottom: 0;
  }

  .repo-list :global(.kit-checkbox.repo-row.selected) {
    background: color-mix(in srgb, var(--accent-blue) 8%, var(--bg-surface));
  }

  :global(.repo-row .kit-checkbox__label) {
    min-width: 0;
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: var(--space-4);
    align-items: center;
  }

  .repo-copy {
    min-width: 0;
    display: grid;
    gap: var(--space-2);
  }

  .repo-copy strong {
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
  }

  .repo-copy span,
  .repo-meta,
  .no-results {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .repo-meta {
    text-align: right;
    line-height: 1.6;
  }

  .no-results {
    margin: 0;
    padding: var(--space-7);
    text-align: center;
  }

  .step-actions {
    margin-top: var(--space-5);
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--space-5);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .primary-action,
  .secondary-action {
    min-height: 32px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    padding: 0 var(--space-5);
    border-radius: var(--radius-md);
    cursor: pointer;
    font-weight: 650;
  }

  .primary-action {
    border: 1px solid var(--accent-blue);
    background: var(--accent-blue);
    color: var(--bg-primary);
  }

  .primary-action:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .secondary-action {
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
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
    display: grid;
    width: 34px;
    height: 34px;
    place-items: center;
    border-radius: var(--radius-md);
    background: color-mix(in srgb, var(--accent-blue) 12%, var(--bg-inset));
    color: var(--accent-blue);
  }

  .sync-summary div,
  .workspace-header div:nth-child(2) {
    display: grid;
    gap: var(--space-2);
  }

  .sync-summary span,
  .workspace-header span,
  .workspace-path span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .sync-percent {
    font-family: var(--font-mono);
  }

  .progress-track {
    height: 3px;
    margin: 0 var(--space-5) var(--space-4);
    overflow: hidden;
    border-radius: 999px;
    background: var(--bg-inset);
  }

  .progress-track span {
    display: block;
    width: 68%;
    height: 100%;
    background: var(--accent-blue);
  }

  .sync-row {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    gap: var(--space-3);
    padding: var(--space-4) var(--space-5);
    border-top: 1px solid var(--border-muted);
    color: var(--accent-green);
    font-size: var(--font-size-sm);
  }

  .sync-row span:first-of-type {
    color: var(--text-primary);
    font-family: var(--font-mono);
  }

  .sync-row span:last-child {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .first-result {
    margin-top: var(--space-6);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-6);
    padding: var(--space-5);
    border: 1px solid color-mix(in srgb, var(--accent-green) 34%, var(--border-default));
    border-radius: var(--radius-lg);
    background: color-mix(in srgb, var(--accent-green) 7%, var(--bg-primary));
  }

  .first-result div {
    display: grid;
    gap: var(--space-2);
  }

  .first-result p {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
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
  }

  .pull-row:last-child {
    border-bottom: 0;
  }

  .selected-pull {
    background: color-mix(in srgb, var(--accent-blue) 8%, var(--bg-surface));
    color: var(--accent-blue);
  }

  .pull-row > span:first-of-type {
    display: grid;
    gap: var(--space-2);
  }

  .pull-row strong {
    color: var(--text-primary);
  }

  .pull-row small,
  .pull-row > span:last-child,
  .action-hint {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .pull-row .ci-ok {
    color: var(--accent-green);
  }

  .action-hint {
    margin: var(--space-4) 0 0;
    text-align: center;
  }

  .workspace-header {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    gap: var(--space-4);
    align-items: center;
    padding: var(--space-5);
    border-bottom: 1px solid var(--border-muted);
  }

  .ready-label {
    color: var(--accent-green) !important;
    font-weight: 800;
    letter-spacing: 0.08em;
  }

  .workspace-path {
    display: grid;
    gap: var(--space-3);
    padding: var(--space-5);
  }

  .workspace-path code {
    padding: var(--space-4);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .launch-actions {
    display: flex;
    gap: var(--space-4);
    padding: 0 var(--space-5) var(--space-5);
  }

  .restart {
    display: block;
    margin: var(--space-5) auto 0;
  }

  @media (max-width: 760px) {
    .wizard-layout {
      grid-template-columns: 1fr;
    }

    .progress {
      padding: var(--space-4);
      border-right: 0;
      border-bottom: 1px solid var(--border-muted);
    }

    .progress ol {
      grid-template-columns: repeat(5, 1fr);
      gap: var(--space-2);
      margin-top: var(--space-4);
    }

    .progress li {
      justify-content: center;
    }

    .progress li > span:last-child,
    .identity,
    .progress-label {
      display: none;
    }

    .wizard-main {
      padding: var(--space-7) var(--space-5);
    }

    .repo-meta {
      display: none;
    }

    .first-result,
    .step-actions {
      align-items: stretch;
      flex-direction: column;
    }
  }
</style>
