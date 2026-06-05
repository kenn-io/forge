import { cleanup, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import RepoTypeahead from "./RepoTypeahead.svelte";
import { getAllCheatsheetEntries, resetRegistry } from "../stores/keyboard/registry.svelte.js";

describe("RepoTypeahead cheatsheet entries", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response("[]", {
            status: 200,
            headers: { "content-type": "application/json" },
          }),
      ),
    );
  });

  afterEach(() => {
    cleanup();
    resetRegistry();
    vi.unstubAllGlobals();
  });

  it("registers arrow-key navigation entries on mount", () => {
    render(RepoTypeahead, {
      props: { selected: undefined, onchange: () => {} },
    });
    const ids = getAllCheatsheetEntries().map((e) => e.id);
    expect(ids).toEqual(expect.arrayContaining(["repo-typeahead.next", "repo-typeahead.prev"]));
  });
});
