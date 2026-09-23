<script lang="ts">
  import { autoReposition, Checkbox, dismissable, floatingPopoverStyle, SearchInput } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import { canonicalProvider, resolvedPlatformHost } from "../../api/provider-routes.js";
  import type { Issue, PullRequest } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppExecution } from "../../app/runtime.js";
  import type { NumberedRouteItemRef } from "../../routes.js";
  import { repoIdentityKey } from "../../utils/repo-label.js";

  type ItemType = "pr" | "issue";
  type SearchOption = {
    key: string;
    itemType: ItemType;
    item: NumberedRouteItemRef | null;
    label: string;
    state?: string;
  };

  const { workspaceID, viewedPR, viewedIssue, hasLinkedPR, hasLinkedIssue, disabled, searchAnchor, onSearchClose, onselect }:
    {
      workspaceID: string;
      viewedPR: NumberedRouteItemRef | null;
      viewedIssue: NumberedRouteItemRef | null;
      hasLinkedPR: boolean;
      hasLinkedIssue: boolean;
      disabled: boolean;
      searchAnchor: HTMLElement;
      onSearchClose: () => void;
      onselect: (itemType: ItemType, item: NumberedRouteItemRef | null) => void;
    } = $props();

  const runtime = getAppRuntime();
  let options = $state.raw<SearchOption[]>([]);
  let loading = $state(false);
  let error = $state("");
  let query = $state("");
  let includeClosed = $state(false);
  let highlightIndex = $state(0);
  const listID = $props.id();
  const resetRows = $derived<SearchOption[]>([
    ...(viewedPR !== null ? [{ key: "reset-pr", itemType: "pr" as const, item: null, label: hasLinkedPR ? "Use linked PR" : "Clear PR selection" }] : []),
    ...(viewedIssue !== null ? [{ key: "reset-issue", itemType: "issue" as const, item: null, label: hasLinkedIssue ? "Use linked issue" : "Clear issue selection" }] : []),
  ]);
  const rows = $derived([
    ...resetRows,
    ...options,
  ]);
  const activeIndex = $derived(Math.min(highlightIndex, Math.max(0, rows.length - 1)));
  let searchExecution: AppExecution<unknown, unknown> | undefined;

  function itemKey(itemType: ItemType, item: NumberedRouteItemRef | null): string {
    return item === null ? "" : JSON.stringify([itemType, repoIdentityKey({
      ...item,
      provider: canonicalProvider(item.provider),
      platformHost: resolvedPlatformHost(item.provider, item.platformHost),
    }), item.number]);
  }

  function searchOption(itemType: ItemType, result: PullRequest | Issue): SearchOption {
    const item: NumberedRouteItemRef = {
      provider: result.repo.provider,
      platformHost: result.repo.platform_host,
      platformRepoId: result.repo.platform_repo_id,
      owner: result.repo.owner,
      name: result.repo.name,
      repoPath: result.repo.repo_path,
      number: result.Number,
    };
    return {
      key: itemKey(itemType, item),
      itemType,
      item,
      label: `#${item.number} · ${result.Title}`,
      state: result.State === "merged" ? "Merged" : result.State === "closed" ? "Closed" : "IsDraft" in result && result.IsDraft ? "Draft" : "Open",
    };
  }

  function selectItem(option: SearchOption): void {
    onselect(option.itemType, option.item);
    searchAnchor.focus();
    onSearchClose();
  }

  function searchItems(query: string): void {
    searchExecution?.interrupt();
    highlightIndex = resetRows.length;
    loading = true;
    error = "";
    options = [];
    const params = { state: includeClosed ? "all" : "open", q: query.trim(), limit: 30 };
    searchExecution = runtime.runCommand(
      Effect.sleep("150 millis").pipe(
        Effect.andThen(Effect.all({
          pulls: executeGeneratedApiRequest("search workspace pull requests", (client, signal) =>
            client.PullRequestsService.listPulls(params, { signal }),
          ),
          issues: executeGeneratedApiRequest("search workspace issues", (client, signal) =>
            client.IssuesService.listIssues(params, { signal }),
          ),
        }, { concurrency: "unbounded" })),
        Effect.tap(({ pulls, issues }) => Effect.sync(() => {
          options = [
            ...pulls.map((pull) => searchOption("pr", pull)),
            ...issues.map((issue) => searchOption("issue", issue)),
          ];
        })),
        Effect.ensuring(Effect.sync(() => { loading = false; })),
      ),
      {
        operation: "search workspace PRs and issues",
        safeContext: { workspaceID },
        onFailure: () => { error = "Could not load PRs and issues. Type to retry."; },
      },
    );
  }

  function mountSearchPopover(node: HTMLDivElement): () => void {
    const anchor = searchAnchor;
    query = "";
    untrack(() => searchItems(""));
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
        selectItem(selected);
      }
    }
  }

  onDestroy(() => {
    searchExecution?.interrupt();
    onSearchClose();
  });
