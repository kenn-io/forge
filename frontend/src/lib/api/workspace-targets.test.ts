import { Deferred, Effect, Fiber } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { makeAppLiveLayer } from "../app/layer.js";
import { makeAppRuntime } from "../app/runtime.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
import { makeGeneratedApiLayer } from "./generated-api.js";
import { repositoryIdKey } from "./repository-key.js";
import { RepositoryReads } from "./repository-reads.js";
import type { RepoCatalog } from "./types.js";
import { updateWorkspaceTarget } from "./workspace-targets.js";

const repository: RepoCatalog = {
  ID: 1,
  PlatformRepoID: 1001,
  Owner: "acme",
  Name: "widgets",
  Platform: "github",
  PlatformHost: "github.com",
  AllowSquashMerge: true,
  AllowMergeCommit: true,
  AllowRebaseMerge: true,
  ViewerCanMerge: true,
  LastSyncStartedAt: null,
  LastSyncCompletedAt: null,
  LastSyncError: "",
  CreatedAt: "2026-03-01T00:00:00Z",
};

const routeItem = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 7,
};

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

it.each(["repository lookup", "target write"])(
  "keeps a later removal hidden when the visit waits on %s",
  async (waitingAt) => {
    await Effect.runPromise(
      Effect.gen(function* () {
        const started = yield* Deferred.make<void>();
        const release = Promise.withResolvers<void>();
        const writes: boolean[] = [];
        let persistedHidden = false;
        const client = makeGeneratedClient({
          RepositoriesService: {
            listRepos: async () => {
              Deferred.doneUnsafe(started, Effect.void);
              await release.promise;
              return [repository];
            },
          },
          WorkspacesService: {
            updateWorkspaceTarget: async (params, body) => {
              expect(params).toEqual({ id: "ws-order" });
              expect(body.repository.platform_repo_id).toBe(1001);
              writes.push(body.hidden);
              if (!body.hidden && waitingAt === "target write") {
                Deferred.doneUnsafe(started, Effect.void);
                await release.promise;
              }
              persistedHidden = body.hidden;
            },
          },
        });
        yield* Effect.gen(function* () {
          const keyedItem = { ...routeItem, repositoryKey: repositoryIdKey(1001) };
          const visit = yield* Effect.forkChild(
            updateWorkspaceTarget(
              "ws-order",
              undefined,
              "issue",
              waitingAt === "repository lookup" ? routeItem : keyedItem,
              false,
            ),
          );
          yield* Deferred.await(started);
          const removal = yield* Effect.forkChild(
            updateWorkspaceTarget("ws-order", undefined, "issue", keyedItem, true),
          );
          yield* Effect.yieldNow;
          const writesBeforeRelease = [...writes];
          release.resolve();
          yield* Fiber.join(visit);
          yield* Fiber.join(removal);
          expect(persistedHidden).toBe(true);
          expect(writes).toEqual([false, true]);
          expect(writesBeforeRelease).toEqual(waitingAt === "repository lookup" ? [] : [false]);
        }).pipe(Effect.provide(makeAppLiveLayer(makeGeneratedApiLayer(client))));
      }),
    );
  },
);

it("tracks the current route occupant after a cached repository is renamed and replaced", async () => {
  const identities: number[] = [];
  let catalog = [repository];
  const client = makeGeneratedClient({
    RepositoriesService: { listRepos: async () => catalog },
    WorkspacesService: {
      updateWorkspaceTarget: async (_params, body) => {
        identities.push(body.repository.platform_repo_id);
      },
    },
  });
  await Effect.runPromise(
    Effect.gen(function* () {
      const reads = yield* RepositoryReads;
      yield* reads.refresh;
      catalog = [
        { ...repository, Name: "renamed-widgets" },
        { ...repository, ID: 2, PlatformRepoID: 1002 },
      ];
      yield* updateWorkspaceTarget("ws-links", undefined, "issue", routeItem, false);
      expect(identities).toEqual([1002]);
    }).pipe(Effect.provide(makeAppLiveLayer(makeGeneratedApiLayer(client)))),
  );
});
