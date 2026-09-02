<script lang="ts">
  import { HarnessMark } from "@kenn-io/kit-ui";
  import BoxIcon from "@lucide/svelte/icons/box";
  import SparklesIcon from "@lucide/svelte/icons/sparkles";
  import TerminalIcon from "@lucide/svelte/icons/terminal";

  import type { LaunchTarget } from "../../api/types.js";
  import { launchTargetMark } from "./agentHarness";

  /**
   * A launch target's visible name: the kit-ui harness wordmark when the
   * target's key resolves to one, otherwise a generic kind icon beside the
   * label. The label text is always in the DOM so the enclosing button keeps
   * its accessible name; it is only hidden visually when the wordmark already
   * says the same thing.
   */
  interface Props {
    target: Pick<LaunchTarget, "kind" | "key" | "label">;
    label: string;
    /** Wordmark height in px. */
    markSize?: number;
    /** Generic icon size in px for targets without a mark; omit to render text only. */
    iconSize?: number | null;
  }

  const { target, label, markSize = 14, iconSize = null }: Props = $props();

  const mark = $derived(launchTargetMark(target));
</script>

<span class="launch-target-name">
  {#if mark}
    <HarnessMark harness={mark.harness} size={markSize} decorative class="launch-target-mark" />
    <span class={["launch-target-label", !mark.showLabel && "kit-sr-only"]}>{label}</span>
  {:else}
    {#if iconSize !== null}
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
  {/if}
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
