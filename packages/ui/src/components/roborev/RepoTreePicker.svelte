<script lang="ts">
  import { getStores, getRoborevClient } from "../../context.js";
  import type { components } from "../../api/roborev/generated/schema.js";

  type RepoWithCount = components["schemas"]["RepoWithCount"];
  type BranchWithCount = components["schemas"]["BranchWithCount"];

  const stores = getStores();
  const client = getRoborevClient();

  let open = $state(false);
  let search = $state("");
  let repos = $state<RepoWithCount[]>([]);
  let expandedRepo = $state<string | undefined>(undefined);
  let branches = $state<BranchWithCount[]>([]);
  let loadingBranches = $state(false);
  let pickerRef = $state<HTMLDivElement>();

  const selectedRepo = $derived(
    stores.roborevJobs?.getFilterRepo(),
  );
  const selectedBranch = $derived(
    stores.roborevJobs?.getFilterBranch(),
  );

  const displayLabel = $derived(
    selectedBranch
      ? selectedBranch
      : selectedRepo
        ? repoDisplayName(selectedRepo)
        : "All Repos",
  );

  const filteredRepos = $derived(
    search
      ? repos.filter((r) =>
          r.name.toLowerCase().includes(search.toLowerCase()),
        )
      : repos,
  );

  function repoDisplayName(rootPath: string): string {
    const repo = repos.find((r) => r.root_path === rootPath);
    return repo?.name ?? rootPath.split("/").pop() ?? rootPath;
  }

  async function loadRepos(): Promise<void> {
    if (!client) return;
    const { data } = await client.GET("/api/repos");
    repos = data?.repos ?? [];
  }

  async function toggleRepo(rootPath: string): Promise<void> {
    if (expandedRepo === rootPath) {
      expandedRepo = undefined;
      branches = [];
      return;
    }
    expandedRepo = rootPath;
    loadingBranches = true;
    if (!client) return;
    const { data } = await client.GET("/api/branches", {
      params: { query: { repo: [rootPath] } },
    });
    if (expandedRepo !== rootPath) return;
    branches = data?.branches ?? [];
    loadingBranches = false;
  }

  function selectRepo(rootPath: string): void {
    stores.roborevJobs?.setFilter("repo", rootPath);
    stores.roborevJobs?.setFilter("branch", undefined);
    open = false;
  }

  function selectBranch(
    rootPath: string,
    branch: string,
  ): void {
    stores.roborevJobs?.setFilter("repo", rootPath);
    stores.roborevJobs?.setFilter("branch", branch);
    open = false;
  }

  function selectAll(): void {
    stores.roborevJobs?.setFilter("repo", undefined);
    stores.roborevJobs?.setFilter("branch", undefined);
    open = false;
  }

  function toggle(): void {
    open = !open;
    if (open) void loadRepos();
  }

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === "Escape") open = false;
  }

  function handleDocumentMousedown(e: MouseEvent): void {
    if (!open) return;
    const target = e.target;
    if (target instanceof Node && pickerRef?.contains(target)) return;
    open = false;
  }
</script>

<svelte:window onkeydown={handleKeydown} />
<svelte:document onmousedown={handleDocumentMousedown} />

