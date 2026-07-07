import type { components } from "@middleman/ui/api/schema";

import { createRuntimeClient } from "../runtime.js";

export const KATA_DAEMON_HEADER = "X-Middleman-Kata-Daemon";

export type KataDaemonInfo = components["schemas"]["KataDaemonResponse"];

const API_PREFIX = "/api" + "/v1";
const KATA_PROXY_ROUTE = `${API_PREFIX}/kata/proxy`;
const KATA_TASKS_ROUTE = `${API_PREFIX}/kata/tasks`;

function appPath(path: string): string {
  const basePath = typeof window !== "undefined" ? (window.__BASE_PATH__ ?? "/") : "/";
  const prefix = basePath === "/" ? "" : basePath.replace(/\/$/, "");
  return `${prefix}${path}`;
}

function apiBaseURL(): string {
  const origin = typeof window !== "undefined" ? window.location.origin : "http://localhost";
  return new URL(appPath(API_PREFIX), origin).toString();
}

export function kataProxyPath(path: string): string {
  const normalized = path.startsWith("/") ? path : `/${path}`;
  return appPath(`${KATA_PROXY_ROUTE}${normalized}`);
}

// Path of middleman's combined task-detail read: daemon detail enriched with
// the workspace target, served by middleman itself rather than the proxy.
export function kataTaskDetailPath(uid: string): string {
  return appPath(`${KATA_TASKS_ROUTE}/${encodeURIComponent(uid)}`);
}

// Daemon-scoped requests carry the daemon selection header: everything under
// the passthrough proxy, plus middleman's own daemon-backed task reads.
function isSameOriginKataDaemonRequest(input: RequestInfo | URL): boolean {
  const origin = globalThis.location?.origin;
  if (!origin) return true;
  let raw: string;
  if (typeof input === "string") raw = input;
  else if (input instanceof URL) raw = input.href;
  else if (typeof Request !== "undefined" && input instanceof Request) raw = input.url;
  else return true;

  try {
    const url = new URL(raw, origin);
    if (url.origin !== origin) return false;
    return (
      url.pathname.startsWith(appPath(`${KATA_PROXY_ROUTE}/`)) ||
      url.pathname.startsWith(appPath(`${KATA_TASKS_ROUTE}/`))
    );
  } catch {
    return false;
  }
}

export async function fetchKataDaemons(fetchImpl: typeof fetch = fetch): Promise<KataDaemonInfo[]> {
  let data: components["schemas"]["KataDaemonRosterResponse"] | undefined;
  let response: Response;
  try {
    const result = await createRuntimeClient(fetchImpl, apiBaseURL()).GET("/kata/daemons");
    data = result.data;
    response = result.response;
  } catch {
    return [];
  }
  if (!response.ok) {
    if (response.status !== 404) {
      console.warn(`fetchKataDaemons: daemon roster returned ${response.status}`);
    }
    return [];
  }
  if (!data || typeof data !== "object") {
    console.warn("fetchKataDaemons: malformed daemon roster response");
    return [];
  }
  const daemons = data.daemons;
  if (!Array.isArray(daemons)) {
    console.warn("fetchKataDaemons: malformed daemon roster response");
    return [];
  }
  return daemons as KataDaemonInfo[];
}

export function withKataDaemon(fetchImpl: typeof fetch, getId: () => string | undefined): typeof fetch {
  return (input: RequestInfo | URL, init?: RequestInit) => {
    const headers = new Headers(typeof Request !== "undefined" && input instanceof Request ? input.headers : undefined);
    if (init?.headers) {
      new Headers(init.headers).forEach((value, key) => headers.set(key, value));
    }

    if (!isSameOriginKataDaemonRequest(input)) {
      if (!headers.has(KATA_DAEMON_HEADER)) return fetchImpl(input, init);
      headers.delete(KATA_DAEMON_HEADER);
      return fetchImpl(input, { ...init, headers });
    }

    const id = getId();
    if (!id || headers.has(KATA_DAEMON_HEADER)) {
      return fetchImpl(input, init);
    }
    headers.set(KATA_DAEMON_HEADER, id);
    return fetchImpl(input, { ...init, headers });
  };
}
