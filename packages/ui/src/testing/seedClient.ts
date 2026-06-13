import { inject } from "vite-plus/test";

// One seeded e2e Go server (cmd/e2e-server) is spawned for the whole Vitest
// run by frontend/src/lib/testing/seedServer.globalSetup.ts, which provides
// its base URL here. These helpers fetch real responses from it so component
// tests assert against the exact shapes the app sees in production, instead of
// checked-in JSON that silently drifts from the server. The server is seeded
// deterministically (acme/widgets, acme/tools, the GitLab read-only repo,
// etc.), so value assertions stay stable across runs.

function seedBaseUrl(): string {
  // inject's key type is ProvidedContext, which we deliberately do not augment
  // across the frontend and packages/ui tsconfigs; the local cast keeps this
  // helper self-contained.
  return (inject as (key: string) => string)("seedBaseUrl");
}

// The server stamps a `$schema` URL (with its ephemeral port) onto detail-style
// responses; strip it so fetched data matches what components receive in the
// app and stays port-independent.
function stripSchema<T>(value: T): T {
  if (Array.isArray(value)) {
    return value.map((item) => stripSchema(item)) as unknown as T;
  }
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, val] of Object.entries(value as Record<string, unknown>)) {
      if (key === "$schema") continue;
      out[key] = stripSchema(val);
    }
    return out as T;
  }
  return value;
}

export async function seedGet<T>(path: string): Promise<T> {
  const response = await fetch(`${seedBaseUrl()}${path}`);
  if (!response.ok) {
    throw new Error(`seed GET ${path} -> ${response.status}`);
  }
  return stripSchema(await response.json()) as T;
}
