<script lang="ts">
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";

  import type { KataTaskChecklistItem, KataTaskDetail } from "../../api/kata/taskTypes.js";
  import { createULID } from "../../api/ulid.js";

  interface Props {
    issue: KataTaskDetail;
    revealed: boolean;
    onPatchMetadata: (uid: string, patch: Record<string, unknown>) => boolean | Promise<boolean>;
    onReveal: () => void;
  }

  let { issue, revealed, onPatchMetadata, onReveal }: Props = $props();

  let checklistPending = $state(false);
  let checklistDraft = $state("");
  let checklistInput: HTMLInputElement | null = $state(null);
  let trackedUID = $state<string | null>(null);

  const visible = $derived(checklistItems().length > 0 || revealed);

  $effect(() => {
    const uid = issue.issue.uid;
    if (trackedUID === null) {
      trackedUID = uid;
      return;
    }
    if (uid === trackedUID) return;
    trackedUID = uid;
    checklistDraft = "";
    checklistPending = false;
  });

  function checklistItems(): KataTaskChecklistItem[] {
    return issue.issue.metadata.checklist ?? [];
  }

  async function replaceChecklist(next: KataTaskChecklistItem[]): Promise<void> {
    await onPatchMetadata(issue.issue.uid, { checklist: next });
    if (next.length === 0) {
      onReveal();
    }
  }

  async function guarded(work: () => Promise<void>): Promise<void> {
    if (checklistPending) return;
    checklistPending = true;
    try {
      await work();
    } finally {
      checklistPending = false;
    }
  }

  async function toggleChecklistItem(id: string): Promise<void> {
    await guarded(() =>
      replaceChecklist(checklistItems().map((item) => (item.id === id ? { ...item, done: !item.done } : item))),
    );
  }

  async function removeChecklistItem(id: string): Promise<void> {
    await guarded(() => replaceChecklist(checklistItems().filter((item) => item.id !== id)));
  }

  async function addChecklistItem(): Promise<void> {
    const text = checklistDraft.trim();
    if (!text) return;
    await guarded(async () => {
      await replaceChecklist([...checklistItems(), { id: createULID(), text, done: false }]);
      checklistDraft = "";
      queueMicrotask(() => checklistInput?.focus());
    });
  }

  function handleChecklistKeydown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      void addChecklistItem();
    }
  }
</script>

{#if visible}
  <section class="checklist" aria-label="Checklist">
    <div class="section-header">
      <h3>Checklist</h3>
    </div>
    {#if checklistItems().length > 0}
      <div class="checklist-items">
        {#each checklistItems() as item (item.id)}
          <div class="checklist-row" class:done={item.done}>
            <label>
              <input
                type="checkbox"
                aria-label={item.text}
                checked={item.done}
                disabled={checklistPending}
                onchange={() => {
                  void toggleChecklistItem(item.id);
                }}
              />
              <span>{item.text}</span>
            </label>
            <button
              type="button"
              class="icon-button"
              aria-label={`Remove ${item.text}`}
              disabled={checklistPending}
              onclick={() => {
                void removeChecklistItem(item.id);
              }}
            >
              <XIcon size={13} strokeWidth={1.9} />
            </button>
          </div>
        {/each}
      </div>
    {/if}
    <div class="checklist-add">
      <PlusIcon size={13} strokeWidth={1.9} aria-hidden="true" />
      <input
        bind:this={checklistInput}
        aria-label="New checklist item"
        placeholder="Add subtask..."
        bind:value={checklistDraft}
        disabled={checklistPending}
        onkeydown={handleChecklistKeydown}
      />
      <button
        type="button"
        class="add-checklist-button"
        disabled={checklistPending || checklistDraft.trim() === ""}
        onclick={() => {
          void addChecklistItem();
        }}
      >
        Add
      </button>
    </div>
  </section>
{/if}

<style>
  .checklist {
    display: grid;
    gap: 6px;
    margin: 0 0 18px;
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 8px;
  }

  .section-header h3 {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
    text-transform: uppercase;
  }

  .checklist-items {
    display: grid;
    gap: 2px;
  }

  .checklist-row {
    min-height: 28px;
    border-radius: 6px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 2px 4px;
  }

  .checklist-row:hover {
    background: var(--bg-hover);
  }

  .checklist-row label {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    cursor: pointer;
  }

  .checklist-row input[type="checkbox"] {
    width: 14px;
    height: 14px;
    margin: 0;
    accent-color: var(--accent-blue);
  }

  .checklist-row.done label span {
    color: var(--text-muted);
    text-decoration: line-through;
  }

  .icon-button {
    width: 22px;
    height: 22px;
    border: 0;
    border-radius: 5px;
    background: transparent;
    color: var(--text-muted);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    opacity: 0;
    cursor: pointer;
  }

  .checklist-row:hover .icon-button,
  .icon-button:focus-visible {
    opacity: 1;
  }

  .icon-button:hover {
    background: var(--bg-hover);
    color: var(--color-danger-fg, #991b1b);
  }

  .checklist-add {
    min-height: 30px;
    border-radius: 6px;
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 2px 4px;
    color: var(--text-muted);
  }

  .checklist-add:focus-within {
    background: var(--bg-hover);
  }

  .checklist-add input {
    flex: 1;
    min-width: 0;
    border: 0;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    padding: 4px 2px;
  }

  .checklist-add input:focus {
    outline: none;
  }

  .add-checklist-button {
    min-height: 24px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-xs);
    padding: 2px 8px;
    cursor: pointer;
  }

  .add-checklist-button:disabled {
    cursor: default;
    opacity: 0.62;
  }

  .add-checklist-button:not(:disabled):hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }
</style>
