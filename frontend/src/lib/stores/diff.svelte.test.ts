import { afterEach, describe, expect, it, vi } from "vitest";
import { createDiffStore } from "@middleman/ui/stores/diff";
import type { DiffStoreOptions } from "@middleman/ui/stores/diff";
import type { DiffResult, FilesResult } from "@middleman/ui/api/types";

const ownerRepoRef = { provider: "github", platformHost: "github.com", owner: "owner", name: "repo", repoPath: "owner/repo" };

type TestClient = NonNullable<DiffStoreOptions["client"]>;

interface TestGetOptions {
  params?: {
    path?: Record<string, string | number>;
    query?: Record<string, string | number | boolean | undefined>;
  };
  signal?: AbortSignal;
}

function makeDiffResult(files: string[]): DiffResult {
  return {
    stale: false,
    whitespace_only_count: 0,
    files: files.map((path) => ({
      path,
      old_path: path,
      status: "modified" as const,
      is_binary: false,
      is_whitespace_only: false,
      additions: 1,
      deletions: 1,
      hunks: [],
    })),
  };
}

function makeFilesResult(
  files: string[],
  overrides: Partial<FilesResult & { whitespace_only_count: number }> = {},
): FilesResult {
  return {
    stale: false,
    files: files.map((path) => ({
      path,
      old_path: path,
      status: "modified" as const,
      is_binary: false,
      is_whitespace_only: false,
      additions: 0,
      deletions: 0,
      hunks: [],
    })),
    ...overrides,
  };
}

function testClient(): TestClient {
  return {
    GET: vi.fn(
      async (path: string, options?: TestGetOptions) => {
        const response = await globalThis.fetch(
          testURL(path, options),
          options?.signal ? { signal: options.signal } : undefined,
        );
        if (!response.ok) {
          return {
            error: await response.json().catch(() => ({})),
            response,
          };
        }
        return {
          data: await response.json(),
          response,
        };
      },
    ),
  } as unknown as TestClient;
}

function testURL(
  path: string,
  options?: TestGetOptions,
): string {
  let url = `/api/v1${path}`;
  for (const [key, value] of Object.entries(options?.params?.path ?? {})) {
    url = url.replace(
      `{${key}}`,
      encodeURIComponent(String(value)),
    );
  }
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(options?.params?.query ?? {})) {
    if (value !== undefined) query.set(key, String(value));
  }
  const qs = query.toString();
  return qs ? `${url}?${qs}` : url;
}

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.removeItem("diff-hide-whitespace");
  localStorage.removeItem("diff-tab-width");
  localStorage.removeItem("diff-collapsed-files");
});

