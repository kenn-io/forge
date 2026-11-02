import { Effect } from "effect";
import { mount, unmount } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import "@kenn-io/kit-ui/theme.css";

import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import UserListEditorTestHarness from "./UserListEditorTestHarness.svelte";

describe("UserListEditor picker position", () => {
  let host: HTMLElement | null = null;
  let app: Record<string, unknown> | null = null;
  let runtime: OwnedAppRuntime | null = null;

  afterEach(async () => {
    if (app) unmount(app);
    host?.remove();
    if (runtime) await Effect.runPromise(runtime.disposeEffect);
    app = null;
    host = null;
    runtime = null;
  });

  it("follows its chip when a sibling that renders later pushes the chip sideways", async () => {
    await page.viewport(1000, 700);
    host = document.createElement("div");
    host.style.cssText = "display: flex; gap: 6px; align-items: center; padding: 40px";
    const sibling = document.createElement("span");
    sibling.style.cssText = "display: inline-block; flex-shrink: 0; width: 1px; height: 20px";
    const editorHost = document.createElement("span");
    host.append(sibling, editorHost);
    document.body.append(host);

    runtime = makeAppRuntime();
    app = mount(UserListEditorTestHarness, {
      target: editorHost,
      props: {
        runtime,
        editorProps: {
          label: "Assignees",
          users: ["alice"],
          canEdit: true,
          loadCandidates: () => Effect.succeed(["alice", "bob"]),
          onchange: vi.fn(),
        },
      },
    });

    const trigger = host.querySelector<HTMLButtonElement>("button[aria-label='Edit assignees']");
    expect(trigger).not.toBeNull();
    trigger!.click();
    const dialog = await vi.waitFor(() => {
      const candidate = document.querySelector<HTMLElement>("[role='dialog'][aria-label='Edit assignees']");
      expect(candidate).not.toBeNull();
      expect(
        Math.abs(candidate!.getBoundingClientRect().left - trigger!.getBoundingClientRect().left),
      ).toBeLessThanOrEqual(1);
      return candidate!;
    });

    // A chip that renders once its data arrives, such as the PR size
    // sparkline, widens without any window resize or scroll event.
    const before = trigger!.getBoundingClientRect().left;
    sibling.style.width = "76px";
    expect(trigger!.getBoundingClientRect().left - before).toBe(75);

    await vi.waitFor(() => {
      expect(Math.abs(dialog.getBoundingClientRect().left - trigger!.getBoundingClientRect().left)).toBeLessThanOrEqual(
        1,
      );
    });
  });
});
