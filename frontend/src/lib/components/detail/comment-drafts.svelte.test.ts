import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";

const ref = { provider: "github", platformHost: "github.com", owner: "acme", name: "widgets", number: 7 };

beforeEach(() => {
  localStorage.clear();
  vi.resetModules();
});

afterEach(() => vi.restoreAllMocks());

it.each(["pull", "issue"] as const)(
  "restores an unsent %s comment after the browser runtime restarts",
  async (target) => {
    const firstRuntime = await import("./comment-drafts.svelte.js");
    const key = firstRuntime.getCommentDraftKey(target, ref);
    firstRuntime.setCommentDraft(key, "  unfinished\ncomment  ");
    firstRuntime.beginCommentSubmit(key);

    vi.resetModules();
    const nextRuntime = await import("./comment-drafts.svelte.js");

    expect(nextRuntime.getCommentDraft(key)).toBe("  unfinished\ncomment  ");
    expect(nextRuntime.isCommentSubmitPending(key)).toBe(false);
  },
);

it("restores separate drafts by provider, host, repository, item type, and number", async () => {
  const firstRuntime = await import("./comment-drafts.svelte.js");
  const examples = [
    ["pull", ref, "pull draft"],
    ["issue", ref, "issue draft"],
    ["pull", { ...ref, provider: "gitea" }, "gitea draft"],
    ["pull", { ...ref, platformHost: "git.example.com" }, "host draft"],
    ["pull", { ...ref, owner: "another" }, "owner draft"],
    ["pull", { ...ref, name: "another" }, "repository draft"],
    ["pull", { ...ref, number: 8 }, "number draft"],
  ] as const;
  for (const [target, item, body] of examples) {
    firstRuntime.setCommentDraft(firstRuntime.getCommentDraftKey(target, item), body);
  }

  vi.resetModules();
  const nextRuntime = await import("./comment-drafts.svelte.js");

  for (const [target, item, body] of examples) {
    expect(nextRuntime.getCommentDraft(nextRuntime.getCommentDraftKey(target, item))).toBe(body);
  }
  expect(
    nextRuntime.getCommentDraft(nextRuntime.getCommentDraftKey("pull", { ...ref, provider: "gh", platformHost: "" })),
  ).toBe("pull draft");
});

it.each(["clear", "empty"] as const)("does not restore a draft after %s removes it", async (action) => {
  const firstRuntime = await import("./comment-drafts.svelte.js");
  const key = firstRuntime.getCommentDraftKey("pull", ref);
  firstRuntime.setCommentDraft(key, "finished comment");
  if (action === "clear") firstRuntime.clearCommentDraft(key);
  else firstRuntime.setCommentDraft(key, "");

  vi.resetModules();
  const nextRuntime = await import("./comment-drafts.svelte.js");

  expect(nextRuntime.getCommentDraft(key)).toBe("");
});

it("keeps editing and clearing in memory when browser storage fails", async () => {
  const drafts = await import("./comment-drafts.svelte.js");
  const key = drafts.getCommentDraftKey("pull", ref);
  for (const method of ["getItem", "setItem", "removeItem"] as const) {
    vi.spyOn(Storage.prototype, method).mockImplementation(() => {
      throw new Error("storage unavailable");
    });
  }

  expect(drafts.getCommentDraft(key)).toBe("");
  drafts.setCommentDraft(key, "still writing");
  expect(drafts.getCommentDraft(key)).toBe("still writing");
  drafts.clearCommentDraft(key);
  expect(drafts.getCommentDraft(key)).toBe("");
});
