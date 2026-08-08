import { configuredAPIBaseURL, configuredAPIPath } from "../runtime-base.js";
import type { components } from "../generated/schema.js";

import { createRuntimeClient } from "../runtime.js";

export const KATA_DAEMON_HEADER = "X-Kenn-Forge-Kata-Daemon";

export type KataDaemonInfo = components["schemas"]["KataDaemonResponse"];

const KATA_PROXY_ROUTE = "/kata/proxy";

function apiBaseURL(): string {
  return configuredAPIBaseURL();
}

export function kataProxyPath(path: string): string {
  const kataUpstreamAPIRoute = "/api/v1";
  const normalized = path.startsWith("/") ? path : `/${path}`;
  return configuredAPIPath(`${KATA_PROXY_ROUTE}${kataUpstreamAPIRoute}${normalized}`);
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
