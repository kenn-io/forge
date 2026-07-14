<script lang="ts">
  import { showFlash } from "@middleman/ui/stores/flash";

  import { createKataTaskAPI } from "../../api/kata/taskClient.js";
  import type {
    KataCreateRecurrenceInput,
    KataPatchRecurrenceInput,
    KataRecurrence,
    KataTaskEditPatch,
  } from "../../api/kata/taskTypes.js";
  import type { KataWorkspaceMetadata } from "../../api/kata/workspaces.js";
  import KataIssueDetail from "../../components/kata/KataIssueDetail.svelte";
  import type { TypeaheadOption } from "../../components/shared/TypeaheadTrigger.svelte";
  import { computeRemoveMessageLinkPatch, readMessageLinks } from "../../messages/messageLinks.js";
  import type { MessageLinkRef } from "../../messages/types";
  import KataRecurrenceDialogs from "../../features/kata/KataRecurrenceDialogs.svelte";
  import { createKataWorkspaceStore } from "../../stores/kata-workspace.svelte.js";

  interface Props {
    kata: KataWorkspaceMetadata;
    disabled?: boolean;
  }

  let { kata, disabled = false }: Props = $props();

  const actor = "middleman";
  const api = createKataTaskAPI({ getDaemonId: () => kata.daemon_id });
  const store = createKataWorkspaceStore({ api });

  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let checklistRevealed = $state(false);
  let pendingMoveIssueUIDs = $state.raw<ReadonlySet<string>>(new Set());
  let unlinkBusyIds = $state<ReadonlySet<number>>(new Set());
  let loadRequestID = 0;
  let issueContextGeneration = 0;
  let recurrenceDialogs = $state<{
    openCreateRecurrence: () => void;
    openEditRecurrence: (recurrence: KataRecurrence) => void;
    openDeleteRecurrence: (recurrence: KataRecurrence) => void;
    closeAll: () => void;
  } | null>(null);

  $effect(() => {
    const issueUID = kata.issue_uid;
    issueContextGeneration += 1;
    const requestID = ++loadRequestID;
    loading = true;
    loadError = null;
    checklistRevealed = false;
    void store
      .bootstrap("all", issueUID, { selectFirst: false })
      .catch((err) => {
        if (requestID !== loadRequestID) return;
        loadError = err instanceof Error ? err.message : "Could not load Kata task.";
      })
      .finally(() => {
        if (requestID === loadRequestID) {
          loading = false;
        }
      });
  });

  function ownerOptions(): TypeaheadOption[] {
    const selected = store.selectedIssue?.issue;
    return [selected?.owner, ...store.currentView.groups.flatMap((group) => group.issues.map((issue) => issue.owner))]
      .filter((owner): owner is string => typeof owner === "string" && owner.trim().length > 0)
      .filter((owner, index, owners) => owners.indexOf(owner) === index)
      .sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }))
      .map((owner) => ({ value: owner, label: owner }));
  }

  function selectedMessageLinks(): MessageLinkRef[] {
    return store.selectedIssue ? readMessageLinks(store.selectedIssue.issue.metadata) : [];
  }

  async function runTask(
    task: () => Promise<void | boolean>,
    shouldSurfaceFailure: () => boolean = () => true,
  ): Promise<boolean> {
    try {
      return (await task()) ?? true;
    } catch (err) {
      if (shouldSurfaceFailure()) {
        showFlash(err instanceof Error ? err.message : "Kata request failed.", { tone: "danger" });
      }
      return false;
    }
  }

  async function runTaskOrThrow(task: () => Promise<void>): Promise<void> {
    await task();
  }

  async function runLoadTask(task: () => Promise<void | boolean>): Promise<boolean> {
    loadError = null;
    try {
      return (await task()) ?? true;
    } catch (err) {
      loadError = err instanceof Error ? err.message : "Could not load Kata task.";
      return false;
    }
  }

  async function moveSelectedIssue(toProjectUID: string): Promise<boolean> {
    const selected = store.selectedIssue?.issue;
    if (!selected || pendingMoveIssueUIDs.has(selected.uid)) return false;
    const sourceIssueUID = selected.uid;
    const generation = issueContextGeneration;
    pendingMoveIssueUIDs = new Set(pendingMoveIssueUIDs).add(sourceIssueUID);
    try {
      return await runTask(
        () => store.moveIssue(sourceIssueUID, actor, toProjectUID),
        () => generation === issueContextGeneration,
      );
    } finally {
      const nextPendingMoves = new Set(pendingMoveIssueUIDs);
      nextPendingMoves.delete(sourceIssueUID);
      pendingMoveIssueUIDs = nextPendingMoves;
    }
  }

  function patchSelectedMetadata(uid: string, patch: Record<string, unknown>): Promise<boolean> {
    return runTask(() => store.patchMetadata(uid, actor, patch));
  }

  function addSelectedComment(uid: string, body: string): Promise<boolean> {
    return runTask(() => store.addComment(uid, actor, body));
  }

  function editSelectedIssue(uid: string, patch: KataTaskEditPatch): Promise<boolean> {
    return runTask(() => store.editIssue(uid, actor, patch));
  }

  function assignSelectedOwner(uid: string, owner: string): Promise<boolean> {
    return runTask(() => store.assignOwner(uid, actor, owner));
  }

  function unassignSelectedOwner(uid: string): Promise<boolean> {
    return runTask(() => store.unassignOwner(uid, actor));
  }

  function setSelectedPriority(uid: string, priority: number | null): Promise<boolean> {
    return runTask(() => store.setPriority(uid, actor, priority));
  }

  function addSelectedLabel(uid: string, label: string): Promise<boolean> {
    return runTask(() => store.addLabel(uid, actor, label));
  }

  async function removeSelectedLabel(uid: string, label: string): Promise<void> {
    await runTask(() => store.removeLabel(uid, actor, label));
  }

  function revealChecklist(): void {
    checklistRevealed = true;
  }

  async function deleteRecurrence(recurrence: KataRecurrence): Promise<boolean> {
    return runTask(() => store.deleteRecurrence(recurrence.id, actor));
  }

  async function createRecurrence(projectID: number, input: KataCreateRecurrenceInput): Promise<void> {
    await runTaskOrThrow(async () => {
      await store.createRecurrence(projectID, input);
    });
  }

  async function patchRecurrence(id: number, input: KataPatchRecurrenceInput, etag: string): Promise<void> {
    await runTaskOrThrow(async () => {
      await store.patchRecurrence(id, input, etag);
    });
  }

  function closeSelectedIssue(
    reason: "done" | "wontfix" | "duplicate" | "superseded",
    message: string,
  ): Promise<boolean> {
    const selected = store.selectedIssue;
    if (!selected) return Promise.resolve(false);
    return runTask(() => store.closeIssue(selected.issue.uid, actor, { reason, message }));
  }

  async function reopenSelectedIssue(): Promise<void> {
    const selected = store.selectedIssue;
    if (!selected) return;
    await runTask(() => store.reopenIssue(selected.issue.uid, actor));
  }

  function deleteSelectedIssue(): Promise<boolean> {
    return closeSelectedIssue("wontfix", "Deleted from workspace sidebar.");
  }

  async function unlinkMessageLink(link: MessageLinkRef): Promise<void> {
    if (unlinkBusyIds.size > 0) return;
    const selected = store.selectedIssue;
    if (!selected) return;
    const links = selectedMessageLinks();
    const patch = computeRemoveMessageLinkPatch(links, link.message_id);
    if (patch === null) return;
    unlinkBusyIds = new Set([link.message_id]);
    await runTask(() =>
      store.patchMetadata(selected.issue.uid, actor, {
        mail_links: patch.mail_links,
      }),
    );
    unlinkBusyIds = new Set();
  }

  async function selectIssue(uid: string): Promise<void> {
    issueContextGeneration += 1;
    await runLoadTask(() => store.selectIssue(uid));
  }
