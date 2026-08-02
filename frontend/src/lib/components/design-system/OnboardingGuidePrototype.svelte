<script lang="ts">
  import { Card, Checkbox } from "@kenn-io/kit-ui";
  import BookOpenIcon from "@lucide/svelte/icons/book-open";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import FolderGitIcon from "@lucide/svelte/icons/folder-git-2";
  import GitPullRequestIcon from "@lucide/svelte/icons/git-pull-request";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import TerminalIcon from "@lucide/svelte/icons/terminal";

  type Task = "connect" | "repos" | "work";

  const repositoryOptions = ["acme/forge", "acme/docs", "acme/runtime"];
  const pullRequests = [
    {
      number: 248,
      title: "Keep workspace activity across reloads",
      repo: "acme/forge",
      status: "CI passed",
    },
    {
      number: 91,
      title: "Clarify repository setup in quickstart",
      repo: "acme/docs",
      status: "Review ready",
    },
  ];

  let activeTask = $state<Task>("connect");
  let selectedRepositories = $state<string[]>(["acme/forge", "acme/docs"]);
  let syncStarted = $state(false);
  let workspaceLaunched = $state(false);
  const availablePulls = $derived(
    pullRequests.filter((pull) => selectedRepositories.includes(pull.repo)),
  );
  const activePull = $derived(availablePulls[0] ?? null);

  function toggleRepository(repository: string): void {
    selectedRepositories = selectedRepositories.includes(repository)
      ? selectedRepositories.filter((candidate) => candidate !== repository)
      : [...selectedRepositories, repository];
  }

  function moveToRepos(): void {
    activeTask = "repos";
  }

  function moveToWork(): void {
    syncStarted = true;
    workspaceLaunched = false;
    activeTask = "work";
  }
</script>

