<script lang="ts">
  import { Checkbox, SegmentedControl } from "@kenn-io/kit-ui";
  import { untrack } from "svelte";
  import type { MergeMethod } from "../../stores/repo-operations.svelte.js";
  import ConfirmDialog from "../shared/ConfirmDialog.svelte";

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

  const hint = $derived(
    [
      methods === undefined ? "Loading merge methods..." : "",
      methods?.length === 0 ? "These repositories share no merge method. Merge them separately." : "",
      deferredCount === 1 ? "1 has pending CI and will merge once the checks pass." : "",
      deferredCount > 1 ? `${deferredCount} have pending CI and will merge once the checks pass.` : "",
    ]
      .filter(Boolean)
      .join(" "),
  );

  function confirm(): void {
    if (method) onConfirm({ method, deleteWorkspaces: hasWorkspaces && deleteWorkspaces });
  }
</script>

<ConfirmDialog
  open
  {title}
  {message}
  hint={hint || undefined}
  {confirmLabel}
  pendingLabel="Merging…"
  {busy}
  confirmDisabled={!method}
  width={460}
  frameId="item-merge-confirm"
  {onCancel}
  onConfirm={confirm}
>
  {#if methods && methods.length > 1}
    <SegmentedControl
      block
      options={methods.map((option) => ({ value: option, label: methodLabels[option] }))}
      value={method ?? ""}
      onchange={(value) => {
        chosen = value as MergeMethod;
      }}
      ariaLabel="Merge method"
    />
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
</ConfirmDialog>
