<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { showFlash } from "../../stores/flash.svelte.js";

  import {
    createKataTaskAPI,
    KataMutationOutcomeUnknownError,
    KataMutationPartiallyAppliedError,
    KataTaskRevisionConflictError,
  } from "../../api/kata/taskClient.js";
  import {
    fetchKataWorkspaceSnapshot,
    searchKataTaskReferences,
    type KataSnapshotIntent,
    type KataTaskReferenceSearch,
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
    KataTaskEffect,
    KataTaskMutationTarget,
    KataTaskSummary,
  } from "../../api/kata/taskTypes.js";
  import {
    normalizeKataEventEnvelope,
    normalizeKataProject,
    normalizeKataTaskDetail,
    normalizeKataTaskSummary,
  } from "../../api/kata/taskNormalizers.js";
  import type { KataWorkspaceMetadata } from "../../api/kata/workspaces.js";
  import KataIssueDetail from "../../components/kata/KataIssueDetail.svelte";
  import type { TypeaheadOption } from "@kenn-io/kit-ui";
  import KataRecurrenceDialogs from "../../features/kata/KataRecurrenceDialogs.svelte";
  import type { KataCommand } from "../../features/kata/kata-command.js";
  import { createKataLinkFilters, type KataLinkFilters } from "../../features/kata/kataLinkFilters.js";
  import { KataRecurrenceConflictError } from "../../features/kata/recurrence-conflict.js";
  import {
    KataMutationError,
    KataWorkflow,
    type KataCustomMutationUncertainty,
    type KataMutationFenceState,
  } from "../../features/kata/kata-workflow.js";
  import {
    commentMutationEvidence,
    editMutationEvidence,
    kataMutationIdentity,
    labelMutationEvidence,
    metadataMutationEvidence,
    moveMutationEvidence,
    ownerMutationEvidence,
    priorityMutationEvidence,
    reconcileRecurrenceMutation,
    recurrenceCreateMatches,
    recurrencePatchMatches,
    statusMutationEvidence,
    type KataMutationEvidence,
  } from "../../features/kata/kata-mutation-evidence.js";
  import { createKataWorkspaceAuthorityOwner } from "../../features/kata/kataWorkspaceAuthorityController.svelte.js";
  import { createKataAuthorityStore } from "../../stores/kata-authority.svelte.js";

  interface Props {
    kata: KataWorkspaceMetadata;
    disabled?: boolean;
  }

  let { kata, disabled = false }: Props = $props();

  const actor = "kenn-forge";
  const appRuntime = getAppRuntime();
  const api = createKataTaskAPI();
  const authorityStore = createKataAuthorityStore();
  const authorityOwner = createKataWorkspaceAuthorityOwner();
  const mutationSurfaceOwner = createKataWorkspaceAuthorityOwner();

  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let mutationRefreshPending = $state(false);
  let mutationAcknowledged = $state(false);
  let mutationDraftResetGeneration = $state(0);
  let mutationRefreshError = $state<string | null>(null);
  let mutationRefreshRetrying = $state(false);
  let recurrenceConflictRecoveryPending = $state(false);
  let mutationOutcomeUnknown = $state(false);
  let mutationPartialOutcome = $state(false);
  let mutationTransportPending = false;
  let mutationRecurrenceRefreshRequired = false;
  let checklistRevealed = $state(false);
  let linkFilters = $state<KataLinkFilters>(createKataLinkFilters("all"));
  let pendingMoveIssueUIDs = $state.raw<ReadonlySet<string>>(new SvelteSet());
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
  let retryExecution: AppExecution<void, never> | null = null;
  let selectionExecution: AppExecution<void, never> | null = null;
  const acceptedSnapshot = $derived(authorityStore.snapshot);
  let activeMutationFenceKey = $state<string | null>(null);

  function selectedDetailFromSnapshot(
    source: NonNullable<NonNullable<typeof acceptedSnapshot>["selected_detail"]>,
  ): KataTaskDetail {
    const detail = normalizeKataTaskDetail(source);
    return source.etag === undefined ? detail : { ...detail, etag: source.etag };
  }

  function observeMutationFence(state: KataMutationFenceState): Effect.Effect<void> {
    return Effect.sync(() => {
      activeMutationFenceKey = state.kind === "resolved" ? null : state.identity.key;
      if (state.kind === "unknown") {
        mutationRefreshPending = true;
        mutationAcknowledged = false;
        mutationOutcomeUnknown = true;
        mutationPartialOutcome = false;
        mutationRefreshError = state.message;
        return;
      }
      if (state.kind === "partial") {
        mutationRefreshPending = true;
        mutationAcknowledged = false;
        mutationOutcomeUnknown = false;
        mutationPartialOutcome = true;
        mutationRefreshError = state.message;
        return;
      }
      if (state.kind === "reconciling") {
        mutationRefreshPending = true;
        mutationAcknowledged = false;
        mutationOutcomeUnknown = true;
        mutationPartialOutcome = false;
        mutationRefreshError = null;
        return;
      }
      if (state.resolution === "applied") mutationDraftResetGeneration += 1;
      mutationRefreshPending = false;
      mutationAcknowledged = false;
      mutationOutcomeUnknown = false;
      mutationPartialOutcome = false;
      mutationRefreshError = null;
    });
  }

  $effect(() => {
    const daemonId = kata.daemon_id;
    const execution = appRuntime.runCommand(
      Effect.scoped(
        Effect.gen(function* () {
          const workflow = yield* KataWorkflow;
          yield* workflow.claimMutations(daemonId, mutationSurfaceOwner, observeMutationFence);
          yield* Effect.addFinalizer(() => workflow.interruptAuthority(authorityOwner));
          return yield* Effect.never;
        }),
      ),
      {
        operation: "claim embedded Kata mutation recovery",
        safeContext: { owner: mutationSurfaceOwner },
        onFailure: () => {},
      },
    );
    return execution.interrupt;
  });
  const selectedIssue = $derived(
    acceptedSnapshot?.selected_detail
      ? selectedDetailFromSnapshot(acceptedSnapshot.selected_detail)
      : null,
  );
  const selectedEvents = $derived(
    acceptedSnapshot ? acceptedSnapshot.selected_history.map(normalizeKataEventEnvelope) : [],
  );
  const projects = $derived(
    acceptedSnapshot ? acceptedSnapshot.projects.map(normalizeKataProject) : [],
  );
  const issueCatalog = $derived(
    acceptedSnapshot ? acceptedSnapshot.issues.map((issue) => normalizeKataTaskSummary(issue)) : [],
  );
  const mutationActionsBlocked = $derived(
    disabled || mutationRefreshPending || authorityStore.state.phase !== "accepted",
  );
  const detailAuthorityBlocked = $derived(
    disabled ||
      (mutationRefreshPending && (mutationAcknowledged || recurrenceConflictRecoveryPending)) ||
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

  const runtimeTaskReferenceSearch: KataTaskReferenceSearch = (query, options = {}) => {
    return searchKataTaskReferences(query, {
      ...options,
      daemon_id: options.daemon_id ?? kata.daemon_id,
    });
  };

  function loadSelectedRecurrences(detail: KataTaskDetail, daemonID: string): KataCommand<boolean> {
    return api.recurrences(detail.issue.project_id, { daemonId: daemonID }).pipe(
      Effect.match({
        onFailure: () => {
          if (selectedIssue?.issue.uid === detail.issue.uid) selectedRecurrences = [];
          return false;
        },
        onSuccess: (response) => {
          if (selectedIssue?.issue.uid !== detail.issue.uid) return false;
          selectedRecurrences = response.recurrences;
          recurrenceDialogs?.reconcileRecurrences(response.recurrences);
          return true;
        },
      }),
    );
  }

  function clearMutationRefresh(): void {
    if (mutationAcknowledged) mutationDraftResetGeneration += 1;
    mutationRefreshPending = false;
    mutationAcknowledged = false;
    mutationRefreshError = null;
    mutationOutcomeUnknown = false;
    mutationPartialOutcome = false;
    mutationRecurrenceRefreshRequired = false;
    recurrenceConflictRecoveryPending = false;
  }

  function beginRecurrenceConflictRecovery(): void {
    mutationRefreshPending = true;
    mutationAcknowledged = false;
    mutationRecurrenceRefreshRequired = true;
    recurrenceConflictRecoveryPending = true;
    mutationRefreshError = "Could not refresh Kata recurrences.";
    mutationOutcomeUnknown = false;
  }

  function loadSelectedSnapshot(uid: string): KataCommand<boolean, unknown> {
    return Effect.gen(function* () {
      loading = selectedIssue?.issue.uid !== uid;
      loadError = null;
      const intent = selectedSnapshotIntent(uid);
      const workflow = yield* KataWorkflow;
      const accepted = yield* workflow.latestSnapshot(
        authorityOwner,
        authorityStore,
        intent,
        fetchKataWorkspaceSnapshot(intent),
      );
      const snapshot = authorityStore.snapshot;
      const detail = snapshot?.selected_detail;
      if (!accepted || snapshot?.selected_issue_uid !== uid || !detail) {
        return yield* Effect.fail(new Error(`Kata snapshot did not include selected task ${uid}`));
      }
      selectedRecurrences = [];
      const selectedDetail = selectedDetailFromSnapshot(detail);
      const awaitRecurrences =
        mutationRefreshPending && !mutationTransportPending && mutationRecurrenceRefreshRequired;
      const recurrencesRefreshed = yield* loadSelectedRecurrences(selectedDetail, snapshot.daemon_id);
      if (awaitRecurrences && !recurrencesRefreshed) {
        return yield* Effect.fail(new Error("Could not refresh Kata recurrences."));
      }
      if (!mutationTransportPending && !mutationOutcomeUnknown && !mutationPartialOutcome) clearMutationRefresh();
      return true;
    }).pipe(Effect.ensuring(Effect.sync(() => (loading = false))));
  }

  $effect(() => {
    const issueUID = kata.issue_uid;
    if (lastPropIssueUID && lastPropIssueUID !== issueUID) {
      untrack(() => recurrenceDialogs?.closeAll());
    }
    lastPropIssueUID = issueUID;
    selectedIssueUID = issueUID;
    loading = true;
    loadError = null;
    checklistRevealed = false;
    linkFilters = createKataLinkFilters("all");
    const execution = untrack(() =>
      appRuntime.runCommand(
        loadSelectedSnapshot(issueUID).pipe(
          Effect.catch((error) =>
            Effect.sync(() => {
              loadError = error instanceof Error ? error.message : "Could not load Kata task.";
            }),
          ),
        ),
        {
          operation: "load embedded Kata task snapshot",
          safeContext: { daemonId: kata.daemon_id },
          onFailure: () => {},
        },
      ),
    );
    return execution.interrupt;
  });

  function ownerOptions(): TypeaheadOption[] {
    const selected = selectedIssue?.issue;
    return [selected?.owner, ...issueCatalog.map((issue) => issue.owner)]
      .filter((owner): owner is string => typeof owner === "string" && owner.trim().length > 0)
      .filter((owner, index, owners) => owners.indexOf(owner) === index)
      .sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }))
      .map((owner) => ({ name: owner, label: owner }));
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

  function mutationError(cause: unknown): Error {
    return cause instanceof Error ? cause : new Error(String(cause));
  }

  function applyMutationFailure(error: unknown): void {
    mutationTransportPending = false;
    mutationRecurrenceRefreshRequired = false;
    if (error instanceof KataMutationOutcomeUnknownError) {
      mutationRefreshPending = true;
      mutationAcknowledged = false;
      mutationRefreshError = error.message;
      mutationOutcomeUnknown = true;
      mutationPartialOutcome = false;
      return;
    }
    if (error instanceof KataMutationPartiallyAppliedError) {
      mutationRefreshPending = true;
      mutationAcknowledged = false;
      mutationRefreshError = error.message;
      mutationOutcomeUnknown = false;
      mutationPartialOutcome = true;
      return;
    }
    mutationRefreshPending = false;
    mutationAcknowledged = false;
    mutationRefreshError = null;
    mutationOutcomeUnknown = false;
    mutationPartialOutcome = false;
  }

  function mutateSelected<T>(
    task: () => KataTaskEffect<T>,
    options:
      | { readonly evidence: KataMutationEvidence; readonly refreshRecurrences?: boolean }
      | { readonly uncertainty: KataCustomMutationUncertainty; readonly refreshRecurrences?: boolean },
  ): KataCommand<void, unknown> {
    return Effect.gen(function* () {
      const context = yield* Effect.try({
        try: () => {
          const uid = selectedIssue?.issue.uid;
          if (!uid) throw new Error("No Kata task is selected.");
          const baseline = acceptedSnapshot;
          if (!baseline) throw new Error("No accepted Kata snapshot is available.");
          return { uid, baseline };
        },
        catch: mutationError,
      });
      const uncertainty = "uncertainty" in options
        ? options.uncertainty
        : {
            identity: options.evidence.identity(context.baseline.daemon_id),
            baseline: context.baseline,
            readFresh: fetchKataWorkspaceSnapshot(
              {
                daemon_id: context.baseline.daemon_id,
                scope: "global",
                authority: "all",
                ...(options.evidence.selectedIssueUID === undefined
                  ? {}
                  : { selected_issue_uid: options.evidence.selectedIssueUID }),
              },
              { fresh: true },
            ),
            isApplied: options.evidence.isApplied,
          };
      const mutation = yield* Effect.try({ try: task, catch: mutationError });
      let replacementUID = context.uid;
      mutationTransportPending = true;
      mutationRecurrenceRefreshRequired = options.refreshRecurrences === true;
      mutationRefreshPending = true;
      mutationAcknowledged = false;
      mutationRefreshError = null;
      mutationOutcomeUnknown = false;

      const workflow = yield* KataWorkflow;
      const result = yield* workflow.mutateAndRevalidate(
        context.baseline.daemon_id,
        mutation.pipe(
          Effect.mapError((cause) => new KataMutationError({ message: mutationError(cause).message, cause })),
        ),
        Effect.suspend(() => loadSelectedSnapshot(replacementUID)).pipe(
          Effect.mapError((cause) => new KataMutationError({ message: mutationError(cause).message, cause })),
        ),
        () =>
          Effect.sync(() => {
            mutationTransportPending = false;
            replacementUID = selectedIssueUID;
            mutationRefreshPending = true;
            mutationAcknowledged = true;
            mutationRefreshError = null;
            mutationOutcomeUnknown = false;
            mutationPartialOutcome = false;
          }),
        uncertainty,
      );
      const replacement = yield* result.replacement;
      if (!replacement.replacementAccepted) {
        mutationRefreshPending = true;
        mutationRefreshError = replacement.replacementError ?? "Kata snapshot replacement was not accepted.";
        showFlash("Change saved, but Kata could not refresh. Retry the snapshot before making more changes.", {
          tone: "warning",
        });
      }
    }).pipe(
      Effect.mapError((error) =>
        error instanceof KataMutationError && error.cause instanceof Error ? error.cause : error,
      ),
      Effect.tapError((error) => Effect.sync(() => applyMutationFailure(error))),
    );
  }

  function runTask(
    task: () => KataCommand<void | boolean, unknown>,
    shouldSurfaceFailure: () => boolean = () => true,
  ): KataCommand<boolean> {
    return Effect.gen(function* () {
      if (mutationActionsBlocked) return false;
      return (yield* task()) ?? true;
    }).pipe(
      Effect.catch((error) =>
        Effect.sync(() => {
          if (shouldSurfaceFailure()) {
            showFlash(error instanceof Error ? error.message : "Kata request failed.", { tone: "danger" });
          }
          return false;
        }),
      ),
    );
  }

  function runTaskOrThrow(task: () => KataCommand<void, unknown>): KataCommand<void, unknown> {
    return Effect.gen(function* () {
      if (mutationActionsBlocked) return;
      yield* task();
    });
  }

  function runLoadTask(task: () => KataCommand<void | boolean, unknown>): KataCommand<boolean> {
    return Effect.gen(function* () {
      loadError = null;
      return (yield* task()) ?? true;
    }).pipe(
      Effect.catch((error) =>
        Effect.sync(() => {
          loadError = error instanceof Error ? error.message : "Could not load Kata task.";
          return false;
        }),
      ),
    );
  }

  function moveSelectedIssue(toProjectUID: string): KataCommand<boolean> {
    return Effect.gen(function* () {
      const selected = selectedIssue?.issue;
      if (!selected || pendingMoveIssueUIDs.has(selected.uid)) return false;
      const sourceIssueUID = selected.uid;
      pendingMoveIssueUIDs = new SvelteSet(pendingMoveIssueUIDs).add(sourceIssueUID);
      const releasePendingMove = Effect.sync(() => {
        const nextPendingMoves = new SvelteSet(pendingMoveIssueUIDs);
        nextPendingMoves.delete(sourceIssueUID);
        pendingMoveIssueUIDs = nextPendingMoves;
      });
      return yield* mutateSelected(
        () => api.moveIssue(
          selectedMutationTarget(sourceIssueUID),
          actor,
          toProjectUID,
          selectedMutationETag(sourceIssueUID),
          acceptedMutationOptions(),
        ),
        { evidence: moveMutationEvidence(sourceIssueUID, toProjectUID) },
      ).pipe(
        Effect.as(true),
        Effect.catch((error) =>
          Effect.sync(() => {
            if (selectedIssue?.issue.uid === sourceIssueUID) {
              showFlash(error instanceof Error ? error.message : "Kata request failed.", { tone: "danger" });
            }
            return false;
          }),
        ),
        Effect.ensuring(releasePendingMove),
      );
    });
  }

  function patchSelectedMetadata(uid: string, patch: Record<string, unknown>): KataCommand<boolean> {
    return runTask(() => mutateSelected(
      () => api.patchIssueMetadata(
        selectedMutationTarget(uid),
        actor,
        patch,
        selectedMutationETag(uid),
        acceptedMutationOptions(),
      ),
      { evidence: metadataMutationEvidence(uid, patch) },
    ));
  }

  function addSelectedComment(uid: string, body: string): KataCommand<boolean> {
    const priorMatches = selectedIssue?.comments.filter(
      (comment) => comment.author === actor && comment.body === body,
    ).length ?? 0;
    return runTask(() =>
      mutateSelected(
        () => api.addComment(selectedMutationTarget(uid), actor, body, acceptedMutationOptions()),
        { evidence: commentMutationEvidence(uid, actor, body, priorMatches) },
      ),
    );
  }

  function editSelectedIssue(uid: string, patch: KataTaskEditPatch): KataCommand<boolean> {
    return runTask(() =>
      mutateSelected(
        () => api.editIssue(selectedMutationTarget(uid), actor, patch, acceptedMutationOptions()),
        { evidence: editMutationEvidence(uid, patch) },
      ),
    );
  }

  function assignSelectedOwner(uid: string, owner: string): KataCommand<boolean> {
    return runTask(() =>
      mutateSelected(
        () => api.assignOwner(selectedMutationTarget(uid), actor, owner, acceptedMutationOptions()),
        { evidence: ownerMutationEvidence(uid, owner) },
      ),
    );
  }

  function unassignSelectedOwner(uid: string): KataCommand<boolean> {
    return runTask(() =>
      mutateSelected(
        () => api.unassignOwner(selectedMutationTarget(uid), actor, acceptedMutationOptions()),
        { evidence: ownerMutationEvidence(uid, undefined) },
      ),
    );
  }

  function setSelectedPriority(uid: string, priority: number | null): KataCommand<boolean> {
    return runTask(() =>
      mutateSelected(
        () => api.setPriority(selectedMutationTarget(uid), actor, priority, acceptedMutationOptions()),
        { evidence: priorityMutationEvidence(uid, priority) },
      ),
    );
  }

  function addSelectedLabel(uid: string, label: string): KataCommand<boolean> {
    return runTask(() =>
      mutateSelected(
        () => api.addLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions()),
        { evidence: labelMutationEvidence(uid, label, true) },
      ),
    );
  }

  function removeSelectedLabel(uid: string, label: string): KataCommand<boolean> {
    return runTask(() =>
      mutateSelected(
        () => api.removeLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions()),
        { evidence: labelMutationEvidence(uid, label, false) },
      ),
    );
  }

  function revealChecklist(): void {
    checklistRevealed = true;
  }

  function recurrenceUncertainty(
    daemonId: string,
    projectID: number,
    family: string,
    operation: string,
    target: string,
    baseline: readonly KataRecurrence[],
    isApplied: (recurrences: readonly KataRecurrence[]) => boolean,
  ): KataCustomMutationUncertainty {
    return {
      identity: kataMutationIdentity(daemonId, family, operation, target),
      reconcile: reconcileRecurrenceMutation(
        baseline,
        Effect.suspend(() => api.recurrences(projectID, { daemonId })),
        isApplied,
      ),
    };
  }

  function deleteRecurrence(recurrence: KataRecurrence): KataCommand<boolean> {
    return runTask(() =>
      Effect.gen(function* () {
        const daemonId = yield* Effect.try({ try: acceptedDaemonIDForMutation, catch: mutationError });
        const baseline = selectedRecurrences;
        yield* mutateSelected(
          () => api.deleteRecurrence(
            recurrence.project_id,
            recurrence.uid,
            actor,
            { daemonId },
            `"rev-${recurrence.revision}"`,
          ),
          {
            refreshRecurrences: true,
            uncertainty: recurrenceUncertainty(
              daemonId,
              recurrence.project_id,
              "recurrence-delete",
              "delete Kata recurrence",
              recurrence.uid,
              baseline,
              (recurrences) => recurrences.every((candidate) => candidate.uid !== recurrence.uid),
            ),
          },
        );
      }).pipe(
        Effect.catch((error) => {
          if (!(error instanceof KataTaskRevisionConflictError)) return Effect.fail(error);
          const detail = selectedIssue;
          const daemonId = acceptedSnapshot?.daemon_id;
          if (detail === null || daemonId === undefined || daemonId === "") return Effect.fail(error);
          return loadSelectedRecurrences(detail, daemonId).pipe(
            Effect.tap((refreshed) => Effect.sync(() => {
              if (!refreshed) beginRecurrenceConflictRecovery();
            })),
            Effect.andThen(Effect.fail(error)),
          );
        }),
      ),
    );
  }

  function createRecurrence(projectID: number, input: KataCreateRecurrenceInput): KataCommand<void, unknown> {
    return runTaskOrThrow(() =>
      Effect.gen(function* () {
        const daemonId = yield* Effect.try({ try: acceptedDaemonIDForMutation, catch: mutationError });
        const baseline = selectedRecurrences;
        const baselineUIDs = new SvelteSet(baseline.map((recurrence) => recurrence.uid));
        yield* mutateSelected(
          () => api.createRecurrence(projectID, input, { daemonId }),
          {
            refreshRecurrences: true,
            uncertainty: recurrenceUncertainty(
              daemonId,
              projectID,
              "recurrence-create",
              "create Kata recurrence",
              `${projectID}:${input.rrule}:${input.dtstart}:${input.timezone}:${input.template.title}`,
              baseline,
              (recurrences) =>
                recurrences.filter(
                  (recurrence) => !baselineUIDs.has(recurrence.uid) && recurrenceCreateMatches(recurrence, input),
                ).length === 1,
            ),
          },
        );
      }),
    );
  }

  function patchRecurrence(id: number, input: KataPatchRecurrenceInput, etag: string): KataCommand<void, unknown> {
    return runTaskOrThrow(() =>
      Effect.gen(function* () {
        const recurrence = selectedRecurrences.find((item) => item.id === id);
        if (recurrence === undefined) {
          return yield* Effect.fail(new Error(`recurrence not loaded: id=${id}`));
        }
        const daemonId = yield* Effect.try({ try: acceptedDaemonIDForMutation, catch: mutationError });
        const baseline = selectedRecurrences;
        yield* mutateSelected(
          () => api.patchRecurrence(recurrence.project_id, recurrence.uid, input, etag, { daemonId }),
          {
            refreshRecurrences: true,
            uncertainty: recurrenceUncertainty(
              daemonId,
              recurrence.project_id,
              "recurrence-patch",
              "edit Kata recurrence",
              recurrence.uid,
              baseline,
              (recurrences) => {
                const fresh = recurrences.find((candidate) => candidate.uid === recurrence.uid);
                return fresh !== undefined && fresh.revision > recurrence.revision && recurrencePatchMatches(fresh, input);
              },
            ),
          },
        ).pipe(
          Effect.catch((error) => {
            if (!(error instanceof KataTaskRevisionConflictError)) return Effect.fail(error);
            return api.showRecurrence(recurrence.project_id, recurrence.uid, { daemonId }).pipe(
              Effect.matchEffect({
                onFailure: () =>
                  Effect.sync(beginRecurrenceConflictRecovery).pipe(Effect.andThen(Effect.fail(error))),
                onSuccess: (response) =>
                  Effect.sync(() => {
                    const fresh = response.recurrence;
                    const found = selectedRecurrences.some((candidate) => candidate.uid === fresh.uid);
                    selectedRecurrences = found
                      ? selectedRecurrences.map((candidate) => candidate.uid === fresh.uid ? fresh : candidate)
                      : [...selectedRecurrences, fresh];
                    const currentEtag = response.etag ?? `"rev-${fresh.revision}"`;
                    return new KataRecurrenceConflictError(error.message, fresh, currentEtag);
                  }).pipe(Effect.flatMap((conflict) => Effect.fail(conflict))),
              }),
            );
          }),
        );
      }),
    );
  }

  function closeSelectedIssue(
    reason: "done" | "wontfix" | "duplicate" | "superseded",
    message: string,
  ): KataCommand<boolean> {
    return Effect.suspend(() => {
      const selected = selectedIssue;
      if (selected === null) return Effect.succeed(false);
      return runTask(() => mutateSelected(
        () => api.closeIssue(
          selectedMutationTarget(selected.issue.uid),
          actor,
          { reason, message },
          acceptedMutationOptions(),
        ),
        { evidence: statusMutationEvidence(selected.issue.uid, "closed", reason) },
      ));
    });
  }

  function reopenSelectedIssue(): KataCommand<boolean> {
    return Effect.suspend(() => {
      const selected = selectedIssue;
      if (selected === null) return Effect.succeed(false);
      return runTask(() =>
        mutateSelected(
          () => api.reopenIssue(selectedMutationTarget(selected.issue.uid), actor, acceptedMutationOptions()),
          { evidence: statusMutationEvidence(selected.issue.uid, "open") },
        ),
      );
    });
  }

  function deleteSelectedIssue(): KataCommand<boolean> {
    return closeSelectedIssue("wontfix", "Deleted from workspace sidebar.");
  }

  function selectIssue(uid: string): KataCommand<boolean> {
    return Effect.sync(() => {
      recurrenceDialogs?.closeAll();
      selectedIssueUID = uid;
    }).pipe(Effect.andThen(runLoadTask(() => loadSelectedSnapshot(uid))));
  }

  function openSelectedIssue(uid: string): void {
    selectionExecution?.interrupt();
    selectionExecution = appRuntime.runCommand(selectIssue(uid).pipe(Effect.asVoid), {
      operation: "open linked embedded Kata task",
      safeContext: { issueUid: uid },
      onFailure: () => {},
    });
  }

  function retryMutationSnapshotCommand(): KataCommand<void> {
    return Effect.gen(function* () {
      const uncertainMutationKey = activeMutationFenceKey;
      const partialMutationKey = mutationPartialOutcome ? uncertainMutationKey : null;
      if (mutationOutcomeUnknown && uncertainMutationKey !== null) {
        const workflow = yield* KataWorkflow;
        const resolution = yield* workflow.reconcileMutation(uncertainMutationKey);
        if (resolution === "ambiguous") return;
      }
      const accepted = yield* loadSelectedSnapshot(selectedIssueUID);
      if (!accepted) return yield* Effect.fail(new Error("Kata snapshot replacement was not accepted."));
      if (partialMutationKey !== null) {
        const workflow = yield* KataWorkflow;
        yield* workflow.acknowledgeMutation(partialMutationKey);
      }
    }).pipe(
      Effect.catch((error) =>
        Effect.sync(() => {
          mutationRefreshError = error instanceof Error ? error.message : "Could not refresh Kata task.";
        }),
      ),
      Effect.ensuring(Effect.sync(() => (mutationRefreshRetrying = false))),
    );
  }

  function retryMutationSnapshot(): void {
    if (mutationRefreshRetrying || !mutationRefreshPending) return;
    mutationRefreshRetrying = true;
    mutationRefreshError = null;
    retryExecution?.interrupt();
    retryExecution = appRuntime.runCommand(retryMutationSnapshotCommand(), {
      operation: "retry embedded Kata mutation snapshot",
      safeContext: { daemonId: kata.daemon_id },
      onFailure: () => {},
    });
  }

  onDestroy(() => {
    retryExecution?.interrupt();
    selectionExecution?.interrupt();
  });
