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
    avatarUrlForUser?: ((username: string) => string) | undefined;
    /// The query the current candidates were fetched for. When set,
    /// the exact-username entry row is withheld until the candidate
    /// list reflects the typed query, so a stale list cannot offer a
    /// name the server is about to return with canonical casing.
    candidatesQuery?: string;
    /// Notified as the filter text changes so the caller can fetch
    /// candidates matching the query from the server.
    onquery?: (query: string) => void;
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
    avatarUrlForUser = undefined,
    candidatesQuery = undefined,
    onquery = undefined,
    ontoggle,
    onclear = undefined,
    onclose,
  }: Props = $props();

  let query = $state("");
  let filterInput: HTMLInputElement | undefined = $state();
  let failedAvatarKeys = $state<string[]>([]);

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
  // The candidate source is synced history, so it cannot know every
  // valid provider username. Typing a name that matches no listed user
  // offers an exact-username entry; the provider rejects names that do
  // not exist.
  const freeEntryUser = $derived.by(() => {
    const trimmed = query.trim();
    if (trimmed === "") return null;
    if (loading) return null;
    if (candidatesQuery !== undefined && candidatesQuery !== trimmed) return null;
    const key = trimmed.toLowerCase();
    const listed = [...selected, ...candidates].some((name) => name.toLowerCase() === key);
    return listed ? null : trimmed;
  });

  function clearSelectedUsers(): void {
    if (pendingUser !== null || selectedNames.size === 0) return;
    void onclear?.();
  }

  function avatarKey(username: string): string {
    return username.toLowerCase();
  }

  function markAvatarFailed(username: string): void {
    const key = avatarKey(username);
    if (failedAvatarKeys.includes(key)) return;
    failedAvatarKeys = [...failedAvatarKeys, key];
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
      oninput={(event) => onquery?.(event.currentTarget.value.trim())}
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
      {@const avatarURL = avatarUrlForUser?.(username) ?? ""}
      {@const showAvatarImage = avatarURL !== "" && !failedAvatarKeys.includes(avatarKey(username))}
      <button
        type="button"
        class={["user-picker__row", { "user-picker__row--selected": isSelected }]}
        role="menuitemcheckbox"
        aria-checked={isSelected}
        disabled={pendingUser !== null}
        onclick={() => ontoggle(username)}
      >
        {#if showAvatarImage}
          <img
            class="user-picker__avatar"
            src={avatarURL}
            alt=""
            loading="lazy"
            aria-hidden="true"
            onerror={() => markAvatarFailed(username)}
          />
        {:else}
          <span class="user-picker__avatar" aria-hidden="true">{username.slice(0, 1).toUpperCase()}</span>
        {/if}
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
      {#if !freeEntryUser}
        <div class="user-picker__empty">{loading ? "Loading users…" : "No users found"}</div>
      {/if}
    {/each}
    {#if freeEntryUser}
      <button
        type="button"
        class="user-picker__row user-picker__row--free-entry"
        role="menuitemcheckbox"
        aria-checked="false"
        disabled={pendingUser !== null}
        onclick={() => ontoggle(freeEntryUser)}
      >
        <span class="user-picker__avatar" aria-hidden="true">@</span>
        <span class="user-picker__name">Add “{freeEntryUser}”</span>
        <span class="user-picker__status">
          {#if pendingUser === freeEntryUser}
            <span class="user-picker__pending">Saving…</span>
          {/if}
        </span>
      </button>
    {/if}
  </div>
</div>

<style>
  .user-picker {
    width: 100%;
    min-width: 0;
    max-height: var(--user-picker-max-height, min(300px, calc(100dvh - 64px)));
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
    min-height: 28px;
    padding: 3px 6px 3px 10px;
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
    width: 22px;
    height: 22px;
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
    padding: 6px;
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
    padding: 3px 8px;
    font: inherit;
    font-size: var(--font-size-sm);
    min-height: 26px;
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
    grid-template-columns: 16px minmax(0, 1fr) 40px;
    align-items: center;
    gap: 8px;
    border: 0;
    background: transparent;
    color: inherit;
    cursor: pointer;
    min-height: 28px;
    padding: 2px 8px 2px 10px;
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

  .user-picker__row--free-entry {
    color: var(--accent-blue);
  }

  .user-picker__row--free-entry .user-picker__avatar {
    color: var(--accent-blue);
  }

  .user-picker__row:disabled {
    cursor: wait;
    opacity: 0.7;
  }

  .user-picker__avatar {
    width: 16px;
    height: 16px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: 999px;
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 700;
    object-fit: cover;
  }

  .user-picker__name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
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
    padding: 12px 10px;
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
