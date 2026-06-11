import { describe, expect, test } from "vite-plus/test";
import { EditorState } from "@codemirror/state";
import { CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { buildIssueCompletionSource } from "./issueCompletion";
import type { IssueSummary, SearchResponse } from "./docsIssueTypes";

function issue(partial: Partial<IssueSummary>): IssueSummary {
  return {
    id: 1,
    uid: "issue-1",
    project_id: 1,
    short_id: "rent",
    qualified_id: "household#rent",
    title: "Pay rent",
    status: "open",
    project_uid: "proj-household",
    project_name: "household",
    metadata: {},
    revision: 1,
    author: "wes",
    created_at: "2026-05-01T00:00:00Z",
    updated_at: "2026-05-01T00:00:00Z",
    ...partial,
  } as IssueSummary;
}

function makeContext(text: string, explicit = false): CompletionContext {
  const state = EditorState.create({ doc: text });
  return new CompletionContext(state, text.length, explicit);
}

describe("buildIssueCompletionSource", () => {
  const issues: IssueSummary[] = [
    issue({ short_id: "rent", title: "Pay rent", project_name: "household" }),
    issue({ short_id: "lease", title: "Renew lease", project_name: "household" }),
    issue({ short_id: "budget", title: "Set monthly budget", project_name: "household" }),
    issue({ short_id: "yoga", title: "Morning yoga", project_name: "personal" }),
    issue({
      short_id: "rentprev",
      title: "Last month rent done",
      project_name: "household",
      status: "closed",
    }),
  ];
  const source = buildIssueCompletionSource(() => issues);

  async function complete(text: string): Promise<CompletionResult | null> {
    return await source(makeContext(text));
  }

  test("suggests issues whose short_id starts with the typed prefix", async () => {
    const result = await complete("draft #re");
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels).toContain("#rent");
    expect(labels).toContain("#rentprev");
    expect(labels[0]).toBe("#rent"); // open before closed
  });

  test("ranks open issues above closed for the same prefix", async () => {
    const result = await complete("ref #r");
    const labels = result!.options.map((o) => o.label);
    const openIdx = labels.indexOf("#rent");
    const closedIdx = labels.indexOf("#rentprev");
    expect(openIdx).toBeLessThan(closedIdx);
  });

  test("returns no suggestions when # is preceded by a word char", async () => {
    expect(await complete("issue#re")).toBeNull();
  });

  test("qualified form filters by project name", async () => {
    const result = await complete("done in household/#");
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels).toEqual(expect.arrayContaining(["household/#rent", "household/#budget", "household/#lease"]));
    // Personal-project issues should not appear under household/#…
    expect(labels).not.toContain("household/#yoga");
    expect(labels).not.toContain("personal/#yoga");
  });

  test("applies the full short_id, not just the typed prefix", async () => {
    const result = await complete("see #bud");
    const budget = result!.options.find((o) => o.label === "#budget");
    expect(budget?.apply).toBe("#budget");
  });

  test("falls back to title substring matches when nothing matches short_id", async () => {
    const result = await complete("#month");
    const labels = result!.options.map((o) => o.label);
    expect(labels).toContain("#budget"); // matched via title "Set monthly budget"
  });

  test("bare # at the start of a line is offered as a trigger", async () => {
    const result = await complete("#");
    expect(result).not.toBeNull();
    expect(result!.options.length).toBeGreaterThan(0);
  });
});

