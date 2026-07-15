import type { QuerySerializerOptions } from "openapi-fetch";

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
export function tracedFetch(inner: FetchFn): FetchFn {
  return (input, init) => {
    const headers = new Headers(input instanceof Request ? input.headers : init?.headers);
    if (input instanceof Request && init?.headers) {
      new Headers(init.headers).forEach((value, key) => headers.set(key, value));
    }
    const { traceparent, baggage } = traceHeadersForRequest();
    headers.set("traceparent", traceparent);
    if (baggage !== null) headers.set("baggage", baggage);
    return inner(input, { ...init, headers });
  };
}

export function createRuntimeClient(fetch?: FetchFn, clientBaseURL = baseUrl) {
  const inner = fetch ?? ((...args: Parameters<typeof globalThis.fetch>) => globalThis.fetch(...args));
  return createAPIClient(clientBaseURL, {
    fetch: csrfFetch(tracedFetch(inner)),
    querySerializer,
  });
}

export const client = createRuntimeClient();

export function apiErrorMessage(
  error: Pick<Partial<components["schemas"]["ProblemError"]>, "detail" | "title"> | undefined,
  fallback: string,
): string {
  return error?.detail ?? error?.title ?? fallback;
}
