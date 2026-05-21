<script module lang="ts">
  import type { CIBucketedChecks, CIBucket } from "../../utils/ci-buckets.js";

  type LabeledBucket = { key: CIBucket; singular: string; plural: string };
  const ORDER: LabeledBucket[] = [
    { key: "failed", singular: "failed check", plural: "failed checks" },
    { key: "pending", singular: "pending check", plural: "pending checks" },
    { key: "unknown", singular: "unknown check", plural: "unknown checks" },
    { key: "passed", singular: "passed check", plural: "passed checks" },
    { key: "skipped", singular: "skipped check", plural: "skipped checks" },
  ];

  export function composeAriaLabel(bucketed: CIBucketedChecks): string {
    const parts = ORDER
      .map(({ key, singular, plural }) => {
        const n = bucketed[key].length;
        if (n === 0) return null;
        return `${n} ${n === 1 ? singular : plural}`;
      })
      .filter((p): p is string => p !== null);
    return parts.length === 0 ? "CI: no checks" : `CI: ${parts.join(", ")}`;
  }
</script>

<script lang="ts">
  import XIcon from "@lucide/svelte/icons/x";
  import CheckIcon from "@lucide/svelte/icons/check";
  import MinusIcon from "@lucide/svelte/icons/minus";
  import DotIcon from "@lucide/svelte/icons/dot";
  import LoaderCircleIcon from "@lucide/svelte/icons/loader-circle";
  import { prefersReducedMotion } from "../../utils/prefers-reduced-motion.svelte.js";

  interface Props {
    bucketed: CIBucketedChecks;
    size: "default" | "compact";
    pendingStyle?: "animated" | "static"; // sidebar passes "static"
  }
  let { bucketed, size, pendingStyle = "animated" }: Props = $props();

  const pendingAnimated = $derived(pendingStyle === "animated" && !prefersReducedMotion());

  // Bare X/Check/Minus glyphs render visually smaller than their CircleX
  // siblings did, so bump the size up slightly to preserve visual weight.
  const iconSize = $derived(size === "compact" ? 12 : 14);
  // DotIcon's rendered dot is tiny relative to the box, so it needs to
  // be drawn larger than the rest to read at the same visual weight.
  const dotSize = $derived(size === "compact" ? 18 : 20);
</script>

{#if bucketed.failed.length > 0}
  <span class="tok tok-red" data-testid="ci-token-failed" aria-hidden="true">
    <XIcon size={iconSize} strokeWidth={2.8} /><span class="ct">{bucketed.failed.length}</span>
  </span>
{/if}
{#if bucketed.pending.length > 0}
  <span class="tok tok-amber" data-testid="ci-token-pending" aria-hidden="true">
    {#if pendingAnimated}
      <span class="spin"><LoaderCircleIcon size={iconSize} strokeWidth={2.5} /></span>
    {:else}
      <DotIcon size={dotSize} />
    {/if}
    <span class="ct">{bucketed.pending.length}</span>
  </span>
{/if}
{#if bucketed.unknown.length > 0}
  <span class="tok tok-purple" data-testid="ci-token-unknown" aria-hidden="true">
    <span class="qmark" aria-hidden="true">?</span><span class="ct">{bucketed.unknown.length}</span>
  </span>
{/if}
{#if bucketed.passed.length > 0}
  <span class="tok tok-green" data-testid="ci-token-passed" aria-hidden="true">
    <CheckIcon size={iconSize} strokeWidth={2.8} /><span class="ct">{bucketed.passed.length}</span>
  </span>
{/if}
{#if bucketed.skipped.length > 0}
  <span class="tok tok-muted" data-testid="ci-token-skipped" aria-hidden="true">
    <MinusIcon size={iconSize} strokeWidth={2.8} /><span class="ct">{bucketed.skipped.length}</span>
  </span>
{/if}

<style>
  .tok {
    display: inline-flex;
    align-items: center;
    vertical-align: middle;
    gap: 2px;
    font-variant-numeric: tabular-nums;
    font-weight: 700;
    line-height: 1;
  }
  .tok .ct { font-size: 0.9em; line-height: 1; }
  .tok :global(svg) { display: block; }
  .qmark { font-weight: 800; font-size: 0.9em; line-height: 1; padding-right: 1px; }
  .tok-red { color: var(--accent-red); }
  .tok-amber { color: var(--accent-amber); }
  .tok-green { color: var(--accent-green); }
  .tok-muted { color: var(--text-muted); }
  .tok-purple { color: var(--accent-purple); }
  .spin { display: inline-flex; animation: spin 0.9s linear infinite; }
  @keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
</style>
