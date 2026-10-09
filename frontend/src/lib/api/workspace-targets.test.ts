import { Effect } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../app/runtime.js";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
import { updateWorkspaceTarget } from "./workspace-targets.js";

afterEach(() => {
  vi.restoreAllMocks();
});

it("resolves the stable repository identity for mixed-case clicked links", async () => {
  const writes: unknown[] = [];
  const api = createMockApiFetch([
    (request) => {
      if (request.method === "PUT" && request.url.pathname === "/api/v1/workspaces/ws-links/targets") {
        writes.push(JSON.parse(request.bodyText));
        return new Response(null, { status: 204 });
      }
      return null;
    },
  ]);
  vi.spyOn(globalThis, "fetch").mockImplementation(api.fetch);
  const runtime = makeAppRuntime();
  try {
    await runtime.runCommand(
      updateWorkspaceTarget(
        "ws-links",
        undefined,
        "issue",
        {
          provider: "github",
          platformHost: "github.com",
          owner: "Acme",
          name: "Widgets",
          repoPath: "Acme/Widgets",
          number: 7,
        },
        false,
      ),
      { operation: "test visit", safeContext: {}, onFailure: () => {} },
    ).exit;
    expect(writes).toEqual([
      {
        repository: { provider: "github", platform_host: "github.com", platform_repo_id: 1001 },
        type: "issue",
        number: 7,
        hidden: false,
      },
    ]);
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
  }
});
