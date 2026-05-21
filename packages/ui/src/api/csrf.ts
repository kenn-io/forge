export type FetchFn = typeof globalThis.fetch;

export const MIDDLEMAN_CSRF_HEADER = "X-Middleman-Csrf";

const formURLSearchParamsContentType = "application/x-www-form-urlencoded;charset=UTF-8";

function headersIncludeContentType(headers: HeadersInit | undefined): boolean {
  if (!headers) return false;
  return new Headers(headers).has("Content-Type");
}

function isURLSearchParamsBody(body: unknown): body is URLSearchParams {
  return body instanceof URLSearchParams || Object.prototype.toString.call(body) === "[object URLSearchParams]";
}

function isGeneratedNonJSONBody(body: BodyInit | null | undefined): boolean {
  if (body == null || typeof body === "string") return false;
  if (body instanceof FormData) return true;
  if (isURLSearchParamsBody(body)) return true;
  if (body instanceof Blob) return true;
  if (body instanceof ArrayBuffer) return true;
  if (ArrayBuffer.isView(body)) return true;
  if (typeof ReadableStream !== "undefined" && body instanceof ReadableStream) return true;
  return false;
}

function normalizeRequestInit(init: RequestInit | undefined): RequestInit | undefined {
  if (!init || !isURLSearchParamsBody(init.body)) return init;
  const headers = new Headers(init.headers);
  if (!headers.has("Content-Type")) {
    headers.set("Content-Type", formURLSearchParamsContentType);
  }
  return { ...init, body: init.body.toString(), headers };
}

function shouldDefaultContentTypeToJSON(init: RequestInit | undefined, request: Request): boolean {
  if (headersIncludeContentType(init?.headers)) return false;
  if (isGeneratedNonJSONBody(init?.body)) return false;
  const contentType = request.headers.get("Content-Type");
  return contentType === null || contentType.toLowerCase().startsWith("text/plain");
}

export function csrfFetch(inner: FetchFn): FetchFn {
  return (input, init) => {
    const requestInit = normalizeRequestInit(init);
    const request = new Request(input, requestInit);
    const method = request.method.toUpperCase();
    if (method !== "GET" && method !== "HEAD") {
      const defaultToJSON = shouldDefaultContentTypeToJSON(requestInit, request);
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
