import { KATA_DAEMON_HEADER, kataProxyPath } from "./daemons.js";
import { normalizeKataRecurrenceResponse, normalizeKataRecurrences } from "./taskNormalizers.js";
import type {
  KataCreateRecurrenceInput,
  KataPinnedDaemonOptions,
  KataTaskAPI,
  KataTaskMetadataPatch,
  KataTaskMutationResponse,
  KataTaskMutationTarget,
} from "./taskTypes.js";

export interface CreateKataTaskAPIOptions {
  fetchImpl?: typeof fetch | undefined;
}

interface RequestResult<T> {
  body: T;
  headers: Headers;
}

// The explicit `| undefined` unions are required by
// exactOptionalPropertyTypes: call sites pass values that may be
// undefined (e.g. daemonHeaders() or an optional caller signal).
interface KataRequestInit {
  method?: string | undefined;
  body?: unknown;
  headers?: Record<string, string> | undefined;
  signal?: AbortSignal | undefined;
}

interface ErrorEnvelope {
  code: string;
  message: string;
  details?: unknown;
}

const KATA_TASK_API_PREFIX = "/api" + "/v1";

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parseErrorEnvelope(body: unknown, status: number): ErrorEnvelope {
  const source = isObject(body) && isObject(body.error) ? body.error : body;
  if (isObject(source)) {
    const code = typeof source.code === "string" ? source.code : `http_${status}`;
    const message =
      typeof source.message === "string"
        ? source.message
        : typeof source.detail === "string"
          ? source.detail
          : typeof source.title === "string"
            ? source.title
            : `HTTP ${status}`;
    return { code, message, details: source.details };
  }
  return { code: `http_${status}`, message: `HTTP ${status}` };
}

function taskPath(path: string): string {
  return `${KATA_TASK_API_PREFIX}${path}`;
}

interface KataCreateProtocolResponse extends KataTaskMutationResponse {
  issueUID?: string | undefined;
  etag?: string | undefined;
}

function normalizeMutationResponse(raw: unknown): KataTaskMutationResponse {
  const source = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  const body = isObject(source) ? source : {};
  return { changed: body.changed === true };
}

function normalizeCreateProtocolResponse(raw: unknown, headers: Headers): KataCreateProtocolResponse {
  const source = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  const body = isObject(source) ? source : {};
  const issue = isObject(body.issue) ? body.issue : {};
  return {
    changed: body.changed === true,
    issueUID: typeof issue.uid === "string" && issue.uid !== "" ? issue.uid : undefined,
    etag: headers.get("etag") ?? undefined,
  };
}

export class KataTaskAPIError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details?: unknown;
  readonly headers: Headers;

  constructor(input: { status: number; code: string; message: string; details?: unknown; headers: Headers }) {
    super(input.message);
    this.name = "KataTaskAPIError";
    this.status = input.status;
    this.code = input.code;
    this.details = input.details;
    this.headers = input.headers;
  }
}

export class KataTaskRevisionConflictError extends KataTaskAPIError {
  constructor(input: { status: number; code: string; message: string; details?: unknown; headers: Headers }) {
    super(input);
    this.name = "KataTaskRevisionConflictError";
  }
}

