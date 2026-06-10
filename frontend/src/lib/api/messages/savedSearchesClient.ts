import { apiErrorMessage, createRuntimeClient } from "../runtime.js";
import { normalizeSavedSearches, type SavedSearch } from "../../messages/savedSearches.js";
import type { components } from "@middleman/ui/api/schema";

export interface SavedSearchesListResponse {
  searches: SavedSearch[];
  etag: string;
}

export interface SavedSearchesAPI {
  list(): Promise<SavedSearchesListResponse>;
  replace(searches: SavedSearch[], ifMatch?: string): Promise<SavedSearchesListResponse>;
}

export interface SavedSearchesAPIError extends Error {
  status: number;
  code?: string;
  reason?: string;
}

export interface SavedSearchesClientOptions {
  baseURL?: string;
  fetch?: typeof fetch;
}

export function createSavedSearchesAPI(options: SavedSearchesClientOptions = {}): SavedSearchesAPI {
  const client = createRuntimeClient(options.fetch, options.baseURL);

  return {
    async list() {
      const { data, error, response } = await client.GET("/messages/saved-searches");
      throwOnSavedSearchesError(error, response);
      return normalizeResponse(data!);
    },
    async replace(searches, ifMatch) {
      const { data, error, response } = await client.PUT("/messages/saved-searches", {
        params: {
          header:
            ifMatch !== undefined ? { "If-Match": ifMatch, "X-Middleman-Csrf": "1" } : { "X-Middleman-Csrf": "1" },
        },
        body: { searches },
      });
      throwOnSavedSearchesError(error, response);
      return normalizeResponse(data!);
    },
  };
}

export interface MockSavedSearchesOptions {
  initial?: SavedSearch[];
}

export function createMockSavedSearchesBackend(options: MockSavedSearchesOptions = {}): SavedSearchesAPI {
  let state = normalizeSavedSearches(options.initial ?? []);
  let etag = computeETag(state);

  function reject(status: number, code: string, reason: string, message: string): never {
    const err = new Error(message) as SavedSearchesAPIError;
    err.name = "SavedSearchesAPIError";
    err.status = status;
    err.code = code;
    err.reason = reason;
    throw err;
  }

  function snapshot(): SavedSearchesListResponse {
    return { searches: state.map((s) => ({ ...s })), etag };
  }

  return {
    async list() {
      return snapshot();
    },
    async replace(searches, ifMatch) {
      if (ifMatch !== undefined && ifMatch !== etag) {
        reject(412, "conflict", "stale_etag", "stale etag");
      }
      state = normalizeSavedSearches(searches);
      etag = computeETag(state);
      return snapshot();
    },
  };
}

function normalizeResponse(data: components["schemas"]["MessagesSavedSearchesBody"]): SavedSearchesListResponse {
  return { searches: normalizeSavedSearches(data.searches ?? []), etag: data.etag };
}

function throwOnSavedSearchesError(
  error: Pick<Partial<components["schemas"]["ProblemError"]>, "code" | "detail" | "details" | "title"> | undefined,
  response: Response,
): void {
  if (response.ok) return;
  const err = new Error(apiErrorMessage(error, `${response.status}`)) as SavedSearchesAPIError;
  err.name = "SavedSearchesAPIError";
  err.status = response.status;
  if (typeof error?.code === "string") {
    err.code = error.code;
  }
  const reason = error?.details?.["reason"];
  if (typeof reason === "string") {
    err.reason = reason;
  }
  throw err;
}

function computeETag(list: SavedSearch[]): string {
  const json = JSON.stringify(list);
  let h = 2166136261;
  for (let i = 0; i < json.length; i++) {
    h = Math.imul(h ^ json.charCodeAt(i), 16777619);
  }
  return `"mock:${(h >>> 0).toString(16)}"`;
}
