import { describe, expect, it } from "vite-plus/test";
import { repoIdentityMatches, type DetailRefLike } from "./detail-match.js";

const cloudUUID = "0f5d2a4e-3b1c-4d7e-9a8b-1c2d3e4f5a6b";

function cloudDetail(uuid: string) {
  return {
    repo_owner: "team",
    repo_name: "cloud-app",
    repo: {
      provider: "bitbucket",
      platform_host: "bitbucket.org",
      platform_repo_id: 0,
      bitbucket_repository_uuid: uuid,
      repo_path: "team/cloud-app",
    },
  };
}

const cloudRef: DetailRefLike = {
  provider: "bitbucket",
  repositoryKey: { kind: "uuid", uuid: cloudUUID },
  owner: "team",
  name: "cloud-app",
  repoPath: "team/cloud-app",
  number: 3,
};

describe("repoIdentityMatches", () => {
  it("matches a Bitbucket Cloud detail by repository UUID", () => {
    expect(repoIdentityMatches(cloudDetail(`{${cloudUUID.toUpperCase()}}`), cloudRef)).toBe(true);
  });

  it("rejects a Bitbucket Cloud detail for another repository at the same route", () => {
    expect(repoIdentityMatches(cloudDetail("11111111-2222-4333-8444-555555555555"), cloudRef)).toBe(false);
  });

  it("rejects an integer-keyed detail when the selection carries a UUID key", () => {
    const detail = cloudDetail("");
    detail.repo.platform_repo_id = 42;
    expect(repoIdentityMatches(detail, cloudRef)).toBe(false);
    expect(repoIdentityMatches(detail, { ...cloudRef, repositoryKey: { kind: "id", id: 42 } })).toBe(true);
  });

  it("accepts a route-only selection before the repository key is known", () => {
    expect(repoIdentityMatches(cloudDetail(cloudUUID), { ...cloudRef, repositoryKey: undefined })).toBe(true);
  });
});
