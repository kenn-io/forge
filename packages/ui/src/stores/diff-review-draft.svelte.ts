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
  let draft = $state<DiffReviewDraft | null>(null);
  let loading = $state(false);
  let submitting = $state(false);
  let storeError = $state<string | null>(null);
  let wasEnabled = false;

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
    ].join(":");
  }

  function setContext(
    nextRef: ProviderRouteRef,
    nextNumber: number,
    nextEnabled: boolean,
  ): void {
    const changed =
      !ref ||
      ref.provider !== nextRef.provider ||
      ref.platformHost !== nextRef.platformHost ||
      ref.repoPath !== nextRef.repoPath ||
      number !== nextNumber;
    const enabling = !wasEnabled && nextEnabled;
    ref = nextRef;
    number = nextNumber;
    enabled = nextEnabled;
    wasEnabled = nextEnabled;
    if (!enabled) {
      draft = null;
      storeError = null;
      return;
    }
    if (changed || enabling) {
      draft = null;
      void loadDraft();
    }
  }

  function setRouteContext(
    nextRef: ProviderRouteRef,
    nextNumber: number,
  ): void {
    ref = nextRef;
    number = nextNumber;
  }

  function clear(): void {
    enabled = false;
    wasEnabled = false;
    ref = null;
    number = 0;
    draft = null;
    loading = false;
    submitting = false;
    storeError = null;
  }

  async function loadDraft(): Promise<void> {
    if (!enabled || !ref) return;
    const params = currentParams();
    if (!params) return;
    const key = requestKey();
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
      if (requestKey() !== key) return;
      draft = {
        ...data,
        comments: data.comments ?? [],
        supported_actions: data.supported_actions ?? [],
      };
    } catch (err) {
      if (requestKey() !== key) return;
      storeError = err instanceof Error ? err.message : String(err);
    } finally {
      if (requestKey() === key) {
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
    submitting = true;
    storeError = null;
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
      await loadDraft();
      return true;
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
      return false;
    } finally {
      submitting = false;
    }
  }

  async function deleteComment(commentID: string): Promise<boolean> {
    if (!enabled || !ref) return false;
    const params = currentParams();
    if (!params) return false;
    submitting = true;
    storeError = null;
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
      await loadDraft();
      return true;
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
      return false;
    } finally {
      submitting = false;
    }
  }

  async function publish(action: string, body = ""): Promise<boolean> {
    if (!enabled || !ref) return false;
    const params = currentParams();
    if (!params) return false;
    const publishedRef = ref;
    const publishedNumber = number;
    const key = requestKey();
    submitting = true;
    storeError = null;
    try {
      const { error, response } = await apiClient.POST(
        providerItemPath("pulls", ref, "/review-draft/publish"),
        { params, body: { action, body } },
      );
      if (!response.ok) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      draft = null;
      await loadDraft();
      if (requestKey() === key) {
        try {
          await opts.onPublished?.(publishedRef, publishedNumber);
        } catch {
          // The provider publish already succeeded. Detail refresh is best-effort.
        }
      }
      return true;
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
      return false;
    } finally {
      submitting = false;
    }
  }

  async function discard(): Promise<boolean> {
    if (!enabled || !ref) return false;
    const params = currentParams();
    if (!params) return false;
    submitting = true;
    storeError = null;
    try {
      const { error, response } = await apiClient.DELETE(
        providerItemPath("pulls", ref, "/review-draft"),
        { params },
      );
      if (!response.ok) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      draft = null;
      return true;
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
      return false;
    } finally {
      submitting = false;
    }
  }

  async function setThreadResolved(
    threadID: string,
    resolved: boolean,
  ): Promise<boolean> {
    if (!ref || !number) return false;
    const params = currentParams();
    if (!params) return false;
    submitting = true;
    storeError = null;
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
      storeError = err instanceof Error ? err.message : String(err);
      return false;
    } finally {
      submitting = false;
    }
  }

  return {
    isEnabled,
    getDraft,
    getComments,
    isLoading,
    isSubmitting,
    getError,
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
