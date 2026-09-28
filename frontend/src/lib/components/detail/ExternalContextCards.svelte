<script lang="ts">
  import { Button, Card } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import type { ProviderRouteRef } from "../../api/provider-routes.js";
  import type { ExternalContextSourceInfo } from "../../api/generated/models/externalContextSourceInfo.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { DocumentVisibility } from "../../browser/document-visibility.js";
  import {
    externalContextErrorMessage,
    externalContextKey,
    ExternalContextWorkflow,
    type ExternalContextState,
    type ExternalContextWorkflowService,
  } from "../../stores/external-context-workflow.svelte.js";
  import ExternalContextCard from "./ExternalContextCard.svelte";

  interface Props {
    ref: ProviderRouteRef;
    platformRepoId: number;
    number: number;
    headSha: string;
  }

  const { ref, platformRepoId, number, headSha }: Props = $props();
  const runtime = getAppRuntime();
  const pull = $derived({ ref, platformRepoId, number, headSha });
  const key = $derived(externalContextKey(pull));
  let workflow = $state.raw<ExternalContextWorkflowService | null>(null);
  let view = $state.raw<{
    key: string;
    revision: number;
    entries: { source: ExternalContextSourceInfo; state: ExternalContextState }[];
    error: string | null;
  } | null>(null);
  let refreshSources = $state(0);
  const currentView = $derived(view?.key === key ? view : null);

  $effect(() => {
    const execution = untrack(() => runtime.runCommand(Effect.gen(function* () {
      workflow = yield* ExternalContextWorkflow;
    }), { operation: "open external context", safeContext: {}, onFailure: () => {} }));
    return execution.interrupt;
  });

  $effect(() => {
    const owner = workflow;
    if (!owner) return;
    const currentPull = pull;
    const currentKey = key;
    const revision = owner.revision + refreshSources;
    const execution = untrack(() => runtime.runCommand(Effect.gen(function* () {
      const visibility = yield* DocumentVisibility;
      yield* visibility.untilVisible;
      const sources = yield* owner.sources;
      view = { key: currentKey, revision, entries: sources.map((source) => ({ source, state: owner.state(currentPull, source.id) })), error: null };
    }), {
      operation: "load external context sources",
      safeContext: { number: currentPull.number, revision },
      onFailure: (error) => { view = { key: currentKey, revision, entries: [], error: externalContextErrorMessage(error) }; },
    }));
    return execution.interrupt;
  });
</script>

{#if currentView?.error}
  <Card level="inset" padding="sm">
    <div class="external-context">
      <p role="alert">{currentView.error}</p>
      <Button size="sm" onclick={() => { refreshSources++; }}>Refresh</Button>
    </div>
  </Card>
{/if}
{#if workflow}
  {#each currentView?.entries ?? [] as { source, state } (`${key}:${source.id}:${currentView?.revision}`)}
    <ExternalContextCard {source} {state} {pull} {workflow} />
  {/each}
{/if}

<style>
  .external-context { display: flex; align-items: center; gap: var(--space-3); font-size: var(--font-size-sm); }
  p { margin: 0; }
</style>
