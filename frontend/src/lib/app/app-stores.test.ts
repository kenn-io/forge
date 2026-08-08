import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "./runtime.js";
import { createAppStores } from "../app-stores.svelte.js";

let runtime: OwnedAppRuntime;

beforeEach(() => {
  runtime = makeAppRuntime();
});

afterEach(async () => {
  await Effect.runPromise(runtime.disposeEffect);
});

describe("app store composition", () => {
  it("builds the provider store graph directly from the application runtime", () => {
    const composition = createAppStores({
      runtime,
      getPage: () => "pulls",
      hostState: {
        getGlobalRepo: () => "github/github.com/acme/widgets",
        getGroupByRepo: () => false,
      },
    });

    expect(composition.stores.pulls.getPulls()).toEqual([]);
    expect(composition.stores.issues.getIssues()).toEqual([]);
    expect(composition.stores.activity.getActivityItems()).toEqual([]);
    expect(composition.stores.grouping.getGroupByRepo()).toBe(true);
  });
});
