import { Effect, Schema } from "effect";
import { TransientTransportError } from "../effect-errors.js";
import type { components } from "../generated/schema.js";
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

interface RequestResult {
  body: unknown;
  headers: Headers;
}

interface KataRequestInit {
  method?: string | undefined;
  body?: unknown;
  headers?: Record<string, string> | undefined;
}

interface ErrorEnvelope {
  code: string;
  message: string;
  details?: unknown;
}

const mutationOutcomeUnknownCode: components["schemas"]["ProblemError"]["code"] = "mutationOutcomeUnknown";

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
  return path.startsWith("/") ? path : `/${path}`;
}

interface KataCreateProtocolResponse extends KataTaskMutationResponse {
  issueUID?: string | undefined;
  etag?: string | undefined;
}

function normalizeMutationResponse(raw: unknown): KataTaskMutationResponse {
  const source = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  if (!isObject(source)) throw new Error("mutation response was not an object");
  const hasAcknowledgement =
    typeof source.changed === "boolean" ||
    isObject(source.issue) ||
    isObject(source.comment) ||
    isObject(source.label) ||
    isObject(source.event) ||
    (typeof source.new_short_id === "string" && source.new_short_id !== "");
  if (!hasAcknowledgement) throw new Error("mutation response did not include an acknowledgement");
  return { changed: typeof source.changed === "boolean" ? source.changed : true };
}

function normalizeProjectMutationResponse(raw: unknown): KataTaskMutationResponse {
  const source = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  if (!isObject(source) || !isObject(source.project)) {
    throw new Error("project create response did not include a project");
  }
  if (typeof source.project.uid !== "string" || source.project.uid === "") {
    throw new Error("project create response did not include a project uid");
  }
  return { changed: typeof source.changed === "boolean" ? source.changed : true };
}

