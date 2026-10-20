<script lang="ts">
  import { Chip, type ChipTone } from "@kenn-io/kit-ui";
  import AlertTriangleIcon from "@lucide/svelte/icons/alert-triangle";
  import CircleCheckBigIcon from "@lucide/svelte/icons/circle-check-big";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import CircleDotIcon from "@lucide/svelte/icons/circle-dot";
  import GitMergeIcon from "@lucide/svelte/icons/git-merge";
  import GitPullRequestClosedIcon from "@lucide/svelte/icons/git-pull-request-closed";
  import GitPullRequestIcon from "@lucide/svelte/icons/git-pull-request";
  import OctagonXIcon from "@lucide/svelte/icons/octagon-x";
  import type { WorklistItem } from "../../api/worklists.js";
  import { bucketCIChecks, parseCIChecks } from "../../utils/ci-buckets.js";
  import { preventShiftTextSelection } from "../../utils/range-selection.svelte.js";
  import CITokenCluster, { composeAriaLabel } from "../shared/CITokenCluster.svelte";
  import WorkspaceIndicator from "../shared/WorkspaceIndicator.svelte";
  import { worklistChangeMarkers, worklistItemKey } from "./worklist-items.js";

  interface Props {
    item: WorklistItem;
    opened: boolean;
    multiSelected: boolean;
    onclick: (event: MouseEvent) => void;
    oncontextmenu: (event: MouseEvent) => void;
  }

  let { item, opened, multiSelected, onclick, oncontextmenu }: Props = $props();

  const id = $props.id();
  const markers = $derived(worklistChangeMarkers(item));
  const state = $derived((item.live ?? item.snapshot).state.trim().toLowerCase());
  const kindTone = $derived.by(() => {
    if (item.live === null) return "muted";
    if (state === "merged") return "merged";
    if (state === "closed") return item.item_type === "pr" ? "closed" : "merged";
    return "open";
  });

  const scoreTone = $derived.by((): ChipTone => {
    const score = item.score ?? 0;
    if (score >= 9) return "success";
    if (score >= 7) return "info";
    if (score >= 5) return "warning";
    return "muted";
  });

  const statusChip = $derived.by((): { label: string; tone: ChipTone } | null => {
    if (item.live === null) return null;
    if (state === "merged") return { label: "Merged", tone: "merged" };
    if (state === "closed") return { label: "Closed", tone: "danger" };
    const pull = item.pull;
    if (!pull) return null;
    if (pull.MergeableState.trim().toLowerCase() === "dirty") return { label: "Conflict", tone: "danger" };
    if (pull.IsDraft) return { label: "Draft", tone: "muted" };
    return null;
  });

  const review = $derived.by((): { kind: "approved" | "changes-requested"; label: string } | null => {
    if (!item.pull || state !== "open") return null;
    const decision = item.pull.ReviewDecision.trim().toUpperCase();
    if (decision === "APPROVED") return { kind: "approved", label: "PR approved" };
    if (decision === "CHANGES_REQUESTED") return { kind: "changes-requested", label: "Changes requested" };
    return null;
  });

  const ci = $derived.by(() => {
    if (!item.pull || state !== "open") return null;
    const parsed = parseCIChecks(item.pull.CIChecksJSON);
    if (parsed.error !== null) return null;
    const bucketed = bucketCIChecks(parsed.checks);
    return bucketed.all.length > 0 ? bucketed : null;
  });

  const workspace = $derived(item.pull?.workspace ?? item.issue?.workspace ?? null);
</script>

<button
  type="button"
  class={["worklist-row", { opened, "multi-selected": multiSelected, gone: item.freshness === "gone" }]}
  data-worklist-key={worklistItemKey(item)}
  aria-labelledby={`${id}-title`}
  aria-describedby={`${id}-details ${id}-status`}
  aria-current={opened ? "true" : undefined}
  aria-pressed={multiSelected || undefined}
  onmousedown={preventShiftTextSelection}
  {onclick}
  {oncontextmenu}
