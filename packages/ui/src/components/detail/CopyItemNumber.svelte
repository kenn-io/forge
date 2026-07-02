<script lang="ts">
  import { onDestroy } from "svelte";
  import { copyToClipboard } from "@kenn-io/kit-ui";

  interface Props {
    kind: "pull" | "issue";
    number: number;
    url: string;
  }

  const { kind, number, url }: Props = $props();

  let copied = $state(false);
  let copyTimeout: ReturnType<typeof setTimeout> | null = null;

  const itemLabel = $derived(kind === "pull" ? "PR" : "issue");

  onDestroy(() => {
    if (copyTimeout !== null) {
      clearTimeout(copyTimeout);
    }
  });

  function copyLink(): void {
    if (!url) return;
    void copyToClipboard(url).then((ok) => {
      if (!ok) return;
      copied = true;
      if (copyTimeout !== null) {
        clearTimeout(copyTimeout);
      }
      copyTimeout = setTimeout(() => {
        copied = false;
        copyTimeout = null;
      }, 1500);
    });
  }
</script>

<button
  type="button"
  class="copy-number-btn"
  class:copy-number-btn--copied={copied}
  onclick={copyLink}
  disabled={!url}
  aria-label={`Copy ${itemLabel} #${number} link`}
  title={copied ? "Copied!" : `Copy ${itemLabel} link`}
>
  #{number}
</button>

<style>
  .copy-number-btn {
    appearance: none;
    border: 0;
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
    font: inherit;
    font-size: var(--font-size-sm);
    line-height: inherit;
    padding: 0;
    transition: box-shadow 0.1s, color 0.1s;
  }

  .copy-number-btn:hover,
  .copy-number-btn:focus-visible {
    color: var(--text-primary);
    box-shadow: inset 0 -1px 0 var(--text-muted);
  }

  .copy-number-btn:focus-visible {
    outline: 1px solid var(--border-strong);
    outline-offset: 2px;
    border-radius: 3px;
  }

  .copy-number-btn--copied {
    color: var(--accent-green);
  }

  .copy-number-btn:disabled {
    cursor: default;
    color: var(--text-muted);
    text-decoration: none;
  }

  @media (max-width: 640px) {
    .copy-number-btn {
      min-width: var(--detail-mobile-hit-target, 37px);
      min-height: var(--detail-mobile-hit-target, 37px);
      padding: var(--detail-mobile-space-xs, 6.5px);
      border-radius: 8.5px;
      font-size: var(--font-size-sm);
      line-height: 1.35;
    }
  }
</style>
