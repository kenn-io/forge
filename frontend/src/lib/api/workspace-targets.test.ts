import { Deferred, Effect, Fiber } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { AppLiveLayer } from "../app/layer.js";
import { makeAppRuntime } from "../app/runtime.js";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
import { repositoryIdKey } from "./repository-key.js";
import { RepositoryReads } from "./repository-reads.js";
import type { RepoCatalog } from "./types.js";
import { updateWorkspaceTarget, visitWorkspaceTargetReference } from "./workspace-targets.js";

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

it.each([
  {
    provider: "github",
    platformHost: "github.com",
    repoPath: "/api/v1/repo/github/acme/widgets",
    key: { PlatformRepoID: 1001 },
    wire: { platform_repo_id: 1001 },
  },
  {
    provider: "gitlab",
    platformHost: "gitlab.example.com",
    repoPath: "/api/v1/host/gitlab.example.com/repo/gitlab/acme/widgets",
    key: { PlatformRepoID: 1002 },
    wire: { platform_repo_id: 1002 },
  },
  {
    provider: "bitbucket",
    platformHost: "bitbucket.org",
    repoPath: "/api/v1/repo/bitbucket/acme/widgets",
    key: { PlatformRepoID: 0, BitbucketRepositoryUUID: "11111111-2222-3333-4444-555555555555" },
    wire: { platform_repo_id: 0, bitbucket_repository_uuid: "11111111-2222-3333-4444-555555555555" },
  },
])(
  "saves a resolved $provider link after its repository is hidden from the catalog",
  async ({ provider, platformHost, repoPath, key, wire }) => {
    const repo = { ...repository, Platform: provider, PlatformHost: platformHost, ...key };
    let hidden = false;
    const writes: unknown[] = [];
    const resolved: number[] = [];
    const api = createMockApiFetch([
      (request) => {
        if (request.url.pathname === "/api/v1/repos") return Response.json(hidden ? [] : [repo]);
        if (request.url.pathname === `${repoPath}/resolve/7`)
          return Response.json({ item_type: "issue", repo_tracked: true, number: 7 });
        if (request.url.pathname === repoPath) return Response.json(repo);
        if (request.method === "PUT" && request.url.pathname === "/api/v1/workspaces/ws-links/targets") {
          writes.push(JSON.parse(request.bodyText));
          return new Response(null, { status: 204 });
        }
        return null;
      },
    ]);
    vi.spyOn(globalThis, "fetch").mockImplementation(api.fetch);
    await Effect.runPromise(
      Effect.gen(function* () {
        const reads = yield* RepositoryReads;
        yield* reads.refresh;
        hidden = true;
        const result = yield* Effect.exit(
          visitWorkspaceTargetReference("ws-links", undefined, { ...routeItem, provider, platformHost }, (item) =>
            resolved.push(item.number),
          ),
        );
        expect(resolved).toEqual([7]);
        expect(writes).toEqual([
          { repository: { provider, platform_host: platformHost, ...wire }, type: "issue", number: 7, hidden: false },
        ]);
        expect(result._tag).toBe("Success");
      }).pipe(Effect.provide(AppLiveLayer)),
    );
  },
);

it("resolves the stable repository identity for mixed-case clicked links", async () => {
  const writes: unknown[] = [];
  const api = createMockApiFetch([
    (request) => {
      if (request.url.pathname === "/api/v1/repo/github/Acme/Widgets") return Response.json(repository);
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
        const api = createMockApiFetch([
          async (request) => {
            if (request.url.pathname === "/api/v1/repo/github/acme/widgets") {
              Deferred.doneUnsafe(started, Effect.void);
              await release.promise;
              return Response.json(repository);
            }
            if (request.method === "PUT" && request.url.pathname === "/api/v1/workspaces/ws-order/targets") {
              const body = JSON.parse(request.bodyText);
              expect(body.repository.platform_repo_id).toBe(1001);
              writes.push(body.hidden);
              if (!body.hidden && waitingAt === "target write") {
                Deferred.doneUnsafe(started, Effect.void);
                await release.promise;
              }
              persistedHidden = body.hidden;
              return new Response(null, { status: 204 });
            }
            return null;
          },
        ]);
        vi.spyOn(globalThis, "fetch").mockImplementation(api.fetch);
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
        }).pipe(Effect.provide(AppLiveLayer));
      }),
    );
  },
);

it("tracks the current route occupant after a cached repository is renamed and replaced", async () => {
  const identities: number[] = [];
  let routeOccupant = repository;
  const api = createMockApiFetch([
    (request) => {
      if (request.url.pathname === "/api/v1/repos") return Response.json([repository]);
      if (request.url.pathname === "/api/v1/repo/github/acme/widgets") return Response.json(routeOccupant);
      if (request.method === "PUT" && request.url.pathname === "/api/v1/workspaces/ws-links/targets") {
        identities.push(JSON.parse(request.bodyText).repository.platform_repo_id);
        return new Response(null, { status: 204 });
      }
      return null;
    },
  ]);
  vi.spyOn(globalThis, "fetch").mockImplementation(api.fetch);
  await Effect.runPromise(
    Effect.gen(function* () {
      const reads = yield* RepositoryReads;
      yield* reads.refresh;
      routeOccupant = { ...repository, ID: 2, PlatformRepoID: 1002 };
      yield* updateWorkspaceTarget("ws-links", undefined, "issue", routeItem, false);
      expect(identities).toEqual([1002]);
    }).pipe(Effect.provide(AppLiveLayer)),
  );
});
