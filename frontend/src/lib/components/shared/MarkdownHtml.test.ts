import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import AppRuntimeHarness from "../../../test/AppRuntimeHarness.svelte";
import MarkdownHtml from "./MarkdownHtml.svelte";

describe("MarkdownHtml", () => {
  afterEach(cleanup);

  it("shows the synchronous rendering while the owned rich rendering completes", async () => {
    render(AppRuntimeHarness, {
      props: { component: MarkdownHtml, raw: "**Effect-owned**" },
    });

    expect(screen.getByText("Effect-owned").tagName).toBe("STRONG");
    await waitFor(() => expect(screen.getByText("Effect-owned").tagName).toBe("STRONG"));
  });
});
