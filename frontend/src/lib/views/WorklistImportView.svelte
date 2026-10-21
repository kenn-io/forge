<script lang="ts">
  import { Button, Chip, Spinner, type ChipTone } from "@kenn-io/kit-ui";
  import AlertTriangleIcon from "@lucide/svelte/icons/alert-triangle";
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import {
    importWorklist,
    previewWorklistImport,
    worklistFailureMessage,
    type WorklistImportItem,
    type WorklistImportPreview,
  } from "../api/worklists.js";
  import { getAppRuntime } from "../app/runtime-context.js";
  import { getStores } from "../context.js";
  import { buildWorklistRoute, buildWorklistsRoute } from "../routes.js";
  import { getRoute, navigate, replaceUrl } from "../stores/router.svelte.js";

  interface Props {
    fragment: string;
  }

  let { fragment }: Props = $props();

  const runtime = getAppRuntime();
  const { worklists } = getStores();

  type PreviewState =
    | { readonly kind: "loading" }
    | { readonly kind: "ready"; readonly preview: WorklistImportPreview }
    | { readonly kind: "failed"; readonly message: string };

  let previewState = $state.raw<PreviewState>({ kind: "loading" });
  let creating = $state(false);
  let createError = $state<string | null>(null);

  $effect(() => {
    const current = fragment;
    previewState = { kind: "loading" };
    createError = null;
    const execution = untrack(() =>
      runtime.runCommand(
        previewWorklistImport(current).pipe(
          Effect.tap((preview) => Effect.sync(() => (previewState = { kind: "ready", preview }))),
        ),
        {
          operation: "preview worklist import",
          safeContext: { length: current.length },
          onFailure: (failure) => {
            previewState = { kind: "failed", message: worklistFailureMessage(failure, "Could not read the link.") };
          },
        },
      ),
    );
    return () => execution.interrupt();
  });

  function stillShowing(current: string): boolean {
    const route = getRoute();
    return route.page === "worklists" && route.importFragment === current;
  }

  // The import is not cancelled when the page is left: the list is created
  // either way, and the change event adds it to the Worklists tab.
  function create(): void {
    const current = fragment;
    creating = true;
    createError = null;
    runtime.runCommand(
      importWorklist(current).pipe(
        Effect.tap((created) =>
          Effect.sync(() => {
            creating = false;
            if (stillShowing(current)) replaceUrl(buildWorklistRoute(created.worklist_id));
          }),
        ),
        Effect.andThen(() => worklists.refreshSummariesEffect().pipe(Effect.ignore)),
      ),
      {
        operation: "import worklist",
        safeContext: { length: current.length },
        onFailure: (failure) => {
          creating = false;
          if (stillShowing(current)) createError = worklistFailureMessage(failure, "Could not create the worklist.");
        },
      },
    );
  }

  const statusLabels: Record<WorklistImportItem["status"], string | null> = {
    importable: null,
    repo_not_tracked: "Not tracked",
    item_not_found: "Not synced",
    duplicate: "Duplicate",
  };

  function scoreTone(score: number): ChipTone {
    if (score >= 9) return "success";
    if (score >= 7) return "info";
    if (score >= 5) return "warning";
    return "muted";
  }

  function itemTitle(item: WorklistImportItem): string | null {
    return item.pull?.Title ?? item.issue?.Title ?? null;
  }
</script>

