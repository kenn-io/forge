import type { QuerySerializerOptions } from "openapi-fetch";

import { createAPIClient } from "@middleman/ui/api/client";
import type { components } from "@middleman/ui/api/schema";
import { csrfFetch, type FetchFn } from "@middleman/ui/api/csrf";
import { setUnauthenticated } from "../stores/auth.svelte.js";

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

export function detectUnauthorized(inner: FetchFn): FetchFn {
  return async (input, init) => {
    const response = await inner(input, init);
    if (response.status === 401) setUnauthenticated();
    return response;
  };
}

export function createRuntimeClient(fetch?: FetchFn, clientBaseURL = baseUrl) {
  const inner = fetch ?? ((...args: Parameters<typeof globalThis.fetch>) => globalThis.fetch(...args));
  return createAPIClient(clientBaseURL, {
    fetch: detectUnauthorized(csrfFetch(inner)),
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
