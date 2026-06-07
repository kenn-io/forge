<script lang="ts">
  import { tick, untrack } from "svelte";

  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
  import type { ModalFrameAction } from "@middleman/ui/stores/keyboard/keyspec";
  import { getStores, ItemStateChip } from "@middleman/ui";
  import { timeAgo } from "@middleman/ui/utils/time";
  import type { Issue, PullRequest } from "@middleman/ui/api/types";
  import {
    buildIssueRoute,
    buildPullRequestRoute,
    type RoutedItemRef,
  } from "@middleman/ui/routes";
  import {
    closePalette,
    isPaletteOpen,
  } from "../../stores/keyboard/palette-state.svelte.js";
  import { buildContext } from "../../stores/keyboard/context.svelte.js";
  import { handleCommandResult } from "../../stores/keyboard/dispatch.svelte.js";
  import { getAllActions } from "../../stores/keyboard/registry.svelte.js";
  import { isActionVisible } from "../../stores/keyboard/visibility.js";
  import {
    groupResults,
    parsePaletteQuery,
  } from "../../stores/keyboard/palette-search.svelte.js";
  import {
    pruneStale,
    readRecents,
    writeRecent,
  } from "../../stores/keyboard/recents.svelte.js";
  import { navigate } from "../../stores/router.svelte.js";
  import type { Action, RecentsState } from "../../stores/keyboard/types.js";

  const RECENT_CAP = 8;

  // getStores() returns undefined when the palette is mounted outside the
  // <Provider> context (notably the unit-test fixture in
  // Palette.svelte.test.ts). In that case the search inputs collapse to empty
  // arrays and the palette still renders so downstream tests can drive the
  // shell without setting up a full app context.
  const stores = getStores() as
    | ReturnType<typeof getStores>
    | undefined;

  let dialogEl: HTMLDivElement | undefined = $state();
  let inputEl: HTMLInputElement | undefined = $state();
  let query = $state("");
  let highlightIndex = $state(0);
  let recents = $state<RecentsState>({ version: 1, items: [] });

  const parsed = $derived(parsePaletteQuery(query));

  // Hydrate the recents snapshot once per palette open: prune stale entries
  // (>30 days) first, then capture the post-prune state. We deliberately don't
  // re-read on every render because each readRecents() call hits localStorage
  // and rebuilds the items array, which would defeat the snapshot semantics
  // expected by the recently-used section.
  $effect(() => {
    if (!isPaletteOpen()) return;
    untrack(() => {
      pruneStale();
      recents = readRecents();
    });
  });
  const visibleCommands = $derived.by(() => {
    if (!stores) {
      // Test-fixture path: with no Provider there is no Context to evaluate
      // `when` predicates against, so surface every registered action so the
      // unit tests can drive preview/highlight behavior without standing up
      // the full app context. Production callers always provide stores.
      return getAllActions();
    }
    const ctx = buildContext(stores);
    return getAllActions().filter((a) => isActionVisible(a, ctx));
  });
  const grouped = $derived(
    groupResults({
      commands: visibleCommands,
      pulls: stores ? stores.pulls.getPulls() : [],
      issues: stores ? stores.issues.getIssues() : [],
      parsed,
    }),
  );

  type RecentResult =
    | { kind: "recent-pr"; ref: RoutedItemRef; lastSelectedAt: string }
    | { kind: "recent-issue"; ref: RoutedItemRef; lastSelectedAt: string };

  type FlatResult =
    | { kind: "command"; item: Action }
    | { kind: "pull"; item: PullRequest }
    | { kind: "issue"; item: Issue }
    | RecentResult;

  // Recents only appear in the empty-state view (no query, default scope).
  // Reserved prefixes (`repo:`, `ws:`) and any active filter/scope take over
  // and hide the recents section so the user's typed intent dominates.
  const recentResults = $derived<RecentResult[]>(
    parsed.scope === "all" && parsed.query === ""
      ? recents.items.slice(0, RECENT_CAP).map<RecentResult>((item) =>
          item.kind === "pr"
            ? {
                kind: "recent-pr",
                ref: item.ref,
                lastSelectedAt: item.lastSelectedAt,
              }
            : {
                kind: "recent-issue",
                ref: item.ref,
                lastSelectedAt: item.lastSelectedAt,
              },
        )
      : [],
  );

  const hasResults = $derived(
    grouped.commands.length +
      grouped.pulls.length +
      grouped.issues.length +
      recentResults.length >
      0,
  );

  const flatResults = $derived<FlatResult[]>([
    ...recentResults,
    ...grouped.commands.map<FlatResult>((c) => ({ kind: "command", item: c })),
    ...grouped.pulls.map<FlatResult>((p) => ({ kind: "pull", item: p })),
    ...grouped.issues.map<FlatResult>((i) => ({ kind: "issue", item: i })),
  ]);

  // Reset the highlight to the top whenever the query changes. The first match
  // is the assumed pick, and keeping a stale offset across keystrokes makes
  // the preview jump around as the result list rebuilds.
  $effect(() => {
    void query;
    untrack(() => {
      highlightIndex = 0;
    });
  });

  // Clamp highlightIndex back into range when the result list shrinks. When
  // empty, leave it at 0 — `highlighted` will still be null.
  $effect(() => {
    const n = flatResults.length;
    if (n === 0) return;
    if (highlightIndex >= n) {
      untrack(() => {
        highlightIndex = n - 1;
      });
    } else if (highlightIndex < 0) {
      untrack(() => {
        highlightIndex = 0;
      });
    }
  });

  const highlighted = $derived<FlatResult | null>(
    flatResults[highlightIndex] ?? null,
  );

  // Scroll the highlighted row into view when arrow keys move past the
  // visible window. Without this, the highlight class moves but the
  // .palette-list scroll position doesn't follow, leaving the user staring
  // at a list where the "selected" row is offscreen. jsdom (used by the
  // unit tests) doesn't implement scrollIntoView, so guard the call.
  $effect(() => {
    void highlightIndex;
    if (!dialogEl) return;
    const row = dialogEl.querySelector<HTMLElement>(".palette-row-highlight");
    if (row && typeof row.scrollIntoView === "function") {
      row.scrollIntoView({ block: "nearest" });
    }
  });

  function pullKey(repoOwner: string, repoName: string, num: number): string {
    return `${repoOwner}/${repoName}#${num}`;
  }

  function refMatchesPull(ref: RoutedItemRef, pr: PullRequest): boolean {
    return (
      ref.repoPath === pr.repo.repo_path &&
      ref.number === pr.Number &&
      ref.provider === pr.repo.provider &&
      (ref.platformHost ?? "") === (pr.repo.platform_host ?? "")
    );
  }

  function refMatchesIssue(ref: RoutedItemRef, issue: Issue): boolean {
    return (
      ref.repoPath === issue.repo.repo_path &&
      ref.number === issue.Number &&
      ref.provider === issue.repo.provider &&
      (ref.platformHost ?? "") === (issue.repo.platform_host ?? "")
    );
  }

  function loadedPullFor(ref: RoutedItemRef): PullRequest | undefined {
    if (!stores) return undefined;
    return stores.pulls.getPulls().find((pr) => refMatchesPull(ref, pr));
  }

  function loadedIssueFor(ref: RoutedItemRef): Issue | undefined {
    if (!stores) return undefined;
    return stores.issues.getIssues().find((issue) => refMatchesIssue(ref, issue));
  }

  function runHighlighted(): void {
    const result = highlighted;
    if (result === null) return;
    if (result.kind === "command") {
      // Close before invoking the handler so navigation-style commands
      // (e.g. nav.settings) don't race the modal teardown — the route
      // change can unmount the palette host while the handler runs.
      const action = result.item;
      const ctxStores = stores;
      closePalette();
      // The unit-test fixture mounts the palette without a Provider, so
      // `stores` is undefined and `buildContext` cannot run. Hand the
      // handler an empty context object in that case — production
      // actions all read `stores()` via their own getter, so the only
      // handlers that actually invoke through the test fixture are the
      // simple ones the unit tests register.
      const ctx = ctxStores
        ? buildContext(ctxStores)
        : ({} as ReturnType<typeof buildContext>);
      try {
        const out = untrack(() => action.handler(ctx));
        handleCommandResult(out, (err) => {
          console.error(`palette action ${action.id} failed`, err);
        });
      } catch (err) {
        // Mirror dispatch.svelte.ts/runHandler: log and keep the palette
        // host alive so a throwing handler doesn't crash the app.
        console.error(`palette action ${action.id} failed`, err);
      }
      return;
    }
    if (result.kind === "pull") {
      const pr = result.item;
      const ref: RoutedItemRef = {
        itemType: "pr",
        provider: pr.repo.provider,
        platformHost: pr.repo.platform_host,
        owner: pr.repo.owner,
        name: pr.repo.name,
        repoPath: pr.repo.repo_path,
        number: pr.Number,
      };
      // Persist before navigating so the recents store reflects the pick
      // even if the route change tears the palette down before any
      // post-navigate hooks would run.
      writeRecent("pr", ref);
      closePalette();
      navigate(buildPullRequestRoute(ref));
      return;
    }
    if (result.kind === "issue") {
      const issue = result.item;
      const ref: RoutedItemRef = {
        itemType: "issue",
        provider: issue.repo.provider,
        platformHost: issue.repo.platform_host,
        owner: issue.repo.owner,
        name: issue.repo.name,
        repoPath: issue.repo.repo_path,
        number: issue.Number,
      };
      writeRecent("issue", ref);
      closePalette();
      navigate(buildIssueRoute(ref));
      return;
    }
    if (result.kind === "recent-pr") {
      // Refresh the timestamp + bump to the front. The destination view
      // tolerates a missing record itself, so we don't pre-filter recents
      // against the currently-loaded pulls.
      writeRecent("pr", result.ref);
      closePalette();
      navigate(buildPullRequestRoute(result.ref));
      return;
    }
    // recent-issue
    writeRecent("issue", result.ref);
    closePalette();
    navigate(buildIssueRoute(result.ref));
  }

  function selectRowAt(index: number): void {
    highlightIndex = index;
    runHighlighted();
  }

  function bodyExcerpt(body: string | undefined): string {
    if (!body) return "";
    return body.slice(0, 200);
  }

  function onPaletteKeyDown(e: KeyboardEvent): void {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      const last = flatResults.length - 1;
      if (last < 0) return;
      highlightIndex = Math.min(last, highlightIndex + 1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      highlightIndex = Math.max(0, highlightIndex - 1);
    } else if (e.key === "Enter") {
      e.preventDefault();
      runHighlighted();
    }
  }

  $effect(() => {
    if (!isPaletteOpen()) return;
    const closeAction: ModalFrameAction = {
      id: "palette.close",
      label: "Close palette",
      binding: [
        { key: "Escape" },
        { key: "k", ctrlOrMeta: true },
        { key: "p", ctrlOrMeta: true },
      ],
      priority: 100,
      when: () => true,
      handler: () => closePalette(),
    };
    const cleanup = untrack(() => pushModalFrame("palette", [closeAction]));
    // Move keyboard focus into the search input on open. Without this the
    // user's existing focus stays on whatever was active before, so typed
    // characters land in the wrong field.
    void tick().then(() => inputEl?.focus());
    return cleanup;
  });

  // Focus trap: keep Tab / Shift+Tab cycling within the palette dialog so
  // focus never escapes to the page underneath while the palette is open.
  // Initial focus is handled by the effect above via tick(); this trap only
  // intercepts subsequent Tab navigation.
  $effect(() => {
    if (!isPaletteOpen() || !dialogEl) return;
    const focusable = (): HTMLElement[] =>
      Array.from(
        dialogEl!.querySelectorAll<HTMLElement>(
          "input, button, [tabindex]:not([tabindex='-1'])",
        ),
      ).filter((e) => !e.hasAttribute("disabled"));
    function trap(e: KeyboardEvent): void {
      if (e.key !== "Tab") return;
      const els = focusable();
      if (els.length === 0) return;
      const currentIndex = els.findIndex((el) => el === document.activeElement);
      const nextIndex = e.shiftKey
        ? (currentIndex <= 0 ? els.length - 1 : currentIndex - 1)
        : (currentIndex < 0 || currentIndex === els.length - 1 ? 0 : currentIndex + 1);
      els[nextIndex]?.focus();
      e.preventDefault();
    }
    // Capture dialogEl into a local before registering so the cleanup
    // detaches from the same node we attached to, even if dialogEl is
    // reassigned or unmounted before cleanup runs.
    const el = dialogEl;
    el.addEventListener("keydown", trap);
    return () => el.removeEventListener("keydown", trap);
  });
