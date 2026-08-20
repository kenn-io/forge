<script lang="ts">
  import RotateCcwIcon from "@lucide/svelte/icons/rotate-ccw";
  import { Button, Checkbox, CodeBlock } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import type {
    MCPSettings as MCPSettingsType,
    MCPSettingsUpdate,
  } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import {
    SettingsWorkflow,
    settingsErrorMessage,
  } from "../../stores/settings-workflow.js";

  interface Props {
    mcp: MCPSettingsType;
    onUpdate: (mcp: MCPSettingsType) => void;
  }

  let { mcp, onUpdate }: Props = $props();
  const runtime = getAppRuntime();
  const embedded = isEmbedded();
  // svelte-ignore state_referenced_locally
  let currentMCP = $state(mcp);
  let saving = $state(false);
  // svelte-ignore state_referenced_locally
  let enabledDraft = $state(mcp.enabled);
  // svelte-ignore state_referenced_locally
  let portDraft = $state<number | undefined>(mcp.port);
  let portValid = $state(true);
  // svelte-ignore state_referenced_locally
  let diffCacheDraft = $state<number | undefined>(mcp.diff_cache_mb);
  let diffCacheValid = $state(true);

  const parsedPort = $derived(portDraft ?? 0);
  const parsedDiffCache = $derived(diffCacheDraft ?? 0);
  const pendingMCP: MCPSettingsUpdate = $derived({
    enabled: enabledDraft,
    port: parsedPort,
    diff_cache_mb: parsedDiffCache,
  });
  const isDirty = $derived(
    enabledDraft !== currentMCP.enabled ||
      parsedPort !== (currentMCP.port ?? 0) ||
      parsedDiffCache !== (currentMCP.diff_cache_mb ?? 0),
  );
  const canSave = $derived(
    !embedded && !saving && isDirty && portValid && diffCacheValid,
  );
  const clientConfiguration = $derived.by(() => {
    if (!currentMCP.active_url) return "";
    const server: Record<string, unknown> = {
      type: "http",
      url: currentMCP.active_url,
    };
    if (currentMCP.active_requires_auth) {
      server.headers = {
        Authorization: "Bearer ${KENN_FORGE_API_TOKEN}",
      };
    }
    return JSON.stringify({ mcpServers: { "kenn-forge": server } }, null, 2);
  });

  function resetDraft(): void {
    enabledDraft = currentMCP.enabled;
    portDraft = currentMCP.port;
    portValid = true;
    diffCacheDraft = currentMCP.diff_cache_mb;
    diffCacheValid = true;
  }

  function save(): void {
    if (!canSave) return;
    saving = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.persist(() => ({ mcp: pendingMCP }));
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              showFlash(settingsErrorMessage(failure), { tone: "danger" });
            }),
          onSuccess: (settings) =>
            Effect.sync(() => {
              currentMCP = settings.mcp;
              resetDraft();
              onUpdate(settings.mcp);
            }),
        }),
        Effect.ensuring(
          Effect.sync(() => {
            saving = false;
          }),
        ),
      ),
      {
        operation: "save MCP companion settings",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }
</script>

