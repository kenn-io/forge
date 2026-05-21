<script lang="ts">
  import { onMount, tick } from "svelte";
  import { getStores } from "@middleman/ui";
  import { client } from "../api/runtime.js";
  import type { ConfigRepo, Repo } from "@middleman/ui/api/types";
  import { ChevronDownIcon } from "../icons.ts";
  import {
    parseRepoFilterValue,
    serializeRepoFilterValue,
  } from "../stores/filter.svelte.js";
  import { registerCheatsheetEntries } from "../stores/keyboard/registry.svelte.js";

  interface Props {
    selected: string | undefined;
    onchange: (repo: string | undefined) => void;
  }

  let { selected, onchange }: Props = $props();

  const stores = getStores();

  onMount(() =>
    registerCheatsheetEntries("repo-typeahead", [
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
    ]),
  );

  let fetchedRepos = $state<Repo[]>([]);
  let reposLoading = $state(false);
  let query = $state("");
  let open = $state(false);
  let highlightIndex = $state(0);
  let inputEl = $state<HTMLInputElement>();
  let containerEl = $state<HTMLDivElement>();
  let repoFetchVersion = 0;
  let latestRepoFetchKey = "";

  type RepoOption = { value: string; owner: string; name: string };

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
    return {
      value: `${repo.PlatformHost}/${repo.Owner}/${repo.Name}`,
      owner: repo.Owner,
      name: repo.Name,
    };
  }

  function optionFromConfigRepo(repo: ConfigRepo): RepoOption {
    const path = repo.repo_path || `${repo.owner}/${repo.name}`;
    return {
      value: `${repo.platform_host}/${path}`,
      owner: repo.owner,
      name: repo.name,
    };
  }

  function mergeOptions(
    configured: ConfigRepo[],
    fetched: Repo[],
  ): RepoOption[] {
    const merged: RepoOption[] = [];
    const seen: string[] = [];
    const addOption = (option: RepoOption) => {
      if (seen.includes(option.value)) return;
      seen.push(option.value);
      merged.push(option);
    };

    for (const repo of configured.filter((entry) => !entry.is_glob)) {
      addOption(optionFromConfigRepo(repo));
    }

    for (const repo of fetched) {
      addOption(optionFromRepo(repo));
    }

    return merged;
  }

  const options = $derived.by(() => {
    if (settingsLoaded || configuredRepos.length > 0) {
      return mergeOptions(configuredRepos, fetchedRepos);
    }
    return fetchedRepos.map(optionFromRepo);
  });

  const filtered = $derived.by(() => {
    if (!query) return options;
    const q = query.toLowerCase();
    return options.filter(
      (o) => o.value.toLowerCase().includes(q),
    );
  });

  const selectedValues = $derived(parseRepoFilterValue(selected));
  const selectedSet = $derived(new Set(selectedValues));
  const displayValue = $derived.by(() => {
    if (selectedValues.length === 0) return "All repos";
    if (selectedValues.length === 1) return selectedValues[0];
    return `${selectedValues.length} repos`;
  });

  $effect(() => {
    if (selectedValues.length === 0 || reposLoading) return;
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

  function toggleRepo(value: string) {
    const next = selectedSet.has(value)
      ? selectedValues.filter((repo) => repo !== value)
      : [...selectedValues, value];
    onchange(serializeRepoFilterValue(next));
  }

  function handleKeydown(e: KeyboardEvent) {
    const total = filtered.length + 1;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      highlightIndex = Math.min(highlightIndex + 1, total - 1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      highlightIndex = Math.max(highlightIndex - 1, 0);
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (highlightIndex === 0) {
        clearSelection();
      } else {
        const item = filtered[highlightIndex - 1];
        if (item) toggleRepo(item.value);
      }
    } else if (e.key === " ") {
      e.preventDefault();
      if (highlightIndex === 0) clearSelection();
      else {
        const item = filtered[highlightIndex - 1];
        if (item) toggleRepo(item.value);
      }
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
        <input
          class="typeahead-checkbox"
          type="checkbox"
          checked={selectedValues.length === 0}
          tabindex="-1"
          aria-hidden="true"
        />
        <span>All repos</span>
      </li>
      {#each filtered as option, i (option.value)}
        <li
          class="typeahead-option"
          class:highlighted={i + 1 === highlightIndex}
          class:selected={selectedSet.has(option.value)}
          role="option"
          aria-selected={selectedSet.has(option.value)}
          onmousedown={() => toggleRepo(option.value)}
          onmouseenter={() => (highlightIndex = i + 1)}
        >
          <input
            class="typeahead-checkbox"
            type="checkbox"
            checked={selectedSet.has(option.value)}
            tabindex="-1"
            aria-hidden="true"
          />
          <span class="typeahead-option-label">
            {#each highlightSegments(option.value, query) as seg, segIndex (`${option.value}-${segIndex}-${seg.text}-${seg.match}`)}{#if seg.match}<mark class="match">{seg.text}</mark>{:else}{seg.text}{/if}{/each}
          </span>
        </li>
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

  .typeahead-option {
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

  .typeahead-checkbox {
    width: 12px;
    height: 12px;
    margin: 0;
    flex-shrink: 0;
    accent-color: var(--accent-blue);
    pointer-events: none;
  }

  .typeahead-option-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .typeahead-option.highlighted {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .typeahead-option.selected {
    color: var(--accent-blue);
    font-weight: 600;
  }

  .match {
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