export function createKataTaskAPI(options: CreateKataTaskAPIOptions = {}): KataTaskAPI {
  const fetchImpl = options.fetchImpl ?? fetch;
  let api: KataTaskAPI;

  function pinnedDaemonHeaders(
    options: KataPinnedDaemonOptions,
    headers: Record<string, string> = {},
  ): Record<string, string> {
    return { ...headers, [KATA_DAEMON_HEADER]: options.daemonId };
  }

  async function request<T>(path: string, init: KataRequestInit = {}): Promise<RequestResult<T>> {
    const headers = new Headers(init.headers);
    const requestInit: RequestInit = {
      method: init.method ?? "GET",
      headers,
    };
    if (init.signal) {
      requestInit.signal = init.signal;
    }
    if (init.body !== undefined) {
      headers.set("Content-Type", "application/json");
      requestInit.body = JSON.stringify(init.body);
    }

    const response = await fetchImpl(kataProxyPath(path), requestInit);
    const text = await response.text();
    let body: unknown = {};
    if (text.trim() !== "") {
      try {
        body = JSON.parse(text);
      } catch {
        body = text;
      }
    }

    if (!response.ok) {
      const envelope = parseErrorEnvelope(body, response.status);
      const input = {
        status: response.status,
        code: envelope.code,
        message: envelope.message,
        details: envelope.details,
        headers: response.headers,
      };
      if (envelope.code === "revision_conflict") {
        throw new KataTaskRevisionConflictError(input);
      }
      throw new KataTaskAPIError(input);
    }

    return { body: body as T, headers: response.headers };
  }

  function issuePath(target: KataTaskMutationTarget): string {
    return taskPath(`/projects/${target.project_id}/issues/${encodeURIComponent(target.ref)}`);
  }

  async function mutate(
    path: string,
    body: unknown,
    options: KataPinnedDaemonOptions,
    method = "POST",
    headers: Record<string, string> = {},
  ): Promise<KataTaskMutationResponse> {
    const result = await request<unknown>(path, {
      method,
      body,
      headers: pinnedDaemonHeaders(options, headers),
    });
    return normalizeMutationResponse(result.body);
  }

  function patchMetadata(
    path: string,
    actor: string,
    patch: KataTaskMetadataPatch,
    ifMatch: string,
    options: KataPinnedDaemonOptions,
    idempotencyKey?: string,
  ): Promise<KataTaskMutationResponse> {
    return mutate(path, { actor, patch }, options, "PUT", {
      "If-Match": ifMatch,
      ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
    });
  }

  async function postRecurrence(path: string, input: KataCreateRecurrenceInput, options: KataPinnedDaemonOptions) {
    const result = await request<unknown>(path, {
      method: "POST",
      body: input,
      headers: pinnedDaemonHeaders(options),
    });
    return normalizeKataRecurrenceResponse(result.body, result.headers.get("etag") ?? undefined);
  }

  api = {
    async createProject(name, options) {
      await request<unknown>(taskPath("/projects"), {
        method: "POST",
        body: { name },
        headers: pinnedDaemonHeaders(options),
      });
      return { changed: true };
    },

    async createIssue(projectID, actor, draft, options, idempotencyKey) {
      const { metadata, ...createDraft } = draft;
      const result = await request<unknown>(taskPath(`/projects/${projectID}/issues`), {
        method: "POST",
        body: { actor, ...createDraft },
        headers: {
          ...pinnedDaemonHeaders(options),
          ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
        },
      });
      const created = normalizeCreateProtocolResponse(result.body, result.headers);
      if (!metadata || Object.keys(metadata).length === 0) return { changed: created.changed };
      if (!created.issueUID) {
        throw new KataTaskAPIError({
          status: 500,
          code: "invalid_issue_response",
          message: "issue create response did not include an issue",
          headers: result.headers,
        });
      }
      if (!created.etag) {
        throw new KataTaskAPIError({
          status: 409,
          code: "mutation_precondition_unavailable",
          message: "issue create response did not include an ETag for metadata update",
          headers: result.headers,
        });
      }
      return patchMetadata(
        taskPath(`/projects/${projectID}/issues/${encodeURIComponent(created.issueUID)}/metadata`),
        actor,
        metadata,
        created.etag,
        options,
        idempotencyKey ? `${idempotencyKey}:metadata` : undefined,
      );
    },

    addComment(target, actor, body, options) {
      return mutate(`${issuePath(target)}/comments`, { actor, body }, options);
    },

    addLabel(target, actor, label, options) {
      return mutate(`${issuePath(target)}/labels`, { actor, label }, options);
    },

    removeLabel(target, actor, label, options) {
      const path = `${issuePath(target)}/labels/${encodeURIComponent(label)}?actor=${encodeURIComponent(actor)}`;
      return mutate(path, undefined, options, "DELETE");
    },

    assignOwner(target, actor, owner, options) {
      return mutate(`${issuePath(target)}/actions/assign`, { actor, owner }, options);
    },

    unassignOwner(target, actor, options) {
      return mutate(`${issuePath(target)}/actions/unassign`, { actor }, options);
    },

    setPriority(target, actor, priority, options) {
      return mutate(`${issuePath(target)}/actions/priority`, { actor, priority }, options);
    },

    closeIssue(target, actor, close, options) {
      return mutate(`${issuePath(target)}/actions/close`, { actor, ...close }, options);
    },

    reopenIssue(target, actor, options) {
      return mutate(`${issuePath(target)}/actions/reopen`, { actor }, options);
    },

    editIssue(target, actor, patch, options) {
      return mutate(issuePath(target), { actor, ...patch }, options, "PATCH");
    },

    patchIssueMetadata(target, actor, patch, ifMatch, options) {
      return patchMetadata(`${issuePath(target)}/metadata`, actor, patch, ifMatch, options);
    },

    async moveIssue(target, actor, toProjectUID, ifMatch, options) {
      const result = await request<unknown>(`${issuePath(target)}/actions/move`, {
        method: "POST",
        body: { actor, to_project_uid: toProjectUID },
        headers: pinnedDaemonHeaders(options, { "If-Match": ifMatch }),
      });
      return normalizeMutationResponse(result.body);
    },

    async recurrences(projectID, options) {
      const result = await request<unknown>(taskPath(`/projects/${projectID}/recurrences`), {
        headers: pinnedDaemonHeaders(options),
        signal: options.signal,
      });
      return normalizeKataRecurrences(result.body);
    },

    createRecurrence(projectID, input, options) {
      return postRecurrence(taskPath(`/projects/${projectID}/recurrences`), input, options);
    },

    async showRecurrence(projectID, recurrenceUID, options) {
      const result = await request<unknown>(
        taskPath(`/projects/${projectID}/recurrences/${encodeURIComponent(recurrenceUID)}`),
        { headers: pinnedDaemonHeaders(options) },
      );
      return normalizeKataRecurrenceResponse(result.body, result.headers.get("etag") ?? undefined);
    },

    async patchRecurrence(projectID, recurrenceUID, patch, ifMatch, options) {
      const result = await request<unknown>(
        taskPath(`/projects/${projectID}/recurrences/${encodeURIComponent(recurrenceUID)}`),
        {
          method: "PATCH",
          body: patch,
          headers: pinnedDaemonHeaders(options, { "If-Match": ifMatch }),
        },
      );
      return normalizeKataRecurrenceResponse(result.body, result.headers.get("etag") ?? undefined);
    },

    async deleteRecurrence(projectID, recurrenceUID, actor, options, ifMatch) {
      await request<unknown>(
        taskPath(
          `/projects/${projectID}/recurrences/${encodeURIComponent(recurrenceUID)}?actor=${encodeURIComponent(actor)}`,
        ),
        {
          method: "DELETE",
          headers: pinnedDaemonHeaders(options, ifMatch ? { "If-Match": ifMatch } : {}),
        },
      );
    },
  };

  return api;
}
