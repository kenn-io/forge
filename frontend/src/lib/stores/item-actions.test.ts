import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { PullRequest } from "../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import { runListItemAction } from "./item-actions.js";
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
  const setPrLabels = vi.fn();

  beforeEach(() => {
    setPrLabels.mockReset().mockResolvedValue({});
    runtime = makeAppRuntime(makeGeneratedClient({ PullRequestsService: { setPrLabels } }));
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
    expect(setPrLabels).not.toHaveBeenCalled();

    earlier.resolve();
    await vi.waitFor(() => expect(settled).toHaveBeenCalledOnce());
    expect(earlierCommitted).toHaveBeenCalledOnce();
    expect(setPrLabels).toHaveBeenCalledOnce();
    expect(setPrLabels.mock.calls[0]?.[1]).toEqual({ labels: ["docs"] });
  });
});
