<script lang="ts">
  import { HarnessIcon } from "@kenn-io/kit-ui";
  import BoxIcon from "@lucide/svelte/icons/box";
  import SparklesIcon from "@lucide/svelte/icons/sparkles";
  import TerminalIcon from "@lucide/svelte/icons/terminal";

  import type { LaunchTarget } from "../../api/types.js";
  import { launchTargetHarness } from "./agentHarness";

  /**
   * A launch target's icon and label. The icon is the kit-ui harness glyph
   * when the target's key resolves to one, otherwise the generic kind icon.
   * The label is always the target's own.
   */
  interface Props {
    target: Pick<LaunchTarget, "kind" | "key">;
    label: string;
    /** Icon size in px, for the harness glyph and the generic fallback alike. */
    iconSize?: number;
    /** Render the generic kind icon when no glyph matches; off by default. */
    fallbackIcon?: boolean;
  }

  const { target, label, iconSize = 14, fallbackIcon = false }: Props = $props();

  const harness = $derived(launchTargetHarness(target));
</script>

<span class="launch-target-name">
  {#if harness}
    <HarnessIcon {harness} size={iconSize} decorative class="launch-target-icon" />
  {:else if fallbackIcon}
    <span class="launch-target-icon" aria-hidden="true">
      {#if target.kind === "plain_shell"}
        <TerminalIcon size={iconSize} strokeWidth="2" />
      {:else if target.kind === "agent"}
        <SparklesIcon size={iconSize} strokeWidth="2" />
      {:else}
        <BoxIcon size={iconSize} strokeWidth="2" />
      {/if}
    </span>
  {/if}
  <span class="launch-target-label">{label}</span>
</span>

<style>
  .launch-target-name {
    display: inline-flex;
    align-items: center;
    gap: var(--launch-target-gap, 8px);
    min-width: 0;
  }

  .launch-target-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
    color: var(--launch-target-icon-color, var(--text-muted));
    transition: color 80ms ease;
  }

  .launch-target-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
