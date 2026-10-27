import { Effect } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import type { ProviderRouteRef } from "../api/provider-routes.js";
import type { OwnedAppRuntime } from "../app/runtime.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import { createDetailStore } from "./detail.svelte.js";

const ref = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widget",
  repoPath: "acme/widget",
  repositoryKey: { kind: "id", id: 1001 },
} satisfies ProviderRouteRef;
let runtime: OwnedAppRuntime | undefined;
afterEach(async () => {
  if (runtime) await Effect.runPromise(runtime.disposeEffect);
});

async function fixture() {
  let head = "head-a";
  let number = 7;
  const read = () => ({
    repo_owner: "acme",
    repo_name: "widget",
    repo: { provider: "github", platform_host: "github.com", platform_repo_id: 1001, repo_path: "acme/widget" },
    merge_request: { Number: number, State: "open", MergeableState: "behind" },
    platform_head_sha: head,
    events: [],
  });
  const update = Promise.withResolvers<unknown>();
  const post = vi.fn(async (path: string) => (path.endsWith("/update-branch") ? update.promise : { data: read() }));
  runtime = makeTestAppRuntime({ GET: async () => ({ data: read() }), POST: post });
  const store = createDetailStore({ runtime });
  store.loadDetail("acme", "widget", 7, { ...ref, sync: false });
  await vi.waitFor(() => expect(store.isDetailLoading()).toBe(false));
  return {
    store,
    post,
    update,
    changeHead: () => {
      head = "head-b";
    },
    navigate: () => {
      number = 8;
      store.loadDetail("acme", "widget", 8, { ...ref, sync: false });
    },
  };
}

it("deduplicates clicks and stays pending after acceptance until a refreshed head changes", async () => {
  const { store, post, update, changeHead } = await fixture();
  store.updatePullBranch(ref, 7, "head-a");
  store.updatePullBranch(ref, 7, "head-a");
  expect(store.pullBranchUpdate(ref, 7)).toBe("submitting");
  await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
  expect(post).toHaveBeenCalledWith(
    expect.stringContaining("/update-branch"),
    expect.objectContaining({ body: { expected_head_sha: "head-a" } }),
  );
  update.resolve({ data: { status: "update_requested" }, response: new Response(null, { status: 202 }) });
  await vi.waitFor(() => expect(store.pullBranchUpdate(ref, 7)).toBe("requested"));
  store.updatePullBranch(ref, 7, "head-a");
  changeHead();
  store.refreshDetailOnly("acme", "widget", 7, ref);
  await vi.waitFor(() => expect(store.pullBranchUpdate(ref, 7)).toBeUndefined());
  expect(post.mock.calls.filter(([path]) => path.endsWith("/update-branch"))).toHaveLength(1);
});

it.each(["conflict", "mutationOutcomeUnknown", "transport"])(
  "handles %s without automatic replay or cross-route state",
  async (code) => {
    const { store, post, update, navigate } = await fixture();
    store.updatePullBranch(ref, 7, "head-a");
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
    navigate();
    await vi.waitFor(() => expect(store.getDetail()?.merge_request.Number).toBe(8));
    update.resolve({
      error: {
        code,
        status: code === "conflict" ? 409 : 502,
        title: "Update rejected",
        detail: "Refresh the pull request",
        details: { reason: "stale_state" },
      },
      response: new Response(null, { status: code === "conflict" ? 409 : 502 }),
    });
    await vi.waitFor(() => expect(store.pullBranchUpdate(ref, 7)).toBe(code === "conflict" ? undefined : "uncertain"));
    expect(store.pullBranchUpdate(ref, 8)).toBeUndefined();
    expect(store.getDetail()?.merge_request.Number).toBe(8);
    expect(post.mock.calls.filter(([path]) => path.endsWith("/update-branch"))).toHaveLength(1);
  },
);
