import { describe, it, expect, vi, beforeEach } from "vite-plus/test";

const diffRef = {
  provider: "github",
  platformHost: "github.com",
  owner: "o",
  name: "n",
  repoPath: "o/n",
};
const storage = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (k: string) => storage.get(k) ?? null,
  setItem: (k: string, v: string) => storage.set(k, v),
  removeItem: (k: string) => storage.delete(k),
  clear: () => storage.clear(),
});

import { createDiffStore } from "@middleman/ui/stores/diff";
import type { DiffScope, DiffStoreOptions } from "@middleman/ui/stores/diff";

type TestClient = NonNullable<DiffStoreOptions["client"]>;

interface TestGetOptions {
  params?: {
    query?: Record<string, string | number | boolean | undefined>;
  };
}

let mockGet: ReturnType<typeof vi.fn>;
let store: ReturnType<typeof createDiffStore>;

function makeDiffResponse() {
  return {
    stale: false,
    whitespace_only_count: 0,
    files: [
      {
        path: "a.go",
        old_path: "a.go",
        status: "modified",
        is_binary: false,
        is_whitespace_only: false,
        additions: 1,
        deletions: 0,
        hunks: [],
      },
    ],
  };
}

function makeCommitsResponse(n: number = 3) {
  const commits = [];
  for (let i = n; i > 0; i--) {
    commits.push({
      sha: `sha${i}`,
      message: `commit ${i}`,
      author_name: "Alice",
      authored_at: "2026-01-01T00:00:00Z",
    });
  }
  return { commits };
}

function installClient(commitCount = 3): void {
  mockGet = vi.fn(async (path: string) => {
    if (path.includes("/commits")) {
      return {
        data: makeCommitsResponse(commitCount),
        response: new Response(null),
      };
    }
    if (path.includes("/files")) {
      return {
        data: {
          stale: false,
          files: makeDiffResponse().files,
        },
        response: new Response(null),
      };
    }
    if (path.includes("/diff")) {
      return {
        data: makeDiffResponse(),
        response: new Response(null),
      };
    }

    throw new Error(`unexpected client path: ${path}`);
  });
  store = createDiffStore({
    client: { GET: mockGet } as unknown as TestClient,
  });
}

function lastQuery(): URLSearchParams {
  const options = mockGet.mock.calls.at(-1)?.[1] as TestGetOptions | undefined;
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(options?.params?.query ?? {})) {
    if (value !== undefined) query.set(key, String(value));
  }
  return query;
}

describe("diff store scope", () => {
  beforeEach(() => {
    storage.clear();
    installClient();
  });

  it("starts at HEAD scope", () => {
    expect(store.getScope()).toEqual({ kind: "head" });
  });

  it("loadCommits fetches and stores commits", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();

    expect(store.getCommits()).toHaveLength(3);
    expect(store.getCommits()![0]!.sha).toBe("sha3");
  });

  it("loadCommits is a no-op if already loaded", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    await store.loadCommits();

    expect(mockGet).toHaveBeenCalledTimes(3);
  });

  it("selectCommit sets scope and refetches diff", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectCommit("sha2");

    expect(store.getScope()).toEqual({ kind: "commit", sha: "sha2" });
  });

  it("selectRange orders SHAs by commit index", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectRange("sha3", "sha1");

    const s = store.getScope() as Extract<DiffScope, { kind: "range" }>;
    expect(s.kind).toBe("range");
    expect(s.fromSha).toBe("sha1");
    expect(s.toSha).toBe("sha3");
  });

  it("resetToHead returns to HEAD and refetches", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectCommit("sha2");
    store.resetToHead();

    expect(store.getScope()).toEqual({ kind: "head" });
  });

  it("stepPrev from HEAD goes to newest commit", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.stepPrev();

    expect(store.getScope()).toEqual({ kind: "commit", sha: "sha3" });
  });

  it("stepNext from HEAD is a no-op", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.stepNext();

    expect(store.getScope()).toEqual({ kind: "head" });
  });

  it("stepNext from newest commit returns to HEAD", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectCommit("sha3");
    store.stepNext();

    expect(store.getScope()).toEqual({ kind: "head" });
  });

  it("stepPrev from oldest commit is a no-op", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectCommit("sha1");
    store.stepPrev();

    expect(store.getScope()).toEqual({ kind: "commit", sha: "sha1" });
  });

  it("stepPrev from range collapses to fromSha", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectRange("sha1", "sha3");
    store.stepPrev();

    expect(store.getScope()).toEqual({ kind: "commit", sha: "sha1" });
  });

  it("stepNext from range collapses to toSha", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectRange("sha1", "sha3");
    store.stepNext();

    expect(store.getScope()).toEqual({ kind: "commit", sha: "sha3" });
  });

  it("diff fetch includes commit param when scope is single commit", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectCommit("sha2");

    await vi.waitFor(() => {
      expect(lastQuery().get("commit")).toBe("sha2");
    });
  });

  it("diff fetch includes from+to params when scope is range", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectRange("sha1", "sha3");

    await vi.waitFor(() => {
      const query = lastQuery();
      expect(query.get("from")).toBe("sha1");
      expect(query.get("to")).toBe("sha3");
    });
  });

  it("clearDiff resets scope and commits", async () => {
    installClient();

    await store.loadDiff("o", "n", 1, diffRef);
    await store.loadCommits();
    store.selectCommit("sha2");
    store.clearDiff();

    expect(store.getScope()).toEqual({ kind: "head" });
    expect(store.getCommits()).toBeNull();
  });
});
