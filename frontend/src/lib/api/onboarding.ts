import { Effect } from "effect";
import { InvalidExternalPayload } from "./effect-errors.js";
import { createItemWorkspace } from "./item-workspace.js";
import { repositoryKeyFromWire } from "./repository-key.js";
import type { PullRequest } from "./types.js";

export const createPullRequestWorkspace = Effect.fn("Onboarding.createPullRequestWorkspace")(function* (
  pull: PullRequest,
  hostKey?: string,
) {
  const data = yield* createItemWorkspace(hostKey, {
    kind: "pull",
    repo: {
      provider: pull.repo.provider,
      platformHost: pull.repo.platform_host,
      repositoryKey: repositoryKeyFromWire(pull.repo),
      owner: pull.repo.owner,
      name: pull.repo.name,
      repoPath: pull.repo.repo_path,
    },
    number: pull.Number,
  });
  if (!data.id) {
    return yield* Effect.fail(
      InvalidExternalPayload.make({
        operation: "decode created pull request workspace",
        cause: new Error("workspace response did not include an id"),
      }),
    );
  }
  return {
    id: data.id,
    status: data.status ?? "provisioning",
  };
});
