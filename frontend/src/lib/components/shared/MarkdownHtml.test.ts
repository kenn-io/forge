import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import AppRuntimeHarness from "../../../test/AppRuntimeHarness.svelte";
import MarkdownHtml from "./MarkdownHtml.svelte";

describe("MarkdownHtml", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows the synchronous rendering while the owned rich rendering completes", async () => {
    render(AppRuntimeHarness, {
      props: { component: MarkdownHtml, raw: "**Effect-owned**" },
    });

    expect(screen.getByText("Effect-owned").tagName).toBe("STRONG");
    await waitFor(() => expect(screen.getByText("Effect-owned").tagName).toBe("STRONG"));
  });

  it("keeps task checkboxes usable while an attachment check is pending", async () => {
    const pending = vi.fn(() => new Promise<Response>(() => {}));
    vi.stubGlobal("fetch", pending);
    const { container } = render(AppRuntimeHarness, {
      props: {
        component: MarkdownHtml,
        raw: "- [ ] Ship it\n\nhttps://github.com/user-attachments/assets/pending-task-check",
        repo: {
          provider: "github",
          platformHost: "github.com",
          owner: "acme",
          name: "widgets",
          repoPath: "acme/widgets",
        },
        options: { interactiveTasks: true },
      },
    });

    await waitFor(() => expect(pending).toHaveBeenCalled());
    const checkbox = container.querySelector<HTMLInputElement>('input[type="checkbox"]');
    expect(checkbox?.disabled).toBe(false);
    expect(container.querySelector(".task-drag-handle")).not.toBeNull();
  });

  it("highlights code without waiting for an attachment check", async () => {
    const pending = vi.fn(() => new Promise<Response>(() => {}));
    vi.stubGlobal("fetch", pending);
    const { container } = render(AppRuntimeHarness, {
      props: {
        component: MarkdownHtml,
        raw: "```ts\nconst answer = 42;\n```\n\nhttps://github.com/user-attachments/assets/pending-highlight-check",
        repo: {
          provider: "github",
          platformHost: "github.com",
          owner: "acme",
          name: "widgets",
          repoPath: "acme/widgets",
        },
      },
    });

    await waitFor(() => expect(container.querySelector("pre.shiki")).not.toBeNull(), { timeout: 5_000 });
    expect(pending).toHaveBeenCalled();
  });

  it("remounts with the highlighted rendering instead of the plain one", async () => {
    const raw = "```ts\nconst remounted = true;\n```";
    const first = render(AppRuntimeHarness, { props: { component: MarkdownHtml, raw } });
    await waitFor(() => expect(first.container.querySelector("pre.shiki")).not.toBeNull(), { timeout: 5_000 });
    first.unmount();

    const second = render(AppRuntimeHarness, { props: { component: MarkdownHtml, raw } });
    expect(second.container.querySelector("pre.shiki")).not.toBeNull();
  });
});
