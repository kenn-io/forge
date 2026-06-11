<script lang="ts">
  import MoreHorizontalIcon from "@lucide/svelte/icons/more-horizontal";
  import type { KataTaskDetail } from "../../api/kata/taskTypes.js";
  import Modal from "../shared/Modal.svelte";

  interface Props {
    issue: KataTaskDetail;
    hasChecklist: boolean;
    hasRecurrence: boolean;
    onAddChecklist: () => void;
    onCreateRecurrence: () => void;
    onDeleteIssue: () => boolean | Promise<boolean>;
  }

  let {
    issue,
    hasChecklist,
    hasRecurrence,
    onAddChecklist,
    onCreateRecurrence,
    onDeleteIssue,
  }: Props = $props();

  let menuOpen = $state(false);
  let menuRoot: HTMLDivElement | null = $state(null);
  let deleteOpen = $state(false);
  let pending = $state(false);
  let trackedUID = $state<string | null>(null);

  const canAddChecklist = $derived(!hasChecklist);
  const canCreateRecurrence = $derived(!hasRecurrence);
  const canDeleteIssue = $derived(issue.issue.status !== "closed");
  const hasAnyAction = $derived(canAddChecklist || canCreateRecurrence || canDeleteIssue);

  $effect(() => {
    if (issue.issue.uid === trackedUID) return;
    trackedUID = issue.issue.uid;
    menuOpen = false;
    deleteOpen = false;
    pending = false;
  });

  $effect(() => {
    if (!menuOpen) return;
    function onPointerDown(event: PointerEvent): void {
      if (!menuRoot) return;
      if (event.target instanceof Node && menuRoot.contains(event.target)) return;
      menuOpen = false;
    }
    window.addEventListener("pointerdown", onPointerDown, true);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown, true);
    };
  });

  function revealChecklist(): void {
    menuOpen = false;
    onAddChecklist();
  }

  function openCreateRecurrence(): void {
    menuOpen = false;
    onCreateRecurrence();
  }

  function openDeleteDialog(): void {
    menuOpen = false;
    deleteOpen = true;
  }

  function closeDeleteDialog(): void {
    if (pending) return;
    deleteOpen = false;
  }

  async function deleteIssue(): Promise<void> {
    if (pending) return;
    pending = true;
    try {
      const ok = await onDeleteIssue();
      if (ok) {
        deleteOpen = false;
      }
    } finally {
      pending = false;
    }
  }
</script>

{#if hasAnyAction}
  <div class="overflow-host" bind:this={menuRoot} role="presentation">
    <button
      type="button"
      class="icon-button overflow-trigger"
      aria-label="More actions"
      aria-haspopup="menu"
      aria-expanded={menuOpen}
      onclick={() => {
        menuOpen = !menuOpen;
      }}
    >
      <MoreHorizontalIcon size={14} strokeWidth={1.9} />
    </button>
    {#if menuOpen}
      <ul class="overflow-menu" role="menu" aria-label="Task actions">
        {#if canAddChecklist}
          <li>
            <button type="button" class="overflow-item" role="menuitem" onclick={revealChecklist}>
              Add checklist
            </button>
          </li>
        {/if}
        {#if canCreateRecurrence}
          <li>
            <button type="button" class="overflow-item" role="menuitem" onclick={openCreateRecurrence}>
              Mark as recurring...
            </button>
          </li>
        {/if}
        {#if canDeleteIssue}
          {#if canAddChecklist || canCreateRecurrence}
            <li class="overflow-separator" role="separator"></li>
          {/if}
          <li>
            <button type="button" class="overflow-item overflow-item--danger" role="menuitem" onclick={openDeleteDialog}>
              Delete issue
            </button>
          </li>
        {/if}
      </ul>
    {/if}
  </div>
{/if}

<Modal open={deleteOpen} title="Delete issue" onClose={closeDeleteDialog} width={420}>
  <div class="delete-dialog">
    <p>
      Delete <strong>{issue.issue.title}</strong>?
    </p>
    <p class="delete-hint">
      The task moves to closed / won't-do state. Reopen it if you change your mind.
    </p>
  </div>

  {#snippet footer()}
    <button type="button" class="ghost-button" onclick={closeDeleteDialog} disabled={pending}>
      Cancel
    </button>
    <button type="button" class="danger-button" onclick={() => { void deleteIssue(); }} disabled={pending}>
      {pending ? "Deleting..." : "Delete"}
    </button>
  {/snippet}
</Modal>

<style>
  .icon-button,
  .ghost-button,
  .danger-button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    border-radius: 6px;
    font-size: var(--font-size-sm);
    font-weight: 650;
  }

  .icon-button {
    width: 28px;
    height: 28px;
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-secondary);
  }

  .icon-button:hover {
    border-color: var(--border-muted);
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .ghost-button,
  .danger-button {
    min-height: 28px;
    padding: 5px 11px;
  }

  .ghost-button {
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-secondary);
  }

  .danger-button {
    border: 1px solid var(--accent-red);
    background: var(--accent-red);
    color: white;
  }

  .ghost-button:disabled,
  .danger-button:disabled {
    cursor: default;
    opacity: 0.62;
  }

  .overflow-host {
    position: relative;
    display: inline-flex;
  }

  .overflow-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    z-index: 35;
    min-width: 190px;
    margin: 0;
    padding: 5px;
    border: 1px solid var(--border-default);
    border-radius: 8px;
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
    list-style: none;
  }

  .overflow-item {
    width: 100%;
    border-radius: 5px;
    padding: 7px 9px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    text-align: left;
  }

  .overflow-item:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .overflow-item--danger {
    color: var(--accent-red);
  }

  .overflow-separator {
    height: 1px;
    margin: 5px 2px;
    background: var(--border-muted);
  }

  .delete-dialog {
    display: grid;
    gap: 8px;
  }

  .delete-dialog p {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    line-height: 1.45;
  }

  .delete-hint {
    color: var(--text-muted) !important;
    font-size: var(--font-size-sm) !important;
  }
</style>
