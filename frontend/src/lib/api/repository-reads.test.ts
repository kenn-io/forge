import { Effect, Fiber, Option, Stream } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { AppLiveLayer } from "../app/layer.js";
import { createMockApiFetch, jsonResponse } from "../../test/mockApiFetch.js";
import { executeGeneratedApiRequest } from "./generated-api.js";
import { repositoryIdKey, repositoryUUIDKey } from "./repository-key.js";
import { RepositoryReads } from "./repository-reads.js";

afterEach(() => vi.restoreAllMocks());

it.each([
  { provider: "github", host: "github.com", id: 1001, uuid: "", key: repositoryIdKey(1001) },
  {
    provider: "bitbucket",
    host: "bitbucket.org",
    id: 0,
    uuid: "11111111-2222-3333-4444-555555555555",
    key: repositoryUUIDKey("11111111-2222-3333-4444-555555555555"),
  },
])(
  "keeps $provider repository previews within their verified identity and route",
  async ({ provider, host, id, uuid, key }) => {
    vi.spyOn(globalThis, "fetch").mockImplementation(createMockApiFetch().fetch);
    await Effect.runPromise(
      Effect.gen(function* () {
        const reads = yield* RepositoryReads;
        const repo = yield* executeGeneratedApiRequest("read fixture repository", (client, signal) =>
          client.RepositoriesService.getRepo({ provider: "github", owner: "acme", name: "widgets" }, { signal }),
        );
        reads.rememberDetail({
          ...repo,
          Platform: provider,
          PlatformHost: host,
          PlatformRepoID: id,
          BitbucketRepositoryUUID: uuid,
        });
        const ref = {
          provider,
          platformHost: host,
          repositoryKey: key,
          owner: "acme",
          name: "widgets",
          repoPath: "acme/widgets",
        };
        expect(reads.detailSnapshot(ref)?.AllowSquashMerge).toBe(true);
        expect(reads.detailSnapshot({ ...ref, repositoryKey: undefined })).toBeUndefined();
        expect(reads.detailSnapshot({ ...ref, repositoryKey: repositoryIdKey(2002) })).toBeUndefined();
        expect(reads.detailSnapshot({ ...ref, platformHost: "other.example.com" })).toBeUndefined();
        expect(reads.detailSnapshot({ ...ref, provider: "gitlab" })).toBeUndefined();
        expect(reads.detailSnapshot({ ...ref, repoPath: "acme/renamed" })).toBeUndefined();
      }).pipe(Effect.provide(AppLiveLayer)),
    );
  },
);

it("keeps a fresh route observation when an older catalog request finishes later", async () => {
  const started = Promise.withResolvers<void>();
  const release = Promise.withResolvers<void>();
  const api = createMockApiFetch([
    async ({ url }) => {
      if (url.pathname !== "/api/v1/repos") return null;
      started.resolve();
      await release.promise;
      return jsonResponse([
        { Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001, Owner: "acme", Name: "widgets" },
      ]);
    },
  ]);
  vi.spyOn(globalThis, "fetch").mockImplementation(api.fetch);
  await Effect.runPromise(
    Effect.gen(function* () {
      const reads = yield* RepositoryReads;
      const pending = yield* reads.refresh.pipe(Effect.forkChild);
      yield* Effect.promise(() => started.promise);
      yield* reads.observeRoute({
        provider: "github",
        platformHost: "github.com",
        repositoryKey: repositoryIdKey(2002),
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
      });
      release.resolve();
      yield* Fiber.join(pending);
      const current = yield* Stream.runHead(reads.identities);
      expect(Option.getOrThrow(current)).toEqual([
        {
          provider: "github",
          platformHost: "github.com",
          repositoryKey: { kind: "id", id: 2002 },
          fromDetail: true,
          owner: "acme",
          name: "widgets",
          repoPath: "acme/widgets",
        },
      ]);
    }).pipe(Effect.provide(AppLiveLayer)),
  );
});

it("retains detail identities omitted by a later catalog read", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(
    createMockApiFetch([({ url }) => (url.pathname === "/api/v1/repos" ? jsonResponse([]) : null)]).fetch,
  );
  await Effect.runPromise(
    Effect.gen(function* () {
      const reads = yield* RepositoryReads;
      const ref = {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
        repositoryKey: repositoryIdKey(1001),
      };
      yield* reads.observeRoute(ref);
      yield* reads.refresh;
      const current = yield* Stream.runHead(reads.identities);
      expect(Option.getOrThrow(current)).toEqual([expect.objectContaining(ref)]);
    }).pipe(Effect.provide(AppLiveLayer)),
  );
});

it.each([false, true])(
  "merges unrelated catalog routes during an observation (previous identity: %s)",
  async (known) => {
    const started = Promise.withResolvers<void>();
    const release = Promise.withResolvers<void>();
    const api = createMockApiFetch([
      ({ url }) =>
        url.pathname === "/api/v1/repos"
          ? jsonResponse([
              { Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001, Owner: "acme", Name: "widgets" },
              { Platform: "github", PlatformHost: "github.com", PlatformRepoID: 3003, Owner: "acme", Name: "tools" },
            ])
          : null,
    ]);
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const response = await api.fetch(input, init);
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
      if (url.pathname === "/api/v1/repos") {
        started.resolve();
        await release.promise;
      }
      return response;
    });
    await Effect.runPromise(
      Effect.gen(function* () {
        const reads = yield* RepositoryReads;
        const ref = {
          provider: "github",
          platformHost: "github.com",
          owner: "acme",
          name: "tools",
          repoPath: "acme/tools",
          repositoryKey: repositoryIdKey(2002),
        };
        if (known) yield* reads.observeRoute(ref);
        const pending = yield* reads.refresh.pipe(Effect.forkChild);
        yield* Effect.promise(() => started.promise);
        yield* reads.observeRoute({
          ...ref,
          name: "widgets",
          repoPath: "acme/widgets",
          repositoryKey: repositoryIdKey(4004),
        });
        release.resolve();
        yield* Fiber.join(pending);
        const current = Option.getOrThrow(yield* Stream.runHead(reads.identities));
        expect(current).toEqual([
          expect.objectContaining({ repoPath: "acme/widgets", repositoryKey: repositoryIdKey(4004), fromDetail: true }),
          expect.objectContaining({ repoPath: "acme/tools", repositoryKey: repositoryIdKey(3003), fromDetail: false }),
        ]);
      }).pipe(Effect.provide(AppLiveLayer)),
    );
  },
);
