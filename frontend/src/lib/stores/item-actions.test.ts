import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { dismissFlash, getFlashes } from "./flash.svelte.js";
import type { Issue, PullRequest } from "../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import { listTargetsNoun, runListItemAction } from "./item-actions.js";
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
