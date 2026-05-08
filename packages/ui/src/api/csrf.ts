export type FetchFn = typeof globalThis.fetch;

export const MIDDLEMAN_CSRF_HEADER = "X-Middleman-Csrf";

function headersIncludeContentType(headers: HeadersInit | undefined): boolean {
  if (!headers) return false;
  return new Headers(headers).has("Content-Type");
}

function isGeneratedNonJSONBody(body: BodyInit | null | undefined): boolean {
  if (body == null || typeof body === "string") return false;
  if (body instanceof FormData) return true;
  if (body instanceof URLSearchParams) return true;
  if (body instanceof Blob) return true;
  if (body instanceof ArrayBuffer) return true;
  if (ArrayBuffer.isView(body)) return true;
  if (typeof ReadableStream !== "undefined" && body instanceof ReadableStream) return true;
  return false;
}

function shouldDefaultContentTypeToJSON(init: RequestInit | undefined, request: Request): boolean {
  if (headersIncludeContentType(init?.headers)) return false;
  if (isGeneratedNonJSONBody(init?.body)) return false;
  const contentType = request.headers.get("Content-Type");
  return contentType === null || contentType.toLowerCase().startsWith("text/plain");
}

export function csrfFetch(inner: FetchFn): FetchFn {
  return (input, init) => {
    const request = new Request(input, init);
    const method = request.method.toUpperCase();
    if (method !== "GET" && method !== "HEAD") {
      const defaultToJSON = shouldDefaultContentTypeToJSON(init, request);
      if (defaultToJSON || !request.headers.has(MIDDLEMAN_CSRF_HEADER)) {
        const headers = new Headers(request.headers);
        if (defaultToJSON) {
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
