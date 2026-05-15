import type { MiddlemanClient } from "../types.js";
import type { components } from "../api/generated/schema.js";
import {
  providerItemPath,
  providerRouteParams,
  type ProviderRouteRef,
} from "../api/provider-routes.js";

export type DiffReviewDraft = components["schemas"]["DiffReviewDraftResponse"];
export type DiffReviewDraftComment = components["schemas"]["DiffReviewDraftComment"];
export type DiffReviewLineRange = components["schemas"]["DiffReviewLineRange"];

export interface DiffReviewDraftStoreOptions {
  client: MiddlemanClient;
  onPublished?: (
    ref: ProviderRouteRef,
    number: number,
  ) => Promise<void> | void;
}

function apiErrorMessage(
  error: { detail?: string; title?: string } | undefined,
  fallback: string,
): string {
  return error?.detail ?? error?.title ?? fallback;
}

export function createDiffReviewDraftStore(opts: DiffReviewDraftStoreOptions) {
  const apiClient = opts.client;

  let enabled = $state(false);
  let ref = $state<ProviderRouteRef | null>(null);
  let number = $state(0);
  let diffHeadSHA = $state<string | undefined>(undefined);
  let draft = $state<DiffReviewDraft | null>(null);
  let loading = $state(false);
  let submitting = $state(false);
  let storeError = $state<string | null>(null);
  let storeWarning = $state<string | null>(null);
  let wasEnabled = false;
  let draftVersion = 0;
  let submitVersion = 0;

  function isEnabled(): boolean {
    return enabled;
  }

  function getDraft(): DiffReviewDraft | null {
    return draft;
  }

  function getComments(): DiffReviewDraftComment[] {
    return draft?.comments ?? [];
  }

  function isLoading(): boolean {
    return loading;
  }

  function isSubmitting(): boolean {
    return submitting;
  }

  function getError(): string | null {
    return storeError;
  }

  function getWarning(): string | null {
    return storeWarning;
  }

  function currentParams() {
    if (!ref || !number) return null;
    return {
      path: { ...providerRouteParams(ref), number },
    };
  }

  function requestKey(): string {
    if (!ref || !number) return "";
    return [
      enabled ? "enabled" : "disabled",
      ref.provider,
      ref.platformHost ?? "",
      ref.repoPath,
      number,
      diffHeadSHA ?? "",
    ].join(":");
  }

  function beginSubmit(): number {
    const token = ++submitVersion;
    submitting = true;
    return token;
  }

  function finishSubmit(token: number): void {
    if (submitVersion === token) {
      submitting = false;
    }
  }

  function cancelSubmit(): void {
    submitVersion += 1;
    submitting = false;
  }

  function setContext(
    nextRef: ProviderRouteRef,
    nextNumber: number,
    nextEnabled: boolean,
    nextDiffHeadSHA?: string,
  ): void {
    const changed =
      !ref ||
      ref.provider !== nextRef.provider ||
      ref.platformHost !== nextRef.platformHost ||
      ref.repoPath !== nextRef.repoPath ||
      number !== nextNumber ||
      diffHeadSHA !== nextDiffHeadSHA;
    const enabling = !wasEnabled && nextEnabled;
    ref = nextRef;
    number = nextNumber;
    diffHeadSHA = nextDiffHeadSHA;
    enabled = nextEnabled;
    wasEnabled = nextEnabled;
    if (!enabled) {
      draft = null;
      storeError = null;
      storeWarning = null;
      cancelSubmit();
      return;
    }
    if (changed || enabling) {
      draft = null;
      storeWarning = null;
      cancelSubmit();
      void loadDraft();
    }
  }

  function setRouteContext(
    nextRef: ProviderRouteRef,
    nextNumber: number,
  ): void {
    const changed =
      !ref ||
      ref.provider !== nextRef.provider ||
      ref.platformHost !== nextRef.platformHost ||
      ref.repoPath !== nextRef.repoPath ||
      number !== nextNumber;
    ref = nextRef;
    number = nextNumber;
    if (changed) {
      draftVersion += 1;
      cancelSubmit();
      storeError = null;
      storeWarning = null;
    }
  }

  function invalidateDraftLoad(): void {
    draftVersion += 1;
    loading = false;
  }

  function clear(): void {
    invalidateDraftLoad();
    cancelSubmit();
    enabled = false;
    wasEnabled = false;
    ref = null;
    number = 0;
    diffHeadSHA = undefined;
    draft = null;
    loading = false;
    storeError = null;
    storeWarning = null;
  }

  async function loadDraft(): Promise<void> {
    if (!enabled || !ref) return;
    const params = currentParams();
    if (!params) return;
    const key = requestKey();
    const version = ++draftVersion;
    const isCurrent = () => requestKey() === key && draftVersion === version;
    loading = true;
    storeError = null;
    try {
      const { data, error, response } = await apiClient.GET(
        providerItemPath("pulls", ref, "/review-draft"),
        { params },
      );
      if (!data) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      if (!isCurrent()) return;
      draft = {
        ...data,
        comments: data.comments ?? [],
        supported_actions: data.supported_actions ?? [],
      };
    } catch (err) {
      if (!isCurrent()) return;
      storeError = err instanceof Error ? err.message : String(err);
    } finally {
      if (isCurrent()) {
        loading = false;
      }
    }
  }

  async function createComment(
    body: string,
    range: DiffReviewLineRange,
  ): Promise<boolean> {
    if (!enabled || !ref) return false;
    const params = currentParams();
    if (!params) return false;
    const key = requestKey();
    invalidateDraftLoad();
    const version = draftVersion;
    const isCurrent = () => requestKey() === key && draftVersion === version;
    const submitToken = beginSubmit();
    storeError = null;
    storeWarning = null;
    try {
      const { data, error, response } = await apiClient.POST(
        providerItemPath("pulls", ref, "/review-draft/comments"),
        {
          params,
          body: { body, range },
        },
      );
      if (!data) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      if (!isCurrent()) return true;
      await loadDraft();
      return true;
    } catch (err) {
      if (isCurrent()) {
        storeError = err instanceof Error ? err.message : String(err);
      }
      return false;
    } finally {
      finishSubmit(submitToken);
    }
  }

  async function deleteComment(commentID: string): Promise<boolean> {
    if (!enabled || !ref) return false;
    const params = currentParams();
    if (!params) return false;
    const key = requestKey();
    invalidateDraftLoad();
    const version = draftVersion;
    const isCurrent = () => requestKey() === key && draftVersion === version;
    const submitToken = beginSubmit();
    storeError = null;
    storeWarning = null;
    try {
      const { error, response } = await apiClient.DELETE(
        providerItemPath("pulls", ref, "/review-draft/comments/{draft_comment_id}"),
        {
          params: {
            path: {
              ...params.path,
              draft_comment_id: commentID,
            },
          },
        },
      );
      if (!response.ok) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      if (!isCurrent()) return true;
      await loadDraft();
      return true;
    } catch (err) {
      if (isCurrent()) {
        storeError = err instanceof Error ? err.message : String(err);
      }
      return false;
    } finally {
      finishSubmit(submitToken);
    }
  }

  async function publish(action: string, body = ""): Promise<boolean> {
    if (!enabled || !ref) return false;
    const params = currentParams();
    if (!params) return false;
    const publishedRef = ref;
    const publishedNumber = number;
    const key = requestKey();
    invalidateDraftLoad();
    const version = draftVersion;
    const isCurrent = () => requestKey() === key && draftVersion === version;
    const submitToken = beginSubmit();
    storeError = null;
    storeWarning = null;
    try {
      const { data, error, response } = await apiClient.POST(
        providerItemPath("pulls", ref, "/review-draft/publish"),
        { params, body: { action, body } },
      );
      if (!response.ok) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      const partial = data?.status === "partially_published";
      if (!isCurrent()) return true;
      draft = null;
      await loadDraft();
      if (partial && requestKey() === key) {
        storeWarning =
          "Review was partially published. Some inline comments or the selected review action may not have been submitted.";
      }
      if (requestKey() === key) {
        try {
          await opts.onPublished?.(publishedRef, publishedNumber);
        } catch {
          // The provider publish already succeeded. Detail refresh is best-effort.
        }
      }
      return true;
    } catch (err) {
      if (isCurrent()) {
        storeError = err instanceof Error ? err.message : String(err);
      }
      return false;
    } finally {
      finishSubmit(submitToken);
    }
  }

  async function discard(): Promise<boolean> {
    if (!enabled || !ref) return false;
    const params = currentParams();
    if (!params) return false;
    const key = requestKey();
    invalidateDraftLoad();
    const version = draftVersion;
    const isCurrent = () => requestKey() === key && draftVersion === version;
    const submitToken = beginSubmit();
    storeError = null;
    storeWarning = null;
    try {
      const { error, response } = await apiClient.DELETE(
        providerItemPath("pulls", ref, "/review-draft"),
        { params },
      );
      if (!response.ok) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      if (!isCurrent()) return true;
      draft = null;
      return true;
    } catch (err) {
      if (isCurrent()) {
        storeError = err instanceof Error ? err.message : String(err);
      }
      return false;
    } finally {
      finishSubmit(submitToken);
    }
  }

  async function setThreadResolved(
    threadID: string,
    resolved: boolean,
  ): Promise<boolean> {
    if (!ref || !number) return false;
    const params = currentParams();
    if (!params) return false;
    const key = requestKey();
    const version = ++draftVersion;
    const isCurrent = () => requestKey() === key && draftVersion === version;
    const submitToken = beginSubmit();
    storeError = null;
    storeWarning = null;
    try {
      const path = resolved
        ? "/review-threads/{thread_id}/resolve"
        : "/review-threads/{thread_id}/unresolve";
      const { error, response } = await apiClient.POST(
        providerItemPath("pulls", ref, path),
        {
          params: {
            path: {
              ...params.path,
              thread_id: threadID,
            },
          },
        },
      );
      if (!response.ok) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      return true;
    } catch (err) {
      if (isCurrent()) {
        storeError = err instanceof Error ? err.message : String(err);
      }
      return false;
    } finally {
      finishSubmit(submitToken);
    }
  }

  return {
    isEnabled,
    getDraft,
    getComments,
    isLoading,
    isSubmitting,
    getError,
    getWarning,
    setContext,
    setRouteContext,
    clear,
    loadDraft,
    createComment,
    deleteComment,
    publish,
    discard,
    setThreadResolved,
  };
}

export type DiffReviewDraftStore = ReturnType<typeof createDiffReviewDraftStore>;
