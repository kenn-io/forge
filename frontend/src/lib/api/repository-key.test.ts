import { describe, expect, it } from "vite-plus/test";
import { repoIdentityKey } from "../utils/repo-label.js";
import {
  repositoryKeyFromCatalog,
  repositoryKeyFromStored,
  repositoryKeyFromWire,
  repositoryKeyString,
  repositoryKeyToRequiredWire,
  repositoryKeyToStored,
  repositoryKeyToWire,
  sameRepositoryKey,
} from "./repository-key.js";

const cloudUUID = "0f5d2a4e-3b1c-4d7e-9a8b-1c2d3e4f5a6b";

describe("repository keys", () => {
  it("round-trips an integer repository ID through the wire encoding", () => {
    const key = repositoryKeyFromWire({ platform_repo_id: 42 });
    expect(key).toEqual({ kind: "id", id: 42 });
    expect(repositoryKeyToWire(key)).toEqual({ platform_repo_id: 42 });
    expect(repositoryKeyFromWire(repositoryKeyToWire(key))).toEqual(key);
  });

  it("round-trips a Bitbucket Cloud UUID without sending an integer ID", () => {
    const key = repositoryKeyFromWire({ platform_repo_id: 0, bitbucket_repository_uuid: cloudUUID });
    expect(key).toEqual({ kind: "uuid", uuid: cloudUUID });
    expect(repositoryKeyToWire(key)).toEqual({ bitbucket_repository_uuid: cloudUUID });
    expect(repositoryKeyToRequiredWire(key!)).toEqual({ platform_repo_id: 0, bitbucket_repository_uuid: cloudUUID });
    expect(repositoryKeyFromWire(repositoryKeyToWire(key))).toEqual(key);
  });

  it("canonicalizes braced and uppercase UUIDs", () => {
    const braced = repositoryKeyFromWire({ bitbucket_repository_uuid: `{${cloudUUID.toUpperCase()}}` });
    expect(braced).toEqual({ kind: "uuid", uuid: cloudUUID });
    expect(
      sameRepositoryKey(braced, repositoryKeyFromCatalog({ PlatformRepoID: 0, BitbucketRepositoryUUID: cloudUUID })),
    ).toBe(true);
  });

  it("rejects missing or contradictory identities", () => {
    expect(repositoryKeyFromWire({})).toBeUndefined();
    expect(repositoryKeyFromWire({ platform_repo_id: 0, bitbucket_repository_uuid: "" })).toBeUndefined();
    expect(repositoryKeyFromWire({ platform_repo_id: -3 })).toBeUndefined();
    expect(repositoryKeyFromWire({ platform_repo_id: 7, bitbucket_repository_uuid: cloudUUID })).toBeUndefined();
    expect(repositoryKeyToWire(undefined)).toEqual({});
  });

  it("keeps the persisted integer identity format and separates UUID keys", () => {
    expect(repositoryKeyString({ kind: "id", id: 42 })).toBe("id|42");
    expect(repositoryKeyString({ kind: "uuid", uuid: cloudUUID })).toBe(`uuid|${cloudUUID}`);
    // Remembered new-workspace repositories and comment drafts were stored with this exact string.
    expect(
      repoIdentityKey({
        provider: "github",
        platformHost: "github.com",
        repositoryKey: { kind: "id", id: 42 },
        owner: "acme",
        name: "widgets",
      }),
    ).toBe("github|github.com|id|42");
  });

  it("reads route refs stored before UUID keys and stores UUID keys separately", () => {
    expect(repositoryKeyFromStored({ platformRepoId: 1055 })).toEqual({ kind: "id", id: 1055 });
    expect(repositoryKeyToStored({ kind: "id", id: 1055 })).toEqual({ platformRepoId: 1055 });
    expect(repositoryKeyToStored({ kind: "uuid", uuid: cloudUUID })).toEqual({ bitbucketRepositoryUUID: cloudUUID });
    expect(repositoryKeyFromStored({ bitbucketRepositoryUUID: cloudUUID })).toEqual({ kind: "uuid", uuid: cloudUUID });
    expect(repositoryKeyFromStored({})).toBeUndefined();
  });

  it("decodes an unchanged identity to the same key value", () => {
    expect(repositoryKeyFromWire({ platform_repo_id: 42 })).toBe(repositoryKeyFromWire({ platform_repo_id: 42 }));
    expect(repositoryKeyFromWire({ bitbucket_repository_uuid: cloudUUID })).toBe(
      repositoryKeyFromCatalog({ BitbucketRepositoryUUID: `{${cloudUUID}}` }),
    );
  });

  it("compares keys by kind and value", () => {
    expect(sameRepositoryKey({ kind: "id", id: 1 }, { kind: "id", id: 1 })).toBe(true);
    expect(sameRepositoryKey({ kind: "id", id: 1 }, { kind: "id", id: 2 })).toBe(false);
    expect(sameRepositoryKey({ kind: "uuid", uuid: cloudUUID }, { kind: "id", id: 1 })).toBe(false);
    expect(sameRepositoryKey({ kind: "id", id: 1 }, undefined)).toBe(false);
    expect(sameRepositoryKey(undefined, undefined)).toBe(true);
  });
});
