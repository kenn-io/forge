<script lang="ts">
  import { autoReposition, dismissable, EmptyState, floatingPopoverStyle, SearchInput, type TypeaheadOption } from "@kenn-io/kit-ui";
  import { Effect, Option, Schema } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import { canonicalProvider, resolvedPlatformHost, type ProviderRouteRef } from "../../api/provider-routes.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppExecution } from "../../app/runtime.js";
  import PullDetail from "../detail/PullDetail.svelte";

  const { workspaceID, workspaceHostKey, repo, linkedPRNumber, refreshToken, disabled, searchAnchor, onSearchClose }:
    {
      workspaceID: string;
      workspaceHostKey?: string | undefined;
      repo: ProviderRouteRef;
      linkedPRNumber: number | null;
      refreshToken: number;
      disabled: boolean;
      searchAnchor: HTMLElement | null;
      onSearchClose: () => void;
    } = $props();

  const runtime = getAppRuntime();
  const storageKey = $derived(`kenn-forge-workspace-viewed-pr:${JSON.stringify([workspaceHostKey ?? "self", workspaceID])}`);
  const PRNumber = Schema.NumberFromString.check(Schema.isInt(), Schema.isGreaterThan(0));
  let viewedPRNumber = $state<number | null>(untrack(readSelection));
  const displayedPRNumber = $derived(viewedPRNumber ?? linkedPRNumber);
  let options = $state.raw<TypeaheadOption[]>([]);
  let loading = $state(false);
  let error = $state("");
  let query = $state("");
  let highlightIndex = $state(0);
  const listID = $props.id();
  const rows = $derived<TypeaheadOption[]>([
    ...(viewedPRNumber !== null ? [{ name: "", label: "Use linked PR" }] : []),
    ...options,
  ]);
  const activeIndex = $derived(Math.min(highlightIndex, Math.max(0, rows.length - 1)));
  let searchExecution: AppExecution<unknown, unknown> | undefined;

  function readSelection(): number | null {
    try {
      return Option.getOrNull(Schema.decodeUnknownOption(PRNumber)(localStorage.getItem(storageKey)));
    } catch {
      return null;
    }
  }

  function selectPR(value: string): void {
    viewedPRNumber = value === "" ? null : Number(value);
    try {
      if (viewedPRNumber === null) localStorage.removeItem(storageKey);
      else localStorage.setItem(storageKey, String(viewedPRNumber));
    } catch {
      // Best-effort UI preference persistence; keep the in-memory selection.
    }
    searchAnchor?.focus();
    onSearchClose();
  }

  function searchPRs(query: string): void {
    searchExecution?.interrupt();
    highlightIndex = viewedPRNumber !== null ? 1 : 0;
    loading = true;
    error = "";
    options = [];
    const repoFilter = `${canonicalProvider(repo.provider)}|${resolvedPlatformHost(repo.provider, repo.platformHost)}/${repo.repoPath}`;
    searchExecution = runtime.runCommand(
      Effect.sleep("150 millis").pipe(
        Effect.andThen(executeGeneratedApiRequest("search workspace pull requests", (client, signal) =>
          client.PullRequestsService.listPulls({ repo: repoFilter, state: "all", q: query.trim(), limit: 30 }, { signal }),
        )),
        Effect.tap((pulls) => Effect.sync(() => {
          options = pulls.map((pull) => ({
            name: String(pull.Number),
            label: `#${pull.Number} · ${pull.Title}`,
            meta: pull.State === "merged" ? "Merged" : pull.State === "closed" ? "Closed" : pull.IsDraft ? "Draft" : "Open",
          }));
        })),
        Effect.ensuring(Effect.sync(() => { loading = false; })),
      ),
      {
        operation: "search workspace pull requests",
        safeContext: { workspaceID },
        onFailure: () => { error = "Could not load PRs. Type to retry."; },
      },
    );
  }

  function mountSearchPopover(node: HTMLDivElement): () => void {
    const anchor = searchAnchor!;
    query = "";
    searchPRs("");
    const position = () => {
      node.style.cssText = floatingPopoverStyle({
        trigger: anchor.getBoundingClientRect(),
        viewportWidth: window.innerWidth,
        viewportHeight: window.innerHeight,
        popoverWidth: node.offsetWidth,
        popoverHeight: node.offsetHeight,
        align: "end",
        maxWidth: 420,
        constrainWidth: true,
      });
    };
    position();
    const stopPositioning = autoReposition(() => [anchor, node], position);
    const stopDismiss = dismissable({
      owners: () => [anchor, node],
      dismiss: onSearchClose,
      escapeFocus: () => anchor,
    });
    return () => {
      stopPositioning();
      stopDismiss();
      searchExecution?.interrupt();
    };
  }

  function handleSearchKeydown(event: KeyboardEvent): void {
    if (loading) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      highlightIndex = Math.max(0, Math.min(rows.length - 1, highlightIndex + (event.key === "ArrowDown" ? 1 : -1)));
      document.getElementById(`${listID}-${highlightIndex}`)?.scrollIntoView?.({ block: "nearest" });
    } else if (event.key === "Enter") {
      const selected = rows[activeIndex];
      if (selected) {
        event.preventDefault();
        selectPR(selected.name);
      }
    }
  }

  onDestroy(() => {
    searchExecution?.interrupt();
    onSearchClose();
  });
