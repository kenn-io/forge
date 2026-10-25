import { Effect } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { AppLiveLayer } from "../app/layer.js";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
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