</script>

{#if isPaletteOpen()}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="palette-backdrop" onclick={closePalette}></div>
  <div
    bind:this={dialogEl}
    class="palette"
    role="dialog"
    aria-modal="true"
    aria-label="Command palette"
  >
    <input
      bind:this={inputEl}
      bind:value={query}
      onkeydown={onPaletteKeyDown}
      class="palette-input"
      placeholder="Search loaded PRs, issues, commands..."
    />
    <div class="palette-body">
      <div class="palette-list">
        {#if parsed.scope === "reserved"}
          <div class="palette-row palette-row-disabled">
            Repo and workspace search land in v2. Try
            <code>pr:</code>
            or
            <code>issue:</code>
            instead.
          </div>
        {:else if !hasResults}
          <div class="palette-row palette-row-disabled">No results</div>
        {:else}
          {#if recentResults.length > 0}
            <div class="palette-group">
              <div class="palette-group-header">Recently used</div>
              {#each recentResults as recent, ri (recent.kind + "|" + recent.ref.repoPath + "#" + recent.ref.number)}
                {@const flatIdx = ri}
                <button
                  class="palette-row {flatIdx === highlightIndex
                    ? 'palette-row-highlight'
                    : ''}"
                  type="button"
                  onclick={() => selectRowAt(flatIdx)}
                >
                  <span class="palette-row-tag">
                    {recent.ref.owner}/{recent.ref.name} #{recent.ref.number}
                  </span>
                  <span class="palette-row-label">
                    {recent.kind === "recent-pr" ? "PR" : "Issue"}
                  </span>
                </button>
              {/each}
            </div>
          {/if}
          {#if grouped.commands.length > 0}
            <div class="palette-group">
              <div class="palette-group-header">Commands</div>
              {#each grouped.commands as command, ci (command.id)}
                {@const flatIdx = recentResults.length + ci}
                <button
                  class="palette-row {flatIdx === highlightIndex
                    ? 'palette-row-highlight'
                    : ''}"
                  type="button"
                  onclick={() => selectRowAt(flatIdx)}
                >
                  <span class="palette-row-label">{command.label}</span>
                  <span class="palette-row-tag">{command.id}</span>
                </button>
              {/each}
            </div>
          {/if}
          {#if grouped.pulls.length > 0}
            <div class="palette-group">
              <div class="palette-group-header">Pull requests</div>
              {#each grouped.pulls as pr, pi (pullKey(pr.repo_owner, pr.repo_name, pr.Number))}
                {@const flatIdx =
                  recentResults.length + grouped.commands.length + pi}
                <button
                  class="palette-row {flatIdx === highlightIndex
                    ? 'palette-row-highlight'
                    : ''}"
                  type="button"
                  onclick={() => selectRowAt(flatIdx)}
                >
                  <span class="palette-row-tag">
                    {pr.repo_owner}/{pr.repo_name} #{pr.Number}
                  </span>
                  <span class="palette-row-label">{pr.Title}</span>
                </button>
              {/each}
            </div>
          {/if}
          {#if grouped.issues.length > 0}
            <div class="palette-group">
              <div class="palette-group-header">Issues</div>
              {#each grouped.issues as issue, ii (pullKey(issue.repo_owner, issue.repo_name, issue.Number))}
                {@const flatIdx =
                  recentResults.length +
                  grouped.commands.length +
                  grouped.pulls.length +
                  ii}
                <button
                  class="palette-row {flatIdx === highlightIndex
                    ? 'palette-row-highlight'
                    : ''}"
                  type="button"
                  onclick={() => selectRowAt(flatIdx)}
                >
                  <span class="palette-row-tag">
                    {issue.repo_owner}/{issue.repo_name} #{issue.Number}
                  </span>
                  <span class="palette-row-label">{issue.Title}</span>
                </button>
              {/each}
            </div>
          {/if}
        {/if}
      </div>
      <div class="palette-preview">
        {#if highlighted === null}
          <div class="preview-empty">Highlight a result to preview it</div>
        {:else if highlighted.kind === "command"}
          {@const action = highlighted.item}
          <div class="preview-title">{action.label}</div>
          <div class="preview-subtitle">Scope: {action.scope}</div>
          <div class="preview-meta">Available when: context-conditional</div>
        {:else if highlighted.kind === "pull"}
          {@const pr = highlighted.item}
          <div class="preview-header">
            <div class="preview-title">{pr.Title}</div>
            <ItemStateChip
              state={(pr.IsDraft ? "draft" : pr.State).toLowerCase()}
              class="preview-badge"
            />
          </div>
          <div class="preview-subtitle">
            {pr.repo_owner}/{pr.repo_name} #{pr.Number}
          </div>
          {#if pr.UpdatedAt}
            <div class="preview-meta">Updated {timeAgo(pr.UpdatedAt)}</div>
          {/if}
          {#if pr.Body}
            <div class="preview-body">{bodyExcerpt(pr.Body)}</div>
          {/if}
        {:else if highlighted.kind === "issue"}
          {@const issue = highlighted.item}
          <div class="preview-header">
            <div class="preview-title">{issue.Title}</div>
            <ItemStateChip
              state={issue.State.toLowerCase()}
              class="preview-badge"
            />
          </div>
          <div class="preview-subtitle">
            {issue.repo_owner}/{issue.repo_name} #{issue.Number}
          </div>
          {#if issue.UpdatedAt}
            <div class="preview-meta">Updated {timeAgo(issue.UpdatedAt)}</div>
          {/if}
          {#if issue.Body}
            <div class="preview-body">{bodyExcerpt(issue.Body)}</div>
          {/if}
        {:else if highlighted.kind === "recent-pr"}
          {@const ref = highlighted.ref}
          {@const loaded = loadedPullFor(ref)}
          <div class="preview-title">
            {loaded ? loaded.Title : `${ref.owner}/${ref.name} #${ref.number}`}
          </div>
          <div class="preview-subtitle">
            {ref.provider}{ref.platformHost ? ` · ${ref.platformHost}` : ""}
          </div>
          <div class="preview-meta">
            Last used {timeAgo(highlighted.lastSelectedAt)}
          </div>
          {#if loaded?.Body}
            <div class="preview-body">{bodyExcerpt(loaded.Body)}</div>
          {/if}
        {:else}
          {@const ref = highlighted.ref}
          {@const loaded = loadedIssueFor(ref)}
          <div class="preview-title">
            {loaded ? loaded.Title : `${ref.owner}/${ref.name} #${ref.number}`}
          </div>
          <div class="preview-subtitle">
            {ref.provider}{ref.platformHost ? ` · ${ref.platformHost}` : ""}
          </div>
          <div class="preview-meta">
            Last used {timeAgo(highlighted.lastSelectedAt)}
          </div>
          {#if loaded?.Body}
            <div class="preview-body">{bodyExcerpt(loaded.Body)}</div>
          {/if}
        {/if}
      </div>
    </div>
    <div class="palette-footer">
      <span>up/down navigate</span>
      <span>enter run</span>
      <span>esc close</span>
    </div>
  </div>
{/if}

<style>
  .palette-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.55);
    z-index: 100;
  }

  .palette {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: 920px;
    max-width: calc(100vw - 32px);
    height: 480px;
    max-height: calc(100vh - 32px);
    display: grid;
    grid-template-rows: auto 1fr auto;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: 10px;
    box-shadow: var(--shadow-lg);
    z-index: 101;
  }

  .palette-input {
    padding: 12px 16px;
    border: none;
    border-bottom: 1px solid var(--border-muted);
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    outline: none;
  }

  .palette-body {
    display: grid;
    grid-template-columns: 360px 1fr;
    overflow: hidden;
  }

  .palette-list {
    border-right: 1px solid var(--border-muted);
    overflow-y: auto;
  }

  .palette-group {
    padding: 4px 0;
  }

  .palette-group-header {
    padding: 6px 16px 4px;
    font-size: var(--font-size-2xs);
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--text-secondary);
  }

  .palette-row {
    width: 100%;
    display: flex;
    align-items: baseline;
    gap: 10px;
    padding: 6px 16px;
    background: transparent;
    border: none;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    text-align: left;
    cursor: pointer;
  }

  .palette-row:hover,
  .palette-row:focus-visible {
    background: var(--bg-surface-hover);
    outline: none;
  }

  .palette-row-highlight {
    background: var(--bg-surface-hover);
    box-shadow: inset 2px 0 0 0 var(--accent-blue, var(--text-primary));
  }

  .palette-row-label {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .palette-row-tag {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    flex: 0 0 auto;
  }

  .palette-row-disabled {
    color: var(--text-muted);
    cursor: default;
  }

  .palette-row-disabled:hover,
  .palette-row-disabled:focus-visible {
    background: transparent;
  }

  .palette-row-disabled code {
    font-family: var(--font-mono, monospace);
    font-size: var(--font-size-xs);
    padding: 1px 4px;
    border-radius: 3px;
    background: var(--bg-surface-hover);
    margin: 0 2px;
  }

  .palette-preview {
    padding: 16px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .preview-empty {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    font-style: italic;
  }

  .preview-header {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .preview-title {
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    font-weight: 600;
    flex: 1 1 auto;
  }

  .preview-subtitle {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .preview-meta {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .preview-body {
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    line-height: 1.5;
    white-space: pre-wrap;
    margin-top: 4px;
  }

  .palette-footer {
    padding: 6px 12px;
    border-top: 1px solid var(--border-muted);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    display: flex;
    gap: 16px;
  }
</style>