</script>

{#if searchAnchor && !disabled}
  <div class="pr-search-popover kit-popover-card" role="dialog" aria-label="Search pull requests" {@attach mountSearchPopover}>
    <!-- kit-ui-check-ignore: this toolbar popover needs an icon trigger and an immediately focused search input, which Typeahead does not expose -->
    <SearchInput role="combobox"
      bind:value={query}
      size="sm"
      block
      autofocus
      ariaLabel="Search PRs"
      placeholder="Search PRs by number or title"
      ariaExpanded={true}
      ariaControls={listID}
      {...(!loading && rows[activeIndex] ? { ariaActivedescendant: `${listID}-${activeIndex}` } : {})}
      ariaAutocomplete="list"
      oninput={searchPRs}
      onkeydown={handleSearchKeydown}
    />
    {#if error}<div class="pr-search-message" role="alert">{error}</div>{/if}
    {#if loading}<div class="pr-search-message" role="status">Loading PRs…</div>{/if}
    <!-- kit-ui-check-ignore: same toolbar popover exception as the combobox above -->
    <div id={listID} class="pr-search-results" role="listbox" aria-label="Pull requests" aria-busy={loading}>
      {#if !loading}
        {#each rows as option, index (option.name)}
          <button
            id={`${listID}-${index}`}
            class="pr-search-option"
            class:highlighted={index === activeIndex}
            type="button"
            role="option"
            aria-selected={option.name === String(displayedPRNumber ?? "")}
            onmouseenter={() => { highlightIndex = index; }}
            onclick={() => selectPR(option.name)}
          >
            <span>{option.label}</span>
            {#if option.meta}<small>{option.meta}</small>{/if}
          </button>
        {:else}
          <div class="pr-search-message">No PRs found</div>
        {/each}
      {/if}
    </div>
  </div>
{/if}
{#if displayedPRNumber && displayedPRNumber > 0}
  {#key `${displayedPRNumber}:${refreshToken}`}
    <div class="pr-scroll" inert={disabled}>
      <PullDetail
        {...repo}
        number={displayedPRNumber}
        hideTabs={true}
        hideWorkspaceAction={true}
      />
    </div>
  {/key}
{:else}
  <EmptyState title="No linked PR" description="Search by number or title to choose a PR." />
{/if}

<style>
  .pr-search-popover {
    position: fixed;
    z-index: var(--z-popover);
    padding: var(--space-2);
  }

  .pr-search-results {
    max-height: min(360px, 60vh);
    overflow-y: auto;
    margin-top: var(--space-1);
  }

  .pr-search-option {
    display: flex;
    align-items: baseline;
    gap: var(--space-2);
    width: 100%;
    padding: var(--space-2);
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
  }

  .pr-search-option.highlighted,
  .pr-search-option:hover {
    background: var(--bg-surface-hover);
  }

  .pr-search-option span {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pr-search-option small,
  .pr-search-message {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .pr-search-message {
    padding: var(--space-2);
  }

  .pr-scroll {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
</style>
