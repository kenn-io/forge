<script lang="ts">
  import { Button, Card, Chip, EmptyState, PageHeader, Spinner, formatRelativeTime } from "@kenn-io/kit-ui";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import ListOrderedIcon from "@lucide/svelte/icons/list-ordered";
  import { Effect } from "effect";
  import { onMount, untrack } from "svelte";
  import type { ApiProblemError, TransientTransportError } from "../../api/effect-errors.js";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import { repositoryKeyFromWire } from "../../api/repository-key.js";
  import { retryIdempotentRead } from "../../api/retry-policy.js";
  import { apiErrorMessage } from "../../api/runtime.js";
  import type { PullRequest } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppServices } from "../../app/runtime.js";
  import { getStores } from "../../context.js";
  import { pollWhileVisible } from "../../effect/poll-while-visible.js";
  import { makeLatestSharedRead } from "../../effect/latest-shared-read.js";
  import { buildPullRequestRoute } from "../../routes.js";
  import { getGlobalRepo } from "../../stores/filter.svelte.js";
  import { navigate, withBasePath } from "../../stores/router.svelte.js";
  import { localDateTimeLabel } from "../../utils/time.js";
  import {
    formatMergeQueueEta,
    hasMergeQueuePosition,
    mergeQueueEntryDescription,
    mergeQueueRemovalDescription,
    mergeQueueRemovalReason,
    mergeQueueState,
  } from "../detail/merge-queue.js";
  import RepoPrivateIcon from "../shared/RepoPrivateIcon.svelte";
  import { groupMergeQueues } from "./merge-queues.js";

  type ReadError = ApiProblemError | TransientTransportError;

  const runtime = getAppRuntime();
  const { events } = getStores();

  let pulls = $state.raw<PullRequest[]>([]);
  let loaded = $state(false);
  let loadError = $state<string | null>(null);
  let refresh: (() => void) | null = null;

  const groups = $derived(groupMergeQueues(pulls));
  const queuedCount = $derived(groups.reduce((sum, group) => sum + group.entries.length, 0));
  const removedCount = $derived(groups.reduce((sum, group) => sum + group.removals.length, 0));
  const summary = $derived.by(() => {
    const parts = [`${queuedCount} queued`];
    if (removedCount > 0) parts.push(`${removedCount} removed`);
    return `${parts.join(" · ")} across ${groups.length} ${groups.length === 1 ? "queue" : "queues"}`;
  });

  function readErrorMessage(failure: ReadError): string {
    return failure._tag === "ApiProblemError"
      ? apiErrorMessage(failure.problem, "Could not load merge queues.")
      : "Could not reach Forge to load merge queues.";
  }

  // Only open pull requests have queue state, and the server keeps the ones
  // in a queue or last removed from one.
  function listQueuedPulls(repo: string | undefined) {
    return executeGeneratedApiRequest("GET /pulls merge queues", (client, signal) =>
      client.PullRequestsService.listPulls(
        { state: "open", attributes: "merge_queue", ...(repo !== undefined && { repo }) },
        { signal },
      ),
    ).pipe(retryIdempotentRead, Effect.map((result) => result ?? []));
  }

  function prRoute(pr: PullRequest): string {
    return buildPullRequestRoute({
      provider: pr.repo.provider,
      platformHost: pr.repo.platform_host,
      repositoryKey: repositoryKeyFromWire(pr.repo),
      owner: pr.repo.owner,
      name: pr.repo.name,
      repoPath: pr.repo.repo_path,
      number: pr.Number,
    });
  }

  // Plain clicks route in-app; modifier and middle clicks keep the href.
  function openPull(event: MouseEvent, pr: PullRequest): void {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    navigate(prRoute(pr));
  }

  function authorLabel(pr: PullRequest): string {
    return pr.AuthorDisplayName || pr.Author;
  }

  onMount(() => {
    const lifecycle = runtime.runCommand(
      Effect.scoped(
        Effect.gen(function* () {
          const reads = yield* makeLatestSharedRead<PullRequest[], ReadError, AppServices>();
          const readOnce = Effect.suspend(() => {
            const repo = getGlobalRepo();
            return reads.read(JSON.stringify(repo ?? null), listQueuedPulls(repo));
          }).pipe(
            Effect.match({
              onSuccess: (result) => {
                pulls = result;
                loadError = null;
                loaded = true;
              },
              onFailure: (failure) => {
                loadError = readErrorMessage(failure);
              },
            }),
          );
          yield* Effect.acquireRelease(
            Effect.sync(() => {
              refresh = () => {
                runtime.runCommand(readOnce, {
                  operation: "merge-queues.read",
                  safeContext: { surface: "merge-queues" },
                  onFailure: () => undefined,
                });
              };
              return events.subscribeWorkspaceEvents((event) => {
                if (event.type === "data_changed" || event.type === "open") refresh?.();
              });
            }),
            (unsubscribe) =>
              Effect.sync(() => {
                unsubscribe();
                refresh = null;
              }),
          );
          // The poll also keeps the relative times current.
          yield* pollWhileVisible(readOnce, "30 seconds", { immediate: true });
        }),
      ),
      {
        operation: "merge-queues.lifecycle",
        safeContext: { surface: "merge-queues" },
        onFailure: () => undefined,
      },
    );
    return () => lifecycle.interrupt();
  });

  // The header repository filter narrows the page like the PR list.
  $effect(() => {
    getGlobalRepo();
    untrack(() => refresh?.());
  });