</script>

<div class="kata-workspace-sidebar" inert={disabled}>
  {#if mutationRefreshPending && (mutationAcknowledged || recurrenceConflictRecoveryPending || mutationOutcomeUnknown) && !mutationRefreshError}
    <div class="authority-recovery" role="status">
      {mutationOutcomeUnknown
        ? "Checking Kata snapshot before allowing more changes…"
        : recurrenceConflictRecoveryPending
          ? "Refreshing the current recurrence revision…"
          : "Change saved. Refreshing Kata snapshot…"}
    </div>
  {:else if mutationRefreshError}
    <div class="authority-recovery" role="alert">
      <span>
        {mutationPartialOutcome
          ? mutationRefreshError
          : mutationOutcomeUnknown
          ? `Kata could not confirm whether the last change was applied: ${mutationRefreshError}`
          : recurrenceConflictRecoveryPending
            ? `The recurrence changed, but its current revision could not be loaded: ${mutationRefreshError}`
            : `Change saved, but Kata snapshot refresh failed: ${mutationRefreshError}`}
      </span>
      <button type="button" disabled={mutationRefreshRetrying} onclick={retryMutationSnapshot}>
        {mutationRefreshRetrying
          ? "Retrying…"
          : mutationPartialOutcome
            ? "Acknowledge partial change"
            : "Retry Kata snapshot"}
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
      searchReferences={runtimeTaskReferenceSearch}
      activeDaemonId={kata.daemon_id}
      {linkFilters}
      onLinkFiltersChange={(next) => {
        linkFilters = next;
      }}
      {projects}
      ownerOptions={ownerOptions()}
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
      onRevealChecklist={revealChecklist}
      onCreateRecurrence={() => recurrenceDialogs?.openCreateRecurrence()}
      onEditRecurrence={(recurrence) => recurrenceDialogs?.openEditRecurrence(recurrence)}
      onDeleteRecurrence={(recurrence) => recurrenceDialogs?.openDeleteRecurrence(recurrence)}
      onCloseIssue={closeSelectedIssue}
      onReopenIssue={reopenSelectedIssue}
      onDeleteIssue={deleteSelectedIssue}
      onSelectIssue={(target) => openSelectedIssue(target.uid)}
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
