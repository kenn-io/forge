<script lang="ts">
  import { onMount, tick } from "svelte";
  import {
    canonicalRepoFilterValue,
    displayRepoFilterValue,
    getStores,
    normalizeRepoFilterSelection,
  } from "@middleman/ui";
  import { client } from "../api/runtime.js";
  import type { ConfigRepo, Repo } from "@middleman/ui/api/types";
  import { canonicalProvider } from "@middleman/ui/api/provider-routes";
  import type { RepoTreeOption } from "./repoTree.js";
  import RepoTreeNode from "./RepoTreeNode.svelte";
  import TreeCheckbox from "./TreeCheckbox.svelte";
  import {
    buildRepoTree,
    visibleRows,
    nodeSelectionState,
    toggleSubtree,
    type VisibleRow,
  } from "./repoTree.js";
  import { createRepoTreeExpansionStore } from "../stores/repoTreeExpansion.svelte.js";
  import { ChevronDownIcon } from "../icons.ts";
  import {
    parseRepoFilterValue,
    serializeRepoFilterValue,
  } from "../stores/filter.svelte.js";
  import { registerCheatsheetEntries } from "../stores/keyboard/registry.svelte.js";

  interface Props {
    selected: string | undefined;
    onchange: (repo: string | undefined) => void;
    initialOpen?: boolean;
  }

  let { selected, onchange, initialOpen = false }: Props = $props();

  const stores = getStores();

  onMount(() => {
    if (initialOpen) open = true;

    return registerCheatsheetEntries("repo-typeahead", [
      {
        id: "repo-typeahead.next",
        label: "Next repo",
        binding: { key: "ArrowDown" },
        scope: "view-pulls",
      },
      {
        id: "repo-typeahead.prev",
        label: "Previous repo",
        binding: { key: "ArrowUp" },
        scope: "view-pulls",
      },
      {
        id: "repo-typeahead.expand",
        label: "Expand / collapse group",
        binding: { key: "ArrowRight" },
        scope: "view-pulls",
      },
      {
        id: "repo-typeahead.toggle-select",
        label: "Select / deselect",
        binding: { key: " " },
        scope: "view-pulls",
      },
    ]);
  });

  let fetchedRepos = $state<Repo[]>([]);
  let reposLoading = $state(false);
  let query = $state("");
  let open = $state(false);
  let highlightIndex = $state(0);
  let inputEl = $state<HTMLInputElement>();
  let containerEl = $state<HTMLDivElement>();
  let repoFetchVersion = 0;
  let latestRepoFetchKey = "";

  type RepoOption = RepoTreeOption & { repoPath: string };

  $effect(() => {
    const configuredRepoKey = configuredRepos
      .map((repo) => `${repo.provider}/${repo.platform_host}/${repo.repo_path || `${repo.owner}/${repo.name}`}`)
      .join("\0");
    const fetchKey = `${++repoFetchVersion}:${settingsLoaded}:${configuredRepoKey}`;

    latestRepoFetchKey = fetchKey;
    reposLoading = true;
    fetchedRepos = [];

    void client.GET("/repos").then(({ data, error }) => {
      if (fetchKey !== latestRepoFetchKey) return;
      reposLoading = false;
      if (error) return;
      fetchedRepos = data ?? [];
    });
  });

  const configuredRepos = $derived(
    stores?.settings?.getConfiguredRepos?.() ?? [],
  );
  const settingsLoaded = $derived(
    stores?.settings?.isSettingsLoaded?.() ?? false,
  );

  function optionFromRepo(repo: Repo): RepoOption {
    const repoPath = `${repo.Owner}/${repo.Name}`;
    return {
      value: `${repo.PlatformHost}/${repoPath}`,
      owner: repo.Owner,
      name: repo.Name,
      provider: canonicalProvider(repo.Platform),
      platformHost: repo.PlatformHost,
      repoPath,
    };
  }

  function optionFromConfigRepo(repo: ConfigRepo): RepoOption | null {
    if (repo.is_glob) return null;
    const path = repo.repo_path || `${repo.owner}/${repo.name}`;
    if (!repo.platform_host || !path) return null;
    return {
      value: `${repo.platform_host}/${path}`,
      owner: repo.owner,
      name: repo.name,
      provider: canonicalProvider(repo.provider),
      platformHost: repo.platform_host,
      repoPath: path,
    };
  }

  function mergeOptions(
    configured: ConfigRepo[],
    fetched: Repo[],
  ): RepoOption[] {
    const merged: RepoOption[] = [];
    const seen: string[] = [];
    const addOption = (option: RepoOption) => {
      const identity = `${option.provider}|${option.platformHost}/${option.repoPath}`;
      if (seen.includes(identity)) return;
      seen.push(identity);
      merged.push(option);
    };

    for (const repo of configured) {
      const option = optionFromConfigRepo(repo);
      if (option) addOption(option);
    }

    for (const repo of fetched) {
      addOption(optionFromRepo(repo));
    }

    const identities = merged.map((option) => ({
      provider: option.provider,
      platformHost: option.platformHost,
      repoPath: option.repoPath,
      isGlob: false,
    }));
    return merged.map((option, index) => ({
      ...option,
      value: canonicalRepoFilterValue(identities[index]!, identities) ?? option.value,
    }));
  }

  const options = $derived.by(() => {
    if (settingsLoaded || configuredRepos.length > 0) {
      return mergeOptions(configuredRepos, fetchedRepos);
    }
    return fetchedRepos.map(optionFromRepo);
  });

  const selectedValues = $derived(parseRepoFilterValue(selected));
  const selectedSet = $derived(new Set(selectedValues));
  const displayValue = $derived.by(() => {
    if (selectedValues.length === 0) return "All repos";
    if (selectedValues.length === 1) return displayRepoFilterValue(selectedValues[0]!);
    return `${selectedValues.length} repos`;
  });

  const expansion = createRepoTreeExpansionStore();

  const tree = $derived(buildRepoTree(options));

  const rows = $derived(
    visibleRows(tree, { isCollapsed: expansion.isCollapsed, query }),
  );

  function rowAriaLabel(row: VisibleRow): string {
    return row.node.kind === "host" ? row.node.platformHost : displayRepoFilterValue(row.node.id);
  }

  function toggleRowSelect(row: VisibleRow) {
    onchange(serializeRepoFilterValue(toggleSubtree(row.node, selectedValues)));
  }

  function toggleRowExpand(row: VisibleRow) {
    if (row.hasChildren) expansion.toggle(row.node.id);
  }

  $effect(() => {
    if (selectedValues.length === 0 || reposLoading) return;
    const normalized = normalizeRepoFilterSelection(
      selected,
      options.map((option) => ({
        provider: option.provider,
        platformHost: option.platformHost,
        repoPath: option.repoPath,
        isGlob: false,
      })),
    );
    if (normalized !== selected) {
      onchange(normalized);
      return;
    }
    const validValues = new Set(options.map((option) => option.value));
    const next = selectedValues.filter((value) => validValues.has(value));
    if (next.length === selectedValues.length) return;
    onchange(serializeRepoFilterValue(next));
  });

  async function openDropdown() {
    query = "";
    open = true;
    highlightIndex = 0;
    await tick();
    inputEl?.focus();
  }

  function closeDropdown() {
    open = false;
    query = "";
  }

  function clearSelection() {
    onchange(undefined);
  }

  function handleKeydown(e: KeyboardEvent) {
    const total = rows.length + 1; // +1 for the "All repos" row at index 0
    if (e.key === "ArrowDown") {
      e.preventDefault();
      highlightIndex = Math.min(highlightIndex + 1, total - 1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      highlightIndex = Math.max(highlightIndex - 1, 0);
    } else if (e.key === "ArrowRight") {
      e.preventDefault();
      const row = rows[highlightIndex - 1];
      if (row?.hasChildren && !row.expanded) expansion.toggle(row.node.id);
    } else if (e.key === "ArrowLeft") {
      e.preventDefault();
      const idx = highlightIndex - 1;
      const row = rows[idx];
      if (row?.hasChildren && row.expanded) {
        expansion.toggle(row.node.id);
      } else if (row) {
        // On a leaf (or an already-collapsed group), move focus to the parent:
        // the nearest preceding visible row at a shallower depth.
        for (let i = idx - 1; i >= 0; i -= 1) {
          const candidate = rows[i];
          if (candidate && candidate.depth < row.depth) {
            highlightIndex = i + 1;
            break;
          }
        }
      }
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (highlightIndex === 0) {
        clearSelection();
        return;
      }
      const row = rows[highlightIndex - 1];
      if (!row) return;
      if (row.hasChildren) expansion.toggle(row.node.id);
      else toggleRowSelect(row);
    } else if (e.key === " ") {
      e.preventDefault();
      if (highlightIndex === 0) {
        clearSelection();
        return;
      }
      const row = rows[highlightIndex - 1];
      if (row) toggleRowSelect(row);
    } else if (e.key === "Escape") {
      closeDropdown();
    }
  }

  function handleInput() {
    highlightIndex = 0;
  }

  function highlightSegments(
    text: string, q: string,
  ): { text: string; match: boolean }[] {
    if (!q) return [{ text, match: false }];
    const idx = text.toLowerCase().indexOf(q.toLowerCase());
    if (idx === -1) return [{ text, match: false }];
    return [
      ...(idx > 0
        ? [{ text: text.slice(0, idx), match: false }]
        : []),
      { text: text.slice(idx, idx + q.length), match: true },
      ...(idx + q.length < text.length
        ? [{ text: text.slice(idx + q.length), match: false }]
        : []),
    ];
  }

  function handleBlur(e: FocusEvent) {
    const related = e.relatedTarget as Node | null;
    if (containerEl && related && containerEl.contains(related)) {
      return;
    }
    closeDropdown();
  }

  function preventBlur(e: MouseEvent) {
    e.preventDefault();
  }
</script>

<div class="typeahead" bind:this={containerEl}>
  {#if open}
    <input
      bind:this={inputEl}
      class="typeahead-input"
      type="text"
      bind:value={query}
      oninput={handleInput}
      onkeydown={handleKeydown}
      onblur={handleBlur}
      placeholder="Filter repos..."
      aria-label="Filter repos"
      autocomplete="off"
    />
    <ul class="typeahead-list" role="listbox" onmousedown={preventBlur}>
      <li
        class="typeahead-option"
        class:highlighted={highlightIndex === 0}
        class:selected={selectedValues.length === 0}
        role="option"
        aria-selected={selectedValues.length === 0}
        onmousedown={clearSelection}
        onmouseenter={() => (highlightIndex = 0)}
      >
        <TreeCheckbox
          value={selectedValues.length === 0 ? "checked" : "unchecked"}
          decorative
        />
        <span>All repos</span>
      </li>
      {#each rows as row, i (row.node.id)}
        <RepoTreeNode
          kind={row.node.kind}
          label={row.displayLabel ?? row.node.label}
          ariaLabel={rowAriaLabel(row)}
          provider={row.node.kind === "host" ? row.node.provider : undefined}
          depth={row.depth}
          hasChildren={row.hasChildren}
          expanded={row.expanded}
          selectionState={nodeSelectionState(row.node, selectedSet)}
          highlighted={i + 1 === highlightIndex}
          segments={query !== "" && row.node.kind === "repo"
            ? highlightSegments(row.displayLabel ?? row.node.label, query)
            : undefined}
          onToggleExpand={() => toggleRowExpand(row)}
          onToggleSelect={() => toggleRowSelect(row)}
          onHover={() => (highlightIndex = i + 1)}
        />
      {:else}
        <li class="typeahead-empty">No matching repos</li>
      {/each}
    </ul>
  {:else}
    <button class="typeahead-trigger" onclick={openDropdown} title="Select repository">
      <span class="typeahead-value">{displayValue}</span>
      <ChevronDownIcon
        class="typeahead-chevron"
        size="10"
        strokeWidth="2"
        aria-hidden="true"
      />
    </button>
  {/if}
</div>

<style>
  .typeahead {
    position: relative;
    min-width: 160px;
    max-width: 260px;
  }

  .typeahead-trigger {
    height: 26px;
    width: 100%;
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 0 8px;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    cursor: pointer;
    transition: border-color 0.15s;
    text-align: left;
  }

  .typeahead-trigger:hover {
    border-color: var(--border-default);
  }

  .typeahead-value {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  :global(.typeahead-chevron) {
    flex-shrink: 0;
    opacity: 0.5;
  }

  .typeahead-input {
    height: 26px;
    width: 100%;
    padding: 0 8px;
    background: var(--bg-inset);
    border: 1px solid var(--accent-blue);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-xs);
    color: var(--text-primary);
    outline: none;
    box-sizing: border-box;
  }

  .typeahead-input::placeholder {
    color: var(--text-muted);
  }

  .typeahead-list {
    position: absolute;
    top: 100%;
    left: 0;
    right: auto;
    min-width: 100%;
    width: max-content;
    max-width: min(520px, 90vw);
    margin-top: 2px;
    max-height: 50vh;
    overflow-y: auto;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-md);
    z-index: 100;
    list-style: none;
    padding: 2px;
  }

  /* RepoTreeNode renders rows as a child component, so the shared row, */
  /* checkbox, and match-highlight rules are scoped to descendants of */
  /* the RepoTypeahead-owned .typeahead-list rather than this component */
  /* alone. The :global() escape keeps them off the rest of the app. */
  .typeahead-list :global(.typeahead-option) {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px 8px;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    cursor: pointer;
    border-radius: 3px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .typeahead-list :global(.typeahead-option.highlighted) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .typeahead-list :global(.typeahead-option.selected) {
    color: var(--accent-blue);
    font-weight: 600;
  }

  .typeahead-list :global(.match) {
    background: color-mix(in srgb, var(--accent-blue) 40%, transparent);
    color: var(--accent-blue);
    font-weight: 600;
    border-radius: 1px;
  }

  .typeahead-empty {
    padding: 6px 8px;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    font-style: italic;
  }
</style>