<section class="worklist-import" aria-busy={previewState.kind === "loading"}>
  {#if previewState.kind === "loading"}
    <div class="worklist-import-state" role="status"><Spinner size={18} />Reading worklist link</div>
  {:else if previewState.kind === "failed"}
    <div class="worklist-import-state" role="alert">
      <span>{previewState.message}</span>
      <Button onclick={() => navigate(buildWorklistsRoute())}>Back to worklists</Button>
    </div>
  {:else}
    {@const preview = previewState.preview}
    {@const total = preview.items.length}
    <header class="worklist-import-header">
      <h1 class="worklist-import-title">{preview.title}</h1>
      <div class="worklist-import-meta">
        {#if preview.created_by}<span>{preview.created_by}</span>{/if}
        <span>{total === 1 ? "1 item" : `${total} items`}</span>
      </div>
      {#if preview.recipe}
        <p class="worklist-import-recipe">{preview.recipe}</p>
      {/if}
    </header>
    <ol class="worklist-import-items">
      {#each preview.items as item (item.position)}
        {@const skipped = statusLabels[item.status]}
        {@const title = itemTitle(item)}
        <li class={["worklist-import-item", { skipped: skipped !== null }]}>
          <span class="worklist-import-score">
            {#if item.score !== null}
              <Chip size="sm" tone={scoreTone(item.score)} uppercase={false} title="Agent score">{item.score}</Chip>
            {/if}
          </span>
          <span class="worklist-import-body">
            <span class="worklist-import-line">
              <span class="worklist-import-item-title">{title ?? `${item.owner}/${item.name}#${item.number}`}</span>
              {#if item.section}<Chip size="xs" tone="muted" uppercase={false}>{item.section}</Chip>{/if}
              {#if skipped}<Chip size="xs" tone="warning" uppercase={false}>{skipped}</Chip>{/if}
            </span>
            <span class="worklist-import-details">
              {#if title}<span>{item.owner}/{item.name}#{item.number}</span>{/if}
              {#if item.note}<span>{item.note}</span>{/if}
            </span>
            {#if item.caveat}
              <span class="worklist-import-caveat">
                <AlertTriangleIcon size={11} aria-hidden="true" />
                <span>{item.caveat}</span>
              </span>
            {/if}
          </span>
        </li>
      {/each}
    </ol>
    <footer class="worklist-import-actions">
      {#if createError}
        <span class="worklist-import-error" role="alert">{createError}</span>
      {:else if preview.importable_count === 0}
        <span class="worklist-import-error">None of these items are tracked here.</span>
      {/if}
      <Button onclick={() => navigate(buildWorklistsRoute())} disabled={creating}>Cancel</Button>
      <Button tone="info" surface="solid" onclick={create} disabled={creating || preview.importable_count === 0}>
        {preview.importable_count === total
          ? "Create worklist"
          : `Create with ${preview.importable_count} of ${total}`}
      </Button>
    </footer>
  {/if}
</section>

<style>
  .worklist-import {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    width: 100%;
    max-width: 880px;
    margin: 0 auto;
    background: var(--bg-surface);
  }

  .worklist-import-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-8) var(--space-5);
    color: var(--text-muted);
  }

  .worklist-import-header {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-5);
    border-bottom: 1px solid var(--border-default);
  }

  .worklist-import-title {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    font-weight: 600;
    overflow-wrap: anywhere;
  }

  .worklist-import-meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2) var(--space-4);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .worklist-import-recipe {
    max-height: 9em;
    margin: 0;
    overflow-y: auto;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  .worklist-import-items {
    flex: 1;
    min-height: 0;
    margin: 0;
    padding: 0;
    overflow-y: auto;
    list-style: none;
  }

  .worklist-import-item {
    display: grid;
    grid-template-columns: 28px minmax(0, 1fr);
    gap: var(--space-4);
    padding: 7px 12px;
    border-bottom: 1px solid var(--border-muted);
  }

  .worklist-import-item.skipped {
    opacity: 0.6;
  }

  .worklist-import-score {
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding-top: 1px;
    font-variant-numeric: tabular-nums;
  }

  .worklist-import-body {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .worklist-import-line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2) var(--space-3);
    min-width: 0;
  }

  .worklist-import-item-title {
    min-width: 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    overflow-wrap: anywhere;
  }

  .worklist-import-details {
    display: flex;
    flex-wrap: wrap;
    gap: 0 var(--space-3);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    overflow-wrap: anywhere;
  }

  .worklist-import-caveat {
    display: flex;
    align-items: baseline;
    gap: var(--space-1);
    color: var(--accent-amber);
    font-size: var(--font-size-xs);
    overflow-wrap: anywhere;
  }

  .worklist-import-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: var(--space-3);
    padding: var(--space-4) var(--space-5);
    border-top: 1px solid var(--border-default);
  }

  .worklist-import-error {
    margin-right: auto;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
</style>
