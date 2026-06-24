// @vitest-environment jsdom

import { createQuerySerializer, type QuerySerializerOptions } from "openapi-fetch";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { createRepoBrowserStore } from "./repo-browser.svelte.js";
import type { RepoBrowserStoreOptions } from "./repo-browser.svelte.js";

const repo = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
};

type TestClient = NonNullable<RepoBrowserStoreOptions["client"]>;
type TestGetOptions = {
  params?: { path?: Record<string, string>; query?: Record<string, unknown> };
  querySerializer?: QuerySerializerOptions;
};

const runtimeQuerySerializerOptions: QuerySerializerOptions = {
  array: {
    style: "form",
    explode: false,
  },
};

function testClient(): TestClient {
  return {
    GET: vi.fn(async (path: string, options?: TestGetOptions) => {
      const url = testURL(path, options);
      if (url === "/repo/github/acme/widgets/browser/refs?repo_path=acme%2Fwidgets") {
        return {
          data: {
            repo,
            refs: [
              { type: "branch", name: "main", sha: "main-sha", stale: false },
              { type: "tag", name: "v1.0.0", sha: "tag-sha", stale: false },
            ],
            default_ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-sha"
      ) {
        return {
          data: {
            repo,
            ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
            entries: [
              { path: "README.md", type: "blob", size: 12 },
              { path: "src/app.ts", type: "blob", size: 30 },
            ],
            truncated: false,
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=tag&ref_name=v1.0.0&ref_sha=tag-sha"
      ) {
        return {
          data: {
            repo,
            ref: { type: "tag", name: "v1.0.0", sha: "tag-sha", stale: false },
            entries: [
              { path: "src/app.ts", type: "blob", size: 30 },
              { path: "docs/guide.md", type: "blob", size: 20 },
            ],
            truncated: false,
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md&path=src%2Fapp.ts"
      ) {
        return {
          data: {
            repo,
            ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
            commits: {
              "README.md": commit("readme changed"),
              "src/app.ts": commit("app changed"),
            },
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=tag-sha&path=src%2Fapp.ts&path=docs%2Fguide.md"
      ) {
        return {
          data: {
            repo,
            ref: { type: "tag", name: "v1.0.0", sha: "tag-sha", stale: false },
            commits: {
              "src/app.ts": commit("tag app changed"),
              "docs/guide.md": commit("guide changed"),
            },
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md"
      ) {
        return {
          data: {
            repo,
            ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
            blob: {
              path: "README.md",
              sha: "blob-sha",
              size: 12,
              media_type: "text/markdown; charset=utf-8",
              encoding: "utf-8",
              content: "# Widgets\n",
              binary: false,
              too_large: false,
            },
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md"
      ) {
        return {
          data: {
            repo,
            ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
            path: "README.md",
            commits: [commit("readme changed")],
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=src%2Fapp.ts"
      ) {
        return {
          data: {
            repo,
            ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
            blob: {
              path: "src/app.ts",
              sha: "app-blob-sha",
              size: 30,
              media_type: "text/typescript; charset=utf-8",
              encoding: "utf-8",
              content: "export const app = true;\n",
              binary: false,
              too_large: false,
            },
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=tag-sha&path=src%2Fapp.ts"
      ) {
        return {
          data: {
            repo,
            ref: { type: "tag", name: "v1.0.0", sha: "tag-sha", stale: false },
            blob: {
              path: "src/app.ts",
              sha: "tag-app-blob-sha",
              size: 26,
              media_type: "text/typescript; charset=utf-8",
              encoding: "utf-8",
              content: "export const tag = true;\n",
              binary: false,
              too_large: false,
            },
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=src%2Fapp.ts"
      ) {
        return {
          data: {
            repo,
            ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
            path: "src/app.ts",
            commits: [commit("app changed")],
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=tag-sha&path=src%2Fapp.ts"
      ) {
        return {
          data: {
            repo,
            ref: { type: "tag", name: "v1.0.0", sha: "tag-sha", stale: false },
            path: "src/app.ts",
            commits: [commit("tag app changed")],
          },
          response: new Response(null, { status: 200 }),
        };
      }
      return {
        error: { detail: `unexpected ${url}` },
        response: new Response(null, { status: 404 }),
      };
    }),
  } as unknown as TestClient;
}

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("createRepoBrowserStore", () => {
  it("loads refs, tree metadata, first blob, and file history for a repo", async () => {
    const store = createRepoBrowserStore({ client: testClient() });

    await store.loadRepo(repo);

    expect(store.getDefaultRef()?.name).toBe("main");
    expect(store.getSelectedPath()).toBe("README.md");
    expect(store.getFileEntries().map((entry) => [entry.path, entry.lastChanged?.subject])).toEqual([
      ["README.md", "readme changed"],
      ["src/app.ts", "app changed"],
    ]);
    expect(store.getBlob()?.content).toBe("# Widgets\n");
    expect(store.getFileHistory().map((item) => item.subject)).toEqual(["readme changed"]);
  });

  it("persists source and preview view mode", () => {
    const store = createRepoBrowserStore({ client: testClient() });

    store.setViewMode("preview");

    expect(store.getViewMode()).toBe("preview");
    expect(localStorage.getItem("repo-browser-view-mode")).toBe("preview");
  });

  it("ignores stale blob and history responses after selecting another path", async () => {
    const base = testClient();
    const readmeBlob = deferredResponse();
    const readmeHistory = deferredResponse();
    let deferReadme = false;
    const client = {
      GET: vi.fn((path: string, options?: TestGetOptions) => {
        const url = testURL(path, options);
        if (
          deferReadme &&
          url ===
            "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md"
        ) {
          return readmeBlob.promise;
        }
        if (
          deferReadme &&
          url ===
            "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md"
        ) {
          return readmeHistory.promise;
        }
        return base.GET(path, options);
      }),
    } as unknown as TestClient;
    const store = createRepoBrowserStore({ client });
    await store.loadRepo(repo);

    deferReadme = true;
    const staleReadme = store.selectPath("README.md");
    await store.selectPath("src/app.ts");
    readmeBlob.resolve(blobResponse("README.md", "# stale\n"));
    readmeHistory.resolve(historyResponse("README.md", "stale readme"));
    await staleReadme;

    expect(store.getSelectedPath()).toBe("src/app.ts");
    expect(store.getBlob()?.path).toBe("src/app.ts");
    expect(store.getBlob()?.content).toBe("export const app = true;\n");
    expect(store.getFileHistory().map((item) => item.subject)).toEqual(["app changed"]);
    expect(store.isBlobLoading()).toBe(false);
  });

  it("does not auto-select over a user path selection while last-changed metadata loads", async () => {
    const base = testClient();
    const lastChanged = deferredResponse();
    const client = {
      GET: vi.fn((path: string, options?: TestGetOptions) => {
        const url = testURL(path, options);
        if (
          url ===
          "/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md&path=src%2Fapp.ts"
        ) {
          return lastChanged.promise;
        }
        return base.GET(path, options);
      }),
    } as unknown as TestClient;
    const store = createRepoBrowserStore({ client });

    const load = store.loadRepo(repo);
    await vi.waitFor(() => {
      expect(store.getTree().map((entry) => entry.path)).toEqual(["README.md", "src/app.ts"]);
    });
    const selectedPath = store.selectPath("src/app.ts");
    lastChanged.resolve({
      data: {
        repo,
        ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
        commits: {
          "README.md": commit("readme changed"),
          "src/app.ts": commit("app changed"),
        },
      },
      response: new Response(null, { status: 200 }),
    });

    await Promise.all([load, selectedPath]);

    expect(store.getSelectedPath()).toBe("src/app.ts");
    expect(store.getBlob()?.path).toBe("src/app.ts");
    expect(store.getFileHistory().map((item) => item.subject)).toEqual(["app changed"]);
  });

  it("keeps a user path selection when last-changed metadata fails after tree load", async () => {
    const base = testClient();
    const lastChanged = deferredResponse();
    const client = {
      GET: vi.fn((path: string, options?: TestGetOptions) => {
        const url = testURL(path, options);
        if (
          url ===
          "/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md&path=src%2Fapp.ts"
        ) {
          return lastChanged.promise;
        }
        return base.GET(path, options);
      }),
    } as unknown as TestClient;
    const store = createRepoBrowserStore({ client });

    const load = store.loadRepo(repo);
    await vi.waitFor(() => {
      expect(store.getTree().map((entry) => entry.path)).toEqual(["README.md", "src/app.ts"]);
    });
    const selectedPath = store.selectPath("src/app.ts");
    lastChanged.resolve({
      error: { detail: "last changed failed" },
      response: new Response(null, { status: 500 }),
    });

    await Promise.all([load, selectedPath]);

    expect(store.getTree().map((entry) => entry.path)).toEqual(["README.md", "src/app.ts"]);
    expect(store.getSelectedPath()).toBe("src/app.ts");
    expect(store.getBlob()?.path).toBe("src/app.ts");
    expect(store.getFileHistory().map((item) => item.subject)).toEqual(["app changed"]);
    expect(store.getError()).toBeNull();
  });

  it("clears path-dependent data while a new path is loading", async () => {
    const base = testClient();
    const srcBlob = deferredResponse();
    const srcHistory = deferredResponse();
    let deferSrc = false;
    const client = {
      GET: vi.fn((path: string, options?: TestGetOptions) => {
        const url = testURL(path, options);
        if (
          deferSrc &&
          url ===
            "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=src%2Fapp.ts"
        ) {
          return srcBlob.promise;
        }
        if (
          deferSrc &&
          url ===
            "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=src%2Fapp.ts"
        ) {
          return srcHistory.promise;
        }
        return base.GET(path, options);
      }),
    } as unknown as TestClient;
    const store = createRepoBrowserStore({ client });
    await store.loadRepo(repo);

    deferSrc = true;
    const pending = store.selectPath("src/app.ts");

    expect(store.getSelectedPath()).toBe("src/app.ts");
    expect(store.getBlob()).toBeNull();
    expect(store.getFileHistory()).toEqual([]);
    expect(store.getSelectedCommit()).toBeNull();
    expect(store.isBlobLoading()).toBe(true);

    srcBlob.resolve(blobResponse("src/app.ts", "export const app = true;\n"));
    srcHistory.resolve(historyResponse("src/app.ts", "app changed"));
    await pending;

    expect(store.getBlob()?.path).toBe("src/app.ts");
    expect(store.getFileHistory().map((item) => item.subject)).toEqual(["app changed"]);
    expect(store.isBlobLoading()).toBe(false);
  });

  it("ignores stale commit-detail responses and reports current commit errors", async () => {
    const base = testClient();
    const slowCommit = deferredResponse();
    const client = {
      GET: vi.fn((path: string, options?: TestGetOptions) => {
        const url = testURL(path, options);
        if (
          url ===
          "/repo/github/acme/widgets/browser/commit?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md&sha=slow-sha"
        ) {
          return slowCommit.promise;
        }
        if (
          url ===
          "/repo/github/acme/widgets/browser/commit?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md&sha=fast-sha"
        ) {
          return Promise.resolve(commitResponse("fast-sha", "fast commit"));
        }
        if (
          url ===
          "/repo/github/acme/widgets/browser/commit?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md&sha=missing-sha"
        ) {
          return Promise.resolve({
            error: { detail: "commit failed" },
            response: new Response(null, { status: 404 }),
          });
        }
        return base.GET(path, options);
      }),
    } as unknown as TestClient;
    const store = createRepoBrowserStore({ client });
    await store.loadRepo(repo);

    const stale = store.selectCommit("slow-sha");
    expect(store.getSelectedCommit()).toBeNull();
    await store.selectCommit("fast-sha");
    expect(store.getSelectedCommit()?.sha).toBe("fast-sha");
    slowCommit.resolve(commitResponse("slow-sha", "slow commit"));
    await stale;
    expect(store.getSelectedCommit()?.sha).toBe("fast-sha");

    await store.selectCommit("missing-sha");
    expect(store.getSelectedCommit()).toBeNull();
    expect(store.getError()).toBe("commit failed");
  });

  it("clears dependent state and reports errors when ref switching fails", async () => {
    const base = testClient();
    const client = {
      GET: vi.fn((path: string, options?: TestGetOptions) => {
        const url = testURL(path, options);
        if (
          url ===
          "/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=tag&ref_name=v1.0.0&ref_sha=tag-sha"
        ) {
          return Promise.resolve({
            error: { detail: "tree failed" },
            response: new Response(null, { status: 500 }),
          });
        }
        return base.GET(path, options);
      }),
    } as unknown as TestClient;
    const store = createRepoBrowserStore({ client });
    await store.loadRepo(repo);

    await store.selectRef({ type: "tag", name: "v1.0.0", sha: "tag-sha", stale: false });

    expect(store.getSelectedRef()?.name).toBe("v1.0.0");
    expect(store.getTree()).toEqual([]);
    expect(store.getSelectedPath()).toBeNull();
    expect(store.getBlob()).toBeNull();
    expect(store.getFileHistory()).toEqual([]);
    expect(store.getError()).toBe("tree failed");
    expect(store.isLoading()).toBe(false);
  });

  it("preserves refs when an initial requested ref tree fails", async () => {
    const base = testClient();
    const initialRef = { type: "branch" as const, name: "deleted", sha: "deleted-sha", stale: false };
    const client = {
      GET: vi.fn((path: string, options?: TestGetOptions) => {
        const url = testURL(path, options);
        if (
          url ===
          "/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=deleted&ref_sha=deleted-sha"
        ) {
          return Promise.resolve({
            error: { detail: "tree failed" },
            response: new Response(null, { status: 404 }),
          });
        }
        return base.GET(path, options);
      }),
    } as unknown as TestClient;
    const store = createRepoBrowserStore({ client });

    await store.loadRepo(repo, { ref: initialRef, path: "README.md" });

    expect(store.getRefs().map((ref) => ref.name)).toEqual(["main", "v1.0.0"]);
    expect(store.getDefaultRef()?.name).toBe("main");
    expect(store.getSelectedRef()).toEqual(initialRef);
    expect(store.getTree()).toEqual([]);
    expect(store.getSelectedPath()).toBeNull();
    expect(store.getBlob()).toBeNull();
    expect(store.getFileHistory()).toEqual([]);
    expect(store.getError()).toBe("tree failed");
    expect(store.isLoading()).toBe(false);
  });

  it("preserves the selected path when switching refs if that path still exists", async () => {
    const store = createRepoBrowserStore({ client: testClient() });
    await store.loadRepo(repo);
    await store.selectPath("src/app.ts");

    await store.selectRef({ type: "tag", name: "v1.0.0", sha: "tag-sha", stale: false });

    expect(store.getSelectedRef()?.name).toBe("v1.0.0");
    expect(store.getSelectedPath()).toBe("src/app.ts");
    expect(store.getBlob()?.content).toBe("export const tag = true;\n");
    expect(store.getFileHistory().map((item) => item.subject)).toEqual(["tag app changed"]);
  });

  it("retains an explicit missing initial path instead of selecting an unrelated file", async () => {
    const store = createRepoBrowserStore({ client: testClient() });

    await store.loadRepo(repo, { path: "missing.md" });

    expect(store.getSelectedPath()).toBe("missing.md");
    expect(store.getBlob()).toBeNull();
    expect(store.getFileHistory()).toEqual([]);
    expect(store.getSelectedCommit()).toBeNull();
    expect(store.getError()).toBe("Path not found: missing.md");
  });
});

function testURL(path: string, options?: TestGetOptions): string {
  let url = path;
  for (const [key, value] of Object.entries(options?.params?.path ?? {})) {
    url = url.replace(`{${key}}`, encodeURIComponent(String(value)));
  }
  const serializer = createQuerySerializer(options?.querySerializer ?? runtimeQuerySerializerOptions);
  const qs = serializer(options?.params?.query ?? {});
  return qs ? `${url}?${qs}` : url;
}

function commit(subject: string) {
  return {
    sha: `${subject}-sha`,
    subject,
    body: "",
    author_name: "Alice",
    author_email: "alice@example.com",
    authored_at: "2026-06-01T00:00:00Z",
  };
}

function blobResponse(path: string, content: string) {
  return {
    data: {
      repo,
      ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
      blob: {
        path,
        sha: `${path}-blob-sha`,
        size: content.length,
        media_type: "text/plain; charset=utf-8",
        encoding: "utf-8",
        content,
        binary: false,
        too_large: false,
      },
    },
    response: new Response(null, { status: 200 }),
  };
}

function historyResponse(path: string, subject: string) {
  return {
    data: {
      repo,
      ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
      path,
      commits: [commit(subject)],
    },
    response: new Response(null, { status: 200 }),
  };
}

function commitResponse(sha: string, subject: string) {
  return {
    data: {
      repo,
      ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
      path: "README.md",
      commit: {
        ...commit(subject),
        sha,
      },
    },
    response: new Response(null, { status: 200 }),
  };
}

function deferredResponse() {
  let resolve!: (value: unknown) => void;
  const promise = new Promise<unknown>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}
