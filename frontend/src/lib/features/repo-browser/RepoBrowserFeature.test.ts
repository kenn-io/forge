// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { createQuerySerializer, type QuerySerializerOptions } from "openapi-fetch";
import { tick } from "svelte";
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

    await screen.findByRole("heading", { name: "Install" });
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

  it("renders markdown issue references as provider item links", async () => {
    render(RepoBrowserFeature, {
      props: {
        client: testClient(),
        route,
        onRouteChange: vi.fn(),
      },
    });

    const issueLink = await screen.findByRole("link", { name: "#12" });

    expect(issueLink.classList.contains("item-ref")).toBe(true);
    expect(issueLink.getAttribute("href")).toBe("/issues/github/acme/widgets/12");
    expect(issueLink.getAttribute("data-provider")).toBe("github");
    expect(issueLink.getAttribute("data-repo-path")).toBe("acme/widgets");
    expect(issueLink.getAttribute("data-external-url")).toBe("https://github.com/acme/widgets/issues/12");
  });

  it("applies route anchor changes for the selected markdown file", async () => {
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    const client = testClient();
    const onRouteChange = vi.fn();
    const { rerender } = render(RepoBrowserFeature, {
      props: {
        client,
        route: {
          ...route,
          path: "docs/guide.md",
          anchor: "install",
        },
        onRouteChange,
      },
    });

    await screen.findByRole("heading", { name: "Install" });
    await waitFor(() => expect(scrolledHeadingIDs(scrollIntoView)).toContain("install"));

    scrollIntoView.mockClear();
    await rerender({
      client,
      route: {
        ...route,
        path: "docs/guide.md",
        anchor: "usage",
      },
      onRouteChange,
    });

    await screen.findByRole("heading", { name: "Usage" });
    await waitFor(() => expect(scrolledHeadingIDs(scrollIntoView)).toContain("usage"));
  });

  it("does not reload the repo when the resolved branch SHA is added to the route", async () => {
    const client = testClient();
    const onRouteChange = vi.fn();
    const { rerender } = render(RepoBrowserFeature, {
      props: {
        client,
        route: {
          ...route,
          refType: "branch",
          refName: "main",
          path: undefined,
        },
        onRouteChange,
      },
    });

    await waitFor(() => expect(client.GET).toHaveBeenCalledTimes(5));
    await waitFor(() => {
      expect(onRouteChange).toHaveBeenLastCalledWith(
        expect.objectContaining({
          refType: "branch",
          refName: "main",
          refSHA: "main-sha",
          path: "README.md",
        }),
        { replace: true },
      );
    });

    await rerender({
      client,
      route: {
        ...route,
        refType: "branch",
        refName: "main",
        refSHA: "main-sha",
        path: "README.md",
      },
      onRouteChange,
    });

    await tick();
    expect(client.GET).toHaveBeenCalledTimes(5);
  });

  it("reloads the repo when the same branch route moves to a different resolved SHA", async () => {
    const client = testClient();
    const onRouteChange = vi.fn();
    const { rerender } = render(RepoBrowserFeature, {
      props: {
        client,
        route: {
          ...route,
          refType: "branch",
          refName: "main",
          refSHA: "main-sha",
          path: "README.md",
        },
        onRouteChange,
      },
    });

    await waitFor(() => expect(client.GET).toHaveBeenCalledTimes(5));
    await rerender({
      client,
      route: {
        ...route,
        refType: "branch",
        refName: "main",
        refSHA: "main-next-sha",
        path: "README.md",
      },
      onRouteChange,
    });

    await waitFor(() => expect(client.GET).toHaveBeenCalledTimes(10));
    const calls = (client.GET as unknown as { mock: { calls: Array<[string, TestGetOptions | undefined]> } }).mock
      .calls;
    const urls = calls.map(([path, options]) => testURL(path, options));
    expect(urls).toContain(
      "/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-next-sha",
    );
  });

  it("does not reload the repo when a no-ref route is self-replaced with the default ref", async () => {
    const client = testClient();
    const onRouteChange = vi.fn();
    const { rerender } = render(RepoBrowserFeature, {
      props: {
        client,
        route: {
          ...route,
          path: undefined,
        },
        onRouteChange,
      },
    });

    await waitFor(() => expect(client.GET).toHaveBeenCalledTimes(5));
    await waitFor(() => {
      expect(onRouteChange).toHaveBeenLastCalledWith(
        expect.objectContaining({
          refType: "branch",
          refName: "main",
          refSHA: "main-sha",
          path: "README.md",
        }),
        { replace: true },
      );
    });

    await rerender({
      client,
      route: {
        ...route,
        refType: "branch",
        refName: "main",
        refSHA: "main-sha",
        path: "README.md",
      },
      onRouteChange,
    });

    await tick();
    expect(client.GET).toHaveBeenCalledTimes(5);
  });

  it("does not reload the repo again after a user ref change updates the route", async () => {
    const client = testClient();
    const onRouteChange = vi.fn();
    const { rerender } = render(RepoBrowserFeature, {
      props: {
        client,
        route,
        onRouteChange,
      },
    });

    await waitFor(() => expect(client.GET).toHaveBeenCalledTimes(5));
    await fireEvent.change(await screen.findByRole("combobox", { name: "Select repository ref" }), {
      target: { value: "tag\0v1.0.0\0tag-sha" },
    });
    await waitFor(() => expect(client.GET).toHaveBeenCalledTimes(9));
    await waitFor(() => {
      expect(onRouteChange).toHaveBeenLastCalledWith(
        expect.objectContaining({
          refType: "tag",
          refName: "v1.0.0",
          refSHA: "tag-sha",
          path: "README.md",
        }),
        undefined,
      );
    });

    await rerender({
      client,
      route: {
        ...route,
        refType: "tag",
        refName: "v1.0.0",
        refSHA: "tag-sha",
      },
      onRouteChange,
    });

    await tick();
    expect(client.GET).toHaveBeenCalledTimes(9);
  });
});

