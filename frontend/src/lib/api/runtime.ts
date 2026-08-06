import type { QuerySerializerOptions } from "openapi-fetch";

import { createAPIClient } from "./generated/client.js";
import { configuredAPIBaseURL } from "./runtime-base.js";
import type { components } from "./generated/schema.js";
import { csrfFetch, type FetchFn } from "./csrf.js";

import { traceHeadersForRequest } from "../instrumentation/traceContext.js";

const baseUrl = configuredAPIBaseURL();

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
