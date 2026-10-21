import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { dismissFlash, getFlashes } from "./flash.svelte.js";
import type { Issue, PullRequest } from "../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import { listTargetsNoun, runListItemAction, type ListItemTarget } from "./item-actions.js";
import { ProviderMutations } from "./ordered-mutations.js";
import { providerMutationKey } from "./provider-key.js";

const pull = {
  Number: 21,
  State: "open",
  labels: [],
  repo: { provider: "github", platform_host: "github.com", owner: "acme", name: "widgets", repo_path: "acme/widgets" },
} as unknown as PullRequest;

describe("sidebar item actions", () => {
  let runtime: OwnedAppRuntime;
  const updatePrLabels = vi.fn();

  beforeEach(() => {
    updatePrLabels.mockReset().mockResolvedValue({});
    runtime = makeAppRuntime(makeGeneratedClient({ PullRequestsService: { updatePrLabels } }));
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("waits for an earlier label change to the same item before sending its own", async () => {
    const earlier = Promise.withResolvers<void>();
    const earlierCommitted = vi.fn();
    // The detail view's label editor submits under this key.
    const key = providerMutationKey(
      "pull",
      { provider: "github", platformHost: "github.com", owner: "acme", name: "widgets", number: 21 },
      "labels",
    );
    runtime.runCommand(
      Effect.gen(function* () {
        const mutations = yield* ProviderMutations;
        yield* mutations.submit({
          key,
          baseline: undefined,
          optimistic: undefined,
          apply: () => Effect.void,
          commit: Effect.promise(() => earlier.promise).pipe(Effect.tap(() => Effect.sync(earlierCommitted))),
          refreshOnStale: Effect.void,
        });
      }),
      { operation: "earlier label change", safeContext: {} },
    );

    const settled = vi.fn();
    runListItemAction(
      runtime,
      [{ kind: "pull", item: pull }],
      { type: "labels", add: ["docs"], remove: [] },
      { onSettled: settled },
    );
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(updatePrLabels).not.toHaveBeenCalled();

    earlier.resolve();
    await vi.waitFor(() => expect(settled).toHaveBeenCalledOnce());
    expect(earlierCommitted).toHaveBeenCalledOnce();
    expect(updatePrLabels).toHaveBeenCalledOnce();
    expect(updatePrLabels.mock.calls[0]?.[1]).toEqual({ add: ["docs"], remove: [] });
  });
});

describe("sidebar item actions on a mixed selection", () => {
  let runtime: OwnedAppRuntime;
  const setPrGithubState = vi.fn();
  const setIssueGithubState = vi.fn();

  beforeEach(() => {
    for (const flash of getFlashes()) dismissFlash(flash.id);
    setPrGithubState.mockReset().mockResolvedValue({});
    setIssueGithubState.mockReset().mockResolvedValue({});
    runtime = makeAppRuntime(
      makeGeneratedClient({ PullRequestsService: { setPrGithubState }, IssuesService: { setIssueGithubState } }),
    );
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("names pull requests and issues together as items", async () => {
    const issue = { ...pull, Number: 7 } as unknown as Issue;
    const settled = vi.fn();

    runListItemAction(
      runtime,
      [
        { kind: "pull", item: pull },
        { kind: "issue", item: issue },
      ],
      { type: "close" },
      { onSettled: settled },
    );

    await vi.waitFor(() => expect(settled).toHaveBeenCalledOnce());
    expect(setPrGithubState.mock.calls[0]?.[0]).toMatchObject({ number: 21 });
    expect(setIssueGithubState.mock.calls[0]?.[0]).toMatchObject({ number: 7 });
    expect(getFlashes().map((flash) => flash.message)).toContain("Closed 2 items.");
  });
});

describe("merging a selection of pull requests", () => {
  let runtime: OwnedAppRuntime;
  const mergePull = vi.fn();
  const deferMergePull = vi.fn();

  beforeEach(() => {
    for (const flash of getFlashes()) dismissFlash(flash.id);
    mergePull.mockReset();
    deferMergePull.mockReset().mockResolvedValue({});
    runtime = makeAppRuntime(makeGeneratedClient({ PullRequestsService: { mergePull, deferMergePull } }));
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
  });

  function target(number: number, overrides: Partial<PullRequest> = {}): ListItemTarget {
    return {
      kind: "pull",
      item: {
        ...pull,
        Number: number,
        Title: `Change ${number}`,
        CIStatus: "success",
        CIChecksJSON: "[]",
        ...overrides,
      },
    };
  }

  it("merges now, after pending CI, or through the merge queue, and says which", async () => {
    mergePull.mockImplementation((params: { number: number }) =>
      Promise.resolve(
        params.number === 23
          ? { merged: false, queued: true, sha: "", message: "" }
          : { merged: true, sha: "abc", message: "" },
      ),
    );
    const settled = vi.fn();

    runListItemAction(
      runtime,
      [
        target(21, { workspace: { id: "ws-21" } as PullRequest["workspace"] }),
        target(22, { CIStatus: "pending" }),
        target(23),
      ],
      { type: "merge", method: "squash", deleteWorkspaces: true },
      { onSettled: settled },
    );

    await vi.waitFor(() => expect(settled).toHaveBeenCalledOnce());
    expect(mergePull.mock.calls.map((call) => call[0].number).sort()).toEqual([21, 23]);
    expect(deferMergePull.mock.calls.map((call) => call[0].number)).toEqual([22]);
    expect(mergePull.mock.calls.find((call) => call[0].number === 21)?.[1]).toMatchObject({
      method: "squash",
      commit_title: "Change 21 (#21)",
      delete_workspace_id: "ws-21",
    });
    expect(getFlashes().map((flash) => flash.message)).toContain(
      "Merged 1 PR. 1 PR will merge once CI passes. Added 1 PR to the merge queue.",
    );
  });

  it("reports a merge the provider declined as a failure", async () => {
    mergePull.mockImplementation((params: { number: number }) =>
      Promise.resolve(
        params.number === 22
          ? { merged: false, sha: "", message: "Base branch was modified" }
          : { merged: true, sha: "abc", message: "" },
      ),
    );
    const settled = vi.fn();

    runListItemAction(
      runtime,
      [target(21), target(22)],
      { type: "merge", method: "merge", deleteWorkspaces: false },
      { onSettled: settled },
    );

    await vi.waitFor(() => expect(settled).toHaveBeenCalledOnce());
    expect(getFlashes().map((flash) => flash.message)).toContain(
      "Merged 1 PR. Could not merge 1 of 2 PRs. acme/widgets#22: Base branch was modified.",
    );
  });
});

describe("listTargetsNoun", () => {
  it("follows the kinds in the selection", () => {
    const issue = { ...pull, Number: 7 } as unknown as Issue;
    expect(listTargetsNoun([{ kind: "pull", item: pull }], 2)).toBe("PRs");
    expect(listTargetsNoun([{ kind: "issue", item: issue }], 3)).toBe("issues");
    expect(
      listTargetsNoun(
        [
          { kind: "pull", item: pull },
          { kind: "issue", item: issue },
        ],
        5,
      ),
    ).toBe("items");
    expect(
      listTargetsNoun(
        [
          { kind: "pull", item: pull },
          { kind: "issue", item: issue },
        ],
        1,
      ),
    ).toBe("item");
  });
});
