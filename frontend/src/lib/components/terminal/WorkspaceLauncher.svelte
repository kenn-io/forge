<script lang="ts">
  import type {
    LaunchTarget,
    QuickAction,
    RuntimeSession,
  } from "../../api/types.js";
  import MessagesSquareIcon from "@lucide/svelte/icons/messages-square";
  import PlayIcon from "@lucide/svelte/icons/play";
  import ZapIcon from "@lucide/svelte/icons/zap";
  import LaunchTargetName from "./LaunchTargetName.svelte";
  import { isVisibleLaunchTarget } from "./launchTargets";
  import { sortQuickActionsByLabel } from "../../stores/workspace-quick-actions.js";

  interface WorkspaceLauncherProps {
    launchTargets: LaunchTarget[];
    sessions: RuntimeSession[];
    displayLabels?: Record<string, string>;
    launchingKey?: string | null;
    readonly?: boolean;
    /** Configured quick actions; the section renders only when non-empty and onQuickAction is set. */
    quickActions?: QuickAction[];
    onLaunch?: (targetKey: string) => void;
    onQuickAction?: ((action: QuickAction) => void) | undefined;
    onOpenSession?: (sessionKey: string) => void;
  }

  const {
    launchTargets,
    sessions,
    displayLabels = {},
    launchingKey = null,
    readonly = false,
    quickActions = [],
    onLaunch,
    onQuickAction,
    onOpenSession,
  }: WorkspaceLauncherProps = $props();

  const visibleTargets = $derived(launchTargets.filter(isVisibleLaunchTarget));
  // ACP agents open a chat, not a terminal, so they get their own section.
  const terminalTargets = $derived(visibleTargets.filter((target) => target.kind !== "acp"));
  const acpTargets = $derived(visibleTargets.filter((target) => target.kind === "acp"));
  const sortedQuickActions = $derived(sortQuickActionsByLabel(quickActions));
  const showQuickActions = $derived(quickActions.length > 0 && onQuickAction !== undefined);

  function quickActionTarget(action: QuickAction): LaunchTarget | undefined {
    return launchTargets.find((target) => target.key === action.agent && (target.kind === "agent" || target.kind === "acp"));
  }

  function quickActionDisabledReason(action: QuickAction): string {
    const target = quickActionTarget(action);
    if (!target) return `Agent "${action.agent}" is not configured`;
    if (!target.available) return target.disabled_reason || `Agent "${action.agent}" is not available`;
    return "";
  }

  function targetLabel(target: LaunchTarget): string {
    return target.kind === "plain_shell" ? "Shell" : target.label;
  }

  function statusLabel(status: string): string {
    if (status === "running") return "Running";
    if (status === "starting") return "Starting";
    if (status === "exited") return "Exited";
    if (status === "parked") return "Stopped while idle";
    return status;
  }

  function labelFor(session: RuntimeSession): string {
    return displayLabels[session.key] ?? session.label;
  }
</script>