function scrolledHeadingIDs(scrollIntoView: ReturnType<typeof vi.fn>): string[] {
  return scrollIntoView.mock.contexts.flatMap((context) => {
    if (context instanceof HTMLElement && context.id) return [context.id];
    return [];
  });
}

function testClient(): MiddlemanClient {
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
        "/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=main-next-sha"
      ) {
        return {
          data: {
            repo,
            ref: { type: "branch", name: "main", sha: "main-next-sha", stale: false },
            entries: [
              { path: "README.md", type: "blob", size: 13 },
              { path: "docs/guide.md", type: "blob", size: 18 },
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
        "/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md&path=docs%2Fguide.md"
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
        "/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-next-sha&path=README.md&path=docs%2Fguide.md"
      ) {
        return {
          data: {
            repo,
            ref: { type: "branch", name: "main", sha: "main-next-sha", stale: false },
            commits: {},
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=tag-sha&path=README.md&path=docs%2Fguide.md"
      ) {
        return {
          data: {
            repo,
            ref: { type: "tag", name: "v1.0.0", sha: "tag-sha", stale: false },
            commits: {},
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md"
      ) {
        return blobResponse("README.md", "[Guide](docs/guide.md#install)\n\nSee #12.\n");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-next-sha&path=README.md"
      ) {
        return blobResponse("README.md", "Updated README\n");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=tag-sha&path=README.md"
      ) {
        return blobResponse("README.md", "[Guide](docs/guide.md#install)\n\nSee #12.\n");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=README.md"
      ) {
        return historyResponse("README.md");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-next-sha&path=README.md"
      ) {
        return historyResponse("README.md");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=tag-sha&path=README.md"
      ) {
        return historyResponse("README.md");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=docs%2Fguide.md"
      ) {
        return blobResponse("docs/guide.md", "# Install\n\n## Usage\n");
      }
      if (
        url ===
        "/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=main-sha&path=docs%2Fguide.md"
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
