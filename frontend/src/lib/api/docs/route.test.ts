import { describe, expect, test } from "vite-plus/test";

import { defaultDocsRoute, docsHref, docsSearch, parseDocsRoute, type DocsRoute } from "./route";

describe("docs route helpers", () => {
  test("reads folder and document query params", () => {
    expect(parseDocsRoute("?folder=notes&doc=README.md")).toMatchObject({
      mode: "docs",
      folder: "notes",
      doc: "README.md",
    });
  });

  test("treats empty query params as absent", () => {
    expect(parseDocsRoute("?folder=&doc=")).toMatchObject(defaultDocsRoute);
  });

  test("serializes folder and document params", () => {
    const route: DocsRoute = { mode: "docs", folder: "notes", doc: "Projects/launch plan.md" };

    expect(docsSearch(route)).toBe("?folder=notes&doc=Projects%2Flaunch+plan.md");
  });

  test("builds docs hrefs from route state", () => {
    const route: DocsRoute = { mode: "docs", folder: "notes", doc: "README.md" };

    expect(docsHref(route)).toBe("/docs?folder=notes&doc=README.md");
    expect(docsHref(defaultDocsRoute)).toBe("/docs");
  });
});