<div class="repo-picker" bind:this={pickerRef}>
  <button
    class="picker-button"
    onclick={toggle}
    title="Filter by repository"
  >
    <svg
      width="14"
      height="14"
      viewBox="0 0 16 16"
      fill="currentColor"
    >
      <path
        d="M2 2.5A2.5 2.5 0 014.5 0h8.75a.75.75
          0 01.75.75v12.5a.75.75 0
          01-.75.75h-2.5a.75.75 0
          010-1.5h1.75v-2h-8a1 1 0
          00-.714 1.7.75.75 0
          01-1.072 1.05A2.495 2.495 0
          012 11.5v-9zm10.5-1h-6a1 1 0
          00-1 1v6.708A2.486 2.486 0
          016.5 9h6V1.5z"
      />
    </svg>
    {displayLabel}
    <svg
      class="chevron"
      class:chevron-up={open}
      width="12"
      height="12"
      viewBox="0 0 16 16"
      fill="currentColor"
    >
      <path d="M4.427 7.427l3.396 3.396a.25.25
        0 00.354 0l3.396-3.396A.25.25 0
        0011.396 7H4.604a.25.25 0
        00-.177.427z"
      />
    </svg>
  </button>

  {#if open}
    <div class="dropdown">
      <input
        class="search-input"
        type="text"
        placeholder="Filter repos..."
        bind:value={search}
      />
      <div class="dropdown-list">
        <button
          class="dropdown-item"
          class:active={!selectedRepo}
          onclick={selectAll}
        >
          All Repos
        </button>

        {#each filteredRepos as repo (repo.root_path)}
          <div class="repo-group">
            <div class="repo-row">
              <button
                class="expand-btn"
                onclick={() => toggleRepo(repo.root_path)}
                title="Show branches"
              >
                <svg
                  class="expand-icon"
                  class:expanded={expandedRepo === repo.root_path}
                  width="10"
                  height="10"
                  viewBox="0 0 16 16"
                  fill="currentColor"
                >
                  <path d="M6 4l4 4-4 4" />
                </svg>
              </button>
              <button
                class="dropdown-item repo-item"
                class:active={selectedRepo === repo.root_path && !selectedBranch}
                onclick={() => selectRepo(repo.root_path)}
              >
                {repo.name}
                <span class="count">{repo.count}</span>
              </button>
            </div>

            {#if expandedRepo === repo.root_path}
              <div class="branches">
                {#if loadingBranches}
                  <span class="branch-loading">Loading...</span>
                {:else}
                  {#each branches as branch (branch.name)}
                    <button
                      class="dropdown-item branch-item"
                      class:active={selectedBranch === branch.name && selectedRepo === repo.root_path}
                      onclick={() => selectBranch(repo.root_path, branch.name)}
                    >
                      {branch.name}
                      <span class="count">{branch.count}</span>
                    </button>
                  {/each}
                  {#if branches.length === 0}
                    <span class="branch-loading">No branches</span>
                  {/if}
                {/if}
              </div>
            {/if}
          </div>
        {/each}

        {#if filteredRepos.length === 0 && search}
          <span class="no-results">No repos match</span>
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .repo-picker {
    position: relative;
  }

  .picker-button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    cursor: pointer;
    white-space: nowrap;
  }

  .picker-button:hover {
    background: var(--bg-surface-hover);
  }

  .chevron {
    transition: transform 0.15s;
  }
  .chevron-up {
    transform: rotate(180deg);
  }

  .dropdown {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    z-index: 100;
    width: 260px;
    max-height: 320px;
    display: flex;
    flex-direction: column;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-md);
  }

  .search-input {
    padding: 6px 10px;
    border: none;
    border-bottom: 1px solid var(--border-muted);
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    outline: none;
    flex-shrink: 0;
  }

  .search-input::placeholder {
    color: var(--text-muted);
  }

  .dropdown-list {
    overflow-y: auto;
    padding: 4px 0;
  }

  .dropdown-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    padding: 5px 10px;
    border: none;
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
  }

  .dropdown-item:hover {
    background: var(--bg-surface-hover);
  }

  .dropdown-item.active {
    color: var(--accent-blue);
    font-weight: 500;
  }

  .repo-group {
    display: flex;
    flex-direction: column;
  }

  .repo-row {
    display: flex;
    align-items: center;
  }

  .expand-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    margin-left: 4px;
    border: none;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    flex-shrink: 0;
    border-radius: var(--radius-sm);
  }

  .expand-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .expand-icon {
    transition: transform 0.15s;
  }
  .expanded {
    transform: rotate(90deg);
  }

  .repo-item {
    flex: 1;
    min-width: 0;
  }

  .branches {
    padding-left: 26px;
  }

  .branch-item {
    font-size: var(--font-size-xs);
    padding: 3px 10px;
    color: var(--text-secondary);
  }

  .branch-loading,
  .no-results {
    display: block;
    padding: 6px 10px;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .count {
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    flex-shrink: 0;
  }
</style>
