// @vitest-environment jsdom

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

function testClient(): TestClient {
  return {
    GET: vi.fn(
      async (
        path: string,
        options?: { params?: { path?: Record<string, string>; query?: Record<string, unknown> } },
      ) => {
        const url = testURL(path, options);
        if (url === "/repo/github/acme/widgets/browser/refs?repo_path=acme%2Fwidgets") {
          return {
            data: {
              repo,
              refs: [
                { type: "branch", name: "main", sha: "main-sha" },
                { type: "tag", name: "v1.0.0", sha: "tag-sha" },
              ],
              default_ref: { type: "branch", name: "main", sha: "main-sha" },
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
              ref: { type: "branch", name: "main", sha: "main-sha" },
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
          "/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-sha&path=README.md&path=src%2Fapp.ts"
        ) {
          return {
            data: {
              repo,
              ref: { type: "branch", name: "main", sha: "main-sha" },
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
          "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-sha&path=README.md"
        ) {
          return {
            data: {
              repo,
              ref: { type: "branch", name: "main", sha: "main-sha" },
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
          "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-sha&path=README.md"
        ) {
          return {
            data: {
              repo,
              ref: { type: "branch", name: "main", sha: "main-sha" },
              path: "README.md",
              commits: [commit("readme changed")],
            },
            response: new Response(null, { status: 200 }),
          };
        }
        return {
          error: { detail: `unexpected ${url}` },
          response: new Response(null, { status: 404 }),
        };
      },
    ),
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
});

function testURL(
  path: string,
  options?: { params?: { path?: Record<string, string>; query?: Record<string, unknown> } },
): string {
  let url = path;
  for (const [key, value] of Object.entries(options?.params?.path ?? {})) {
    url = url.replace(`{${key}}`, encodeURIComponent(String(value)));
  }
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(options?.params?.query ?? {})) {
    if (Array.isArray(value)) {
      for (const item of value) query.append(key, String(item));
    } else if (value !== undefined) {
      query.set(key, String(value));
    }
  }
  const qs = query.toString();
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