describe("createDiffStore loadDiff", () => {
  it("loads default branch commit diffs through the repo route", async () => {
    const calls: string[] = [];
    const diff = makeDiffResult(["internal/cache.go"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);
        if (url.includes("/repo/github/owner/repo/commits/abc123/diff")) {
          return Response.json(diff);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });

    await store.loadCommitDiff(ownerRepoRef, "abc123");

    expect(calls).toEqual([
      "/api/v1/repo/github/owner/repo/commits/abc123/diff",
    ]);
    expect(store.getDiff()?.files[0]?.path).toBe("internal/cache.go");
  });

  it("refetches default branch commit diffs when toggling whitespace hiding", async () => {
    const calls: string[] = [];
    const diffAll = makeDiffResult(["a.ts", "b.ts"]);
    const diffHidden = makeDiffResult(["a.ts"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);
        if (url.includes("whitespace=hide")) {
          return Response.json(diffHidden);
        }
        if (url.includes("/repo/github/owner/repo/commits/abc123/diff")) {
          return Response.json(diffAll);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });

    await store.loadCommitDiff(ownerRepoRef, "abc123");
    store.setHideWhitespace(true);

    await vi.waitFor(() => {
      expect(store.getDiff()?.files).toHaveLength(1);
    });
    expect(calls).toContain(
      "/api/v1/repo/github/owner/repo/commits/abc123/diff?whitespace=hide",
    );
  });

  it("loads workspace files and the full workspace diff", async () => {
    const calls: string[] = [];
    const files = makeFilesResult([
      "src/app.go",
      "src/app_test.go",
      "docs/plan.md",
    ]);
    const diff = makeDiffResult([
      "src/app.go",
      "src/app_test.go",
      "docs/plan.md",
    ]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);
        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(files);
        }
        if (url.includes("/workspaces/ws-1/diff")) {
          return Response.json(diff);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });

    await store.loadWorkspaceDiff("ws-1", "pushed");

    expect(calls).toContain("/api/v1/workspaces/ws-1/files?base=pushed");
    expect(calls).toContain("/api/v1/workspaces/ws-1/diff?base=pushed");
    expect(store.getVisibleDiffFiles().map((file) => file.path)).toEqual([
      "src/app.go",
      "src/app_test.go",
      "docs/plan.md",
    ]);
    expect(store.getFileCategoryCounts()).toEqual({
      all: 3,
      plansDocs: 1,
      generated: 0,
      code: 1,
      tests: 1,
      other: 0,
    });
  });

  it("loads workspace diffs against the merge target", async () => {
    const calls: string[] = [];
    const files = makeFilesResult(["src/app.go"]);
    const diff = makeDiffResult(["src/app.go"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);
        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(files);
        }
        if (url.includes("/workspaces/ws-1/diff")) {
          return Response.json(diff);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });

    await store.loadWorkspaceDiff("ws-1", "merge-target");

    expect(calls).toContain(
      "/api/v1/workspaces/ws-1/files?base=merge-target",
    );
    expect(calls).toContain(
      "/api/v1/workspaces/ws-1/diff?base=merge-target",
    );
  });

  it("loads commits for the active workspace diff", async () => {
    const calls: string[] = [];
    const files = makeFilesResult(["src/app.go"]);
    const diff = makeDiffResult(["src/app.go"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);
        if (url.includes("/workspaces/ws-1/commits")) {
          return Response.json({
            commits: [
              {
                sha: "sha2",
                message: "second",
                author_name: "Alice",
                authored_at: "2026-01-01T00:00:00Z",
              },
              {
                sha: "sha1",
                message: "first",
                author_name: "Alice",
                authored_at: "2026-01-01T00:00:00Z",
              },
            ],
          });
        }
        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(files);
        }
        if (url.includes("/workspaces/ws-1/diff")) {
          return Response.json(diff);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadWorkspaceDiff("ws-1", "head");
    await store.loadCommits();

    expect(calls).toContain("/api/v1/workspaces/ws-1/commits");
    expect(store.getCommits()?.map((commit) => commit.sha)).toEqual([
      "sha2",
      "sha1",
    ]);
  });

  it("applies selected commit scope to workspace files and patch requests", async () => {
    const calls: string[] = [];
    const files = makeFilesResult(["src/app.go"]);
    const diff = makeDiffResult(["src/app.go"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);
        if (url.includes("/workspaces/ws-1/commits")) {
          return Response.json({
            commits: [
              {
                sha: "sha2",
                message: "second",
                author_name: "Alice",
                authored_at: "2026-01-01T00:00:00Z",
              },
              {
                sha: "sha1",
                message: "first",
                author_name: "Alice",
                authored_at: "2026-01-01T00:00:00Z",
              },
            ],
          });
        }
        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(files);
        }
        if (url.includes("/workspaces/ws-1/diff")) {
          return Response.json(diff);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadWorkspaceDiff("ws-1", "merge-target");
    await store.loadCommits();

    store.selectCommit("sha2");

    await vi.waitFor(() => {
      expect(calls).toContain(
        "/api/v1/workspaces/ws-1/files?base=merge-target&commit=sha2",
      );
      expect(calls).toContain(
        "/api/v1/workspaces/ws-1/diff?base=merge-target&commit=sha2",
      );
    });
  });

  it("applies selected range scope to workspace files and patch requests", async () => {
    const calls: string[] = [];
    const files = makeFilesResult(["src/app.go"]);
    const diff = makeDiffResult(["src/app.go"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);
        if (url.includes("/workspaces/ws-1/commits")) {
          return Response.json({
            commits: [
              {
                sha: "sha3",
                message: "third",
                author_name: "Alice",
                authored_at: "2026-01-01T00:00:00Z",
              },
              {
                sha: "sha2",
                message: "second",
                author_name: "Alice",
                authored_at: "2026-01-01T00:00:00Z",
              },
              {
                sha: "sha1",
                message: "first",
                author_name: "Alice",
                authored_at: "2026-01-01T00:00:00Z",
              },
            ],
          });
        }
        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(files);
        }
        if (url.includes("/workspaces/ws-1/diff")) {
          return Response.json(diff);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadWorkspaceDiff("ws-1", "merge-target");
    await store.loadCommits();

    store.selectRange("sha3", "sha2");

    await vi.waitFor(() => {
      expect(calls).toContain(
        "/api/v1/workspaces/ws-1/files?base=merge-target&from=sha2&to=sha3",
      );
      expect(calls).toContain(
        "/api/v1/workspaces/ws-1/diff?base=merge-target&from=sha2&to=sha3",
      );
    });
  });

  it("preserves workspace scope when switching between single-file and stacked diffs", async () => {
    const calls: string[] = [];
    const files = makeFilesResult(["src/app.go"]);
    const diff = makeDiffResult(["src/app.go"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);
        if (url.includes("/workspaces/ws-1/commits")) {
          return Response.json({
            commits: [
              {
                sha: "sha2",
                message: "second",
                author_name: "Alice",
                authored_at: "2026-01-01T00:00:00Z",
              },
              {
                sha: "sha1",
                message: "first",
                author_name: "Alice",
                authored_at: "2026-01-01T00:00:00Z",
              },
            ],
          });
        }
        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(files);
        }
        if (url.includes("/workspaces/ws-1/diff")) {
          return Response.json(diff);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadWorkspaceDiff("ws-1", "merge-target");
    await store.loadCommits();

    store.selectCommit("sha2");

    await vi.waitFor(() => {
      expect(calls).toContain(
        "/api/v1/workspaces/ws-1/files?base=merge-target&commit=sha2",
      );
    });

    calls.length = 0;
    await store.loadWorkspaceDiff("ws-1", "merge-target", true);

    expect(calls).toContain(
      "/api/v1/workspaces/ws-1/files?base=merge-target&commit=sha2",
    );
    expect(calls).toContain(
      "/api/v1/workspaces/ws-1/diff?base=merge-target&commit=sha2",
    );
    expect(store.getScope()).toEqual({ kind: "commit", sha: "sha2" });
  });

  it("refetches workspace files when toggling whitespace hiding", async () => {
    const calls: string[] = [];
    const filesAll = makeFilesResult(["a.ts", "whitespace.ts"]);
    const filesHidden = makeFilesResult(["a.ts"]);
    const diffAll = makeDiffResult(["a.ts", "whitespace.ts"]);
    const diffHidden = makeDiffResult(["a.ts"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);

        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(
            url.includes("whitespace=hide") ? filesHidden : filesAll,
          );
        }
        if (url.includes("/workspaces/ws-1/diff")) {
          return Response.json(
            url.includes("whitespace=hide") ? diffHidden : diffAll,
          );
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadWorkspaceDiff("ws-1", "head");

    expect(store.getFileList()?.files.map((file) => file.path)).toEqual([
      "a.ts",
      "whitespace.ts",
    ]);

    store.setHideWhitespace(true);
    await vi.waitFor(() => {
      expect(store.getFileList()?.files.map((file) => file.path)).toEqual([
        "a.ts",
      ]);
    });
    await vi.waitFor(() => {
      expect(store.isDiffLoading()).toBe(false);
    });

    expect(calls).toContain(
      "/api/v1/workspaces/ws-1/files?base=head&whitespace=hide",
    );
    expect(calls).toContain(
      "/api/v1/workspaces/ws-1/diff?base=head&whitespace=hide",
    );
  });

  it("scrolls to workspace files without reloading the diff", async () => {
    const calls: string[] = [];
    const files = makeFilesResult(["a.ts", "b.ts"]);
    const diff = makeDiffResult(["a.ts", "b.ts"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        calls.push(url);

        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(files);
        }
        if (url.includes("/workspaces/ws-1/diff")) {
          return Response.json(diff);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadWorkspaceDiff("ws-1", "head");

    store.requestScrollToFile("b.ts");

    expect(store.getActiveFile()).toBe("b.ts");
    expect(store.getScrollTarget()).toEqual({ path: "b.ts" });
    expect(calls.filter((url) =>
      url.includes("/api/v1/workspaces/ws-1/diff"),
    )).toEqual(["/api/v1/workspaces/ws-1/diff?base=head"]);
  });

  it("uses the workspace diff whitespace count", async () => {
    const files = makeFilesResult(["a.ts", "whitespace.ts"], {
      whitespace_only_count: 7,
    });
    const diff = makeDiffResult(["a.ts", "whitespace.ts"]);
    diff.whitespace_only_count = 7;

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;

        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(files);
        }
        if (url.includes("/workspaces/ws-1/diff")) {
          return Response.json(diff);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadWorkspaceDiff("ws-1", "head");

    expect(store.getDiff()?.whitespace_only_count).toBe(7);
  });

  it("clears workspace diff loading when the file list fails", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;

        if (url.includes("/workspaces/ws-1/files")) {
          return Response.json(
            { title: "workspace files failed" },
            { status: 502 },
          );
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadWorkspaceDiff("ws-1", "head");

    expect(store.isDiffLoading()).toBe(false);
    expect(store.getDiffError()).toBe("workspace files failed");
  });

  it("clears stale data when switching PRs", async () => {
    const filesA = makeFilesResult(["a.ts"]);
    const diffA = makeDiffResult(["a.ts"]);
    const filesB = makeFilesResult(["b.ts"]);
    const diffB = makeDiffResult(["b.ts"]);

    // Deferred responses to control resolution order.
    let resolveFilesB: () => void = () => {};
    let resolveDiffB: () => void = () => {};

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;

        // PR A fetches resolve immediately.
        if (url.includes("/1/files")) {
          return Response.json(filesA);
        }
        if (url.includes("/1/diff")) {
          return Response.json(diffA);
        }
        // PR B: both deferred so we control timing explicitly.
        if (url.includes("/2/files")) {
          return new Promise((resolve) => {
            resolveFilesB = () => resolve(Response.json(filesB));
          });
        }
        if (url.includes("/2/diff")) {
          return new Promise((resolve) => {
            resolveDiffB = () => resolve(Response.json(diffB));
          });
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });

    // Load PR A fully.
    await store.loadDiff("owner", "repo", 1, ownerRepoRef);
    expect(store.getDiff()?.files[0]?.path).toBe("a.ts");
    expect(store.getFileList()?.files[0]?.path).toBe("a.ts");

    // Start loading PR B — don't await yet.
    const loadB = store.loadDiff("owner", "repo", 2, ownerRepoRef);

    // Both stale PR A values must be null immediately.
    expect(store.getDiff()).toBeNull();
    expect(store.getFileList()).toBeNull();

    // Release /files for B and let it settle.
    resolveFilesB();
    await vi.waitFor(() => {
      expect(store.getFileList()?.files[0]?.path).toBe("b.ts");
    });

    // Diff still null (not yet resolved).
    expect(store.getDiff()).toBeNull();

    // Release /diff for B.
    resolveDiffB();
    await loadB;

    expect(store.getDiff()?.files[0]?.path).toBe("b.ts");
    expect(store.getFileList()?.files[0]?.path).toBe("b.ts");
  });

  it("aborts in-flight requests when switching PRs", async () => {
    const diffB = makeDiffResult(["b.ts"]);
    const filesB = makeFilesResult(["b.ts"]);

    let diffAAborted = false;
    let filesAAborted = false;

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
            : input.url;
        const signal = input instanceof Request ? input.signal : init?.signal;

        if (url.includes("/1/files")) {
          return new Promise((_resolve, reject) => {
            signal?.addEventListener("abort", () => {
              filesAAborted = true;
              reject(new DOMException("Aborted", "AbortError"));
            });
          });
        }
        if (url.includes("/1/diff")) {
          return new Promise((_resolve, reject) => {
            signal?.addEventListener("abort", () => {
              diffAAborted = true;
              reject(new DOMException("Aborted", "AbortError"));
            });
          });
        }
        if (url.includes("/2/files")) {
          return Response.json(filesB);
        }
        if (url.includes("/2/diff")) {
          return Response.json(diffB);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });

    // Start loading PR A (will hang).
    void store.loadDiff("owner", "repo", 1, ownerRepoRef);

    // Switch to PR B — should abort PR A.
    await store.loadDiff("owner", "repo", 2, ownerRepoRef);

    expect(diffAAborted).toBe(true);
    expect(filesAAborted).toBe(true);
    expect(store.getDiff()?.files[0]?.path).toBe("b.ts");
  });

  it("shows loading when /files fails but /diff still in flight", async () => {
    const diff = makeDiffResult(["a.ts"]);
    let resolveDiff: () => void = () => {};

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;

        if (url.includes("/files")) {
          return Response.json({ detail: "server error" }, { status: 500 });
        }
        if (url.includes("/diff")) {
          return new Promise((resolve) => {
            resolveDiff = () => resolve(Response.json(diff));
          });
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    const loadP = store.loadDiff("owner", "repo", 1, ownerRepoRef);

    // Wait for /files to fail.
    await vi.waitFor(() => {
      expect(store.getFileList()).toBeNull();
    });

    // isFileListLoading must stay true — /diff is still in flight.
    expect(store.isFileListLoading()).toBe(true);

    // Resolve /diff — file list falls through to diff.files.
    resolveDiff();
    await loadP;

    expect(store.isFileListLoading()).toBe(false);
    expect(store.getFileList()?.files[0]?.path).toBe("a.ts");
  });

  it("prefers diff.files over /files for whitespace filtering", async () => {
    // /files returns all files including whitespace-only ones.
    const filesResult = makeFilesResult(["a.ts", "b.ts"]);
    // /diff with whitespace=hide filters out whitespace-only file.
    const diffResult = makeDiffResult(["a.ts"]);

    const fetchedUrls: string[] = [];
    let resolveFiles: () => void = () => {};
    let resolveDiff: () => void = () => {};

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        fetchedUrls.push(url);

        if (url.includes("/files")) {
          return new Promise((resolve) => {
            resolveFiles = () => resolve(Response.json(filesResult));
          });
        }
        if (url.includes("/diff")) {
          return new Promise((resolve) => {
            resolveDiff = () => resolve(Response.json(diffResult));
          });
        }
        return Response.json({}, { status: 404 });
      },
    );

    // Enable whitespace hiding before loading.
    localStorage.setItem("diff-hide-whitespace", "true");
    const store = createDiffStore({ client: testClient() });
    const loadP = store.loadDiff("owner", "repo", 1, ownerRepoRef);

    // Verify /diff request includes whitespace=hide query param.
    expect(fetchedUrls.some((u) => u.includes("diff?whitespace=hide"))).toBe(
      true,
    );

    // /files arrives first — shows unfiltered preview.
    resolveFiles();
    await vi.waitFor(() => {
      expect(store.getFileList()?.files).toHaveLength(2);
    });

    // /diff arrives — authoritative, whitespace-filtered.
    resolveDiff();
    await loadP;

    expect(store.getFileList()?.files).toHaveLength(1);
    expect(store.getFileList()?.files[0]?.path).toBe("a.ts");
  });

  it("does not fall back to stale /files preview after whitespace toggle fails", async () => {
    const filesResult = makeFilesResult(["a.ts", "b.ts"]);
    const diffAll = makeDiffResult(["a.ts", "b.ts"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;

        if (url.includes("/files")) {
          return Response.json(filesResult);
        }
        if (url.includes("/diff")) {
          if (url.includes("whitespace=hide")) {
            // Whitespace-filtered diff request fails.
            return Response.json(
              { detail: "server error" },
              { status: 500 },
            );
          }
          return Response.json(diffAll);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadDiff("owner", "repo", 1, ownerRepoRef);
    expect(store.getFileList()?.files).toHaveLength(2);

    // Toggle whitespace — /diff reload will fail.
    store.setHideWhitespace(true);
    await vi.waitFor(() => {
      expect(store.getDiffError()).toBeTruthy();
    });

    // fileList was cleared by reloadDiffOnly, diff is null from error.
    // Sidebar must NOT fall back to stale unfiltered /files preview.
    expect(store.getFileList()).toBeNull();
  });

  it("clears file list when /diff fails so sidebar shows no stale files", async () => {
    const filesResult = makeFilesResult(["a.ts", "b.ts"]);

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;

        if (url.includes("/files")) {
          return Response.json(filesResult);
        }
        if (url.includes("/diff")) {
          return Response.json({ detail: "server error" }, { status: 500 });
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadDiff("owner", "repo", 1, ownerRepoRef);

    // /diff failed — sidebar must not show stale /files data.
    expect(store.getDiffError()).toBeTruthy();
    expect(store.getFileList()).toBeNull();
  });

  it("clears file list when /diff fails before /files resolves", async () => {
    const filesResult = makeFilesResult(["a.ts"]);
    let resolveFiles: () => void = () => {};

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;

        if (url.includes("/files")) {
          return new Promise((resolve) => {
            resolveFiles = () => resolve(Response.json(filesResult));
          });
        }
        if (url.includes("/diff")) {
          // /diff fails immediately.
          return Response.json({ detail: "server error" }, { status: 500 });
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    const loadP = store.loadDiff("owner", "repo", 1, ownerRepoRef);

    // /diff fails fast, /files still pending — release it.
    resolveFiles();
    await loadP;

    // Late /files must not repopulate sidebar after /diff error.
    expect(store.getDiffError()).toBeTruthy();
    expect(store.getFileList()).toBeNull();
  });

  it("normalizes null files from API to empty array", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;

        if (url.includes("/files")) {
          // API returns files: null (Go nil slice serialization).
          return Response.json({ stale: false, files: null });
        }
        if (url.includes("/diff")) {
          return Response.json({
            stale: false,
            whitespace_only_count: 0,
            files: null,
          });
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadDiff("owner", "repo", 1, ownerRepoRef);

    // getFileList must return [] not null, even when API sends null.
    const result = store.getFileList();
    expect(result).not.toBeNull();
    expect(result!.files).toEqual([]);
  });

  it("filters loaded diff and file list by selected file category", async () => {
    const result: DiffResult = {
      stale: false,
      whitespace_only_count: 0,
      files: [
        {
          path: "docs/review-plan.md",
          old_path: "docs/review-plan.md",
          status: "modified",
          is_binary: false,
          is_whitespace_only: false,
          additions: 1,
          deletions: 1,
          hunks: [],
        },
        {
          path: "src/App.svelte",
          old_path: "src/App.svelte",
          status: "modified",
          is_binary: false,
          is_whitespace_only: false,
          additions: 1,
          deletions: 1,
          hunks: [],
        },
        {
          path: "src/App.test.ts",
          old_path: "src/App.test.ts",
          status: "modified",
          is_binary: false,
          is_whitespace_only: false,
          additions: 1,
          deletions: 1,
          hunks: [],
        },
        {
          path: "bun.lock",
          old_path: "bun.lock",
          status: "modified",
          is_binary: false,
          is_whitespace_only: false,
          additions: 1,
          deletions: 1,
          hunks: [],
        },
      ],
    };

    vi.spyOn(globalThis, "fetch").mockImplementation(
      async (input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;

        if (url.includes("/files")) {
          return Response.json({ stale: false, files: result.files });
        }
        if (url.includes("/diff")) {
          return Response.json(result);
        }
        return Response.json({}, { status: 404 });
      },
    );

    const store = createDiffStore({ client: testClient() });
    await store.loadDiff("owner", "repo", 1, ownerRepoRef);

    expect(store.getFileCategoryFilter()).toBe("all");
    expect(store.getVisibleDiffFiles().map((file) => file.path)).toEqual([
      "docs/review-plan.md",
      "src/App.svelte",
      "src/App.test.ts",
      "bun.lock",
    ]);
    expect(store.getFileCategoryCounts()).toEqual({
      plansDocs: 1,
      generated: 1,
      code: 1,
      tests: 1,
      other: 0,
      all: 4,
    });

    store.setFileCategoryFilter("tests");

    expect(store.getVisibleDiffFiles().map((file) => file.path)).toEqual([
      "src/App.test.ts",
    ]);
    expect(store.getVisibleFileList()?.files.map((file) => file.path)).toEqual([
      "src/App.test.ts",
    ]);
  });
});
