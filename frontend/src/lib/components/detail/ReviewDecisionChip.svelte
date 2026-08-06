<script lang="ts">
  import type { PREvent } from "../../api/types.js";
  import { Chip, type ChipTone } from "@kenn-io/kit-ui";

  interface Props {
    decision: string;
    events?: PREvent[] | null;
  }

  let { decision, events = [] }: Props = $props();

  let popupOpen = $state(false);
  let wrapEl = $state<HTMLDivElement>();

  const approvers = $derived.by(() =>
    approversFromReviewEvents(events ?? []),
  );
  const normalizedDecision = $derived(normalizeReviewDecision(decision));
  const label = $derived(reviewLabel(normalizedDecision, approvers.length));
  const chipTone = $derived(reviewTone(normalizedDecision));
  const canExpand = $derived(
    normalizedDecision === "APPROVED" && approvers.length > 0,
  );

  function normalizeReviewDecision(reviewDecision: string): string {
    return reviewDecision.trim().toUpperCase();
  }

  function reviewTone(reviewDecision: string): ChipTone {
    if (reviewDecision === "APPROVED") return "success";
    if (reviewDecision === "CHANGES_REQUESTED") return "danger";
    return "muted";
  }

  function reviewLabel(reviewDecision: string, approverCount: number): string {
    const baseLabel = reviewDecision.replace(/_/g, " ");
    if (reviewDecision === "APPROVED" && approverCount > 1) {
      return `${baseLabel} (${approverCount})`;
    }
    return baseLabel;
  }

  function approversFromReviewEvents(reviewEvents: PREvent[]): string[] {
    const latestByAuthor = new Map<
      string,
      { state: string; createdMs: number }
    >();
    for (const event of reviewEvents) {
      if (event.EventType !== "review" || !event.Author) continue;
      const state = event.Summary.toUpperCase();
      if (
        state !== "APPROVED" &&
        state !== "CHANGES_REQUESTED" &&
        state !== "DISMISSED"
      ) {
        continue;
      }
      const createdMs = Date.parse(event.CreatedAt);
      const previous = latestByAuthor.get(event.Author);
      if (!previous || createdMs >= previous.createdMs) {
        latestByAuthor.set(event.Author, { state, createdMs });
      }
    }
    return Array.from(latestByAuthor.entries())
      .filter(([, review]) => review.state === "APPROVED")
      .map(([author]) => author)
      .sort((left, right) => left.localeCompare(right));
  }

  function closePopup(): void {
    popupOpen = false;
  }

  function togglePopup(): void {
    if (!canExpand) return;
    popupOpen = !popupOpen;
  }

  function onKeydown(e: KeyboardEvent): void {
    if (popupOpen && e.key === "Escape") {
      closePopup();
    }
  }

  function onDocumentMousedown(e: MouseEvent): void {
    if (!popupOpen) return;
    const target = e.target as Node;
    if (!wrapEl?.contains(target)) closePopup();
  }

  function onFocusout(): void {
    queueMicrotask(() => {
      if (!popupOpen) return;
      const active = document.activeElement;
      if (active && wrapEl?.contains(active)) return;
      closePopup();
    });
  }
</script>

<svelte:window onkeydown={onKeydown} />
<svelte:document onmousedown={onDocumentMousedown} />

{#if canExpand}
  <div
    class="approval-chip-wrap"
    bind:this={wrapEl}
    onfocusout={onFocusout}
  >
    <Chip size="sm"
      tone={chipTone}
      interactive
      expanded={popupOpen}
      title="Show approving reviewers"
      onclick={togglePopup}
    >
      {label}
    </Chip>
    {#if popupOpen}
      <div class="approval-popup" role="list">
        {#each approvers as approver (approver)}
          <div class="approval-popup-item" role="listitem">
            {approver}
          </div>
        {/each}
      </div>
    {/if}
  </div>
{:else}
  <Chip size="sm" tone={chipTone}>{label}</Chip>
{/if}

<style>
  .approval-chip-wrap {
    position: relative;
    display: inline-flex;
  }

  .approval-popup {
    position: absolute;
    z-index: 20;
    top: calc(100% + 6px);
    left: 0;
    min-width: 120px;
    max-width: min(220px, 70vw);
    padding: 6px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
  }

  .approval-popup-item {
    overflow: hidden;
    padding: 4px 6px;
    border-radius: 4px;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    line-height: 1.3;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .approval-popup-item + .approval-popup-item {
    margin-top: 2px;
  }
</style>
