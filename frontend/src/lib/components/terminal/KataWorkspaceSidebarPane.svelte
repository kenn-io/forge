<script lang="ts">
  import { untrack } from "svelte";
  import { showFlash } from "@middleman/ui/stores/flash";

  import { createKataTaskAPI } from "../../api/kata/taskClient.js";
  import {
    searchKataTaskReferences,
    type KataSnapshotIntent,
  } from "../../api/kata/snapshot.js";
  import type {
    KataCreateRecurrenceInput,
    KataPatchRecurrenceInput,
    KataPinnedDaemonOptions,
    KataProjectSummary,
    KataRecurrence,
    KataTaskDetail,
    KataTaskEditPatch,
    KataTaskEvent,
    KataTaskMutationTarget,
    KataTaskSummary,
  } from "../../api/kata/taskTypes.js";
  import type { KataWorkspaceMetadata } from "../../api/kata/workspaces.js";
  import KataIssueDetail from "../../components/kata/KataIssueDetail.svelte";
  import type { TypeaheadOption } from "@kenn-io/kit-ui";
  import { computeRemoveMessageLinkPatch, readMessageLinks } from "../../messages/messageLinks.js";
  import type { MessageLinkRef } from "../../messages/types";
  import KataRecurrenceDialogs from "../../features/kata/KataRecurrenceDialogs.svelte";
  import { createKataLinkFilters, type KataLinkFilters } from "../../features/kata/kataLinkFilters.js";
  import { acknowledgeKataMutationThenRevalidate } from "../../features/kata/kataMutationRevalidation.js";
  import { createKataAuthorityStore } from "../../stores/kata-authority.svelte.js";

  interface Props {
    kata: KataWorkspaceMetadata;
    disabled?: boolean;
  }

  let { kata, disabled = false }: Props = $props();

  const actor = "middleman";
  const api = createKataTaskAPI();
  const authorityStore = createKataAuthorityStore();

  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let mutationRefreshPending = $state(false);
  let mutationAcknowledged = $state(false);
  let mutationDraftResetGeneration = $state(0);
  let mutationRefreshError = $state<string | null>(null);
  let mutationRefreshRetrying = $state(false);
  let mutationRefreshGeneration = 0;
  let mutationTransportPending = false;
  let mutationRecurrenceRefreshRequired = false;
  let checklistRevealed = $state(false);
  let linkFilters = $state<KataLinkFilters>(createKataLinkFilters("all"));
  let pendingMoveIssueUIDs = $state.raw<ReadonlySet<string>>(new Set());
  let unlinkBusyIds = $state<ReadonlySet<number>>(new Set());
  let loadRequestID = 0;
  let issueContextGeneration = 0;
  let lastPropIssueUID = "";
  let selectedIssueUID = $state("");
  let selectedRecurrences = $state.raw<KataRecurrence[]>([]);
  let recurrenceDialogs = $state<{
    openCreateRecurrence: () => void;
    openEditRecurrence: (recurrence: KataRecurrence) => void;
    openDeleteRecurrence: (recurrence: KataRecurrence) => void;
    closeAll: () => void;
    reconcileRecurrences: (recurrences: readonly KataRecurrence[]) => void;
  } | null>(null);
  const acceptedSnapshot = $derived(authorityStore.snapshot);
  const selectedIssue = $derived(
    acceptedSnapshot?.selected_detail
      ? structuredClone(acceptedSnapshot.selected_detail) as KataTaskDetail
      : null,
  );
  const selectedEvents = $derived(
    acceptedSnapshot ? structuredClone(acceptedSnapshot.selected_history) as KataTaskEvent[] : [],
  );
  const projects = $derived(
    acceptedSnapshot ? structuredClone(acceptedSnapshot.projects) as KataProjectSummary[] : [],
  );
  const issueCatalog = $derived(
    acceptedSnapshot ? structuredClone(acceptedSnapshot.issues) as KataTaskSummary[] : [],
  );
  const mutationActionsBlocked = $derived(
    disabled || mutationRefreshPending || authorityStore.state.phase !== "accepted",
  );
  const detailAuthorityBlocked = $derived(
    disabled ||
      (mutationRefreshPending && mutationAcknowledged) ||
      authorityStore.state.phase !== "accepted",
  );

  function selectedSnapshotIntent(uid = selectedIssueUID): KataSnapshotIntent {
    return {
      daemon_id: kata.daemon_id,
      scope: "global",
      authority: "all",
      selected_issue_uid: uid,
    };
  }

  async function loadSelectedRecurrences(detail: KataTaskDetail, daemonID: string, requestID: number): Promise<boolean> {
    try {
      const response = await api.recurrences(detail.issue.project_id, { daemonId: daemonID });
      if (requestID !== loadRequestID || selectedIssue?.issue.uid !== detail.issue.uid) return false;
      selectedRecurrences = response.recurrences;
      recurrenceDialogs?.reconcileRecurrences(response.recurrences);
      return true;
    } catch {
      if (requestID !== loadRequestID || selectedIssue?.issue.uid !== detail.issue.uid) return false;
      selectedRecurrences = [];
      return false;
    }
  }

  function clearMutationRefresh(): void {
    if (mutationAcknowledged) mutationDraftResetGeneration += 1;
    mutationRefreshPending = false;
    mutationAcknowledged = false;
    mutationRefreshError = null;
    mutationRecurrenceRefreshRequired = false;
    mutationRefreshGeneration += 1;
  }

  async function loadSelectedSnapshot(uid: string, requestID = ++loadRequestID): Promise<boolean> {
    loading = selectedIssue?.issue.uid !== uid;
    loadError = null;
    try {
      const accepted = await authorityStore.loadSnapshot(selectedSnapshotIntent(uid));
      if (requestID !== loadRequestID) return false;
      const snapshot = authorityStore.snapshot;
      const detail = snapshot?.selected_detail;
      if (!accepted || snapshot?.selected_issue_uid !== uid || !detail) {
        throw new Error(`Kata snapshot did not include selected task ${uid}`);
      }
      selectedRecurrences = [];
      const selectedDetail = structuredClone(detail) as KataTaskDetail;
      const awaitRecurrences =
        mutationRefreshPending && !mutationTransportPending && mutationRecurrenceRefreshRequired;
      if (awaitRecurrences) {
        const refreshed = await loadSelectedRecurrences(selectedDetail, snapshot.daemon_id, requestID);
        if (!refreshed) throw new Error("Could not refresh Kata recurrences.");
      } else {
        void loadSelectedRecurrences(selectedDetail, snapshot.daemon_id, requestID);
      }
      if (!mutationTransportPending) clearMutationRefresh();
      return true;
    } finally {
      if (requestID === loadRequestID) loading = false;
    }
  }

  $effect(() => {
    const issueUID = kata.issue_uid;
    if (lastPropIssueUID && lastPropIssueUID !== issueUID) {
      untrack(() => recurrenceDialogs?.closeAll());
    }
    lastPropIssueUID = issueUID;
    selectedIssueUID = issueUID;
    issueContextGeneration += 1;
    const requestID = ++loadRequestID;
    loading = true;
    loadError = null;
    checklistRevealed = false;
    linkFilters = createKataLinkFilters("all");
    void untrack(() => loadSelectedSnapshot(issueUID, requestID))
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
    const selected = selectedIssue?.issue;
    return [selected?.owner, ...issueCatalog.map((issue) => issue.owner)]
      .filter((owner): owner is string => typeof owner === "string" && owner.trim().length > 0)
      .filter((owner, index, owners) => owners.indexOf(owner) === index)
      .sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }))
      .map((owner) => ({ name: owner, label: owner }));
  }

  function selectedMessageLinks(): MessageLinkRef[] {
    return selectedIssue ? readMessageLinks(selectedIssue.issue.metadata) : [];
  }

  function selectedMutationTarget(uid: string): KataTaskMutationTarget {
    if (!selectedIssue || selectedIssue.issue.uid !== uid) throw new Error(`issue not selected: ${uid}`);
    return { project_id: selectedIssue.issue.project_id, ref: uid };
  }

  function selectedMutationETag(uid: string): string {
    if (!selectedIssue || selectedIssue.issue.uid !== uid) throw new Error(`issue not selected: ${uid}`);
    if (!selectedIssue.etag) throw new Error(`selected snapshot is missing an ETag for ${uid}`);
    return selectedIssue.etag;
  }

  function acceptedDaemonIDForMutation(): string {
    const daemonID = acceptedSnapshot?.daemon_id;
    if (!daemonID) throw new Error("No accepted Kata snapshot daemon is available.");
    return daemonID;
  }

  function acceptedMutationOptions(): KataPinnedDaemonOptions {
    return { daemonId: acceptedDaemonIDForMutation() };
  }

  async function mutateSelected(
    task: () => Promise<unknown>,
    options: { refreshRecurrences?: boolean } = {},
  ): Promise<void> {
    const uid = selectedIssue?.issue.uid;
    if (!uid) throw new Error("No Kata task is selected.");
    let replacementUID = uid;
    let refreshGeneration = ++mutationRefreshGeneration;
    mutationTransportPending = true;
    mutationRecurrenceRefreshRequired = options.refreshRecurrences === true;
    mutationRefreshPending = true;
    mutationAcknowledged = false;
    mutationRefreshError = null;
    try {
      const result = await acknowledgeKataMutationThenRevalidate(
        task,
        () => loadSelectedSnapshot(replacementUID),
        () => {
          mutationTransportPending = false;
          replacementUID = selectedIssueUID;
          refreshGeneration = ++mutationRefreshGeneration;
          mutationRefreshPending = true;
          mutationAcknowledged = true;
          mutationRefreshError = null;
        },
      );
      void result.replacement.then((replacement) => {
        if (refreshGeneration !== mutationRefreshGeneration || replacement.replacementAccepted) return;
        mutationRefreshPending = true;
        mutationRefreshError = replacement.replacementError ?? "Kata snapshot replacement was not accepted.";
        showFlash("Change saved, but Kata could not refresh. Retry the snapshot before making more changes.", {
          tone: "warning",
        });
      });
    } catch (mutationError) {
      mutationTransportPending = false;
      mutationRecurrenceRefreshRequired = false;
      mutationRefreshGeneration += 1;
      mutationRefreshPending = false;
      mutationAcknowledged = false;
      mutationRefreshError = null;
      throw mutationError;
    }
  }

  async function runTask(
    task: () => Promise<void | boolean>,
    shouldSurfaceFailure: () => boolean = () => true,
  ): Promise<boolean> {
    if (mutationActionsBlocked) return false;
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
    if (mutationActionsBlocked) return;
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
    const selected = selectedIssue?.issue;
    if (!selected || pendingMoveIssueUIDs.has(selected.uid)) return false;
    const sourceIssueUID = selected.uid;
    const generation = issueContextGeneration;
    pendingMoveIssueUIDs = new Set(pendingMoveIssueUIDs).add(sourceIssueUID);
    try {
      return await runTask(
        () => mutateSelected(() => api.moveIssue(
          selectedMutationTarget(sourceIssueUID),
          actor,
          toProjectUID,
          selectedMutationETag(sourceIssueUID),
          acceptedMutationOptions(),
        )),
        () => generation === issueContextGeneration,
      );
    } finally {
      const nextPendingMoves = new Set(pendingMoveIssueUIDs);
      nextPendingMoves.delete(sourceIssueUID);
      pendingMoveIssueUIDs = nextPendingMoves;
    }
  }

  function patchSelectedMetadata(uid: string, patch: Record<string, unknown>): Promise<boolean> {
    return runTask(() => mutateSelected(() =>
      api.patchIssueMetadata(
        selectedMutationTarget(uid),
        actor,
        patch,
        selectedMutationETag(uid),
        acceptedMutationOptions(),
      ),
    ));
  }

  function addSelectedComment(uid: string, body: string): Promise<boolean> {
    return runTask(() =>
      mutateSelected(() => api.addComment(selectedMutationTarget(uid), actor, body, acceptedMutationOptions())),
    );
  }

  function editSelectedIssue(uid: string, patch: KataTaskEditPatch): Promise<boolean> {
    return runTask(() =>
      mutateSelected(() => api.editIssue(selectedMutationTarget(uid), actor, patch, acceptedMutationOptions())),
    );
  }

  function assignSelectedOwner(uid: string, owner: string): Promise<boolean> {
    return runTask(() =>
      mutateSelected(() => api.assignOwner(selectedMutationTarget(uid), actor, owner, acceptedMutationOptions())),
    );
  }

  function unassignSelectedOwner(uid: string): Promise<boolean> {
    return runTask(() =>
      mutateSelected(() => api.unassignOwner(selectedMutationTarget(uid), actor, acceptedMutationOptions())),
    );
  }

  function setSelectedPriority(uid: string, priority: number | null): Promise<boolean> {
    return runTask(() =>
      mutateSelected(() => api.setPriority(selectedMutationTarget(uid), actor, priority, acceptedMutationOptions())),
    );
  }

  function addSelectedLabel(uid: string, label: string): Promise<boolean> {
    return runTask(() =>
      mutateSelected(() => api.addLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions())),
    );
  }

  async function removeSelectedLabel(uid: string, label: string): Promise<void> {
    await runTask(() =>
      mutateSelected(() => api.removeLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions())),
    );
  }

  function revealChecklist(): void {
    checklistRevealed = true;
  }

  async function deleteRecurrence(recurrence: KataRecurrence): Promise<boolean> {
    return runTask(() => mutateSelected(async () => {
      const options = acceptedMutationOptions();
      try {
        await api.deleteRecurrence(
          recurrence.project_id,
          recurrence.uid,
          actor,
          options,
          `"rev-${recurrence.revision}"`,
        );
      } catch (error) {
        // A revision conflict means another client changed this recurrence;
        // reload the list and wait for the still-open dialog to reconcile so
        // a retry acts on current data, never the stale revision.
        if ((error as { status?: number }).status === 412 && selectedIssue) {
          await loadSelectedRecurrences(selectedIssue, options.daemonId, loadRequestID);
        }
        throw error;
      }
    }, { refreshRecurrences: true }));
  }

  async function createRecurrence(projectID: number, input: KataCreateRecurrenceInput): Promise<void> {
    await runTaskOrThrow(() => mutateSelected(
      () => api.createRecurrence(projectID, input, acceptedMutationOptions()),
      { refreshRecurrences: true },
    ));
  }

  async function patchRecurrence(id: number, input: KataPatchRecurrenceInput, etag: string): Promise<void> {
    await runTaskOrThrow(() => mutateSelected(async () => {
      const recurrence = selectedRecurrences.find((item) => item.id === id);
      if (!recurrence) throw new Error(`recurrence not loaded: id=${id}`);
      await api.patchRecurrence(recurrence.project_id, recurrence.uid, input, etag, acceptedMutationOptions());
    }, { refreshRecurrences: true }));
  }

  function closeSelectedIssue(
    reason: "done" | "wontfix" | "duplicate" | "superseded",
    message: string,
  ): Promise<boolean> {
    const selected = selectedIssue;
    if (!selected) return Promise.resolve(false);
    return runTask(() => mutateSelected(() =>
      api.closeIssue(
        selectedMutationTarget(selected.issue.uid),
        actor,
        { reason, message },
        acceptedMutationOptions(),
      ),
    ));
  }

  async function reopenSelectedIssue(): Promise<void> {
    const selected = selectedIssue;
    if (!selected) return;
    await runTask(() =>
      mutateSelected(() =>
        api.reopenIssue(selectedMutationTarget(selected.issue.uid), actor, acceptedMutationOptions()),
      ),
    );
  }

  function deleteSelectedIssue(): Promise<boolean> {
    return closeSelectedIssue("wontfix", "Deleted from workspace sidebar.");
  }

  async function unlinkMessageLink(link: MessageLinkRef): Promise<void> {
    if (unlinkBusyIds.size > 0) return;
    const selected = selectedIssue;
    if (!selected) return;
    const links = selectedMessageLinks();
    const patch = computeRemoveMessageLinkPatch(links, link.message_id);
    if (patch === null) return;
    unlinkBusyIds = new Set([link.message_id]);
    await runTask(() =>
      mutateSelected(() => api.patchIssueMetadata(
        selectedMutationTarget(selected.issue.uid),
        actor,
        { mail_links: patch.mail_links },
        selectedMutationETag(selected.issue.uid),
        acceptedMutationOptions(),
      )),
    );
    unlinkBusyIds = new Set();
  }

  async function selectIssue(uid: string): Promise<void> {
    recurrenceDialogs?.closeAll();
    issueContextGeneration += 1;
    selectedIssueUID = uid;
    await runLoadTask(() => loadSelectedSnapshot(uid));
  }

  async function retryMutationSnapshot(): Promise<void> {
    if (mutationRefreshRetrying || !mutationRefreshPending) return;
    mutationRefreshRetrying = true;
    mutationRefreshError = null;
    try {
      const accepted = await loadSelectedSnapshot(selectedIssueUID);
      if (!accepted) throw new Error("Kata snapshot replacement was not accepted.");
    } catch (retryError) {
      mutationRefreshError = retryError instanceof Error ? retryError.message : "Could not refresh Kata task.";
    } finally {
      mutationRefreshRetrying = false;
    }
  }
