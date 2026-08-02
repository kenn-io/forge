<script lang="ts">
  import { Button, Spinner } from "@kenn-io/kit-ui";

  import type { ToolingStatusValue } from "../../stores/embed-config.svelte.ts";
  import ProviderIcon from "../provider/ProviderIcon.svelte";

  interface Props {
    tooling: ToolingStatusValue | undefined;
    retrying: boolean;
    retryError: string | null;
    onContinueGitHub: () => void;
    onCheckAgain: () => void;
    onOpenSettings: () => void;
  }

  let {
    tooling,
    retrying,
    retryError,
    onContinueGitHub,
    onCheckAgain,
    onOpenSettings,
  }: Props = $props();

  const gh = $derived(tooling?.gh);
  const glab = $derived(tooling?.glab);
  const ghReady = $derived(gh?.available === true && gh.authenticated === true);
  const glabReady = $derived(glab?.available === true && glab.authenticated === true);

  function accountLabel(tool: { host?: string; user?: string } | undefined, fallbackHost: string): string {
    const host = tool?.host || fallbackHost;
    return tool?.user ? `${host} · @${tool.user}` : host;
  }
</script>

<div class="provider-readiness">
  <div class="provider-statuses" role="list" aria-label="Code forge readiness">
    <div class="provider-row" role="listitem">
      <ProviderIcon provider="github" size={20} />
      <div class="provider-copy">
        <div class="provider-title">
          <strong>GitHub</strong>
          {#if tooling === undefined}
            <span class="provider-status">Checking</span>
          {:else if ghReady}
            <span class="provider-status provider-status--ready">Ready</span>
          {:else}
            <span class="provider-status provider-status--attention">Needs attention</span>
          {/if}
        </div>
        {#if tooling === undefined}
          <span>Checking this host for an authenticated <code>gh</code> installation.</span>
        {:else if ghReady}
          <span><code>gh</code> authenticated · {accountLabel(gh, "github.com")}</span>
        {:else if gh?.available}
          <span><code>gh</code> is not authenticated. Sign in from a terminal, then check again.</span>
        {:else}
          <span><code>gh</code> is not installed. Install the GitHub CLI on this host, then check again.</span>
        {/if}
      </div>
    </div>

    <div class="provider-row" role="listitem">
      <ProviderIcon provider="gitlab" size={20} />
      <div class="provider-copy">
        <div class="provider-title">
          <strong>GitLab</strong>
          {#if tooling === undefined}
            <span class="provider-status">Checking</span>
          {:else if glabReady}
            <span class="provider-status provider-status--ready">CLI ready</span>
          {:else if glab?.available}
            <span class="provider-status provider-status--attention">Sign-in needed</span>
          {:else}
            <span class="provider-status">Settings</span>
          {/if}
        </div>
        {#if glabReady}
          <span><code>glab</code> authenticated · {accountLabel(glab, "gitlab.com")}</span>
        {:else if glab?.available}
          <span><code>glab</code> is installed but not authenticated. Configure repositories in Settings.</span>
        {:else}
          <span>Configure a GitLab host, token, and repositories in Settings.</span>
        {/if}
      </div>
    </div>

    <div class="provider-row" role="listitem">
      <ProviderIcon provider="forgejo" size={20} />
      <div class="provider-copy">
        <div class="provider-title">
          <strong>Forgejo</strong>
          <span class="provider-status">Settings</span>
        </div>
        <span>Configure your Forgejo host, token, and repositories in Settings.</span>
      </div>
    </div>

    <div class="provider-row" role="listitem">
      <ProviderIcon provider="gitea" size={20} />
      <div class="provider-copy">
        <div class="provider-title">
          <strong>Gitea</strong>
          <span class="provider-status">Settings</span>
        </div>
        <span>Configure your Gitea host, token, and repositories in Settings.</span>
      </div>
    </div>
  </div>

  {#if !ghReady && gh?.available}
    <pre class="command"><code>gh auth login</code></pre>
  {/if}

  {#if retryError}
    <p class="inline-error" role="alert">{retryError}</p>
  {/if}

  <div class="readiness-actions">
    {#if ghReady}
      <Button tone="info" surface="solid" onclick={onContinueGitHub}>Continue with GitHub</Button>
    {:else}
      <Button tone="info" surface="solid" disabled={retrying} onclick={onCheckAgain}>
        {#if retrying}<Spinner size={13} />{/if}
        Check again
      </Button>
    {/if}
    <Button onclick={onOpenSettings}>Configure GitLab</Button>
    <Button onclick={onOpenSettings}>Configure Forgejo</Button>
    <Button onclick={onOpenSettings}>Configure Gitea</Button>
  </div>
</div>

<style>
  .provider-readiness {
    display: grid;
    gap: var(--space-5);
  }

  .provider-statuses {
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-surface);
  }

  .provider-row {
    min-width: 0;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--space-4);
    align-items: center;
    padding: var(--space-5);
    border-bottom: 1px solid var(--border-muted);
  }

  .provider-row:last-child {
    border-bottom: 0;
  }

  .provider-copy {
    min-width: 0;
    display: grid;
    gap: var(--space-2);
  }

  .provider-title {
    min-width: 0;
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: var(--space-4);
  }

  .provider-title strong {
    color: var(--text-primary);
  }

  .provider-copy > span {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    line-height: 1.45;
  }

  .provider-status {
    flex: 0 0 auto;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
  }

  .provider-status--ready {
    color: var(--accent-green);
  }

  .provider-status--attention,
  .inline-error {
    color: var(--accent-yellow);
  }

  code {
    font-family: var(--font-mono);
  }

  .command {
    margin: 0;
    padding: var(--space-4);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    color: var(--text-secondary);
  }

  .inline-error {
    margin: 0;
    font-size: var(--font-size-xs);
  }

  .readiness-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
  }

  @media (max-width: 640px) {
    .provider-row {
      align-items: start;
      padding: var(--space-4);
    }

    .provider-title {
      align-items: flex-start;
      flex-direction: column;
      gap: var(--space-2);
    }

    .readiness-actions {
      align-items: stretch;
      flex-direction: column;
    }
  }
</style>
