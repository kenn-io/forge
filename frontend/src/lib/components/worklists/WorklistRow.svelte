<script lang="ts">
  import { Chip, DiffStats, type ChipTone } from "@kenn-io/kit-ui";
  import CheckIcon from "@lucide/svelte/icons/check";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import CircleDotIcon from "@lucide/svelte/icons/circle-dot";
  import GitMergeIcon from "@lucide/svelte/icons/git-merge";
  import GitPullRequestClosedIcon from "@lucide/svelte/icons/git-pull-request-closed";
  import GitPullRequestIcon from "@lucide/svelte/icons/git-pull-request";
  import type { WorklistItem } from "../../api/worklists.js";
  import { preventShiftTextSelection } from "../../utils/range-selection.svelte.js";
  import AgentStatusIndicator from "../shared/AgentStatusIndicator.svelte";
  import LabelRow from "../shared/LabelRow.svelte";
  import PRSizeSparkline from "../shared/PRSizeSparkline.svelte";
  import PullStatusIcons from "../shared/PullStatusIcons.svelte";
  import WorkspaceIndicator from "../shared/WorkspaceIndicator.svelte";
  import WorklistCaveatIcon from "./WorklistCaveatIcon.svelte";
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
    if (item.pull?.IsDraft) return "draft";
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
    return null;
  });

  const labels = $derived(item.pull?.labels ?? item.issue?.labels ?? []);
  // Providers that report no line counts leave both at zero; show nothing then.
  const pullSize = $derived(
    item.pull && (item.pull.Additions > 0 || item.pull.Deletions > 0)
      ? { additions: item.pull.Additions, deletions: item.pull.Deletions }
      : null,
  );
  const agentState = $derived(item.pull?.workspace?.agent_state ?? item.issue?.workspace?.agent_state);
</script>

<button
  type="button"
  class={[
    "worklist-row",
    { opened, "multi-selected": multiSelected, gone: item.freshness === "gone", handled: item.handled_at !== null },
  ]}
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
      <LabelRow {labels} compact />
    </span>
    <span class="worklist-row-details" id={`${id}-details`}>
    <span class="worklist-row-meta">
      <span class="worklist-row-ref">{item.repo.owner}/{item.repo.name}#{item.number}</span>
      {#if pullSize}
        <span class="worklist-row-size"><DiffStats additions={pullSize.additions} deletions={pullSize.deletions} dimZeros /></span>
      {/if}
      {#if item.pull}
        <PRSizeSparkline history={item.pull.diff_size_history} currentHead={item.pull.platform_head_sha} currentBase={item.pull.platform_base_sha ?? ""} compact />
      {/if}
      {#if item.note}
        <span class="worklist-row-sep" aria-hidden="true">·</span>
        <span class="worklist-row-note">{item.note}</span>
      {/if}
    </span>
    {#if item.caveat}
      <span class={["worklist-row-caveat", `level-${item.caveat_level}`]} title={item.caveat}>
        <WorklistCaveatIcon level={item.caveat_level} size={11} />
        <span class="worklist-row-caveat-text">{item.caveat}</span>
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
    <AgentStatusIndicator state={agentState} />
    {#if kindTone === "draft"}
      <span class="kit-sr-only">Draft</span>
    {/if}
    {#if item.pull}
      <PullStatusIcons pr={item.pull} />
    {:else if item.issue?.workspace}
      <WorkspaceIndicator status={item.issue.workspace.status} />
    {/if}
    {#if statusChip}
      <Chip size="xs" tone={statusChip.tone} uppercase={false}>{statusChip.label}</Chip>
    {/if}
    {#if item.handled_at !== null}
      <span class="worklist-row-handled" aria-label="Handled" title="Handled">
        <CheckIcon size={13} strokeWidth={2.4} aria-hidden="true" />
      </span>
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
    overflow: hidden;
  }

  .worklist-row-kind {
    display: inline-flex;
    flex-shrink: 0;
  }

  .tone-open {
    color: var(--accent-green);
  }

  .tone-draft {
    color: var(--accent-amber);
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

  /* Starts at its floor and grows only into space the labels leave, so the
   * title gives up its room before any chip does. It shrinks below the floor
   * only when the fixed content beside it, such as the status and number,
   * needs the room. */
  .worklist-row-title {
    flex: 1 1 3rem;
    min-width: 0;
    max-width: max-content;
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

  .worklist-row.handled .worklist-row-score,
  .worklist-row.handled .worklist-row-body {
    opacity: 0.62;
  }

  .worklist-row.handled .worklist-row-title {
    color: var(--text-muted);
    text-decoration: line-through;
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

  .worklist-row-size {
    margin-left: var(--space-2);
    white-space: nowrap;
  }

  .worklist-row-sep {
    margin: 0 var(--space-2);
  }

  .worklist-row-caveat {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .worklist-row-caveat.level-warning {
    color: var(--accent-amber);
  }

  .worklist-row-caveat.level-critical {
    color: var(--accent-red);
  }

  .worklist-row-caveat-text {
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

  /* A PR can carry many status icons; past the cap they wrap onto another
   * line instead of squeezing the title or overflowing a narrow rail. */
  .worklist-row-status {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: var(--space-2);
    max-width: 120px;
    padding-top: 1px;
    white-space: nowrap;
  }

  .worklist-row-handled {
    display: inline-flex;
    color: var(--accent-green);
  }
</style>
