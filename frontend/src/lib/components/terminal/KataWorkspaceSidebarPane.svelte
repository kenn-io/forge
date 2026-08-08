<script lang="ts">
  import { Cause, Effect, Exit, Option } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppServices } from "../../app/runtime.js";
  import { showFlash } from "../../stores/flash.svelte.js";

  import {
    createKataTaskAPI,
    KataMutationOutcomeUnknownError,
    KataMutationPartiallyAppliedError,
  } from "../../api/kata/taskClient.js";
  import {
    fetchKataWorkspaceSnapshot,
    searchKataTaskReferences,
    type KataSnapshotIntent,
    type KataTaskReferenceSearch,
  } from "../../api/kata/snapshot.js";
  import type { KataWorkspaceSnapshotProjection } from "../../api/kata/snapshotProjection.js";
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
  import type { KataWorkspaceMetadata } from "../../api/kata/workspaces.js";
  import KataIssueDetail from "../../components/kata/KataIssueDetail.svelte";
  import type { TypeaheadOption } from "@kenn-io/kit-ui";
  import KataRecurrenceDialogs from "../../features/kata/KataRecurrenceDialogs.svelte";
  import { createKataLinkFilters, type KataLinkFilters } from "../../features/kata/kataLinkFilters.js";
  import {
    KataMutationError,
    KataWorkflow,
    type KataMutationFenceState,
  } from "../../features/kata/kata-workflow.js";
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
  let mutationRefreshGeneration = 0;
  let mutationTransportPending = false;
  let mutationRecurrenceRefreshRequired = false;
  let checklistRevealed = $state(false);
  let linkFilters = $state<KataLinkFilters>(createKataLinkFilters("all"));
  let pendingMoveIssueUIDs = $state.raw<ReadonlySet<string>>(new Set());
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
  const activeMutationFenceKey = $derived(JSON.stringify([kata.daemon_id, kata.issue_uid]));

  function observeMutationFence(state: KataMutationFenceState): Effect.Effect<void> {
    return Effect.sync(() => {
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
      mutationRefreshGeneration += 1;
    });
  }

  $effect(() => {
    const key = activeMutationFenceKey;
    const execution = appRuntime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* KataWorkflow;
        yield* workflow.claimMutation(key, mutationSurfaceOwner, observeMutationFence);
      }),
      {
        operation: "claim embedded Kata mutation recovery",
        safeContext: { owner: mutationSurfaceOwner },
        onFailure: () => {},
      },
    );
    return () => {
      execution.interrupt();
      appRuntime.runCommand(
        Effect.gen(function* () {
          const workflow = yield* KataWorkflow;
          yield* workflow.releaseMutation(mutationSurfaceOwner);
        }),
        {
          operation: "release embedded Kata mutation recovery",
          safeContext: { owner: mutationSurfaceOwner },
          onFailure: () => {},
        },
      );
    };
  });
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

  async function observeAppCommand<A, E>(
    program: Effect.Effect<A, E, AppServices>,
    operation: string,
    signal?: AbortSignal,
  ): Promise<A> {
    const execution = appRuntime.runCommand(program, {
      operation,
      safeContext: { daemonId: kata.daemon_id },
      onFailure: () => {},
    });
    const interrupt = () => execution.interrupt();
    signal?.addEventListener("abort", interrupt, { once: true });
    if (signal?.aborted) interrupt();
    try {
      const exit = await Effect.runPromise(execution.await);
      if (Exit.isSuccess(exit)) return exit.value;
      const failure = Cause.findErrorOption(exit.cause);
      if (Option.isSome(failure)) throw failure.value;
      throw new Error(Cause.pretty(exit.cause));
    } finally {
      signal?.removeEventListener("abort", interrupt);
    }
  }

  const runtimeTaskReferenceSearch: KataTaskReferenceSearch = (query, options = {}) => {
    const { signal, ...requestOptions } = options;
    return observeAppCommand(
      searchKataTaskReferences(query, requestOptions),
      "search embedded Kata task references",
      signal,
    );
  };

  async function loadSelectedRecurrences(detail: KataTaskDetail, daemonID: string, requestID: number): Promise<boolean> {
    try {
      const response = await observeAppCommand(
        api.recurrences(detail.issue.project_id, { daemonId: daemonID }),
        "load embedded Kata recurrences",
      );
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
    mutationOutcomeUnknown = false;
    mutationPartialOutcome = false;
    mutationRecurrenceRefreshRequired = false;
    recurrenceConflictRecoveryPending = false;
    mutationRefreshGeneration += 1;
  }

  function beginRecurrenceConflictRecovery(): void {
    recurrenceDialogs?.closeAll();
    mutationRefreshGeneration += 1;
    mutationRefreshPending = true;
    mutationAcknowledged = false;
    mutationRecurrenceRefreshRequired = true;
    recurrenceConflictRecoveryPending = true;
    mutationRefreshError = "Could not refresh Kata recurrences.";
    mutationOutcomeUnknown = false;
  }

  async function loadSelectedSnapshot(uid: string, requestID = ++loadRequestID): Promise<boolean> {
    loading = selectedIssue?.issue.uid !== uid;
    loadError = null;
    try {
      const intent = selectedSnapshotIntent(uid);
      const program = Effect.gen(function* () {
        const workflow = yield* KataWorkflow;
        return yield* workflow.latestSnapshot(
          authorityOwner,
          authorityStore,
          intent,
          fetchKataWorkspaceSnapshot(intent),
        );
      });
      const execution = appRuntime.runCommand(program, {
        operation: "load embedded Kata task snapshot",
        safeContext: { daemonId: kata.daemon_id },
        onFailure: () => {},
      });
      const exit = await Effect.runPromise(execution.await);
      if (Exit.isFailure(exit)) {
        const failure = Cause.findErrorOption(exit.cause);
        if (Option.isSome(failure)) throw failure.value;
        throw new Error(Cause.pretty(exit.cause));
      }
      const accepted = exit.value;
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
      if (!mutationTransportPending && !mutationOutcomeUnknown && !mutationPartialOutcome) clearMutationRefresh();
      return true;
    } finally {
      if (requestID === loadRequestID) loading = false;
    }
  }

  onDestroy(() => {
    appRuntime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* KataWorkflow;
        yield* workflow.interruptAuthority(authorityOwner);
      }),
      {
        operation: "stop embedded Kata snapshot authority",
        safeContext: {},
        onFailure: () => {},
      },
    );
  });

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

  function hasHTTPStatus(error: unknown, status: number): boolean {
    return typeof error === "object" && error !== null && "status" in error && error.status === status;
  }

  async function mutateSelected<T>(
    task: KataTaskEffect<T>,
    options: {
      refreshRecurrences?: boolean;
      isApplied?: (snapshot: KataWorkspaceSnapshotProjection) => boolean;
    } = {},
  ): Promise<void> {
    const uid = selectedIssue?.issue.uid;
    if (!uid) throw new Error("No Kata task is selected.");
    const baseline = acceptedSnapshot;
    if (!baseline) throw new Error("No accepted Kata snapshot is available.");
    let replacementUID = uid;
    let refreshGeneration = ++mutationRefreshGeneration;
    mutationTransportPending = true;
    mutationRecurrenceRefreshRequired = options.refreshRecurrences === true;
    mutationRefreshPending = true;
    mutationAcknowledged = false;
    mutationRefreshError = null;
    mutationOutcomeUnknown = false;
    try {
      const program = Effect.gen(function* () {
        const workflow = yield* KataWorkflow;
        return yield* workflow.mutateAndRevalidate(
          task.pipe(
            Effect.mapError(
              (cause) =>
                new KataMutationError({ message: cause instanceof Error ? cause.message : String(cause), cause }),
            ),
          ),
          Effect.tryPromise({
            try: () => loadSelectedSnapshot(replacementUID),
            catch: (cause) => new KataMutationError({ message: cause instanceof Error ? cause.message : String(cause), cause }),
          }),
          () => Effect.sync(() => {
            mutationTransportPending = false;
            replacementUID = selectedIssueUID;
            refreshGeneration = ++mutationRefreshGeneration;
            mutationRefreshPending = true;
            mutationAcknowledged = true;
            mutationRefreshError = null;
            mutationOutcomeUnknown = false;
            mutationPartialOutcome = false;
          }),
          {
            identity: {
              key: JSON.stringify([baseline.daemon_id, uid]),
              daemonId: baseline.daemon_id,
              operation: "change Kata task",
              target: uid,
            },
            baseline,
            readFresh: fetchKataWorkspaceSnapshot(selectedSnapshotIntent(uid), { fresh: true }),
            ...(options.isApplied === undefined ? {} : { isApplied: options.isApplied }),
          },
        );
      });
      const execution = appRuntime.runCommand(program, {
        operation: "run Kata workspace mutation",
        safeContext: { daemonId: acceptedDaemonIDForMutation() },
        onFailure: () => {},
      });
      const exit = await Effect.runPromise(execution.await);
      if (Exit.isFailure(exit)) {
        const failure = Cause.findErrorOption(exit.cause);
        if (Option.isNone(failure)) throw new Error(Cause.pretty(exit.cause));
        if (failure.value instanceof KataMutationError && failure.value.cause instanceof Error) {
          throw failure.value.cause;
        }
        throw failure.value;
      }
      appRuntime.runCommand(
        exit.value.replacement.pipe(
          Effect.tap((replacement) => Effect.sync(() => {
            if (refreshGeneration !== mutationRefreshGeneration || replacement.replacementAccepted) return;
            mutationRefreshPending = true;
            mutationRefreshError = replacement.replacementError ?? "Kata snapshot replacement was not accepted.";
            showFlash("Change saved, but Kata could not refresh. Retry the snapshot before making more changes.", {
              tone: "warning",
            });
          })),
        ),
        {
          operation: "revalidate Kata workspace snapshot after mutation",
          safeContext: { daemonId: acceptedDaemonIDForMutation() },
          onFailure: () => {},
        },
      );
    } catch (mutationError) {
      mutationTransportPending = false;
      mutationRecurrenceRefreshRequired = false;
      mutationRefreshGeneration += 1;
      if (mutationError instanceof KataMutationOutcomeUnknownError) {
        mutationRefreshPending = true;
        mutationAcknowledged = false;
        mutationRefreshError = mutationError.message;
        mutationOutcomeUnknown = true;
        mutationPartialOutcome = false;
        throw mutationError;
      }
      if (mutationError instanceof KataMutationPartiallyAppliedError) {
        mutationRefreshPending = true;
        mutationAcknowledged = false;
        mutationRefreshError = mutationError.message;
        mutationOutcomeUnknown = false;
        mutationPartialOutcome = true;
        throw mutationError;
      }
      mutationRefreshPending = false;
      mutationAcknowledged = false;
      mutationRefreshError = null;
      mutationOutcomeUnknown = false;
      mutationPartialOutcome = false;
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
    const releasePendingMove = () => {
      const nextPendingMoves = new Set(pendingMoveIssueUIDs);
      nextPendingMoves.delete(sourceIssueUID);
      pendingMoveIssueUIDs = nextPendingMoves;
    };
    try {
      await mutateSelected(
        api.moveIssue(
          selectedMutationTarget(sourceIssueUID),
          actor,
          toProjectUID,
          selectedMutationETag(sourceIssueUID),
          acceptedMutationOptions(),
        ),
      );
      return true;
    } catch (error) {
      releasePendingMove();
      if (generation === issueContextGeneration) {
        showFlash(error instanceof Error ? error.message : "Kata request failed.", { tone: "danger" });
      }
      return false;
    } finally {
      releasePendingMove();
    }
  }

  function patchSelectedMetadata(uid: string, patch: Record<string, unknown>): Promise<boolean> {
    return runTask(() => mutateSelected(
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
    const priorMatches = selectedIssue?.comments.filter(
      (comment) => comment.author === actor && comment.body === body,
    ).length ?? 0;
    return runTask(() =>
      mutateSelected(api.addComment(selectedMutationTarget(uid), actor, body, acceptedMutationOptions()), {
        isApplied: (snapshot) =>
          snapshot.selected_detail?.issue.uid === uid &&
          snapshot.selected_detail.comments.filter(
            (comment) => comment.author === actor && comment.body === body,
          ).length > priorMatches,
      }),
    );
  }

  function editSelectedIssue(uid: string, patch: KataTaskEditPatch): Promise<boolean> {
    return runTask(() =>
      mutateSelected(api.editIssue(selectedMutationTarget(uid), actor, patch, acceptedMutationOptions())),
    );
  }

  function assignSelectedOwner(uid: string, owner: string): Promise<boolean> {
    return runTask(() =>
      mutateSelected(api.assignOwner(selectedMutationTarget(uid), actor, owner, acceptedMutationOptions())),
    );
  }

  function unassignSelectedOwner(uid: string): Promise<boolean> {
    return runTask(() =>
      mutateSelected(api.unassignOwner(selectedMutationTarget(uid), actor, acceptedMutationOptions())),
    );
  }

  function setSelectedPriority(uid: string, priority: number | null): Promise<boolean> {
    return runTask(() =>
      mutateSelected(api.setPriority(selectedMutationTarget(uid), actor, priority, acceptedMutationOptions())),
    );
  }

  function addSelectedLabel(uid: string, label: string): Promise<boolean> {
    return runTask(() =>
      mutateSelected(api.addLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions())),
    );
  }

  async function removeSelectedLabel(uid: string, label: string): Promise<void> {
    await runTask(() =>
      mutateSelected(api.removeLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions())),
    );
  }

  function revealChecklist(): void {
    checklistRevealed = true;
  }

  async function deleteRecurrence(recurrence: KataRecurrence): Promise<boolean> {
    return runTask(async () => {
      try {
        await mutateSelected(
          api.deleteRecurrence(
            recurrence.project_id,
            recurrence.uid,
            actor,
            acceptedMutationOptions(),
            `"rev-${recurrence.revision}"`,
          ),
          { refreshRecurrences: true },
        );
      } catch (error) {
        // A revision conflict means another client changed this recurrence;
        // reload the list so the open dialog uses the current revision. If
        // reconciliation fails, fence that stale dialog until a retry loads
        // fresh recurrence data.
        if (hasHTTPStatus(error, 412) && selectedIssue) {
          const refreshed = await loadSelectedRecurrences(selectedIssue, acceptedDaemonIDForMutation(), loadRequestID);
          if (!refreshed) beginRecurrenceConflictRecovery();
        }
        throw error;
      }
    });
  }

  async function createRecurrence(projectID: number, input: KataCreateRecurrenceInput): Promise<void> {
    await runTaskOrThrow(() => mutateSelected(
      api.createRecurrence(projectID, input, acceptedMutationOptions()),
      { refreshRecurrences: true },
    ));
  }

  async function patchRecurrence(id: number, input: KataPatchRecurrenceInput, etag: string): Promise<void> {
    const recurrence = selectedRecurrences.find((item) => item.id === id);
    if (!recurrence) throw new Error(`recurrence not loaded: id=${id}`);
    await runTaskOrThrow(() =>
      mutateSelected(
        api.patchRecurrence(recurrence.project_id, recurrence.uid, input, etag, acceptedMutationOptions()),
        { refreshRecurrences: true },
      ),
    );
  }

  function closeSelectedIssue(
    reason: "done" | "wontfix" | "duplicate" | "superseded",
    message: string,
  ): Promise<boolean> {
    const selected = selectedIssue;
    if (!selected) return Promise.resolve(false);
    return runTask(() => mutateSelected(
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
      mutateSelected(api.reopenIssue(selectedMutationTarget(selected.issue.uid), actor, acceptedMutationOptions())),
    );
  }

  function deleteSelectedIssue(): Promise<boolean> {
    return closeSelectedIssue("wontfix", "Deleted from workspace sidebar.");
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
      const uncertainMutationKey = activeMutationFenceKey;
      if (mutationPartialOutcome && uncertainMutationKey !== null) {
        await observeAppCommand(
          Effect.gen(function* () {
            const workflow = yield* KataWorkflow;
            yield* workflow.acknowledgeMutation(uncertainMutationKey);
          }),
          "acknowledge partial embedded Kata mutation",
        );
      } else if (mutationOutcomeUnknown && uncertainMutationKey !== null) {
        const resolution = await observeAppCommand(
          Effect.gen(function* () {
            const workflow = yield* KataWorkflow;
            return yield* workflow.reconcileMutation(uncertainMutationKey);
          }),
          "reconcile uncertain embedded Kata mutation",
        );
        if (resolution === "ambiguous") return;
      }
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
      <button type="button" disabled={mutationRefreshRetrying} onclick={() => void retryMutationSnapshot()}>
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