describe("buildIssueCompletionSource with async daemon search", () => {
  const local: IssueSummary[] = [
    issue({ uid: "u-local-1", short_id: "rent", title: "Pay rent", project_name: "household" }),
  ];
  const remote: IssueSummary[] = [
    issue({
      uid: "u-remote-1",
      short_id: "renew",
      title: "Renew passport",
      project_name: "personal",
    }),
    // Same uid as local — dedupe should keep the local entry only once.
    issue({ uid: "u-local-1", short_id: "rent", title: "Pay rent", project_name: "household" }),
  ];

  function makeSource(remoteHits: IssueSummary[], opts?: { fail?: boolean }) {
    let calls = 0;
    const search = async (): Promise<SearchResponse> => {
      calls++;
      if (opts?.fail) throw new Error("boom");
      return {
        filters: { scope: { kind: "all" }, status: "all", owner: "", label: "", query: "" },
        issues: remoteHits,
        fetched_at: "2026-05-01T00:00:00Z",
      };
    };
    const src = buildIssueCompletionSource({
      getIssues: () => local,
      search,
      debounceMs: 0, // skip debounce in tests
    });
    return { src, callCount: () => calls };
  }

  test("local-first: returns local-only without awaiting daemon when local already has a match", async () => {
    // The loaded view already contains a "rent" issue that matches "#re",
    // so the menu paints from the in-memory snapshot and skips the
    // network round-trip — a slow/hanging daemon must not delay the
    // suggestion list when local already has something to show.
    const { src, callCount } = makeSource(remote);
    const result = await src(makeContext("#re"));
    expect(result).not.toBeNull();
    expect(result!.options.map((o) => o.label)).toEqual(["#rent"]);
    expect(callCount()).toBe(0);
  });

  test("merges daemon hits when local has no match for the prefix", async () => {
    // Prefix "#ne" has no local match, so the source falls through to
    // the daemon and merges the remote-only "renew" result.
    const { src, callCount } = makeSource(remote);
    const result = await src(makeContext("#ne"));
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels).toContain("#renew");
    expect(callCount()).toBe(1);
  });

  test("qualified empty prefixes can search the daemon when the local snapshot is empty", async () => {
    const { src, callCount } = makeSource(remote);
    const result = await src(makeContext("see personal/#"));

    expect(result).not.toBeNull();
    expect(result!.options.map((o) => o.label)).toContain("personal/#renew");
    expect(callCount()).toBe(1);
  });

  test("qualified daemon completions rerun after the user types more characters", async () => {
    const { src } = makeSource(remote);
    const result = await src(makeContext("see personal/#"));

    expect(result).not.toBeNull();
    expect(result!.filter).toBe(false);
    expect(result!.validFor).toBeUndefined();
  });

  test("skips daemon search when the prefix is empty", async () => {
    const { src, callCount } = makeSource(remote);
    const result = await src(makeContext("#"));
    expect(result).not.toBeNull();
    expect(callCount()).toBe(0);
    // Local-only result.
    expect(result!.options.map((o) => o.label)).toEqual(["#rent"]);
  });

  test("falls back to local results when the daemon throws", async () => {
    // Prefix "#ne" doesn't match local, so the source has to hit the
    // daemon — that's the path the throw-recovery code guards. The
    // result should still be local (no crash, no error rendered).
    const { src } = makeSource(remote, { fail: true });
    const result = await src(makeContext("#ne"));
    expect(result).not.toBeNull();
    expect(result!.options.map((o) => o.label)).toEqual([]);
  });

  test("captures the search daemon before the debounce, ignoring a mid-debounce switch", async () => {
    // Regression: the cache key captures the effective daemon at request start;
    // the search must use that SAME daemon, not whatever the active daemon
    // becomes if the user switches during the debounce window — otherwise a
    // result from the new daemon gets cached under the old daemon's key.
    let daemon = "alpha";
    let capturedKey: string | undefined;
    const src = buildIssueCompletionSource({
      getIssues: () => [],
      debounceMs: 5,
      cacheKeyPrefix: () => daemon,
      search: async (_filters, daemonKey) => {
        capturedKey = daemonKey;
        return {
          filters: { scope: { kind: "all" }, status: "all", owner: "", label: "", query: "" },
          issues: [],
          fetched_at: "2026-05-01T00:00:00Z",
        };
      },
    });
    const pending = src(makeContext("#ne"));
    daemon = "beta"; // user switches daemon during the debounce window
    await pending;
    expect(capturedKey).toBe("alpha");
  });
});