<div class="mcp-settings">
  <Checkbox
    class="toggle-row"
    bind:checked={enabledDraft}
    disabled={embedded || saving}
    ariaLabel="Enable MCP companion"
  >
    <span>
      <span class="field-label">Enable MCP companion</span>
      <span class="field-help">
        Expose Forge workflows to local MCP clients over Streamable HTTP.
      </span>
    </span>
  </Checkbox>

  <div class="settings-grid">
    <label class="field">
      <span class="field-label">Port</span>
      <input
        type="number"
        min="0"
        max="65535"
        step="1"
        oninput={(event) => {
          portValid = event.currentTarget.validity.valid;
        }}
        bind:value={portDraft}
        placeholder="Automatic"
        disabled={embedded || saving}
        aria-invalid={!portValid}
      />
      <span class="field-help">Leave blank to use the Forge backend port plus one.</span>
    </label>

    <label class="field">
      <span class="field-label">Diff cache</span>
      <div class="input-with-unit">
        <input
          type="number"
          min="0"
          step="1"
          oninput={(event) => {
            diffCacheValid = event.currentTarget.validity.valid;
          }}
          bind:value={diffCacheDraft}
          placeholder="128"
          disabled={embedded || saving}
          aria-invalid={!diffCacheValid}
        />
        <span>MiB</span>
      </div>
      <span class="field-help">Leave blank for the 128 MiB default.</span>
    </label>
  </div>

  <div class="settings-actions">
    <Button
      size="sm"
      type="button"
      onclick={resetDraft}
      disabled={!isDirty || saving}
    >
      <RotateCcwIcon size="14" strokeWidth="2.2" aria-hidden="true" />
      Reset
    </Button>
    <Button
      tone="info"
      surface="solid"
      type="button"
      onclick={save}
      disabled={!canSave}
    >
      {saving ? "Saving..." : "Save MCP companion"}
    </Button>
  </div>

  {#if currentMCP.restart_required}
    <div class="restart-banner" role="status">
      <strong>Restart required.</strong>
      {#if currentMCP.enabled && !currentMCP.active_url}
        The MCP companion will start after the Forge daemon restarts.
      {:else if !currentMCP.enabled && currentMCP.active_url}
        The active companion will stop after the Forge daemon restarts.
      {:else}
        Restart the Forge daemon to apply these settings.
      {/if}
    </div>
  {/if}

  {#if currentMCP.active_url}
    <section class="connection" aria-labelledby="mcp-active-endpoint">
      <div>
        <h3 id="mcp-active-endpoint">Active endpoint</h3>
        <p>
          This is the endpoint currently served by the running daemon.
          {#if !currentMCP.enabled}
            It remains active until the daemon restarts.
          {/if}
        </p>
      </div>
      <CodeBlock
        code={currentMCP.active_url}
        title="Streamable HTTP endpoint"
        language="text"
        wrapToggle={false}
        copyLabel="Copy MCP endpoint"
      />
      <CodeBlock
        code={clientConfiguration}
        title="Generic client configuration"
        language="json"
        wrapToggle={false}
        copyLabel="Copy MCP client configuration"
      />
      {#if currentMCP.active_requires_auth}
        <p class="auth-note">
          Authentication is required. Set <code>KENN_FORGE_API_TOKEN</code> from
          the <code>token_path</code> reported by
          <code>kenn-forge daemon status --json</code>. Forge never displays the
          bearer token here.
        </p>
      {:else}
        <p class="auth-note">This active endpoint does not require authentication.</p>
      {/if}
    </section>
  {:else if currentMCP.enabled && !currentMCP.restart_required}
    <p class="inactive-note">
      No active MCP endpoint is reported by this daemon. Restart it and check its logs.
    </p>
  {/if}
</div>

<style>
  .mcp-settings {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }

  :global(.toggle-row) {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: var(--space-4);
  }

  :global(.toggle-row .kit-checkbox__box) {
    order: 2;
    margin-top: 2px;
  }

  .field-label {
    display: block;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .field-help,
  .connection p,
  .auth-note,
  .inactive-note {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    line-height: 1.45;
  }

  .settings-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--space-4);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }

  .field input {
    width: 100%;
    min-height: 30px;
    padding: 4px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
  }

  .field input:disabled {
    color: var(--text-muted);
    background: var(--bg-inset);
  }

  .field input[aria-invalid="true"] {
    border-color: var(--accent-red);
  }

  .input-with-unit {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }

  .input-with-unit span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .settings-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
  }

  .restart-banner {
    padding: 9px 11px;
    border: 1px solid var(--diff-stale-border);
    border-radius: var(--radius-sm);
    background: var(--diff-stale-bg);
    color: var(--diff-stale-text);
    font-size: var(--font-size-sm);
    line-height: 1.45;
  }

  .connection {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    padding-top: var(--space-5);
    border-top: 1px solid var(--border-muted);
  }

  .connection h3 {
    margin: 0 0 2px;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 700;
  }

  .auth-note code {
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: inherit;
  }

  @media (max-width: 640px) {
    .settings-grid {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
