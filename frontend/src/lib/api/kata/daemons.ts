import { Effect } from "effect";
import type { components } from "../generated/schema.js";
import { TransientTransportError } from "../effect-errors.js";
import { GeneratedApi } from "../generated-api.js";
import { configuredAPIPath } from "../runtime-base.js";

export const KATA_DAEMON_HEADER = "X-Kenn-Forge-Kata-Daemon";

export type KataDaemonInfo = components["schemas"]["KataDaemonResponse"];

const KATA_PROXY_ROUTE = "/kata/proxy";

export function kataProxyPath(path: string): string {
  const kataUpstreamAPIRoute = "/api/v1";
  const normalized = path.startsWith("/") ? path : `/${path}`;
  return configuredAPIPath(`${KATA_PROXY_ROUTE}${kataUpstreamAPIRoute}${normalized}`);
}

export const fetchKataDaemons = Effect.fn("KataDaemons.fetch")(function* () {
  const { client } = yield* GeneratedApi;
  const result = yield* Effect.tryPromise({
    try: (signal) => client.GET("/kata/daemons", { signal }),
    catch: (cause) => TransientTransportError.make({ operation: "load Kata daemon roster", cause }),
  }).pipe(Effect.option);
  if (result._tag === "None") return [];
  const { data, response } = result.value;
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
  return daemons;
});
