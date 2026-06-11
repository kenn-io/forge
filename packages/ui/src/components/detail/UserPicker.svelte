<script lang="ts">
  import { onMount } from "svelte";
  import CheckIcon from "@lucide/svelte/icons/check";
  import EraserIcon from "@lucide/svelte/icons/eraser";
  import XIcon from "@lucide/svelte/icons/x";

  interface Props {
    title: string;
    candidates: string[];
    selected: string[];
    loading?: boolean;
    pendingUser?: string | null;
    error?: string | null;
    autofocusFilter?: boolean;
    ontoggle: (username: string) => void | Promise<void>;
    onclear?: () => void | Promise<void>;
    onclose: () => void;
  }

  const {
    title,
    candidates,
    selected,
    loading = false,
    pendingUser = null,
    error = null,
    autofocusFilter = false,
    ontoggle,
    onclear = undefined,
    onclose,
  }: Props = $props();

  let query = $state("");
  let filterInput: HTMLInputElement | undefined = $state();

  onMount(() => {
    if (autofocusFilter) filterInput?.focus();
  });

  const selectedNames = $derived(new Set(selected.map((name) => name.toLowerCase())));
  // Selected users always appear in the list even when they are not in
  // the candidate set (for example a user assigned outside middleman).
  const listedUsers = $derived.by(() => {
    const seen = new Set<string>();
    const users: string[] = [];
    for (const name of [...selected, ...candidates]) {
      const key = name.toLowerCase();
      if (name === "" || seen.has(key)) continue;
      seen.add(key);
      users.push(name);
    }
    const needle = query.trim().toLowerCase();
    if (needle === "") return users;
    return users.filter((name) => name.toLowerCase().includes(needle));
  });

  function clearSelectedUsers(): void {
    if (pendingUser !== null || selectedNames.size === 0) return;
    void onclear?.();
  }
</script>

<div class="user-picker" role="dialog" aria-label={title}>
  <div class="user-picker__header">
    <div class="user-picker__title">
      <strong>{title}</strong>
      {#if loading}
        <span class="user-picker__syncing">Loading…</span>
      {/if}
    </div>
    <div class="user-picker__header-actions">
      <button
        type="button"
        class="user-picker__icon-button"
        aria-label="Clear selected users"
        title="Clear selected users"
        disabled={pendingUser !== null || selectedNames.size === 0 || onclear === undefined}
        onclick={clearSelectedUsers}
      >
        <EraserIcon size="14" strokeWidth="2.2" aria-hidden="true" />
      </button>
      <button
        type="button"
        class="user-picker__icon-button"
        aria-label="Close user picker"
        onclick={onclose}
      >
        <XIcon size="15" strokeWidth="2.2" aria-hidden="true" />
      </button>
    </div>
  </div>

  <label class="user-picker__filter">
    <span class="user-picker__sr-only">Filter users</span>
    <input
      bind:this={filterInput}
      bind:value={query}
      type="search"
      placeholder="Filter users"
      aria-label="Filter users"
    />
  </label>

  {#if error}
    <div class="user-picker__error" role="alert">{error}</div>
  {/if}

  <div class="user-picker__list" role="menu" aria-label="Users">
    {#each listedUsers as username (username.toLowerCase())}
      {@const isSelected = selectedNames.has(username.toLowerCase())}
      <button
        type="button"
        class={["user-picker__row", { "user-picker__row--selected": isSelected }]}
        role="menuitemcheckbox"
        aria-checked={isSelected}
        disabled={pendingUser !== null}
        onclick={() => ontoggle(username)}
      >
        <span class="user-picker__avatar" aria-hidden="true">{username.slice(0, 1).toUpperCase()}</span>
        <span class="user-picker__name">{username}</span>
        <span class="user-picker__status">
          {#if pendingUser === username}
            <span class="user-picker__pending">Saving…</span>
          {:else if isSelected}
            <CheckIcon size="14" strokeWidth="2.4" aria-hidden="true" />
          {/if}
        </span>
      </button>
    {:else}
      <div class="user-picker__empty">{loading ? "Loading users…" : "No users found"}</div>
    {/each}
  </div>
</div>

<style>
  .user-picker {
    width: 100%;
    min-width: 0;
    max-height: var(--user-picker-max-height, min(390px, calc(100dvh - 64px)));
    display: flex;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
    color: var(--text-primary);
  }

  .user-picker__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 36px;
    padding: 5px 8px 5px 12px;
    border-bottom: 1px solid var(--border-muted);
  }

  .user-picker__title {
    min-width: 0;
    display: flex;
    align-items: baseline;
    gap: 6px;
    font-size: var(--font-size-sm);
  }

  .user-picker__syncing {
    margin-left: 4px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .user-picker__header-actions {
    display: inline-flex;
    align-items: center;
    gap: 2px;
  }

  .user-picker__icon-button {
    width: 26px;
    height: 26px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
    padding: 0;
    transition: background 0.1s, color 0.1s, border-color 0.1s;
  }

  .user-picker__icon-button:hover:not(:disabled),
  .user-picker__icon-button:focus-visible {
    border-color: var(--border-muted);
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .user-picker__icon-button:disabled {
    cursor: default;
    opacity: 0.42;
  }

  .user-picker__filter {
    display: block;
    padding: 8px;
    border-bottom: 1px solid var(--border-muted);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .user-picker__filter input {
    width: 100%;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    color: var(--text-primary);
    padding: 6px 9px;
    font: inherit;
    min-height: 32px;
    outline: none;
  }

  .user-picker__filter input:focus {
    border-color: var(--accent-blue);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 18%, transparent);
  }

  .user-picker__error {
    margin: 6px 8px 0;
    border: 1px solid var(--accent-red);
    border-radius: var(--radius-sm);
    color: var(--accent-red);
    padding: 6px 8px;
    font-size: var(--font-size-sm);
  }

  .user-picker__list {
    overflow: auto;
    padding: 3px 0;
  }

  .user-picker__row {
    width: 100%;
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr) 48px;
    align-items: center;
    gap: 9px;
    border: 0;
    background: transparent;
    color: inherit;
    cursor: pointer;
    min-height: 36px;
    padding: 4px 8px 4px 12px;
    text-align: left;
    transition: background 0.08s, color 0.08s;
  }

  .user-picker__row:hover:not(:disabled),
  .user-picker__row:focus-visible {
    background: var(--bg-surface-hover);
    outline: none;
  }

  .user-picker__row--selected {
    background: color-mix(in srgb, var(--accent-blue) 7%, transparent);
  }

  .user-picker__row:disabled {
    cursor: wait;
    opacity: 0.7;
  }

  .user-picker__avatar {
    width: 18px;
    height: 18px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: 999px;
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 700;
  }

  .user-picker__name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 600;
    font-size: var(--font-size-sm);
    line-height: 1.2;
  }

  .user-picker__status {
    min-width: 0;
    display: flex;
    justify-content: flex-end;
    align-items: center;
    color: var(--accent-green);
    font-size: var(--font-size-xs);
  }

  .user-picker__pending {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .user-picker__empty {
    padding: 18px 12px;
    color: var(--text-secondary);
    text-align: center;
    font-size: var(--font-size-sm);
  }

  .user-picker__sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
</style>
