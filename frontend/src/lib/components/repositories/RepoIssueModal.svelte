<script lang="ts">
  import { onMount } from "svelte";
  import { Button, Chip,
    CommentEditor, } from "@middleman/ui";
  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
  import {
    repoKey,
    repoStateKey,
    shouldShowPlatformHost,
    type RepoSummaryCard,
  } from "./repoSummary.js";

  interface Props {
    summary: RepoSummaryCard;
    title: string;
    body: string;
    error?: string | null;
    submitting?: boolean;
    outcomeUnknown?: boolean;
    ontitlechange: (value: string) => void;
    onbodychange: (value: string) => void;
    oncancel: () => void;
    onsubmitissue: () => void;
    onacknowledgeoutcome?: () => void;
  }

  let {
    summary,
    title,
    body,
    error = null,
    submitting = false,
    outcomeUnknown = false,
    ontitlechange,
    onbodychange,
    oncancel,
    onsubmitissue,
    onacknowledgeoutcome = () => {},
  }: Props = $props();

  const key = $derived(repoKey(summary));
  const stateKey = $derived(repoStateKey(summary));
  const titleId = $derived(
    `repo-issue-modal-title-${stateKey}`,
  );
  const showPlatformHost = $derived(shouldShowPlatformHost(summary));

  let titleInput = $state<HTMLInputElement | null>(null);

  onMount(() => {
    titleInput?.focus();
  });

  onMount(() => pushModalFrame("repo-issue-modal", []));

  function handleWindowKeydown(event: KeyboardEvent): void {
    if (event.key !== "Escape") return;
    event.preventDefault();
    oncancel();
  }
</script>

<svelte:window onkeydown={handleWindowKeydown} />

<div
  class="issue-modal__backdrop"
  role="presentation"
  onclick={(event) => {
    if (event.target === event.currentTarget) {
      oncancel();
    }
  }}
>
  <div
    class="issue-modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby={titleId}
  >
    <form
      class="issue-modal__form"
      onsubmit={(event) => {
        event.preventDefault();
        onsubmitissue();
      }}
    >
      <header class="issue-modal__header">
        <div class="issue-modal__title-group">
          <h2 id={titleId}>New issue in {key}</h2>
          {#if showPlatformHost}
            <Chip size="xs" tone="muted" uppercase={false}>
              {summary.platform_host}
            </Chip>
          {/if}
        </div>
        <Button size="sm" type="button" onclick={oncancel}>
          Cancel
        </Button>
      </header>

      <div class="issue-modal__body">
        <label class="issue-modal__field">
          <span>Title</span>
          <input
            bind:this={titleInput}
            type="text"
            placeholder="Issue title"
            value={title}
            disabled={submitting}
            oninput={(event) =>
              ontitlechange(event.currentTarget.value)}
          />
        </label>

        <div class="issue-modal__field">
          <span>Body</span>
          <CommentEditor
            provider={summary.repo.provider}
            platformHost={summary.repo.platform_host}
            owner={summary.repo.owner}
            name={summary.repo.name}
            repoPath={summary.repo.repo_path}
            value={body}
            disabled={submitting}
            placeholder="Describe the problem, context, or follow-up work"
            oninput={onbodychange}
            onsubmit={onsubmitissue}
          />
        </div>

        {#if error}
          <p class="issue-modal__error">{error}</p>
        {/if}
        {#if outcomeUnknown}
          <label class="issue-modal__acknowledgement">
            <input
              type="checkbox"
              onchange={(event) => {
                if (event.currentTarget.checked) onacknowledgeoutcome();
              }}
            />
            <span>I checked the issue list and want to retry.</span>
          </label>
        {/if}
      </div>

      <footer class="issue-modal__footer">
        <Button
          type="submit"
          tone="info"
          surface="soft"
          disabled={submitting || outcomeUnknown}
        >
          {submitting ? "Creating..." : "Create issue"}
        </Button>
      </footer>
    </form>
  </div>
</div>

<style>
  .issue-modal__backdrop {
    position: fixed;
    /* kit-ui-check-ignore: bottom-sheet placement on narrow viewports (media query below) — kit Modal only centers; revisit if kit grows a placement prop */
    inset: 0;
    z-index: 40;
    display: grid;
    place-items: center;
    padding: 24px;
    background: var(--overlay-bg);
  }

  .issue-modal {
    width: min(680px, 100%);
    max-height: min(720px, calc(100vh - 48px));
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
  }

  .issue-modal__form {
    max-height: min(720px, calc(100vh - 48px));
    display: grid;
    grid-template-rows: auto minmax(0, 1fr) auto;
  }

  .issue-modal__acknowledgement {
    display: flex;
    align-items: flex-start;
    gap: var(--space-2);
    color: var(--text-secondary);
  }

  .issue-modal__header,
  .issue-modal__footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 14px 16px;
  }

  .issue-modal__header {
    border-bottom: 1px solid var(--border-muted);
  }

  .issue-modal__footer {
    border-top: 1px solid var(--border-muted);
  }

  .issue-modal__title-group {
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .issue-modal__title-group h2 {
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    font-weight: 600;
  }

  .issue-modal__body {
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
    overflow-y: auto;
    padding: 16px;
  }

  .issue-modal__field {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .issue-modal__field > span {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .issue-modal__field input {
    width: 100%;
  }

  .issue-modal__error {
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  :global(.comment-editor-menu) {
    z-index: 60;
  }

  @media (max-width: 640px) {
    .issue-modal__backdrop {
      align-items: end;
      padding: 12px;
    }

    .issue-modal,
    .issue-modal__form {
      max-height: calc(100vh - 24px);
    }

    .issue-modal__header {
      align-items: flex-start;
    }
  }
</style>