function normalizeCreateProtocolResponse(raw: unknown, headers: Headers): KataCreateProtocolResponse {
  const source = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  if (!isObject(source)) throw new Error("issue create response was not an object");
  const issue = isObject(source.issue) ? source.issue : {};
  return {
    changed: typeof source.changed === "boolean" ? source.changed : true,
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

export class KataMutationOutcomeUnknownError extends Schema.TaggedErrorClass<KataMutationOutcomeUnknownError>()(
  "KataMutationOutcomeUnknownError",
  {
    operation: Schema.String,
    message: Schema.String,
    cause: Schema.Defect(),
  },
) {}

export class KataMutationPartiallyAppliedError extends Schema.TaggedErrorClass<KataMutationPartiallyAppliedError>()(
  "KataMutationPartiallyAppliedError",
  {
    operation: Schema.String,
    message: Schema.String,
    issueUID: Schema.String,
    incompleteStep: Schema.Literal("metadata"),
    cause: Schema.Defect(),
  },
) {}

export type KataTaskClientError =
  | KataMutationOutcomeUnknownError
  | KataMutationPartiallyAppliedError
  | KataTaskAPIError
  | TransientTransportError;

function responseError(operation: string, headers: Headers, cause: unknown): KataTaskAPIError {
  return new KataTaskAPIError({
    status: 500,
    code: "invalid_response",
    message: `${operation} returned an invalid response: ${cause instanceof Error ? cause.message : String(cause)}`,
    headers,
  });
}

function mutationOutcomeUnknown(operation: string, cause: unknown): KataMutationOutcomeUnknownError {
  return KataMutationOutcomeUnknownError.make({
    operation,
    message: "Kata could not confirm whether the mutation was applied.",
    cause,
  });
}

function normalizeRecurrenceMutationResponse(raw: unknown, headers: Headers) {
  const body = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  if (
    !isObject(body) ||
    !isObject(body.recurrence) ||
    typeof body.recurrence.uid !== "string" ||
    body.recurrence.uid === ""
  ) {
    throw new Error("recurrence mutation response did not include a recurrence");
  }
  return normalizeKataRecurrenceResponse(raw, headers.get("etag") ?? undefined);
}

export function createKataTaskAPI(options: CreateKataTaskAPIOptions = {}): KataTaskAPI {
  const fetchImpl = options.fetchImpl ?? fetch;

  function pinnedDaemonHeaders(
    options: KataPinnedDaemonOptions,
    headers: Record<string, string> = {},
  ): Record<string, string> {
    return { ...headers, [KATA_DAEMON_HEADER]: options.daemonId };
  }

  const request = Effect.fn("KataTaskClient.request")(function* (path: string, init: KataRequestInit = {}) {
    const method = init.method ?? "GET";
    const operation = `request Kata task ${path}`;
    const { response, text } = yield* Effect.tryPromise({
      try: async (signal) => {
        const headers = new Headers(init.headers);
        const requestInit: RequestInit = {
          method,
          headers,
          signal,
        };
        if (init.body !== undefined) {
          headers.set("Content-Type", "application/json");
          requestInit.body = JSON.stringify(init.body);
        }
        const response = await fetchImpl(kataProxyPath(path), requestInit);
        return { response, text: await response.text() };
      },
      catch: (cause) =>
        method === "GET"
          ? TransientTransportError.make({ operation, cause })
          : mutationOutcomeUnknown(operation, cause),
    });
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
      if (method !== "GET" && envelope.code === mutationOutcomeUnknownCode) {
        return yield* Effect.fail(mutationOutcomeUnknown(operation, new KataTaskAPIError(input)));
      }
      return yield* Effect.fail(
        envelope.code === "revision_conflict" ? new KataTaskRevisionConflictError(input) : new KataTaskAPIError(input),
      );
    }
    return { body, headers: response.headers } satisfies RequestResult;
  });

  function issuePath(target: KataTaskMutationTarget): string {
    return taskPath(`/projects/${target.project_id}/issues/${encodeURIComponent(target.ref)}`);
  }

  const mutate = Effect.fn("KataTaskClient.mutate")(function* (
    path: string,
    body: unknown,
    options: KataPinnedDaemonOptions,
    method = "POST",
    headers: Record<string, string> = {},
  ) {
    const result = yield* request(path, {
      method,
      body,
      headers: pinnedDaemonHeaders(options, headers),
    });
    return yield* Effect.try({
      try: () => normalizeMutationResponse(result.body),
      catch: (cause) => mutationOutcomeUnknown("Kata mutation response", cause),
    });
  });

  function patchMetadata(
    path: string,
    actor: string,
    patch: KataTaskMetadataPatch,
    ifMatch: string,
    options: KataPinnedDaemonOptions,
    idempotencyKey?: string,
  ) {
    return mutate(path, { actor, patch }, options, "PUT", {
      "If-Match": ifMatch,
      ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
    });
  }

  function postRecurrence(path: string, input: KataCreateRecurrenceInput, options: KataPinnedDaemonOptions) {
    return request(path, {
      method: "POST",
      body: input,
      headers: pinnedDaemonHeaders(options),
    }).pipe(
      Effect.flatMap((result) =>
        Effect.try({
          try: () => normalizeRecurrenceMutationResponse(result.body, result.headers),
          catch: (cause) => mutationOutcomeUnknown("Kata recurrence mutation response", cause),
        }),
      ),
    );
  }

  return {
    createProject: Effect.fn("KataTaskClient.createProject")(function* (name, options) {
      const result = yield* request(taskPath("/projects"), {
        method: "POST",
        body: { name },
        headers: pinnedDaemonHeaders(options),
      });
      return yield* Effect.try({
        try: () => normalizeProjectMutationResponse(result.body),
        catch: (cause) => mutationOutcomeUnknown("Kata project create response", cause),
      });
    }),

    createIssue: Effect.fn("KataTaskClient.createIssue")(function* (projectID, actor, draft, options, idempotencyKey) {
      const { metadata, ...createDraft } = draft;
      const result = yield* request(taskPath(`/projects/${projectID}/issues`), {
        method: "POST",
        body: { actor, ...createDraft },
        headers: {
          ...pinnedDaemonHeaders(options),
          ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
        },
      });
      const created = yield* Effect.try({
        try: () => normalizeCreateProtocolResponse(result.body, result.headers),
        catch: (cause) => mutationOutcomeUnknown("Kata issue create response", cause),
      });
      if (!created.issueUID) {
        return yield* Effect.fail(
          mutationOutcomeUnknown("Kata issue create response", new Error("response did not include an issue")),
        );
      }
      if (!metadata || Object.keys(metadata).length === 0) return { changed: created.changed };
      if (!created.etag) {
        return yield* Effect.fail(
          KataMutationPartiallyAppliedError.make({
            operation: "create Kata issue with metadata",
            message: "The Kata issue was created, but its metadata was not applied because no revision was returned.",
            issueUID: created.issueUID,
            incompleteStep: "metadata",
            cause: new Error("response did not include an ETag"),
          }),
        );
      }
      return yield* patchMetadata(
        taskPath(`/projects/${projectID}/issues/${encodeURIComponent(created.issueUID)}/metadata`),
        actor,
        metadata,
        created.etag,
        options,
        idempotencyKey ? `${idempotencyKey}:metadata` : undefined,
      );
    }),

    addComment: (target, actor, body, options) => mutate(`${issuePath(target)}/comments`, { actor, body }, options),
    addLabel: (target, actor, label, options) => mutate(`${issuePath(target)}/labels`, { actor, label }, options),
    removeLabel: (target, actor, label, options) =>
      mutate(
        `${issuePath(target)}/labels/${encodeURIComponent(label)}?actor=${encodeURIComponent(actor)}`,
        undefined,
        options,
        "DELETE",
      ),
    assignOwner: (target, actor, owner, options) =>
      mutate(`${issuePath(target)}/actions/assign`, { actor, owner }, options),
    unassignOwner: (target, actor, options) => mutate(`${issuePath(target)}/actions/unassign`, { actor }, options),
    setPriority: (target, actor, priority, options) =>
      mutate(`${issuePath(target)}/actions/priority`, { actor, priority }, options),
    closeIssue: (target, actor, close, options) =>
      mutate(`${issuePath(target)}/actions/close`, { actor, ...close }, options),
    reopenIssue: (target, actor, options) => mutate(`${issuePath(target)}/actions/reopen`, { actor }, options),
    editIssue: (target, actor, patch, options) => mutate(issuePath(target), { actor, ...patch }, options, "PATCH"),
    patchIssueMetadata: (target, actor, patch, ifMatch, options) =>
      patchMetadata(`${issuePath(target)}/metadata`, actor, patch, ifMatch, options),
    moveIssue: Effect.fn("KataTaskClient.moveIssue")(function* (target, actor, toProjectUID, ifMatch, options) {
      const result = yield* request(`${issuePath(target)}/actions/move`, {
        method: "POST",
        body: { actor, to_project_uid: toProjectUID },
        headers: pinnedDaemonHeaders(options, { "If-Match": ifMatch }),
      });
      return yield* Effect.try({
        try: () => normalizeMutationResponse(result.body),
        catch: (cause) => mutationOutcomeUnknown("Kata move response", cause),
      });
    }),
    recurrences: Effect.fn("KataTaskClient.recurrences")(function* (projectID, options) {
      const result = yield* request(taskPath(`/projects/${projectID}/recurrences`), {
        headers: pinnedDaemonHeaders(options),
      });
      return yield* Effect.try({
        try: () => normalizeKataRecurrences(result.body),
        catch: (cause) => responseError("Kata recurrence list", result.headers, cause),
      });
    }),
    createRecurrence: (projectID, input, options) =>
      postRecurrence(taskPath(`/projects/${projectID}/recurrences`), input, options),
    showRecurrence: Effect.fn("KataTaskClient.showRecurrence")(function* (projectID, recurrenceUID, options) {
      const result = yield* request(
        taskPath(`/projects/${projectID}/recurrences/${encodeURIComponent(recurrenceUID)}`),
        {
          headers: pinnedDaemonHeaders(options),
        },
      );
      return yield* Effect.try({
        try: () => normalizeKataRecurrenceResponse(result.body, result.headers.get("etag") ?? undefined),
        catch: (cause) => responseError("Kata recurrence read", result.headers, cause),
      });
    }),
    patchRecurrence: Effect.fn("KataTaskClient.patchRecurrence")(
      function* (projectID, recurrenceUID, patch, ifMatch, options) {
        const result = yield* request(
          taskPath(`/projects/${projectID}/recurrences/${encodeURIComponent(recurrenceUID)}`),
          {
            method: "PATCH",
            body: patch,
            headers: pinnedDaemonHeaders(options, { "If-Match": ifMatch }),
          },
        );
        return yield* Effect.try({
          try: () => normalizeRecurrenceMutationResponse(result.body, result.headers),
          catch: (cause) => mutationOutcomeUnknown("Kata recurrence patch response", cause),
        });
      },
    ),
    deleteRecurrence: Effect.fn("KataTaskClient.deleteRecurrence")(
      function* (projectID, recurrenceUID, actor, options, ifMatch) {
        yield* request(
          taskPath(
            `/projects/${projectID}/recurrences/${encodeURIComponent(recurrenceUID)}?actor=${encodeURIComponent(actor)}`,
          ),
          {
            method: "DELETE",
            headers: pinnedDaemonHeaders(options, ifMatch ? { "If-Match": ifMatch } : {}),
          },
        );
      },
    ),
  };
}