<section class={["workspace-launcher", readonly && "readonly"]} aria-label="Session launcher">
  {#snippet launchCards(targets: LaunchTarget[])}
    <div class="launch-grid">
      {#each targets as target (target.key)}
        {@const isLaunching = launchingKey === target.key}
        <button
          class="launch-card"
          disabled={readonly || !target.available || isLaunching}
          title={target.disabled_reason ?? targetLabel(target)}
          aria-label={targetLabel(target)}
          onclick={() => onLaunch?.(target.key)}
        >
          <LaunchTargetName {target} label={targetLabel(target)} iconSize={14} fallbackIcon />
          {#if isLaunching}
            <span class="card-status">starting…</span>
          {/if}
        </button>
      {/each}
    </div>
  {/snippet}

  {#if terminalTargets.length > 0 || acpTargets.length === 0}
    <div class="launcher-section" role="group" aria-label="Launch">
      <div class="section-bar">
        <PlayIcon
          class="section-icon"
          size="12"
          strokeWidth="2.25"
          aria-hidden="true"
        />
        <span class="section-title">Launch</span>
        <span class="section-count">{terminalTargets.length}</span>
      </div>
      {@render launchCards(terminalTargets)}
    </div>
  {/if}

  {#if acpTargets.length > 0}
    <div class="launcher-section" role="group" aria-label="ACP agents">
      <div class="section-bar">
        <MessagesSquareIcon
          class="section-icon acp"
          size="12"
          strokeWidth="2.25"
          aria-hidden="true"
        />
        <span class="section-title">ACP agents</span>
        <span class="section-count">{acpTargets.length}</span>
        <span class="section-hint">Chat sessions</span>
      </div>
      {@render launchCards(acpTargets)}
    </div>
  {/if}

  {#if showQuickActions}
    <div class="launcher-section">
      <div class="section-bar">
        <ZapIcon
          class="section-icon quick"
          size="12"
          strokeWidth="2.25"
          aria-hidden="true"
        />
        <span class="section-title">Quick actions</span>
        <span class="section-count">{quickActions.length}</span>
      </div>
      <div class="launch-grid">
        {#each sortedQuickActions as action, index (`${index}:${action.label}`)}
          {@const target = quickActionTarget(action)}
          {@const disabledReason = quickActionDisabledReason(action)}
          <button
            class="launch-card quick-card"
            disabled={readonly || disabledReason !== ""}
            title={disabledReason || action.prompt}
            aria-label={action.label}
            onclick={() => onQuickAction?.(action)}
          >
            <span class="quick-label">{action.label}</span>
            {#if target}
              <span class="quick-agent">
                <LaunchTargetName {target} label={target.label} iconSize={11} fallbackIcon />
              </span>
            {:else}
              <span class="quick-agent">{action.agent}</span>
            {/if}
          </button>
        {/each}
      </div>
    </div>
  {/if}

  {#if sessions.length > 0}
    <div class="launcher-section">
      <div class="section-bar">
        <span class="section-title">Active sessions</span>
        <span class="section-count">{sessions.length}</span>
      </div>
      <div class="session-list">
        {#each sessions as session (session.key)}
          <button
            class="session-row"
            disabled={readonly}
            onclick={() => onOpenSession?.(session.key)}
          >
            <span
              class={["session-dot", session.status]}
              aria-hidden="true"
            ></span>
            <span class="session-label">{labelFor(session)}</span>
            <span class="session-status">{statusLabel(session.status)}</span>
          </button>
        {/each}
      </div>
    </div>
  {/if}
</section>

<style>
  .workspace-launcher {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
    padding: 14px 16px 18px;
    min-width: 0;
    height: 100%;
    overflow: auto;
    background: var(--bg-primary);
    color: var(--text-primary);
  }

  .workspace-launcher.readonly {
    pointer-events: none;
  }

  .launcher-section {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .section-bar {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 18px;
    color: var(--text-muted);
  }

  :global(.section-icon) {
    color: var(--accent-green);
  }

  .workspace-launcher.readonly :global(.section-icon) {
    color: var(--text-muted);
  }

  .section-title {
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: var(--label-transform, uppercase);
    letter-spacing: var(--letter-spacing-label, 0.08em);
  }

  .section-count {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 16px;
    height: 14px;
    padding: 0 4px;
    border-radius: 3px;
    background: var(--bg-inset);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    font-family: var(--font-mono);
  }

  .launch-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 4px;
  }

  .launch-card {
    --launch-target-gap: 8px;
    --launch-target-icon-color: var(--text-secondary);

    display: grid;
    grid-template-columns: minmax(0, 1fr);
    align-items: center;
    gap: 4px 8px;
    /* min-height instead of fixed height: when a card flips into the
     * launching state it adds .card-status as a second grid row; a
     * fixed height clipped that row, leaving "starting…" overlapping
     * the label. */
    min-height: 32px;
    padding: 5px 10px;
    border: 1px solid var(--border-muted);
    border-radius: 3px;
    background: var(--bg-surface);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
    transition: border-color 80ms ease, background-color 80ms ease,
      color 80ms ease;
  }

  .launch-card:hover:not(:disabled) {
    border-color: var(--accent-blue);
    background: color-mix(
      in srgb,
      var(--accent-blue) 6%,
      var(--bg-surface)
    );
  }

  .launch-card:hover:not(:disabled) {
    --launch-target-icon-color: var(--accent-blue);
  }

  .launch-card:disabled {
    cursor: not-allowed;
    color: var(--text-muted);
    opacity: var(--opacity-disabled);
  }

  .launch-card :global(.launch-target-label) {
    font-weight: 600;
    letter-spacing: 0.005em;
  }

  .section-bar :global(.section-icon.quick) {
    color: var(--accent-amber);
  }

  .section-bar :global(.section-icon.acp) {
    color: var(--accent-purple);
  }

  .workspace-launcher.readonly .section-bar :global(.section-icon.quick),
  .workspace-launcher.readonly .section-bar :global(.section-icon.acp) {
    color: var(--text-muted);
  }

  .section-hint {
    margin-left: auto;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .quick-card {
    --launch-target-icon-color: var(--text-muted);

    gap: 2px;
  }

  .quick-label {
    overflow: hidden;
    font-weight: 600;
    letter-spacing: 0.005em;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .quick-agent {
    display: flex;
    min-width: 0;
    overflow: hidden;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    white-space: nowrap;
  }

  .quick-agent :global(.launch-target-label) {
    font-weight: 500;
  }

  .card-status {
    grid-column: 1 / -1;
    margin-top: -2px;
    color: var(--accent-amber);
    font-size: var(--font-size-xs);
    font-family: var(--font-mono);
  }

  .session-list {
    display: flex;
    flex-direction: column;
  }

  .session-row {
    display: grid;
    grid-template-columns: 8px 1fr auto;
    align-items: center;
    gap: var(--space-4);
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border-muted);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
    transition: background-color 80ms ease, color 80ms ease;
  }

  .session-row + .session-row {
    border-top: 0;
  }

  .session-row:first-child {
    border-radius: 3px 3px 0 0;
  }

  .session-row:last-child {
    border-radius: 0 0 3px 3px;
  }

  .session-row:only-child {
    border-radius: 3px;
  }

  .session-row:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .session-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--accent-green);
    flex-shrink: 0;
    box-shadow: 0 0 0 2px
      color-mix(in srgb, var(--accent-green) 25%, transparent);
  }

  .session-dot.starting {
    background: var(--accent-amber);
    box-shadow: 0 0 0 2px
      color-mix(in srgb, var(--accent-amber) 25%, transparent);
    animation: pulse 1.4s ease-in-out infinite;
  }

  .session-dot.exited,
  .session-dot.parked {
    background: var(--text-muted);
    box-shadow: none;
  }

  .session-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
  }

  .session-status {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-family: var(--font-mono);
    letter-spacing: 0;
  }

  @keyframes pulse {
    0%, 100% { opacity: 0.55; }
    50% { opacity: 1; }
  }
</style>
