<script lang="ts">
  // WorkspaceFirstRunPanel is the embed surface that renders when the
  // project registry is empty. It is the first surface a fresh
  // installation lands on, and it must offer at least one action that
  // progresses toward a worktree - the master spec's "no dead ends"
  // rule. Every action returns a CommandResult so the surface can
  // render in-flight, success, and failure states.

  import {
    getProjectAction,
    getWorkspaceData,
    invokeProjectAction,
  } from "../../stores/embed-config.svelte.ts";
  import { resolveToolingStatus } from "../../stores/tooling-status.svelte.ts";
  import ToolingStatusBlock from "./ToolingStatusBlock.svelte";

  type ActionId = "add-existing" | "clone" | "connect-github";

  interface ActionDefinition {
    id: ActionId;
    label: string;
    description: string;
    requiresGh: boolean;
  }

  const ACTIONS: ActionDefinition[] = [
    {
      id: "add-existing",
      label: "Add an existing local repository",
      description: "Pick a folder you already cloned.",
      requiresGh: false,
    },
    {
      id: "clone",
      label: "Clone a repository",
      description: "Provide a URL and we'll clone it.",
      requiresGh: false,
    },
    {
      id: "connect-github",
      label: "Connect a GitHub repository",
      description: "Pick from your GitHub repos.",
      requiresGh: true,
    },
  ];

  let inFlight = $state<ActionId | null>(null);
  let lastError = $state<string | null>(null);

  const tooling = $derived(resolveToolingStatus());
  const provider = $derived.by(() => {
    const workspace = getWorkspaceData();
    if (!workspace) return undefined;
    const selectedHost = workspace.hosts.find(
      (host) => host.key === workspace.selectedHostKey,
    ) ?? workspace.hosts[0];
    return selectedHost?.platform;
  });
  const ghAuthed = $derived(
    tooling?.gh?.available === true &&
      tooling.gh.authenticated === true,
  );

  function isDisabled(definition: ActionDefinition): boolean {
    if (inFlight !== null) return true;
    if (definition.requiresGh && !ghAuthed) return true;
    return false;
  }

  function disabledReason(
    definition: ActionDefinition,
  ): string | undefined {
    if (definition.requiresGh && !ghAuthed) {
      if (!tooling?.gh?.available) {
        return "Install gh to use this option.";
      }
      return "Run gh auth login to use this option.";
    }
    return undefined;
  }

  async function runAction(definition: ActionDefinition): Promise<void> {
    if (isDisabled(definition)) return;
    const action = getProjectAction(definition.id);
    if (!action) {
      lastError =
        "This action is not available in this build. Please update " +
        "the host application.";
      return;
    }
    inFlight = definition.id;
    lastError = null;
    try {
      const result = await invokeProjectAction(action, {
        surface: "first-run-panel",
      });
      if (!result.ok) {
        lastError = result.message ?? "Action failed.";
      }
    } finally {
      inFlight = null;
    }
  }
</script>

<section class="first-run" aria-labelledby="first-run-title">
  <div class="first-run__intro">
    <h1 id="first-run-title" class="first-run__title">
      Get to your first worktree.
    </h1>
    <p class="first-run__lede">
      Worktrees keep one branch checked out per directory so each
      change you start has its own working tree, terminal, and
      agent. Pick a starting point below.
    </p>
  </div>

  <ul class="first-run__actions">
    {#each ACTIONS as action (action.id)}
      {@const disabled = isDisabled(action)}
      {@const reason = disabledReason(action)}
      <li class="first-run-action">
        <button
          type="button"
          class="first-run-action__button"
          {disabled}
          aria-busy={inFlight === action.id}
          aria-describedby={reason
            ? `first-run-action-reason-${action.id}`
            : undefined}
          onclick={() => runAction(action)}
        >
          <span class="first-run-action__label">
            {action.label}
            {#if inFlight === action.id}
              <span class="first-run-action__spinner" aria-hidden="true">…</span>
            {/if}
          </span>
          <span class="first-run-action__description">
            {action.description}
          </span>
        </button>
        {#if reason}
          <p
            class="first-run-action__reason"
            id="first-run-action-reason-{action.id}"
          >
            {reason}
          </p>
        {/if}
      </li>
    {/each}
  </ul>

  {#if lastError}
    <p class="first-run__error" role="alert">
      {lastError}
    </p>
  {/if}

  <ToolingStatusBlock {tooling} {provider} />
</section>

<style>
  .first-run {
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: 100%;
    max-width: 480px;
    margin: 24px auto;
    padding: 16px;
    box-sizing: border-box;
  }

  .first-run__intro {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .first-run__title {
    margin: 0;
    font-size: var(--font-size-xl);
    font-weight: 600;
    color: var(--text-primary);
  }

  .first-run__lede {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-md);
    line-height: 1.5;
  }

  .first-run__actions {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .first-run-action {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .first-run-action__button {
    appearance: none;
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    text-align: left;
    padding: 12px 14px;
    border-radius: var(--radius-md, 8px);
    font: inherit;
    cursor: pointer;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .first-run-action__button:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .first-run-action__button:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }

  .first-run-action__label {
    font-weight: 600;
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .first-run-action__spinner {
    color: var(--text-muted);
  }

  .first-run-action__description {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .first-run-action__reason {
    margin: 0 0 0 14px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .first-run__error {
    margin: 0;
    padding: 8px 12px;
    border: 1px solid var(--accent-red);
    border-radius: var(--radius-md, 8px);
    background: color-mix(in srgb, var(--accent-red) 10%, transparent);
    color: var(--accent-red);
    font-size: var(--font-size-md);
  }
</style>
