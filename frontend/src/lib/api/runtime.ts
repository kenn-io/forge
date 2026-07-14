import type { Middleware, QuerySerializerOptions } from "openapi-fetch";

import { createAPIClient } from "@middleman/ui/api/client";
import type { components } from "@middleman/ui/api/schema";
import { csrfFetch, type FetchFn } from "@middleman/ui/api/csrf";

import { traceHeadersForRequest } from "../instrumentation/traceContext.js";

const basePath = typeof window !== "undefined" ? (window.__BASE_PATH__ ?? "/") : "/";
const baseUrl =
  typeof window !== "undefined"
    ? new URL(`${basePath.replace(/\/$/, "")}/api/v1`, window.location.origin).toString()
    : "http://localhost/api/v1";

export const apiBaseURL = baseUrl;

export const querySerializer: QuerySerializerOptions = {
  array: {
    style: "form",
    explode: false,
  },
};

// Attaches W3C trace context to every request so server spans join the
// frontend's minted traces (see frontend/src/lib/instrumentation/traceContext.ts).
const traceMiddleware: Middleware = {
  onRequest({ request }) {
    const { traceparent, baggage } = traceHeadersForRequest();
    request.headers.set("traceparent", traceparent);
    if (baggage !== null) request.headers.set("baggage", baggage);
    return request;
  },
};

export function createRuntimeClient(fetch?: FetchFn, clientBaseURL = baseUrl) {
  const inner = fetch ?? ((...args: Parameters<typeof globalThis.fetch>) => globalThis.fetch(...args));
  const apiClient = createAPIClient(clientBaseURL, {
    fetch: csrfFetch(inner),
    querySerializer,
  });
  apiClient.use(traceMiddleware);
  return apiClient;
}

export const client = createRuntimeClient();

export function apiErrorMessage(
  error: Pick<Partial<components["schemas"]["ProblemError"]>, "detail" | "title"> | undefined,
  fallback: string,
): string {
  return error?.detail ?? error?.title ?? fallback;
}
