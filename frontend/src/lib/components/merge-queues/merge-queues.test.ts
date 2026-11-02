import { describe, expect, it } from "vite-plus/test";

import type { PullRequest } from "../../api/types.js";
import { filterMergeQueuePulls, groupMergeQueues, mergeQueueStatusKey } from "./merge-queues.js";

let nextID = 1;

function mkPR(repoPath: string, number: number, overrides: Partial<PullRequest> = {}): PullRequest {
  const [owner, name] = repoPath.split("/") as [string, string];
  return {
    ID: nextID++,
    Number: number,
    Title: `PR ${number}`,
    BaseBranch: "main",
    URL: `https://github.com/${repoPath}/pull/${number}`,
    repo: { provider: "github", platform_host: "github.com", owner, name, repo_path: repoPath },
    ...overrides,
  } as PullRequest;
}

function entry(position: number, enqueuedAt: string): Pick<PullRequest, "merge_queue"> {
  return { merge_queue: { position, state: "queued", enqueued_at: enqueuedAt, observed_at: enqueuedAt } };
}

function removal(removedAt: string): Pick<PullRequest, "merge_queue_removal"> {
  return { merge_queue_removal: { reason: "failed_checks", removed_at: removedAt } };
}

const numbers = (pulls: readonly PullRequest[]) => pulls.map((pr) => pr.Number);

describe("groupMergeQueues", () => {
  it("groups by repository and base branch, sorted by repository path then branch", () => {
    const groups = groupMergeQueues([
      mkPR("acme/widgets", 1, entry(1, "2026-07-10T15:00:00Z")),
      mkPR("acme/api", 2, entry(1, "2026-07-10T15:00:00Z")),
      mkPR("acme/widgets", 3, { ...entry(1, "2026-07-10T15:00:00Z"), BaseBranch: "release" }),
      mkPR("acme/widgets", 4, entry(2, "2026-07-10T15:01:00Z")),
    ]);

    expect(groups.map((group) => [group.repo.repo_path, group.branch, numbers(group.entries)])).toEqual([
      ["acme/api", "main", [2]],
      ["acme/widgets", "main", [1, 4]],
      ["acme/widgets", "release", [3]],
    ]);
  });

  it("orders entries by position, with entries the queue has not positioned yet last by enqueue time", () => {
    const [group] = groupMergeQueues([
      mkPR("acme/widgets", 10, entry(0, "2026-07-10T15:09:00Z")),
      mkPR("acme/widgets", 11, entry(3, "2026-07-10T15:02:00Z")),
      mkPR("acme/widgets", 12, entry(0, "2026-07-10T15:08:00Z")),
      mkPR("acme/widgets", 13, entry(1, "2026-07-10T15:05:00Z")),
    ]);

    expect(numbers(group!.entries)).toEqual([13, 11, 12, 10]);
  });

  it("lists removals newest first and leaves out pull requests with no queue state", () => {
    const [group, ...rest] = groupMergeQueues([
      mkPR("acme/widgets", 20, removal("2026-07-10T14:00:00Z")),
      mkPR("acme/widgets", 21),
      mkPR("acme/widgets", 22, removal("2026-07-10T15:00:00Z")),
    ]);

    expect(rest).toEqual([]);
    expect(numbers(group!.entries)).toEqual([]);
    expect(numbers(group!.removals)).toEqual([22, 20]);
  });

  it("keeps a renamed repository's pull requests in one queue by stable repository ID", () => {
    const groups = groupMergeQueues([
      mkPR("acme/widgets", 30, {
        ...entry(1, "2026-07-10T15:00:00Z"),
        repo: {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          repo_path: "acme/widgets",
          platform_repo_id: 7,
        } as PullRequest["repo"],
      }),
      mkPR("acme/gadgets", 31, {
        ...entry(2, "2026-07-10T15:00:00Z"),
        repo: {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "gadgets",
          repo_path: "acme/gadgets",
          platform_repo_id: 7,
        } as PullRequest["repo"],
      }),
    ]);

    expect(groups).toHaveLength(1);
    expect(numbers(groups[0]!.entries)).toEqual([30, 31]);
  });

  it("links the provider's queue page for the branch", () => {
    const [group] = groupMergeQueues([
      mkPR("acme/widgets", 40, { ...entry(1, "2026-07-10T15:00:00Z"), BaseBranch: "release/1.x" }),
    ]);

    expect(group!.providerURL).toBe("https://github.com/acme/widgets/queue/release%2F1.x");
  });
});

describe("filterMergeQueuePulls", () => {
  const pulls = [
    mkPR("acme/widgets", 1, {
      Title: "Add caching",
      Author: "mona",
      AuthorDisplayName: "Mona Lisa",
      ...entry(1, "2026-07-10T15:00:00Z"),
    }),
    mkPR("acme/api", 2, {
      Title: "Fix login",
      Author: "hubot",
      ...entry(1, "2026-07-10T15:00:00Z"),
      BaseBranch: "release",
    }),
    mkPR("acme/api", 3, { Title: "Bump deps", Author: "dependabot[bot]", ...removal("2026-07-10T15:00:00Z") }),
  ];
  const none = { query: "", statuses: [], authors: [] };

  it("requires every search word to match the title, number, author, repository, or branch", () => {
    expect(numbers(filterMergeQueuePulls(pulls, { ...none, query: "lisa caching" }))).toEqual([1]);
    expect(numbers(filterMergeQueuePulls(pulls, { ...none, query: "#3" }))).toEqual([3]);
    expect(numbers(filterMergeQueuePulls(pulls, { ...none, query: "acme/api release" }))).toEqual([2]);
    expect(numbers(filterMergeQueuePulls(pulls, { ...none, query: "caching hubot" }))).toEqual([]);
  });

  it("keeps only the chosen statuses and authors", () => {
    expect(pulls.map(mergeQueueStatusKey)).toEqual(["queued", "queued", "removed"]);
    expect(numbers(filterMergeQueuePulls(pulls, { ...none, statuses: ["removed"] }))).toEqual([3]);
    expect(numbers(filterMergeQueuePulls(pulls, { ...none, authors: ["mona", "hubot"] }))).toEqual([1, 2]);
    expect(numbers(filterMergeQueuePulls(pulls, { ...none, statuses: ["removed"], authors: ["mona"] }))).toEqual([]);
  });
});