<section class="guide" aria-label="Task-oriented start guide prototype">
  <header class="guide-bar">
    <div class="brand"><span>k</span> kenn-forge</div>
    <div class="guide-context"><BookOpenIcon size={15} /> Guide <span>/</span> Start here</div>
    <button type="button">Open app</button>
  </header>

  <div class="guide-layout">
    <aside class="guide-nav">
      <p>START HERE</p>
      <button type="button" aria-label="1. Confirm GitHub" class:active={activeTask === "connect"} onclick={() => { activeTask = "connect"; }}>
        <span>1</span><div><strong>1. Confirm GitHub</strong><small>Detected automatically</small></div>
      </button>
      <button type="button" aria-label="2. Add repositories" class:active={activeTask === "repos"} onclick={() => { activeTask = "repos"; }}>
        <span>2</span><div><strong>2. Add repositories</strong><small>Choose a focused set</small></div>
      </button>
      <button type="button" aria-label="3. Open a PR" class:active={activeTask === "work"} onclick={() => { activeTask = "work"; }}>
        <span>3</span><div><strong>3. Open a PR</strong><small>Launch a workspace</small></div>
      </button>

      <div class="nav-separator"></div>
      <a href="#repositories">Repositories</a>
      <a href="#pull-requests">Pull requests</a>
      <a href="#workspaces">Workspaces</a>
      <a href="#troubleshooting">Troubleshooting</a>
    </aside>

    <main class="article">
      <div class="article-heading">
        <p>QUICKSTART · ABOUT 3 MINUTES</p>
        <h2>From gh to a working PR</h2>
        <span>Use the GitHub login already on this machine. No token copying required.</span>
      </div>

      {#if activeTask === "connect"}
        <article class="task-copy">
          <div class="task-index">01</div>
          <div>
            <h3>Confirm the detected GitHub session</h3>
            <p>
              Kenn Forge asks <code>gh</code> for your authenticated identity
              and repository access. It does not create another credential.
            </p>
            <div class="callout success">
              <CircleCheckIcon size={17} />
              <span><strong>Ready on github.com</strong><small>Authenticated as @maintainer with repo access</small></span>
            </div>
            <details>
              <summary>What command is being checked?</summary>
              <code>gh auth status --hostname github.com</code>
            </details>
            <button type="button" class="primary" onclick={moveToRepos}>Continue to repositories</button>
          </div>
        </article>
      {:else if activeTask === "repos"}
        <article class="task-copy">
          <div class="task-index">02</div>
          <div>
            <h3>Choose what Kenn Forge should track</h3>
            <p>
              Start with repositories you review every week. A small set keeps
              the activity feed useful and first sync quick.
            </p>
            <div class="inline-picker">
              {#each repositoryOptions as repository (repository)}
                <Checkbox
                  class="guide-repo-choice"
                  checked={selectedRepositories.includes(repository)}
                  label={repository}
                  onchange={() => toggleRepository(repository)}
                />
              {/each}
            </div>
            <p class="source-note"><code>gh api user/repos</code> · sorted by recently updated</p>
            <button type="button" class="primary" disabled={selectedRepositories.length === 0} onclick={moveToWork}>
              Add {selectedRepositories.length} and start first sync
            </button>
          </div>
        </article>
      {:else}
        <article class="task-copy">
          <div class="task-index">03</div>
          <div>
            <h3>Open context, then continue locally</h3>
            <p>
              The first open pull requests appear before historical sync
              finishes. Open one, review its state, then create an isolated
              workspace without losing that context.
            </p>
            {#if activePull}
              <ol class="mini-steps">
                <li><CircleCheckIcon size={15} /><span><strong>First sync</strong> · open PRs ready, history at 68%</span></li>
                <li><CircleCheckIcon size={15} /><span><strong>PR #{activePull.number} opened</strong> · CI and review state visible</span></li>
                <li class:complete={workspaceLaunched}>
                  {#if workspaceLaunched}<CircleCheckIcon size={15} />{:else}<TerminalIcon size={15} />{/if}
                  <span><strong>{workspaceLaunched ? "Workspace ready" : "Create workspace"}</strong> · isolated worktree and shell</span>
                </li>
              </ol>
              <button type="button" class="primary" onclick={() => { workspaceLaunched = true; }}>
                {workspaceLaunched ? "Open workspace" : `Create workspace for PR #${activePull.number}`}
              </button>
            {:else}
              <p>No open pull requests were found in the selected repositories.</p>
            {/if}
          </div>
        </article>
      {/if}

      <footer class="article-footer">
        <span>Need a different provider?</span>
        <a href="#configuration">Open repository configuration guide</a>
      </footer>
    </main>

    <aside class="live-preview" aria-label="Live setup preview">
      <div class="preview-header">
        <span>LIVE PREVIEW</span>
        <strong>{activeTask === "connect" ? "GitHub session" : activeTask === "repos" ? "Repository setup" : "Pull request workspace"}</strong>
      </div>

      {#if activeTask === "connect"}
        <Card level="raised" padding="none" class="preview-card">
          <div class="terminal-preview">
            <div class="terminal-title"><TerminalIcon size={13} /> Local tooling</div>
            <pre><span>$</span> gh auth status
<strong>✓</strong> Logged in to github.com
  account: maintainer
  protocol: ssh

<span>$</span> kenn-forge status
<strong>✓</strong> gh authenticated
<strong>✓</strong> git available</pre>
          </div>
        </Card>
        <div class="preview-note"><CircleCheckIcon size={15} /> No credential setup needed</div>
      {:else if activeTask === "repos"}
        <div class="repo-preview-title"><FolderGitIcon size={15} /><span><strong>Repositories</strong><small>{selectedRepositories.length} selected</small></span></div>
        <div class="preview-repos">
          {#each repositoryOptions as repository (repository)}
            <div class:selected={selectedRepositories.includes(repository)}>
              {#if selectedRepositories.includes(repository)}<CircleCheckIcon size={14} />{:else}<span class="empty-check"></span>{/if}
              <span>{repository}</span>
            </div>
          {/each}
        </div>
        <div class="preview-note"><CircleCheckIcon size={15} /> Selection stays editable in Settings</div>
      {:else}
        <div class="sync-strip">
          <RefreshCwIcon size={13} />
          <span><strong>First sync</strong><small>{syncStarted ? "Open PRs ready · history 68%" : "Starts after repository setup"}</small></span>
        </div>
        {#if activePull}
          <Card level="raised" padding="none" class="preview-card">
            <div class="preview-pr">
              <div class="pr-heading"><GitPullRequestIcon size={16} /><span>{activePull.repo} #{activePull.number}</span></div>
              <h3>{activePull.title}</h3>
              <div class="pr-meta"><span class:passed={activePull.status === "CI passed"}>{activePull.status}</span><span>Review requested</span></div>
              <div class="workspace-preview">
                <TerminalIcon size={16} />
                <span><strong>{workspaceLaunched ? "Workspace ready" : "Workspace"}</strong><small>{workspaceLaunched ? "Shell · running" : "Create from this PR"}</small></span>
                <span class="arrow">→</span>
              </div>
            </div>
          </Card>
        {:else}
          <p class="preview-note">No open pull requests found.</p>
        {/if}
      {/if}
    </aside>
  </div>
</section>

<style>
  .guide {
    min-height: 610px;
    color: var(--text-primary);
    background: var(--bg-primary);
    font-family: var(--font-sans, system-ui, sans-serif);
  }

  button { font: inherit; }
  button { cursor: pointer; }
  button:focus-visible,
  a:focus-visible,
  summary:focus-visible { outline: 2px solid var(--accent-blue); outline-offset: 2px; }

  .guide-bar {
    min-height: 46px;
    display: grid;
    grid-template-columns: 230px 1fr auto;
    align-items: center;
    gap: var(--space-5);
    padding: 0 var(--space-5);
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-surface);
  }

  .brand { display: flex; align-items: center; gap: var(--space-3); font-size: var(--font-size-sm); font-weight: 700; }
  .brand > span { display: grid; width: 21px; height: 21px; place-items: center; border-radius: var(--radius-sm); background: var(--accent-blue); color: var(--bg-primary); font-family: var(--font-mono); }
  .guide-context { display: flex; align-items: center; gap: var(--space-3); color: var(--text-muted); font-size: var(--font-size-sm); }
  .guide-context span { color: var(--border-strong); }
  .guide-bar > button { min-height: 28px; padding: 0 var(--space-4); border: 1px solid var(--border-default); border-radius: var(--radius-md); background: var(--bg-surface); color: var(--text-secondary); }

  .guide-layout { min-height: 564px; display: grid; grid-template-columns: 230px minmax(360px, 1fr) minmax(280px, 34%); }
  .guide-nav { padding: var(--space-6) var(--space-4); border-right: 1px solid var(--border-muted); background: var(--bg-inset); }
  .guide-nav > p { margin: 0 var(--space-3) var(--space-4); color: var(--text-muted); font-size: var(--font-size-xs); font-weight: 700; letter-spacing: .09em; }
  .guide-nav > button { width: 100%; min-height: 52px; display: grid; grid-template-columns: auto minmax(0, 1fr); gap: var(--space-3); align-items: center; padding: var(--space-3); border: 0; border-radius: var(--radius-md); background: transparent; color: var(--text-muted); text-align: left; }
  .guide-nav > button.active { background: color-mix(in srgb, var(--accent-blue) 9%, var(--bg-primary)); color: var(--accent-blue); }
  .guide-nav > button > span { display: grid; width: 22px; height: 22px; place-items: center; border: 1px solid var(--border-default); border-radius: 50%; font-family: var(--font-mono); font-size: var(--font-size-xs); }
  .guide-nav > button.active > span { border-color: var(--accent-blue); background: var(--accent-blue); color: var(--bg-primary); }
  .guide-nav button div { min-width: 0; display: grid; gap: var(--space-1); }
  .guide-nav strong { color: var(--text-secondary); font-size: var(--font-size-sm); }
  .guide-nav button.active strong { color: var(--text-primary); }
  .guide-nav small { font-size: var(--font-size-xs); }
  .nav-separator { height: 1px; margin: var(--space-5) var(--space-3); background: var(--border-muted); }
  .guide-nav a { display: block; padding: var(--space-3); color: var(--text-muted); font-size: var(--font-size-sm); text-decoration: none; }

  .article { min-width: 0; padding: var(--space-7); }
  .article-heading { max-width: 680px; }
  .article-heading p { margin: 0 0 var(--space-3); color: var(--accent-blue); font-size: var(--font-size-xs); font-weight: 700; letter-spacing: .09em; }
  .article-heading h2 { margin: 0; font-size: var(--font-size-2xl); letter-spacing: -.025em; }
  .article-heading > span { display: block; margin-top: var(--space-3); color: var(--text-secondary); line-height: 1.5; }

  .task-copy { max-width: 680px; display: grid; grid-template-columns: 40px minmax(0, 1fr); gap: var(--space-5); margin-top: var(--space-7); padding-top: var(--space-6); border-top: 1px solid var(--border-muted); }
  .task-index { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--font-size-sm); }
  .task-copy h3 { margin: 0; font-size: var(--font-size-xl); line-height: 1.3; }
  .task-copy p { margin: var(--space-3) 0 0; color: var(--text-secondary); line-height: 1.55; }
  code,
  pre { font-family: var(--font-mono); }
  .callout { margin-top: var(--space-5); display: flex; align-items: center; gap: var(--space-3); padding: var(--space-4); border: 1px solid var(--border-default); border-radius: var(--radius-md); }
  .callout.success { border-color: color-mix(in srgb, var(--accent-green) 32%, var(--border-default)); color: var(--accent-green); background: color-mix(in srgb, var(--accent-green) 6%, var(--bg-primary)); }
  .callout span { display: grid; gap: var(--space-1); }
  .callout strong { color: var(--text-primary); font-size: var(--font-size-sm); }
  .callout small { color: var(--text-muted); }
  details { margin-top: var(--space-4); color: var(--text-secondary); font-size: var(--font-size-sm); }
  summary { cursor: pointer; }
  details code { display: block; margin-top: var(--space-3); padding: var(--space-4); border-radius: var(--radius-md); background: var(--bg-inset); }
  .primary { min-height: 32px; margin-top: var(--space-5); display: inline-flex; align-items: center; padding: 0 var(--space-5); border: 1px solid var(--accent-blue); border-radius: var(--radius-md); background: var(--accent-blue); color: var(--bg-primary); font-weight: 650; }
  .primary:disabled { opacity: .45; cursor: not-allowed; }
  .inline-picker { margin-top: var(--space-5); border: 1px solid var(--border-default); border-radius: var(--radius-md); background: var(--bg-surface); }
  .inline-picker :global(.kit-checkbox.guide-repo-choice) { width: 100%; box-sizing: border-box; min-height: 38px; display: flex; align-items: center; gap: var(--space-3); padding: 0 var(--space-4); border-bottom: 1px solid var(--border-muted); cursor: pointer; font-family: var(--font-mono); font-size: var(--font-size-sm); }
  .inline-picker :global(.guide-repo-choice:last-child) { border-bottom: 0; }
  .source-note { color: var(--text-muted) !important; font-size: var(--font-size-xs); }
  .mini-steps { display: grid; gap: var(--space-3); margin: var(--space-5) 0 0; padding: 0; list-style: none; }
  .mini-steps li { display: flex; align-items: center; gap: var(--space-3); color: var(--accent-green); }
  .mini-steps li:last-child:not(.complete) { color: var(--accent-blue); }
  .mini-steps span { color: var(--text-secondary); font-size: var(--font-size-sm); }
  .article-footer { max-width: 680px; margin-top: var(--space-7); padding-top: var(--space-5); border-top: 1px solid var(--border-muted); display: flex; justify-content: space-between; gap: var(--space-5); color: var(--text-muted); font-size: var(--font-size-xs); }
  .article-footer a { color: var(--accent-blue); }

  .live-preview { padding: var(--space-6); border-left: 1px solid var(--border-muted); background: var(--bg-inset); }
  .preview-header { display: grid; gap: var(--space-2); padding-bottom: var(--space-4); border-bottom: 1px solid var(--border-muted); }
  .preview-header span { color: var(--text-muted); font-size: var(--font-size-xs); font-weight: 700; letter-spacing: .09em; }
  .preview-header strong { font-size: var(--font-size-md); }
  :global(.preview-card) { margin-top: var(--space-5); overflow: hidden; }
  .terminal-title { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-3) var(--space-4); border-bottom: 1px solid var(--border-muted); color: var(--text-muted); font-size: var(--font-size-xs); }
  .terminal-preview pre { min-height: 220px; margin: 0; padding: var(--space-5); background: var(--bg-inset); color: var(--text-secondary); font-size: var(--font-size-xs); line-height: 1.75; white-space: pre-wrap; }
  .terminal-preview pre span { color: var(--accent-blue); }
  .terminal-preview pre strong { color: var(--accent-green); }
  .preview-note { margin-top: var(--space-4); display: flex; align-items: center; gap: var(--space-3); color: var(--accent-green); font-size: var(--font-size-xs); }
  .repo-preview-title { margin-top: var(--space-5); display: flex; align-items: center; gap: var(--space-3); color: var(--accent-blue); }
  .repo-preview-title span { display: grid; gap: var(--space-1); }
  .repo-preview-title strong { color: var(--text-primary); font-size: var(--font-size-sm); }
  .repo-preview-title small { color: var(--text-muted); }
  .preview-repos { margin-top: var(--space-4); border: 1px solid var(--border-default); border-radius: var(--radius-lg); background: var(--bg-surface); }
  .preview-repos > div { min-height: 44px; display: grid; grid-template-columns: auto 1fr; gap: var(--space-3); align-items: center; padding: 0 var(--space-4); border-bottom: 1px solid var(--border-muted); color: var(--text-muted); font-family: var(--font-mono); font-size: var(--font-size-xs); }
  .preview-repos > div:last-child { border-bottom: 0; }
  .preview-repos > div.selected { color: var(--text-primary); background: color-mix(in srgb, var(--accent-blue) 7%, var(--bg-surface)); }
  .preview-repos :global(svg) { color: var(--accent-green); }
  .empty-check { width: 12px; height: 12px; border: 1px solid var(--border-default); border-radius: 50%; }
  .sync-strip { margin-top: var(--space-5); display: flex; align-items: center; gap: var(--space-3); padding: var(--space-4); border: 1px solid color-mix(in srgb, var(--accent-blue) 30%, var(--border-default)); border-radius: var(--radius-md); color: var(--accent-blue); background: color-mix(in srgb, var(--accent-blue) 6%, var(--bg-primary)); }
  .sync-strip span { display: grid; gap: var(--space-1); }
  .sync-strip strong { color: var(--text-primary); font-size: var(--font-size-sm); }
  .sync-strip small { color: var(--text-muted); }
  .pr-heading { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-4); border-bottom: 1px solid var(--border-muted); color: var(--accent-blue); font-family: var(--font-mono); font-size: var(--font-size-xs); }
  .preview-pr h3 { margin: 0; padding: var(--space-5) var(--space-4); font-size: var(--font-size-md); line-height: 1.35; }
  .pr-meta { display: flex; gap: var(--space-3); padding: 0 var(--space-4) var(--space-4); color: var(--text-muted); font-size: var(--font-size-xs); }
  .pr-meta .passed { color: var(--accent-green); }
  .workspace-preview { margin: 0 var(--space-4) var(--space-4); display: grid; grid-template-columns: auto 1fr auto; gap: var(--space-3); align-items: center; padding: var(--space-4); border: 1px solid color-mix(in srgb, var(--accent-blue) 30%, var(--border-default)); border-radius: var(--radius-md); color: var(--accent-blue); background: color-mix(in srgb, var(--accent-blue) 6%, var(--bg-primary)); }
  .workspace-preview span { display: grid; gap: var(--space-1); }
  .workspace-preview strong { color: var(--text-primary); font-size: var(--font-size-sm); }
  .workspace-preview small { color: var(--text-muted); }
  .workspace-preview .arrow { display: block; color: var(--accent-blue); }

  @media (max-width: 900px) {
    .guide-layout { grid-template-columns: 210px minmax(0, 1fr); }
    .live-preview { display: none; }
    .guide-bar { grid-template-columns: 210px 1fr auto; }
  }

  @media (max-width: 640px) {
    .guide-bar { grid-template-columns: 1fr auto; }
    .guide-context { display: none; }
    .guide-layout { grid-template-columns: 1fr; }
    .guide-nav { display: grid; grid-template-columns: repeat(3, 1fr); padding: var(--space-4); border-right: 0; border-bottom: 1px solid var(--border-muted); }
    .guide-nav > p,
    .nav-separator,
    .guide-nav > a { display: none; }
    .guide-nav > button { display: flex; justify-content: center; padding: var(--space-2); }
    .guide-nav > button div { display: none; }
    .article { padding: var(--space-6) var(--space-5); }
    .task-copy { grid-template-columns: 1fr; }
    .task-index { display: none; }
  }
</style>