</script>

<div class="kata-workspace-sidebar" inert={disabled}>
  {#if mutationRefreshPending && mutationAcknowledged && !mutationRefreshError}
    <div class="authority-recovery" role="status">Change saved. Refreshing Kata snapshot…</div>
  {:else if mutationRefreshError}
    <div class="authority-recovery" role="alert">
      <span>Change saved, but Kata snapshot refresh failed: {mutationRefreshError}</span>
      <button type="button" disabled={mutationRefreshRetrying} onclick={() => void retryMutationSnapshot()}>
        {mutationRefreshRetrying ? "Retrying…" : "Retry Kata snapshot"}
      </button>
    </div>
  {/if}
  <div class="kata-workspace-sidebar__content" inert={detailAuthorityBlocked}>
    {#if loading}
      <div class="state">Loading task</div>
    {:else if loadError && !selectedIssue}
      <div class="state error" role="alert">{loadError}</div>
    {:else if selectedIssue}
      {#if loadError}
        <p class="inline-error" role="alert">{loadError}</p>
      {/if}
      <KataIssueDetail
      issue={selectedIssue}
      events={selectedEvents}
      {issueCatalog}
      searchReferences={searchKataTaskReferences}
      activeDaemonId={kata.daemon_id}
      {linkFilters}
      onLinkFiltersChange={(next) => {
        linkFilters = next;
      }}
      {projects}
      ownerOptions={ownerOptions()}
      messageLinks={selectedMessageLinks()}
      unlinkBusyIds={unlinkBusyIds}
      {selectedRecurrences}
      {checklistRevealed}
      actionsDisabled={mutationActionsBlocked}
      authorityBlocked={detailAuthorityBlocked}
      draftResetGeneration={mutationDraftResetGeneration}
      movePending={pendingMoveIssueUIDs.has(selectedIssue.issue.uid)}
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
      onSelectIssue={(target) => {
        void selectIssue(target.uid);
      }}
      />
    {:else}
      <div class="state">Task not found</div>
    {/if}
  </div>
</div>

<KataRecurrenceDialogs
  bind:this={recurrenceDialogs}
  {selectedIssue}
  {actor}
  disabled={mutationActionsBlocked}
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

  .kata-workspace-sidebar__content {
    min-height: 0;
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .authority-recovery {
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    border-bottom: 1px solid var(--border-muted);
    background: color-mix(in srgb, var(--accent-amber) 10%, transparent);
    padding: 8px 12px;
    color: var(--text-primary);
    font-size: var(--font-size-xs);
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