</script>

<section class="merge-queues-page">
  <PageHeader
    title="Merge queues"
    description="Pull requests in each repository's merge queue, in queue order, from the state Forge last synced."
  >
    {#snippet actions()}
      {#if loaded && groups.length > 0}
        <span class="merge-queues-page__summary">{summary}</span>
      {/if}
    {/snippet}
  </PageHeader>

  {#if !loaded && loadError === null}
    <div class="merge-queues-page__state" role="status"><Spinner size={18} />Loading merge queues</div>
  {:else if !loaded && loadError !== null}
    <EmptyState title="Could not load merge queues" description={loadError}>
      <Button size="sm" onclick={() => refresh?.()}>Retry</Button>
    </EmptyState>
  {:else if groups.length === 0}
    <EmptyState
      title="No pull requests are in a merge queue"
      description="Pull requests you add to a GitHub merge queue appear here, with their position and state, until they merge."
    >
      {#snippet icon()}<ListOrderedIcon size={22} strokeWidth={1.75} />{/snippet}
    </EmptyState>
  {:else}
    {#if loadError !== null}
      <p class="merge-queues-page__error" role="alert">{loadError}</p>
    {/if}
    <div class="merge-queues-page__queues">
      {#each groups as group (group.key)}
        <section aria-label={`${group.repo.repo_path} ${group.branch} merge queue`}>
        <Card padding="none">
          <header class="merge-queue__header">
            <h2 class="merge-queue__title">
              <span class="merge-queue__repo">{group.repo.repo_path}</span>
              <RepoPrivateIcon isPrivate={group.repo.private} />
              <code class="merge-queue__branch">{group.branch}</code>
            </h2>
            <span class="merge-queue__counts">
              {group.entries.length} queued{#if group.removals.length > 0}&nbsp;· {group.removals.length} removed{/if}
            </span>
            {#if group.providerURL}
              <a
                class="merge-queue__provider-link"
                href={group.providerURL}
                target="_blank"
                rel="noopener noreferrer"
                title="Open this merge queue on GitHub"
              >
                GitHub<ExternalLinkIcon size={12} strokeWidth={2} aria-hidden="true" />
              </a>
            {/if}
          </header>

          {#if group.entries.length > 0}
            <ol class="merge-queue__rows" aria-label="Queued pull requests">
              {#each group.entries as pr (pr.ID)}
                {@const entry = pr.merge_queue!}
                {@const state = mergeQueueState(entry.state)}
                <li class="merge-queue__row" title={mergeQueueEntryDescription(entry)}>
                  <span
                    class="merge-queue__position"
                    title={hasMergeQueuePosition(entry) ? undefined : "The queue has not reported a position yet"}
                  >
                    {hasMergeQueuePosition(entry) ? entry.position : "–"}
                  </span>
                  <span class="merge-queue__item">
                    <a class="merge-queue__pr" href={withBasePath(prRoute(pr))} onclick={(event) => openPull(event, pr)}>
                      <span class="merge-queue__pr-title">{pr.Title}</span>
                      <span class="merge-queue__pr-number">#{pr.Number}</span>
                    </a>
                    <span class="merge-queue__meta">
                      {authorLabel(pr)} · queued
                      <time datetime={entry.enqueued_at} title={localDateTimeLabel(entry.enqueued_at)}
                        >{formatRelativeTime(entry.enqueued_at)}</time
                      >
                      {#if entry.estimated_time_to_merge_seconds !== undefined && entry.estimated_time_to_merge_seconds > 0}
                        · merges in {formatMergeQueueEta(entry.estimated_time_to_merge_seconds)}
                      {/if}
                    </span>
                  </span>
                  <Chip size="sm" tone={state.tone} class="merge-queue__state">{state.label}</Chip>
                </li>
              {/each}
            </ol>
          {/if}

          {#if group.removals.length > 0}
            <div class="merge-queue__removed">
              <h3 class="merge-queue__section-title">Removed without merging</h3>
              <ul class="merge-queue__rows" aria-label="Removed pull requests">
                {#each group.removals as pr (pr.ID)}
                  {@const removal = pr.merge_queue_removal!}
                  <li class="merge-queue__row" title={mergeQueueRemovalDescription(removal)}>
                    <span class="merge-queue__position" aria-hidden="true"></span>
                    <span class="merge-queue__item">
                      <a class="merge-queue__pr" href={withBasePath(prRoute(pr))} onclick={(event) => openPull(event, pr)}>
                        <span class="merge-queue__pr-title">{pr.Title}</span>
                        <span class="merge-queue__pr-number">#{pr.Number}</span>
                      </a>
                      <span class="merge-queue__meta">
                        {authorLabel(pr)} · removed
                        <time datetime={removal.removed_at} title={localDateTimeLabel(removal.removed_at)}
                          >{formatRelativeTime(removal.removed_at)}</time
                        >
                        {#if removal.failed_checks && removal.failed_checks.length > 0}
                          · failed
                          {#each removal.failed_checks as check, index (check.name)}
                            {#if index > 0},{/if}
                            {#if check.url}
                              <a class="merge-queue__check" href={check.url} target="_blank" rel="noopener noreferrer"
                                >{check.name}</a
                              >
                            {:else}
                              <span class="merge-queue__check">{check.name}</span>
                            {/if}
                          {/each}
                        {/if}
                      </span>
                    </span>
                    <Chip size="sm" tone="danger" class="merge-queue__state">{mergeQueueRemovalReason(removal)}</Chip>
                  </li>
                {/each}
              </ul>
            </div>
          {/if}
        </Card>
        </section>
      {/each}
    </div>
  {/if}
</section>

<style>
  .merge-queues-page {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
    min-height: 0;
    overflow-y: auto;
    padding: 26px 28px;
    background: var(--bg-primary);
  }

  .merge-queues-page__summary {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    white-space: nowrap;
  }

  .merge-queues-page__state {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-2);
    color: var(--text-muted);
  }

  .merge-queues-page__error {
    margin: 0;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .merge-queues-page__queues {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
    max-width: 960px;
  }

  .merge-queue__header {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-height: 40px;
    padding: 0 var(--space-5);
    border-bottom: var(--border-width) solid var(--border-muted);
  }

  .merge-queue__title {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
    margin: 0;
    font-size: var(--font-size-md);
    font-weight: 600;
  }

  .merge-queue__repo {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .merge-queue__branch {
    padding: 1px 6px;
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-weight: 500;
  }

  .merge-queue__counts {
    margin-inline-start: auto;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    white-space: nowrap;
  }

  .merge-queue__provider-link {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-decoration: none;
  }

  .merge-queue__provider-link:hover {
    color: var(--text-primary);
  }

  .merge-queue__rows {
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .merge-queue__row {
    display: grid;
    grid-template-columns: 28px minmax(0, 1fr) max-content;
    gap: var(--space-3);
    align-items: center;
    padding: var(--space-3) var(--space-5);
  }

  .merge-queue__row + .merge-queue__row {
    border-top: var(--border-width) solid var(--border-muted);
  }

  .merge-queue__position {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    font-variant-numeric: tabular-nums;
    font-weight: 600;
    text-align: right;
  }

  .merge-queue__item {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .merge-queue__pr {
    display: flex;
    gap: var(--space-2);
    min-width: 0;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    text-decoration: none;
  }

  .merge-queue__pr:hover .merge-queue__pr-title {
    text-decoration: underline;
  }

  .merge-queue__pr-title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .merge-queue__pr-number {
    flex: none;
    color: var(--text-muted);
  }

  .merge-queue__meta {
    overflow: hidden;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .merge-queue__check {
    color: var(--accent-red);
  }

  .merge-queue__removed {
    border-top: var(--border-width) solid var(--border-muted);
  }

  .merge-queue__section-title {
    margin: 0;
    padding: var(--space-3) var(--space-5) 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 600;
    letter-spacing: var(--letter-spacing-label, 0.04em);
    text-transform: var(--label-transform, uppercase);
  }
</style>
