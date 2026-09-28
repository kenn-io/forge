<script lang="ts">
  import { mountTerminalPopover } from "./terminal-popover.js";
  import type { LaunchTarget, QuickAction } from "../../api/types.js";
  import PlayIcon from "@lucide/svelte/icons/play";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import ZapIcon from "@lucide/svelte/icons/zap";
  import LaunchTargetName from "./LaunchTargetName.svelte";
  import { isVisibleLaunchTarget } from "./launchTargets";
  import { sortQuickActionsByLabel } from "../../stores/workspace-quick-actions.js";

  interface LaunchMenuProps {
    launchTargets: LaunchTarget[];
    launchingKey?: string | null;
    disabled?: boolean;
    /** False while the workspace host is parked/hidden: closes the popover
     *  so its window listeners don't outlive the visible surface. */
    hostVisible?: boolean;
    onLaunch?: (targetKey: string) => void;
    quickActions?: QuickAction[];
    onQuickAction?: (action: QuickAction) => void;
  }

  const {
    launchTargets,
    launchingKey = null,
    disabled = false,
    hostVisible = true,
    onLaunch,
    quickActions = [],
    onQuickAction,
  }: LaunchMenuProps = $props();

  let open = $state<"launch" | "quick" | null>(null);
  let rootEl = $state<HTMLDivElement | null>(null);
  let panelEl = $state<HTMLDivElement | null>(null);

  const visibleTargets = $derived(launchTargets.filter(isVisibleLaunchTarget));
  const sortedQuickActions = $derived(sortQuickActionsByLabel(quickActions));

  $effect(() => {
    if (disabled || !hostVisible) open = null;
  });

  function launch(targetKey: string): void {
    if (disabled) return;
    open = null;
    onLaunch?.(targetKey);
  }

  function targetLabel(target: LaunchTarget): string {
    return target.kind === "plain_shell" ? "Shell" : target.label;
  }

  $effect(() => {
    if (!open) return;

    function onPointerDown(ev: PointerEvent): void {
      if (rootEl && ev.target instanceof Node && (rootEl.contains(ev.target) || panelEl?.contains(ev.target))) {
        return;
      }
      open = null;
    }
    function onKeydown(ev: KeyboardEvent): void {
      if (ev.key === "Escape") {
        rootEl?.querySelector<HTMLButtonElement>('[aria-expanded="true"]')?.focus();
        open = null;
      }
    }
    window.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("keydown", onKeydown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("keydown", onKeydown);
    };
  });
</script>

<div class="launch-menu" bind:this={rootEl}>
  <button
    class="launch-trigger"
    type="button"
    title="Launch in this pane"
    aria-label="Launch"
    aria-haspopup="true"
    aria-expanded={open === "launch"}
    disabled={disabled}
    onclick={() => {
      if (disabled) return;
      open = open === "launch" ? null : "launch";
    }}
  >
    <PlayIcon
      class="launch-trigger-icon"
      size="11"
      strokeWidth="2.5"
      aria-hidden="true"
    />
    <ChevronDownIcon
      class="launch-trigger-chevron"
      size="12"
      strokeWidth="2"
      aria-hidden="true"
    />
  </button>
  {#if sortedQuickActions.length > 0 && onQuickAction}
    <button
      class="launch-trigger quick-actions-trigger"
      type="button"
      title="Quick actions"
      aria-label="Quick actions"
      aria-haspopup="dialog"
      aria-expanded={open === "quick"}
      {disabled}
      onclick={() => { open = open === "quick" ? null : "quick"; }}
    >
      <ZapIcon size="13" strokeWidth="2.2" aria-hidden="true" />
    </button>
  {/if}
  {#if open}
    <div
      class="launch-popover"
      role="dialog"
      aria-label={open === "quick" ? "Quick actions" : "Run configurations"}
      bind:this={panelEl}
      {@attach (node) => {
        const cleanup = mountTerminalPopover(node, rootEl!);
        node.querySelector<HTMLButtonElement>("button:enabled")?.focus();
        return cleanup;
      }}
    >
      <div class="popover-heading">{open === "quick" ? "Quick actions" : "Run configurations"}</div>
      {#if open === "quick"}
        {#each sortedQuickActions as action (action.label)}
          {@const target = launchTargets.find((candidate) => candidate.key === action.agent && candidate.kind === "agent")}
          <button
            class="launch-option"
            disabled={disabled || !target?.available || launchingKey === action.agent}
            title={!target ? `Agent "${action.agent}" is not configured` : !target.available ? target.disabled_reason || `Agent "${action.agent}" is not available` : action.prompt}
            onclick={() => { open = null; onQuickAction?.(action); }}
          >
            <LaunchTargetName target={{ kind: "agent", key: action.agent }} label={action.label} iconSize={13} fallbackIcon />
          </button>
        {/each}
      {:else}
        {#each visibleTargets as target (target.key)}
          <button
            class="launch-option"
            disabled={disabled || !target.available || launchingKey === target.key}
            title={target.disabled_reason ?? targetLabel(target)}
            onclick={() => launch(target.key)}
          >
            <LaunchTargetName {target} label={targetLabel(target)} iconSize={13} fallbackIcon />
          </button>
        {/each}
      {/if}
    </div>
  {/if}
</div>

<style>
  .launch-menu {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--space-1);
  }

  .quick-actions-trigger {
    width: 24px;
    justify-content: center;
  }

  .launch-trigger {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    height: 24px;
    padding: 0 4px;
    border: 1px solid transparent;
    border-radius: 3px;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    font-weight: 600;
    letter-spacing: 0.01em;
    cursor: pointer;
    transition: background-color 80ms ease, border-color 80ms ease;
  }

  .launch-trigger:hover {
    background: var(--bg-surface-hover);
    border-color: color-mix(in srgb, var(--text-muted) 40%, var(--border-default));
  }

  .launch-trigger[aria-expanded="true"] {
    background: var(--bg-surface-hover);
    border-color: var(--accent-blue);
  }

  :global(.launch-trigger-icon) {
    color: var(--accent-green);
    flex-shrink: 0;
  }

  :global(.launch-trigger-chevron) {
    color: var(--text-muted);
    flex-shrink: 0;
    margin-left: 1px;
  }

  .launch-popover {
    position: fixed;
    z-index: calc(var(--z-overlay) - 1);
    min-width: 220px;
    padding: 4px;
    border: 1px solid var(--border-default);
    border-radius: 4px;
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
  }

  .popover-heading {
    padding: 4px 8px 6px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 1px solid var(--border-muted);
    margin-bottom: 3px;
  }

  .launch-option {
    --launch-target-gap: 8px;
    --launch-target-icon-color: var(--text-muted);

    display: flex;
    align-items: center;
    width: 100%;
    height: 26px;
    padding: 0 8px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
  }

  .launch-option + .launch-option {
    margin-top: 1px;
  }

  .launch-option:hover:not(:disabled),
  .launch-option:focus-visible {
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    color: var(--accent-blue);
    outline: none;
  }

  .launch-option:hover:not(:disabled),
  .launch-option:focus-visible {
    --launch-target-icon-color: var(--accent-blue);
  }

  .launch-option:disabled {
    cursor: not-allowed;
    color: var(--text-muted);
    opacity: var(--opacity-disabled);
  }

  .launch-option :global(.launch-target-label) {
    font-weight: 500;
  }

</style>
