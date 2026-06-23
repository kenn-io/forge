// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { createQuerySerializer, type QuerySerializerOptions } from "openapi-fetch";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { MiddlemanClient } from "@middleman/ui";
import RepoBrowserFeature from "./RepoBrowserFeature.svelte";

const repo = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
};

const route = {
  page: "repo-browser",
  provider: "github",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  path: "README.md",
  mode: "preview",
} as const;

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

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("RepoBrowserFeature", () => {
  it("preserves markdown anchor fragments when opening repo docs", async () => {
    Element.prototype.scrollIntoView = vi.fn();
    const onRouteChange = vi.fn();
    render(RepoBrowserFeature, {
      props: {
        client: testClient(),
        route,
        onRouteChange,
      },
    });

    await fireEvent.click(await screen.findByRole("link", { name: "Guide" }));

    await waitFor(() => {
      expect(onRouteChange).toHaveBeenLastCalledWith(
        expect.objectContaining({
          path: "docs/guide.md",
          viewMode: "preview",
          anchor: "install",
        }),
        undefined,
      );
    });
  });
});

function testClient(): MiddlemanClient {
  return {
    GET: vi.fn(async (path: string, options?: TestGetOptions) => {
      const url = testURL(path, options);
      if (url === "/repo/github/acme/widgets/browser/refs?repo_path=acme%2Fwidgets") {
        return {
          data: {
            repo,
            refs: [{ type: "branch", name: "main", sha: "main-sha", stale: false }],
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
              { path: "README.md", type: "blob", size: 31 },
              { path: "docs/guide.md", type: "blob", size: 18 },
            ],
            truncated: false,
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-sha&path=README.md&path=docs%2Fguide.md"
      ) {
        return {
          data: {
            repo,
            ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
            commits: {},
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-sha&path=README.md"
      ) {
        return blobResponse("README.md", "[Guide](docs/guide.md#install)\n");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-sha&path=README.md"
      ) {
        return historyResponse("README.md");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-sha&path=docs%2Fguide.md"
      ) {
        return blobResponse("docs/guide.md", "# Install\n");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-sha&path=docs%2Fguide.md"
      ) {
        return historyResponse("docs/guide.md");
      }
      return {
        error: { detail: `unexpected ${url}` },
        response: new Response(null, { status: 404 }),
      };
    }),
  } as unknown as MiddlemanClient;
}

function testURL(path: string, options?: TestGetOptions): string {
  let url = path;
  for (const [key, value] of Object.entries(options?.params?.path ?? {})) {
    url = url.replace(`{${key}}`, encodeURIComponent(String(value)));
  }
  const serializer = createQuerySerializer(options?.querySerializer ?? runtimeQuerySerializerOptions);
  const qs = serializer(options?.params?.query ?? {});
  return qs ? `${url}?${qs}` : url;
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
        media_type: "text/markdown; charset=utf-8",
        encoding: "utf-8",
        content,
        binary: false,
        too_large: false,
      },
    },
    response: new Response(null, { status: 200 }),
  };
}

function historyResponse(path: string) {
  return {
    data: {
      repo,
      ref: { type: "branch", name: "main", sha: "main-sha", stale: false },
      path,
      commits: [],
    },
    response: new Response(null, { status: 200 }),
  };
}
