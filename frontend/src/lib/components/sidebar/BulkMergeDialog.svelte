<script lang="ts">
  import { Checkbox } from "@kenn-io/kit-ui";
  import { untrack } from "svelte";
  import type { MergeMethod } from "../../stores/repo-operations.svelte.js";
  import DialogButton from "../shared/DialogButton.svelte";
  import Modal from "../shared/Modal.svelte";

  // Confirms merging several pull requests. Each one merges with its own
  // title and description; the method applies to all of them.
  interface Props {
    title: string;
    message: string;
    /** How many wait for pending CI before merging. */
    deferredCount: number;
    /** Methods every selected repository allows; undefined while loading. */
    methods: readonly MergeMethod[] | undefined;
    /** Offer to delete the selected pull requests' workspaces. */
    hasWorkspaces: boolean;
    confirmLabel: string;
    busy: boolean;
    onCancel: () => void;
    onConfirm: (choice: { method: MergeMethod; deleteWorkspaces: boolean }) => void;
  }

  let { title, message, deferredCount, methods, hasWorkspaces, confirmLabel, busy, onCancel, onConfirm }: Props =
    $props();

  const methodLabels: Record<MergeMethod, string> = {
    squash: "Squash and merge",
    merge: "Create a merge commit",
    rebase: "Rebase and merge",
  };

  let chosen = $state<MergeMethod | undefined>(undefined);
  // Follow the repositories' methods as they load, keeping a still-valid choice.
  const method = $derived(chosen !== undefined && methods?.includes(chosen) ? chosen : methods?.[0]);
  let deleteWorkspaces = $state(untrack(() => hasWorkspaces));

  function close(): void {
    if (!busy) onCancel();
  }

  function confirm(): void {
    if (method) onConfirm({ method, deleteWorkspaces: hasWorkspaces && deleteWorkspaces });
  }
</script>

<Modal open {title} onClose={close} width={460} frameId="item-merge-confirm">
  <div class="bulk-merge">
    <p class="bulk-merge__message">{message}</p>
    {#if methods === undefined}
      <p class="bulk-merge__hint">Loading merge methods...</p>
    {:else if methods.length === 0}
      <p class="bulk-merge__hint">These repositories share no merge method. Merge them separately.</p>
    {:else if methods.length > 1}
      <div class="bulk-merge__methods" role="radiogroup" aria-label="Merge method">
        {#each methods as option (option)}
          <label class="bulk-merge__method" class:bulk-merge__method--active={method === option}>
            <input
              type="radio"
              name="bulk-merge-method"
              value={option}
              checked={method === option}
              onchange={() => {
                chosen = option;
              }}
            />
            {methodLabels[option]}
          </label>
        {/each}
      </div>
    {/if}
    {#if deferredCount > 0}
      <p class="bulk-merge__hint">
        {deferredCount === 1 ? "1 has" : `${deferredCount} have`} pending CI and will merge once the checks pass.
      </p>
    {/if}
    {#if hasWorkspaces}
      <Checkbox
        checked={deleteWorkspaces}
        label="Delete workspaces after merge"
        onchange={(checked) => {
          deleteWorkspaces = checked;
        }}
      />
    {/if}
  </div>

  {#snippet footer()}
    <DialogButton disabled={busy} onclick={onCancel}>Cancel</DialogButton>
    <DialogButton tone="primary" disabled={busy || !method} onclick={confirm}>
      {busy ? "Merging…" : confirmLabel}
    </DialogButton>
  {/snippet}
</Modal>

<style>
  .bulk-merge {
    display: grid;
    gap: var(--space-4);
  }

  .bulk-merge__message {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    line-height: 1.45;
  }

  .bulk-merge__hint {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    line-height: 1.45;
  }

  .bulk-merge__methods {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .bulk-merge__method {
    padding: 5px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 500;
    cursor: pointer;
  }

  .bulk-merge__method input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }

  .bulk-merge__method:hover {
    border-color: var(--accent-blue);
    color: var(--text-primary);
  }

  .bulk-merge__method:focus-within {
    outline: 2px solid var(--accent-blue);
    outline-offset: 1px;
  }

  .bulk-merge__method--active {
    border-color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 12%, transparent);
    color: var(--accent-blue);
  }
</style>