</script>

{#if !disabled}
  <div class="item-search-popover kit-popover-card" role="dialog" aria-label="Search PRs and issues" {@attach mountSearchPopover}>
    <!-- kit-ui-check-ignore: this toolbar popover needs an icon trigger and an immediately focused search input, which Typeahead does not expose -->
    <SearchInput role="combobox"
      bind:value={query}
      size="sm"
      block
      autofocus
      ariaLabel="Search PRs and issues"
      placeholder={includeClosed ? "Search PRs and issues across repositories" : "Search open PRs and issues across repositories"}
      ariaExpanded={true}
      ariaControls={listID}
      {...(!loading && rows[activeIndex] ? { ariaActivedescendant: `${listID}-${activeIndex}` } : {})}
      ariaAutocomplete="list"
      oninput={searchItems}
      onkeydown={handleSearchKeydown}
    />
    <div class="item-search-filter">
      <Checkbox
        checked={includeClosed}
        label="Include closed"
        onchange={(checked) => { includeClosed = checked; searchItems(query); }}
      />
    </div>
    {#if error}<div class="item-search-message" role="alert">{error}</div>{/if}
    {#if loading}<div class="item-search-message" role="status">Loading PRs and issues…</div>{/if}
    <!-- kit-ui-check-ignore: same toolbar popover exception as the combobox above -->
    <div id={listID} class="item-search-results" role="listbox" aria-label="PRs and issues" aria-busy={loading}>
      {#if !loading}
        {#each rows as option, index (option.key)}
          <button
            id={`${listID}-${index}`}
            class="item-search-option"
            class:highlighted={index === activeIndex}
            type="button"
            role="option"
            aria-selected={option.key === itemKey(option.itemType, option.itemType === "pr" ? viewedPR : viewedIssue)}
            onmouseenter={() => { highlightIndex = index; }}
            onclick={() => selectItem(option)}
          >
            <span class="item-search-label">
              <span>{option.label}</span>
              {#if option.item}<small>{option.itemType === "pr" ? "PR" : "Issue"} · {option.item.platformHost}/{option.item.repoPath}</small>{/if}
            </span>
            {#if option.state}<small>{option.state}</small>{/if}
          </button>
        {:else}
          <div class="item-search-message">No PRs or issues found</div>
        {/each}
      {/if}
    </div>
  </div>
{/if}
<style>
  .item-search-popover {
    position: fixed;
    z-index: var(--z-popover);
    padding: var(--space-2);
  }

  .item-search-filter {
    margin-top: var(--space-2);
  }

  .item-search-results {
    max-height: min(360px, 60vh);
    overflow-y: auto;
    margin-top: var(--space-1);
  }

  .item-search-option {
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

  .item-search-option.highlighted,
  .item-search-option:hover {
    background: var(--bg-surface-hover);
  }

  .item-search-label {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }

  .item-search-label span,
  .item-search-label small {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .item-search-option small,
  .item-search-message {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .item-search-message {
    padding: var(--space-2);
  }

</style>
