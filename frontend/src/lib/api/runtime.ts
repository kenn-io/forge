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
  return (input, init) => {
    const response = inner(input, init);
    // Observe the status without inserting an await between the caller
    // and the response: an extra microtask here delays every API
    // response tick and perturbs response-ordering-sensitive callers.
    void response.then(
      (r) => {
        // Only middleman's own auth gate should raise the login
        // overlay. Proxied upstreams (kata, msgvault, fleet peers) can
        // also 401, and treating those as a lost local session would
        // trap an authenticated user behind a login they cannot fix.
        // The gate marks its challenges with this realm.
        if (r.status === 401 && r.headers.get("WWW-Authenticate")?.includes('realm="middleman"')) {
          setUnauthenticated();
        }
      },
      () => {},
    );
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
