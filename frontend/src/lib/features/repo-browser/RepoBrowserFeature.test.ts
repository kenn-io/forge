// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { createQuerySerializer, type QuerySerializerOptions } from "openapi-fetch";
import { tick } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { MiddlemanClient } from "@middleman/ui";

vi.mock("./PierreFileContents.svelte", async () => ({
  default: (await import("./RepoBrowserFeatureTestPierreFileContents.svelte")).default,
}));

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

type TestClientOptions = {
  sourceFile?: {
    path: string;
    content: string;
  };
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

  it("hides zero-count file category filters", async () => {
    render(RepoBrowserFeature, {
      props: {
        client: testClient(),
        route,
        onRouteChange: vi.fn(),
      },
    });

    expect(await screen.findByRole("button", { name: "Plans/docs 2" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "All 2" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Code 0" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Tests 0" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Other 0" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Generated 0" })).toBeNull();
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

  it("renders source files through Pierre file contents", async () => {
    const sourceFile = {
      path: "internal/main.go",
      content: "package main\n\nfunc main() {}\n",
    };
    render(RepoBrowserFeature, {
      props: {
        client: testClient({ sourceFile }),
        route: {
          ...route,
          path: sourceFile.path,
          mode: "source",
        },
        onRouteChange: vi.fn(),
      },
    });

    const viewer = await screen.findByTestId("repo-browser-pierre-file-contents");

    expect(viewer.getAttribute("data-path")).toBe(sourceFile.path);
    expect(viewer.textContent).toContain("package main");
  });
});

function scrolledHeadingIDs(scrollIntoView: ReturnType<typeof vi.fn>): string[] {
  return scrollIntoView.mock.contexts.flatMap((context) => {
    if (context instanceof HTMLElement && context.id) return [context.id];
    return [];
  });
}

function testClient(clientOptions: TestClientOptions = {}): MiddlemanClient {
  return {
    GET: vi.fn(async (path: string, options?: TestGetOptions) => {
      const url = testURL(path, options);
      const treeEntries = testTreeEntries(clientOptions);
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
            entries: treeEntries,
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
            entries: treeEntries,
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
            entries: treeEntries,
            truncated: false,
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (url === lastChangedURL("main-sha", treeEntries)) {
        return lastChangedResponse("main-sha");
      }
      if (url === lastChangedURL("main-next-sha", treeEntries)) {
        return lastChangedResponse("main-next-sha");
      }
      if (url === lastChangedURL("tag-sha", treeEntries)) {
        return lastChangedResponse("tag-sha");
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
      if (clientOptions.sourceFile && url === sourceEndpointURL("blob", "main-sha", clientOptions.sourceFile.path)) {
        const file = clientOptions.sourceFile;
        return blobResponse(file.path, file.content);
      }
      if (clientOptions.sourceFile && url === sourceEndpointURL("history", "main-sha", clientOptions.sourceFile.path)) {
        return historyResponse(clientOptions.sourceFile.path);
      }
      return {
        error: { detail: `unexpected ${url}` },
        response: new Response(null, { status: 404 }),
      };
    }),
  } as unknown as MiddlemanClient;
}

function testTreeEntries(options: TestClientOptions) {
  return [
    { path: "README.md", type: "blob", size: 31 },
    { path: "docs/guide.md", type: "blob", size: 18 },
    ...(options.sourceFile
      ? [{ path: options.sourceFile.path, type: "blob", size: options.sourceFile.content.length }]
      : []),
  ];
}

function lastChangedURL(refSHA: string, entries: ReturnType<typeof testTreeEntries>): string {
  const paths = entries.map((entry) => `path=${encodeURIComponent(entry.path)}`).join("&");
  return `/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=${refSHA}&${paths}`;
}

function sourceEndpointURL(endpoint: "blob" | "history", refSHA: string, path: string): string {
  return `/repo/github/acme/widgets/browser/${endpoint}?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha=${refSHA}&path=${encodeURIComponent(path)}`;
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

function lastChangedResponse(refSHA: string) {
  return {
    data: {
      repo,
      ref: { type: "branch", name: "main", sha: refSHA, stale: false },
      commits: {},
    },
    response: new Response(null, { status: 200 }),
  };
}
