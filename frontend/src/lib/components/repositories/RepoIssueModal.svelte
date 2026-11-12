<script lang="ts">
  import { Checkbox, TextInput } from "@kenn-io/kit-ui";
  import { Button, Chip } from "@kenn-io/kit-ui";
  import { ExternalLinkIcon } from "../../icons.js";
  import CommentEditor from "../detail/CommentEditor.svelte";
  import Modal from "../shared/Modal.svelte";
  import { buildProviderIssueListURL } from "../../utils/item-reference.js";
  import { Effect } from "effect";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import { providerDefaultHost, providerHostRouteParams, providerRouteParams, providerUsesHostRoute, resolvedPlatformHost } from "../../api/provider-routes.js";
  import { repositoryKeyFromWire, repositoryKeyToWire } from "../../api/repository-key.js";
  import {
    repoKey,
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
  const showPlatformHost = $derived(shouldShowPlatformHost(summary));
  let resolvedWebURL = $state<string | undefined>();
  let webURLError = $state(false);
  let webURLLoading = $state(false);
  let retry = $state(0);
  function issueListFor(webURL: string | undefined) {
    const provider = summary.repo.provider;
    const host = resolvedPlatformHost(provider, summary.repo.platform_host).toLowerCase();
    const ref = {
      provider: summary.repo.provider,
      platformHost: host,
      repoPath: summary.repo.repo_path,
      webURL,
    };
    const supplied = webURL && buildProviderIssueListURL(ref);
    if (supplied) return supplied;
    if (!host || host !== providerDefaultHost(provider)) return undefined;
    return buildProviderIssueListURL({ ...ref, webURL: undefined });
  }
  const cachedIssueListURL = $derived(issueListFor(summary.web_url));
  const issueListURL = $derived(cachedIssueListURL || issueListFor(resolvedWebURL));
  const appRuntime = getAppRuntime();
  $effect(() => {
    if (!outcomeUnknown || cachedIssueListURL) return;
    return loadWebURL(retry);
  });
  function loadWebURL(_attempt: number) {
    const ref = { provider: summary.repo.provider, platformHost: summary.repo.platform_host, owner: summary.repo.owner, name: summary.repo.name, repoPath: summary.repo.repo_path };
    const repositoryKey = repositoryKeyToWire(repositoryKeyFromWire(summary.repo));
    resolvedWebURL = undefined;
    webURLError = false;
    webURLLoading = true;
    const execution = appRuntime.runCommand(
      executeGeneratedApiRequest("load repository web address", (client, signal) =>
        providerUsesHostRoute(ref)
          ? client.RepositoriesService.getRepoWebUrlOnHost(providerHostRouteParams(ref), repositoryKey, { signal })
          : client.RepositoriesService.getRepoWebUrl(providerRouteParams(ref), repositoryKey, { signal }),
      ).pipe(
        Effect.tap((result) => Effect.sync(() => {
          resolvedWebURL = result.web_url;
          webURLError = !issueListFor(result.web_url);
          webURLLoading = false;
        })),
        Effect.catch(() => Effect.sync(() => { webURLError = true; webURLLoading = false; })),
      ),
      { operation: "repositories.web-url.read", safeContext: { surface: "repositories" }, onFailure: () => undefined },
    );
    return () => execution.interrupt();
  }
</script>

<Modal
  open
  ariaLabel={`New issue in ${key}`}
  width={680}
  frameId="repo-issue-modal"
  onClose={oncancel}
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
          <h2>New issue in {key}</h2>
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
          <TextInput
            class="issue-modal__title-input"
            value={title}
            placeholder="Issue title"
            ariaLabel="Issue title"
            disabled={submitting}
            autofocus
            oninput={ontitlechange}
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

        {#if error || outcomeUnknown}
          <div class="issue-modal__notice">
            {#if error}
              <p class="issue-modal__error">{error}</p>
            {/if}
            {#if outcomeUnknown}
              {#if issueListURL}
                <a class="issue-modal__issue-list" href={issueListURL} target="_blank" rel="noopener noreferrer">
                  Open issue list
                  <ExternalLinkIcon size="12" strokeWidth="2" aria-hidden="true" />
                </a>
              {:else if webURLLoading}
                <span>Loading issue list...</span>
              {:else if webURLError}
                <div>Could not load the issue list. <Button size="sm" type="button" onclick={() => retry++}>Retry</Button></div>
              {/if}
            {/if}
          </div>
        {/if}
        {#if outcomeUnknown}
          <Checkbox
            class="issue-modal__acknowledgement"
            label="I checked the provider's issue list and want to retry."
            onchange={(checked) => {
              if (checked) onacknowledgeoutcome();
            }}
          />
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
</Modal>

<style>
  :global(.kit-modal-panel:has(.issue-modal__form)) {
    width: min(680px, calc(100vw - 24px));
    max-height: min(720px, calc(100vh - 24px));
  }

  :global(.kit-modal-body:has(> .modal-scope > .issue-modal__form)) {
    padding: 0;
    overflow: hidden;
  }

  .issue-modal__form {
    max-height: min(720px, calc(100vh - 24px));
    display: grid;
    grid-template-rows: auto minmax(0, 1fr) auto;
  }

  :global(.issue-modal__acknowledgement.kit-checkbox) {
    align-items: flex-start;
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

  :global(.issue-modal__title-input.kit-text-input) {
    width: 100%;
  }

  .issue-modal__error {
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .issue-modal__notice {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
    font-size: var(--font-size-sm);
  }

  .issue-modal__issue-list {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--accent-blue);
    text-decoration: underline;
  }

  :global(.comment-editor-menu) {
    z-index: 60;
  }

  @media (max-width: 640px) {
    :global(.kit-modal-overlay:has(.issue-modal__form)) {
      align-items: flex-end;
    }

    :global(.kit-modal-panel:has(.issue-modal__form)) {
      width: 100%;
      max-width: 100%;
      max-height: calc(100dvh - 12px);
      border-radius: var(--radius-lg) var(--radius-lg) 0 0;
    }

    .issue-modal__header {
      align-items: flex-start;
    }
  }
</style>
