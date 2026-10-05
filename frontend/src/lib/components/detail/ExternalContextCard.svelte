<script lang="ts">
  import { Button, Card, Chip } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import type { ExternalContextSourceInfo } from "../../api/generated/models/externalContextSourceInfo.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { DocumentVisibility } from "../../browser/document-visibility.js";
  import { pollWhileVisible } from "../../effect/poll-while-visible.js";
  import type {
    ExternalContextPull,
    ExternalContextState,
    ExternalContextWorkflowService,
  } from "../../stores/external-context-workflow.svelte.js";
  import MarkdownHtml from "../shared/MarkdownHtml.svelte";

  const { source, state, pull, workflow }: {
    source: ExternalContextSourceInfo;
    state: ExternalContextState;
    pull: ExternalContextPull;
    workflow: ExternalContextWorkflowService;
  } = $props();
  const runtime = getAppRuntime();
  const tones = { neutral: "neutral", pending: "info", success: "success", warning: "warning", error: "danger" } as const;
  let refreshRead: AppExecution<void, never> | undefined;

  $effect(() => {
    const currentPull = pull;
    const sourceId = source.id;
    const owner = workflow;
    const execution = untrack(() => runtime.runCommand(Effect.gen(function* () {
      const visibility = yield* DocumentVisibility;
      yield* visibility.untilVisible;
      yield* owner.read(currentPull, sourceId);
    }), { operation: "load external context", safeContext: { number: currentPull.number }, onFailure: () => {} }));
    return () => {
      execution.interrupt();
      refreshRead?.interrupt();
    };
  });

  $effect(() => {
    const interval = state.card?.refresh_after_seconds;
    if (!interval || state.needsRefresh) return;
    const currentPull = pull;
    const sourceId = source.id;
    const owner = workflow;
    const execution = untrack(() => runtime.runCommand(
      pollWhileVisible(owner.read(currentPull, sourceId), `${Math.max(5, interval)} seconds`),
      { operation: "poll external context", safeContext: { number: currentPull.number }, onFailure: () => {} }));
    return execution.interrupt;
  });

  function refresh(): void {
    refreshRead?.interrupt();
    refreshRead = runtime.runCommand(workflow.read(pull, source.id, true), {
      operation: "refresh external context", safeContext: { number: pull.number }, onFailure: () => {},
    });
  }

  function runAction(actionId: string, input?: string): void {
    runtime.runCommand(workflow.action(pull, source.id, actionId, input), {
      operation: "run external context action", safeContext: { number: pull.number }, onFailure: () => {},
    });
  }
</script>

{#if state.card || state.error}
  <Card level="inset" padding="sm">
    <section class="external-context" aria-label={source.name}>
      <div class="context-header">
        <strong>{source.name}</strong>
        {#if state.card}<Chip size="xs" tone={tones[state.card.status]}>{state.card.status}</Chip>{/if}
        <Button
          size="sm"
          disabled={state.loading || state.pendingAction !== null}
          ariaLabel={`Refresh ${source.name}`}
          onclick={refresh}
        >Refresh</Button>
      </div>
      {#if state.card}
        <p>{state.card.summary}</p>
        {#if state.card.result_head_sha && state.card.result_head_sha !== pull.headSha}
          <p class="older-result">Results are for an older commit</p>
        {/if}
        {#if state.card.markdown}
          <details>
            <summary>Details</summary>
            <div class="markdown-body"><MarkdownHtml raw={state.card.markdown} options={{ interactiveTasks: false }} /></div>
          </details>
        {/if}
        {#if state.card.actions?.length}
          <div class="context-actions">
            {#each state.card.actions as action (action.id)}
              {@const blocked = !!action.disabled_reason || state.pendingAction !== null || state.needsRefresh || !pull.headSha}
              <div class="context-action" class:with-input={action.input}>
                {#if action.input}
                  <textarea
                    class="action-input"
                    aria-label={`${action.label} text`}
                    placeholder={action.input.placeholder}
                    rows="3"
                    maxlength={action.input.max_length ?? 16384}
                    disabled={blocked}
                    bind:value={() => workflow.draft(pull, source.id, action.id), (value) => workflow.setDraft(pull, source.id, action.id, value)}
                  ></textarea>
                {/if}
                <Button
                  size="sm"
                  disabled={blocked || (!!action.input && !workflow.draft(pull, source.id, action.id).trim())}
                  title={action.disabled_reason || undefined}
                  onclick={() => runAction(action.id, action.input ? workflow.draft(pull, source.id, action.id) : undefined)}
                >
                  {state.pendingAction === action.id ? "Submitting…" : action.label}
                </Button>
                {#if action.disabled_reason}<span class="disabled-reason">{action.disabled_reason}</span>{/if}
              </div>
            {/each}
          </div>
        {/if}
      {/if}
      {#if state.error}<p class="context-error" role="alert">{state.error}</p>{/if}
    </section>
  </Card>
{/if}

<style>
  .external-context {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    min-width: 0;
    font-size: var(--font-size-sm);
  }

  .context-header, .context-actions, .context-action {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--space-3);
  }

  .context-header strong { margin-right: auto; }
  .context-action.with-input { flex-basis: 100%; flex-direction: column; align-items: flex-start; }
  .action-input { box-sizing: border-box; width: 100%; resize: vertical; }
  .action-input::placeholder { color: var(--text-muted); }
  .action-input:disabled { opacity: var(--opacity-disabled); }
  p { margin: 0; }
  .older-result { color: var(--accent-amber); }
  .context-error { color: var(--accent-red); }
  .disabled-reason { color: var(--text-muted); }
  summary { cursor: pointer; color: var(--text-secondary); }
  .markdown-body { margin-top: var(--space-3); overflow: auto; }
</style>
