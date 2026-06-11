import type { components } from "@middleman/ui/api/schema";

import { apiErrorMessage, createRuntimeClient } from "../runtime.js";
import type {
  MessagesAPI,
  MessagesAggregateResponse,
  MessagesCapabilities,
  MessageDetailData,
  MessageSummary,
  MessagesSearchResult,
} from "./types.js";

export interface MessagesAPIClientOptions {
  baseURL?: string;
  fetch?: typeof fetch;
}

export function createMessagesAPI(options: MessagesAPIClientOptions = {}): MessagesAPI {
  const client = createRuntimeClient(options.fetch, options.baseURL);

  return {
    async capabilities() {
      const { data, error, response } = await client.GET("/msgvault/health");
      throwOnMessagesError(error, response);
      return normalizeCapabilities(data!);
    },
    async search(q, opts) {
      const { data, error, response } = await client.GET("/msgvault/search", {
        params: { query: messagesSearchQuery(q, opts) },
      });
      throwOnMessagesError(error, response);
      return normalizeSearch(data!);
    },
    async message(id) {
      const { data, error, response } = await client.GET("/msgvault/messages/{id}", {
        params: { path: { id } },
      });
      throwOnMessagesError(error, response);
      return normalizeDetail(data!);
    },
    inlineURL: (id, cid) => resourceURLFor(options.baseURL, `msgvault/messages/${id}/inline`, { cid }),
    remoteImageURL: (id, token, index) =>
      resourceURLFor(options.baseURL, `msgvault/messages/${id}/remote-image/${token}/${index}`),
    async aggregates(view, opts) {
      const { data, error, response } = await client.GET("/msgvault/aggregates", {
        params: { query: messagesAggregateQuery(view, opts) },
      });
      throwOnMessagesError(error, response);
      return normalizeAggregate(data!);
    },
    async thread(conversationId) {
      const { data, error, response } = await client.GET("/msgvault/threads/{conversation_id}", {
        params: { path: { conversation_id: conversationId } },
      });
      throwOnMessagesError(error, response);
      return (data!.messages ?? []).map(normalizeSummary);
    },
    async configure(input) {
      const { data, error, response } = await client.POST("/msgvault/configure", {
        params: { header: { "X-Middleman-Csrf": "1" } },
        body: input,
      });
      throwOnMessagesError(error, response);
      return normalizeCapabilities(data!);
    },
  };
}

function resourceURLFor(baseURL: string | undefined, path: string, query: Record<string, string> = {}): string {
  const origin = typeof window !== "undefined" ? window.location.origin : "http://localhost";
  const base = new URL(baseURL ?? defaultAPIBaseURL(), origin).toString().replace(/\/+$/, "");
  const u = new URL(`${base}/${path.replace(/^\/+/, "")}`);
  for (const [key, value] of Object.entries(query)) {
    u.searchParams.set(key, value);
  }
  return u.origin === origin ? u.pathname + u.search : u.toString();
}

function defaultAPIBaseURL(): string {
  const basePath = typeof window !== "undefined" ? (window.__BASE_PATH__ ?? "/") : "/";
  const origin = typeof window !== "undefined" ? window.location.origin : "http://localhost";
  return new URL(`${basePath.replace(/\/$/, "")}/api/v1`, origin).toString();
}

function serializeHideDeleted(value: boolean | undefined): string | undefined {
  return value === undefined ? undefined : String(value);
}

function messagesSearchQuery(
  q: string,
  opts: { mode?: string; page?: number; pageSize?: number } | undefined,
): { q: string; mode?: string; page?: number; page_size?: number } {
  const query: { q: string; mode?: string; page?: number; page_size?: number } = { q };
  if (opts?.mode !== undefined) query.mode = opts.mode;
  if (opts?.page !== undefined) query.page = opts.page;
  if (opts?.pageSize !== undefined) query.page_size = opts.pageSize;
  return query;
}

function messagesAggregateQuery(
  view: string,
  opts:
    | {
        q?: string;
        hideDeleted?: boolean;
        sort?: "count" | "size" | "name";
        direction?: "asc" | "desc";
        limit?: number;
      }
    | undefined,
): { view_type: string; q?: string; hide_deleted?: string; sort?: string; direction?: string; limit?: number } {
  const query: {
    view_type: string;
    q?: string;
    hide_deleted?: string;
    sort?: string;
    direction?: string;
    limit?: number;
  } = {
    view_type: view,
  };
  if (opts?.q !== undefined) query.q = opts.q;
  const hideDeleted = serializeHideDeleted(opts?.hideDeleted);
  if (hideDeleted !== undefined) query.hide_deleted = hideDeleted;
  if (opts?.sort !== undefined) query.sort = opts.sort;
  if (opts?.direction !== undefined) query.direction = opts.direction;
  if (opts?.limit !== undefined) query.limit = opts.limit;
  return query;
}

function normalizeCapabilities(data: components["schemas"]["MsgvaultHealthBody"]): MessagesCapabilities {
  const features = data.features ?? {};
  return {
    ...data,
    modes: (data.modes ?? []) as MessagesCapabilities["modes"],
    features: {
      threads_endpoint: Boolean(features["threads_endpoint"]),
      mutations: Boolean(features["mutations"]),
      attachments_download: Boolean(features["attachments_download"]),
      sse_events: Boolean(features["sse_events"]),
    },
  } as MessagesCapabilities;
}

function normalizeSearch(data: components["schemas"]["MsgvaultSearchBody"]): MessagesSearchResult {
  return {
    ...data,
    mode: data.mode as MessagesSearchResult["mode"],
    messages: (data.messages ?? []).map(normalizeSummary),
  };
}

function normalizeAggregate(data: components["schemas"]["AggregateResult"]): MessagesAggregateResponse {
  return {
    view_type: data.view_type as MessagesAggregateResponse["view_type"],
    rows: data.rows ?? [],
  };
}

function normalizeSummary(data: components["schemas"]["MessageSummary"]): MessageSummary {
  return {
    ...data,
    to: data.to ?? [],
    cc: data.cc ?? [],
    bcc: data.bcc ?? [],
    labels: data.labels ?? [],
  };
}

function normalizeDetail(data: components["schemas"]["MsgvaultMessageBody"]): MessageDetailData {
  return {
    ...data,
    to: data.to ?? [],
    cc: data.cc ?? [],
    bcc: data.bcc ?? [],
    labels: data.labels ?? [],
    attachments: data.attachments ?? [],
  };
}

function throwOnMessagesError(
  error: Pick<Partial<components["schemas"]["ProblemError"]>, "code" | "detail" | "details" | "title"> | undefined,
  response: Response,
): void {
  if (response.ok) return;
  const err = new Error(apiErrorMessage(error, `${response.status}`)) as import("./types.js").MessagesAPIError;
  err.name = "MessagesAPIError";
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