>
  <span class="worklist-row-score">
    {#if item.score !== null}
      <Chip size="sm" tone={scoreTone} uppercase={false} title="Agent score">{item.score}</Chip>
    {/if}
  </span>
  <span class="worklist-row-body">
    <span class="worklist-row-title-line">
      <span class={["worklist-row-kind", `tone-${kindTone}`]} aria-hidden="true">
        {#if item.item_type === "pr"}
          {#if kindTone === "merged"}<GitMergeIcon size={13} />
          {:else if kindTone === "closed"}<GitPullRequestClosedIcon size={13} />
          {:else}<GitPullRequestIcon size={13} />{/if}
        {:else if kindTone === "merged"}<CircleCheckIcon size={13} />
        {:else}<CircleDotIcon size={13} />{/if}
      </span>
      <span class="worklist-row-title" id={`${id}-title`}>{item.title}</span>
    </span>
    <span class="worklist-row-details" id={`${id}-details`}>
    <span class="worklist-row-meta">
      <span class="worklist-row-ref">{item.repo.owner}/{item.repo.name}#{item.number}</span>
      {#if item.note}
        <span class="worklist-row-sep" aria-hidden="true">·</span>
        <span class="worklist-row-note">{item.note}</span>
      {/if}
    </span>
    {#if item.caveat}
      <span class="worklist-row-caveat" title={item.caveat}>
        <AlertTriangleIcon size={11} aria-hidden="true" />
        <span>{item.caveat}</span>
      </span>
    {/if}
    {#if markers.length > 0}
      <span class="worklist-row-since">
        {#each markers as marker (marker.label)}
          <Chip size="xs" tone={marker.tone} uppercase={false}>{marker.label}</Chip>
        {/each}
      </span>
    {/if}
    </span>
  </span>
  <span class="worklist-row-status" id={`${id}-status`}>
    {#if workspace}
      <WorkspaceIndicator status={workspace.status} />
    {/if}
    {#if review}
      <span
        class={["worklist-row-review", `worklist-row-review--${review.kind}`]}
        aria-label={review.label}
        title={review.label}
      >
        {#if review.kind === "approved"}
          <CircleCheckBigIcon size={13} strokeWidth={2.2} aria-hidden="true" />
        {:else}
          <OctagonXIcon size={13} strokeWidth={2.2} aria-hidden="true" />
        {/if}
      </span>
    {/if}
    {#if statusChip}
      <Chip size="xs" tone={statusChip.tone} uppercase={false}>{statusChip.label}</Chip>
    {/if}
    {#if ci}
      <span class="worklist-row-ci" aria-hidden="true">
        <CITokenCluster bucketed={ci} size="compact" pendingStyle="static" />
      </span>
      <span class="kit-sr-only">{composeAriaLabel(ci)}</span>
    {/if}
  </span>
</button>

<style>
  .worklist-row {
    display: grid;
    grid-template-columns: 28px minmax(0, 1fr) auto;
    align-items: start;
    gap: var(--space-4);
    width: 100%;
    padding: 7px 12px;
    border: 0;
    border-bottom: 1px solid var(--sidebar-list-border-muted, var(--border-muted));
    background: var(--sidebar-row-bg, var(--bg-surface));
    color: var(--text-primary);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }

  .worklist-row:hover {
    background: var(--sidebar-row-hover-bg, var(--bg-surface-hover));
  }

  .worklist-row.opened {
    background: var(--bg-row-selected, var(--bg-inset));
  }

  .worklist-row.multi-selected,
  .worklist-row.multi-selected:hover {
    background: color-mix(in srgb, var(--accent-blue) 14%, var(--sidebar-row-bg, var(--bg-surface)));
  }

  .worklist-row:focus-visible {
    outline: none;
    box-shadow: inset 0 0 0 1px var(--accent-blue);
  }

  .worklist-row-score {
    display: flex;
    justify-content: center;
    padding-top: 1px;
    font-variant-numeric: tabular-nums;
  }

  .worklist-row-body {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .worklist-row-title-line {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
  }

  .worklist-row-kind {
    display: inline-flex;
    flex-shrink: 0;
  }

  .tone-open {
    color: var(--accent-green);
  }

  .tone-merged {
    color: var(--accent-purple);
  }

  .tone-closed {
    color: var(--accent-red);
  }

  .tone-muted {
    color: var(--text-muted);
  }

  .worklist-row-title {
    min-width: 0;
    overflow: hidden;
    font-weight: 500;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .worklist-row.opened .worklist-row-title {
    font-weight: 600;
  }

  .worklist-row.gone .worklist-row-title {
    color: var(--text-muted);
  }

  .worklist-row-details {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .worklist-row-meta {
    display: -webkit-box;
    overflow: hidden;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    overflow-wrap: anywhere;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
  }

  .worklist-row-ref {
    font-family: var(--font-mono);
  }

  .worklist-row-sep {
    margin: 0 var(--space-2);
  }

  .worklist-row-caveat {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
    color: var(--accent-amber);
    font-size: var(--font-size-xs);
  }

  .worklist-row-caveat :global(svg) {
    flex-shrink: 0;
  }

  .worklist-row-caveat span {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .worklist-row-since {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
    padding-top: 2px;
  }

  .worklist-row-since :global(.kit-chip) {
    white-space: nowrap;
  }

  .worklist-row-status {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding-top: 1px;
    white-space: nowrap;
  }

  .worklist-row-ci,
  .worklist-row-review {
    display: inline-flex;
  }

  .worklist-row-review--approved {
    color: var(--accent-green);
  }

  .worklist-row-review--changes-requested {
    color: var(--accent-red);
  }
</style>
