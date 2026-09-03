export type FetchFn = typeof globalThis.fetch;

const formURLSearchParamsContentType = "application/x-www-form-urlencoded;charset=UTF-8";

function isURLSearchParamsBody(body: unknown): body is URLSearchParams {
  return body instanceof URLSearchParams || Object.prototype.toString.call(body) === "[object URLSearchParams]";
}

function normalizeRequestInit(init: RequestInit | undefined): RequestInit | undefined {
  if (!init || !isURLSearchParamsBody(init.body)) return init;
  const headers = new Headers(init.headers);
  if (!headers.has("Content-Type")) {
    headers.set("Content-Type", formURLSearchParamsContentType);
  }
  return { ...init, body: init.body.toString(), headers };
}

export function normalizedFetch(inner: FetchFn): FetchFn {
  return (input, init) => inner(new Request(input, normalizeRequestInit(init)));
}
