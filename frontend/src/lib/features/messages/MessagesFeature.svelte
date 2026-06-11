<script lang="ts">
  import { onMount } from "svelte";

  import { createMessagesAPI } from "../../api/messages/api.js";
  import type { MessagesCapabilities } from "../../api/messages/types";
  import type { MessagesRoute } from "../../messages/route";
  import { createSavedSearchesAPI } from "../../api/messages/savedSearchesClient.js";
  import type { MessageLinkInput } from "../../messages/messageLinks";
  import type { KataAPI } from "../../messages/types";
  import MessagesSetupDialog from "../../components/messages/MessagesSetupDialog.svelte";
  import MessagesWorkspace from "../../components/messages/MessagesWorkspace.svelte";

  interface Props {
    route: MessagesRoute;
    onRouteChange: (next: MessagesRoute) => void;
    kata?: Pick<KataAPI, "search"> | undefined;
    onLinkMessage?: ((
      issueUid: string,
      input: MessageLinkInput,
    ) => Promise<{ qualified_id: string }>) | undefined;
    onOpenIssue?: ((uid: string) => void) | undefined;
    onCapabilitiesChange?: ((capabilities: MessagesCapabilities | null) => void) | undefined;
  }

  let {
    route,
    onRouteChange,
    kata = undefined,
    onLinkMessage = undefined,
    onOpenIssue = undefined,
    onCapabilitiesChange = undefined,
  }: Props = $props();

  const messagesApi = createMessagesAPI();
  const savedSearchesApi = createSavedSearchesAPI();

  let capabilities: MessagesCapabilities | null = $state(null);
  let loading = $state(true);
  let loadError: string | null = $state(null);
  let setupOpen = $state(false);
  let messagesConfigVersion = $state(0);

  async function loadCapabilities(): Promise<void> {
    loading = true;
    loadError = null;
    try {
      capabilities = await messagesApi.capabilities();
      onCapabilitiesChange?.(capabilities);
    } catch (err) {
      capabilities = null;
      onCapabilitiesChange?.(null);
      loadError = err instanceof Error ? err.message : "Could not load Messages.";
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void loadCapabilities();
  });

  async function saveSetup(input: { url: string; api_key_env: string }): Promise<MessagesCapabilities> {
    const fresh = await messagesApi.configure(input);
    capabilities = fresh;
    onCapabilitiesChange?.(fresh);
    messagesConfigVersion += 1;
    if (fresh.ok) {
      setTimeout(() => {
        document.querySelector<HTMLElement>("#messages-search-input")?.focus();
      }, 0);
    }
    return fresh;
  }
</script>

<section class="messages-feature" aria-labelledby="messages-title">
  <header class="messages-header">
    <h1 id="messages-title">Messages</h1>
    {#if capabilities && !capabilities.ok}
      <button type="button" class="header-action" onclick={() => (setupOpen = true)}>
        Configure
      </button>
    {/if}
  </header>

  {#if loading}
    <div class="messages-state" role="status">Loading Messages...</div>
  {:else if loadError}
    <div class="messages-state" role="alert">
      <p>{loadError}</p>
      <button type="button" onclick={loadCapabilities}>Retry</button>
    </div>
  {:else if capabilities && !capabilities.configured}
    <div class="messages-state">
      <p>Messages are not set up.</p>
      <button type="button" onclick={() => (setupOpen = true)}>Set up Messages</button>
    </div>
  {:else if capabilities}
    <MessagesWorkspace
      {messagesApi}
      {savedSearchesApi}
      {capabilities}
      {route}
      {onRouteChange}
      {kata}
      {onLinkMessage}
      {onOpenIssue}
      onConfigure={() => (setupOpen = true)}
      {messagesConfigVersion}
    />
  {/if}
</section>

<MessagesSetupDialog
  open={setupOpen}
  initialURL={capabilities?.url}
  initialEnv={capabilities?.api_key_env}
  onClose={() => (setupOpen = false)}
  onSave={saveSetup}
/>

<style>
  .messages-feature {
    display: flex;
    flex-direction: column;
    min-height: 100%;
    background: var(--bg-app);
    color: var(--text-primary);
  }

  .messages-header {
    min-height: 56px;
    padding: 16px 20px;
    border-bottom: 1px solid var(--border-default);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
  }

  .messages-header h1 {
    margin: 0;
    font-size: var(--font-size-lg);
    font-weight: 650;
    line-height: 1.2;
  }

  .header-action,
  .messages-state button {
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    padding: 6px 10px;
    cursor: pointer;
  }

  .header-action:hover,
  .messages-state button:hover {
    background: var(--bg-surface-hover);
  }

  .messages-state {
    display: flex;
    flex: 1;
    min-height: 220px;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .messages-state p {
    margin: 0;
  }
</style>
