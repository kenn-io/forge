<script lang="ts">
  import { getStores } from "@middleman/ui";
  import type { ConfigRepo } from "@middleman/ui/api/types";
  import RepoTypeahead from "../RepoTypeahead.svelte";

  const stores = getStores();

  let blankSelection = $state<string | undefined>(undefined);
  let repoSelection = $state<string | undefined>(undefined);
  let repoSelectionChanged = $state(false);

  const configuredRepoSelection = $derived.by(() => {
    const repo = stores?.settings?.getConfiguredRepos?.().find((entry) => !entry.is_glob);
    return repo ? repoValue(repo) : undefined;
  });

  const selectedRepo = $derived(
    repoSelectionChanged ? repoSelection : configuredRepoSelection,
  );

  function repoValue(repo: ConfigRepo): string {
    const path = repo.repo_path || `${repo.owner}/${repo.name}`;
    return `${repo.platform_host}/${path}`;
  }

  function displaySelection(value: string | undefined): string {
    return value ?? "All repos";
  }

  function updateRepoSelection(value: string | undefined): void {
    repoSelectionChanged = true;
    repoSelection = value;
  }
</script>

<div class="typeahead-demo-grid" data-testid="design-system-typeahead-demo">
  <section
    class="typeahead-example"
    aria-labelledby="typeahead-empty-title"
    data-testid="typeahead-default"
  >
    <div class="example-copy">
      <h3 id="typeahead-empty-title">Default trigger</h3>
      <p>{displaySelection(blankSelection)}</p>
    </div>
    <RepoTypeahead
      selected={blankSelection}
      onchange={(repo) => (blankSelection = repo)}
    />
  </section>

  <section
    class="typeahead-example"
    aria-labelledby="typeahead-selected-title"
    data-testid="typeahead-selected"
  >
    <div class="example-copy">
      <h3 id="typeahead-selected-title">Selected repo</h3>
      <p>{displaySelection(selectedRepo)}</p>
    </div>
    <RepoTypeahead
      selected={selectedRepo}
      onchange={updateRepoSelection}
    />
  </section>

  <section
    class="typeahead-example open-state"
    aria-labelledby="typeahead-open-title"
    data-testid="typeahead-open"
  >
    <div class="example-copy">
      <h3 id="typeahead-open-title">Open dropdown</h3>
      <p>{displaySelection(selectedRepo)}</p>
    </div>
    <RepoTypeahead
      selected={selectedRepo}
      onchange={updateRepoSelection}
      initialOpen={true}
    />
  </section>
</div>

<style>
  .typeahead-demo-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 16px;
    align-items: start;
  }

  .typeahead-example {
    min-width: 0;
    border: 1px solid var(--border-muted);
    border-radius: 8px;
    background: var(--bg-inset);
    padding: 16px;
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(180px, 260px);
    gap: 14px;
    align-items: center;
  }

  .typeahead-example.open-state {
    min-height: 148px;
    align-items: start;
  }

  .example-copy {
    min-width: 0;
    display: grid;
    gap: 4px;
  }

  .example-copy h3,
  .example-copy p {
    margin: 0;
  }

  .example-copy h3 {
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    line-height: 1.3;
  }

  .example-copy p {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.4;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  @media (max-width: 900px) {
    .typeahead-demo-grid {
      grid-template-columns: 1fr;
    }
  }

  @media (max-width: 640px) {
    .typeahead-example {
      grid-template-columns: 1fr;
      align-items: start;
    }
  }
</style>
