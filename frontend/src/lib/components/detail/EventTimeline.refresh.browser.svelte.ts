import { afterEach, expect, it } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";
import { Effect } from "effect";

import type { PREvent } from "../../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { STORES_KEY } from "../../context.js";
import { createDetailStore } from "../../stores/detail.svelte.js";
import EventTimelineTestHarness from "./EventTimelineTestHarness.svelte";

let runtime: OwnedAppRuntime | null = null;

afterEach(async () => {
  cleanup();
  if (runtime) await Effect.runPromise(runtime.disposeEffect);
  runtime = null;
});

it("preserves highlighted review comments and expanded details when the timeline refreshes", async () => {
  runtime = makeAppRuntime();
  const event = {
    ID: 1,
    MergeRequestID: 42,
    PlatformID: 1,
    EventType: "review_comment",
    Author: "alice",
    Body: "```ts\nconst answer = 42;\n```\n\n<details><summary>Explanation</summary>Keep this open.</details>",
    Summary: "",
    MetadataJSON: "",
    DedupeKey: "review-comment-1",
    CreatedAt: "2026-08-20T12:00:00Z",
    ThreadID: "thread-1",
    Resolvable: true,
    Resolved: false,
    diff_thread: { id: "thread-1", path: "src/example.ts", line: 1, side: "right", resolved: false },
  } as PREvent;
  const timelineProps = {
    events: [event],
    provider: "github",
    repoOwner: "acme",
    repoName: "widget",
    repoPath: "acme/widget",
    number: 42,
    canReplyToThreads: true,
  };
  const context = new Map([[STORES_KEY, { detail: createDetailStore({ runtime }) }]]);
  const view = await render(EventTimelineTestHarness, { props: { runtime, timelineProps, context } });
  await expect.poll(() => document.querySelector(".event-body pre.shiki"), { timeout: 5_000 }).not.toBeNull();
  const code = document.querySelector(".event-body pre.shiki");
  const details = document.querySelector<HTMLDetailsElement>(".event-body details")!;
  details.open = true;

  await view.rerender({ runtime, context, timelineProps: { ...timelineProps, events: structuredClone([event]) } });

  expect(document.querySelector(".event-body pre.shiki")).toBe(code);
  expect(document.querySelector<HTMLDetailsElement>(".event-body details")?.open).toBe(true);

  await view.rerender({
    runtime,
    context,
    timelineProps: { ...timelineProps, events: [{ ...event, Body: "Updated comment" }] },
  });
  expect(document.querySelector(".event-body")?.textContent).toContain("Updated comment");
  const reply = document.querySelector<HTMLButtonElement>("[data-thread-reply-inline]")!;
  reply.click();
  await expect
    .poll(() => document.querySelector("[data-thread-reply-inline]")?.getAttribute("aria-expanded"))
    .toBe("true");
});