</script>

<div class="kata-workspace-sidebar" inert={disabled}>
  {#if loading}
    <div class="state">Loading task</div>
  {:else if loadError && !store.selectedIssue}
    <div class="state error" role="alert">{loadError}</div>
  {:else if store.selectedIssue}
    {#if loadError}
      <p class="inline-error" role="alert">{loadError}</p>
    {/if}
    <KataIssueDetail
      issue={store.selectedIssue}
      events={store.selectedEvents}
      currentView={store.currentView}
      api={store.api}
      activeDaemonId={kata.daemon_id}
      projects={store.projects}
      ownerOptions={ownerOptions()}
      messageLinks={selectedMessageLinks()}
      unlinkBusyIds={unlinkBusyIds}
      selectedRecurrences={store.selectedRecurrences}
      {checklistRevealed}
      movePending={pendingMoveIssueUIDs.has(store.selectedIssue.issue.uid)}
      onMoveIssue={moveSelectedIssue}
      onPatchMetadata={patchSelectedMetadata}
      onAddComment={addSelectedComment}
      onEditIssue={editSelectedIssue}
      onAssignOwner={assignSelectedOwner}
      onUnassignOwner={unassignSelectedOwner}
      onSetPriority={setSelectedPriority}
      onAddLabel={addSelectedLabel}
      onRemoveLabel={removeSelectedLabel}
      onUnlinkMessage={unlinkMessageLink}
      onRevealChecklist={revealChecklist}
      onCreateRecurrence={() => recurrenceDialogs?.openCreateRecurrence()}
      onEditRecurrence={(recurrence) => recurrenceDialogs?.openEditRecurrence(recurrence)}
      onDeleteRecurrence={(recurrence) => recurrenceDialogs?.openDeleteRecurrence(recurrence)}
      onCloseIssue={closeSelectedIssue}
      onReopenIssue={reopenSelectedIssue}
      onDeleteIssue={deleteSelectedIssue}
      onSelectIssue={(uid) => {
        void selectIssue(uid);
      }}
    />
  {:else}
    <div class="state">Task not found</div>
  {/if}
</div>

<KataRecurrenceDialogs
  bind:this={recurrenceDialogs}
  selectedIssue={store.selectedIssue}
  {actor}
  onCreate={createRecurrence}
  onPatch={patchRecurrence}
  onDelete={deleteRecurrence}
/>

<style>
  .kata-workspace-sidebar {
    flex: 1;
    min-height: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    background: var(--bg-primary);
  }

  .kata-workspace-sidebar :global(.kata-detail) {
    padding: 16px;
  }

  .state {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .state.error,
  .inline-error {
    color: var(--accent-red);
  }

  .inline-error {
    flex: 0 0 auto;
    margin: 0;
    border-bottom: 1px solid var(--border-muted);
    background: color-mix(in srgb, var(--accent-red) 8%, transparent);
    padding: 8px 12px;
    font-size: var(--font-size-xs);
  }
</style>
