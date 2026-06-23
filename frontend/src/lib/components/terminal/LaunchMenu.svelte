<script lang="ts">
  import type { LaunchTarget } from "@middleman/ui/api/types";
  import PlayIcon from "@lucide/svelte/icons/play";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import SparklesIcon from "@lucide/svelte/icons/sparkles";
  import BoxIcon from "@lucide/svelte/icons/box";
  import { isVisibleLaunchTarget } from "./launchTargets";

  interface LaunchMenuProps {
    launchTargets: LaunchTarget[];
    launchingKey?: string | null;
    disabled?: boolean;
    onLaunch?: (targetKey: string) => void;
  }

  const {
    launchTargets,
    launchingKey = null,
    disabled = false,
    onLaunch,
  }: LaunchMenuProps = $props();

  let open = $state(false);
  let rootEl = $state<HTMLDivElement | null>(null);

  const visibleTargets = $derived(launchTargets.filter(isVisibleLaunchTarget));

  $effect(() => {
    if (disabled) open = false;
  });

  function launch(targetKey: string): void {
    if (disabled) return;
    open = false;
    onLaunch?.(targetKey);
  }

  function sourceLabel(target: LaunchTarget): string {
    if (target.kind === "plain_shell") return "shell";
    if (target.source === "config") return "configured";
    return target.source;
  }

  function targetLabel(target: LaunchTarget): string {
    return target.kind === "plain_shell" ? "Shell" : target.label;
  }

  $effect(() => {
    if (!open) return;

    function onPointerDown(ev: PointerEvent): void {
      if (rootEl && ev.target instanceof Node && rootEl.contains(ev.target)) {
        return;
      }
      open = false;
    }
    function onKeydown(ev: KeyboardEvent): void {
      if (ev.key === "Escape") {
        open = false;
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
    aria-label="Launch"
    aria-haspopup="true"
    aria-expanded={open}
    disabled={disabled}
    onclick={() => {
      if (disabled) return;
      open = !open;
    }}
  >
    <PlayIcon
      class="launch-trigger-icon"
      size="11"
      strokeWidth="2.5"
      aria-hidden="true"
    />
    <span>Launch</span>
    <ChevronDownIcon
      class="launch-trigger-chevron"
      size="12"
      strokeWidth="2"
      aria-hidden="true"
    />
  </button>
  {#if open}
    <div class="launch-popover">
      <div class="popover-heading">Run configurations</div>
      {#each visibleTargets as target (target.key)}
        {@const isShell = target.kind === "plain_shell"}
        {@const isAgent = target.kind === "agent"}
        <button
          class="launch-option"
          disabled={disabled || !target.available || launchingKey === target.key}
          title={target.disabled_reason ?? targetLabel(target)}
          onclick={() => launch(target.key)}
        >
          <span class="option-icon" aria-hidden="true">
            {#if isShell}
              <TerminalIcon size="13" strokeWidth="2" />
            {:else if isAgent}
              <SparklesIcon size="13" strokeWidth="2" />
            {:else}
              <BoxIcon size="13" strokeWidth="2" />
            {/if}
          </span>
          <span class="option-label">{targetLabel(target)}</span>
          <span class="option-source">{sourceLabel(target)}</span>
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .launch-menu {
    position: relative;
  }

  .launch-trigger {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 22px;
    padding: 0 7px 0 8px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    background: var(--bg-surface);
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
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 20;
    min-width: 220px;
    padding: 4px;
    border: 1px solid var(--border-default);
    border-radius: 4px;
    background: var(--bg-surface);
    box-shadow:
      0 1px 2px rgba(0, 0, 0, 0.04),
      0 4px 16px rgba(0, 0, 0, 0.12);
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
    display: grid;
    grid-template-columns: 16px 1fr auto;
    align-items: center;
    gap: 8px;
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

  .launch-option:hover:not(:disabled) .option-icon,
  .launch-option:focus-visible .option-icon {
    color: var(--accent-blue);
  }

  .launch-option:disabled {
    cursor: not-allowed;
    color: var(--text-muted);
    opacity: 0.6;
  }

  .option-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--text-muted);
  }

  .option-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
  }

  .option-source {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-family: var(--font-mono);
    text-transform: lowercase;
    letter-spacing: 0;
  }

  .launch-option:hover:not(:disabled) .option-source {
    color: color-mix(in srgb, var(--accent-blue) 80%, var(--text-muted));
  }
</style>
