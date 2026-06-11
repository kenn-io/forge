export type FetchFn = typeof globalThis.fetch;

export const MIDDLEMAN_CSRF_HEADER = "X-Middleman-Csrf";

export function csrfFetch(inner: FetchFn): FetchFn {
  return (input, init) => {
    const request = new Request(input, init);
    const method = request.method.toUpperCase();
    if (method !== "GET" && method !== "HEAD") {
      if (!request.headers.has("Content-Type") || !request.headers.has(MIDDLEMAN_CSRF_HEADER)) {
        const headers = new Headers(request.headers);
        if (!headers.has("Content-Type")) {
          headers.set("Content-Type", "application/json");
        }
        if (!headers.has(MIDDLEMAN_CSRF_HEADER)) {
          headers.set(MIDDLEMAN_CSRF_HEADER, "1");
        }
        return inner(new Request(request, { headers }));
      }
    }
    return inner(request);
  };
}
