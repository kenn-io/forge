import { afterEach, expect, it, vi } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";
import { Effect } from "effect";
import type { PREvent } from "../../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { navigate } from "../../stores/router.svelte.js";
import EventTimelineTestHarness from "./EventTimelineTestHarness.svelte";

let runtime: OwnedAppRuntime | null = null;
afterEach(async () => {
  cleanup();
  vi.restoreAllMocks();
  navigate("/pulls");
  if (runtime) await Effect.runPromise(runtime.disposeEffect);
  runtime = null;
});

const route = "/host/github.com/pulls/github/example/widgets/42";
const comments = Array.from({ length: 12 }, (_, index) => ({
  ID: index + 1,
  MergeRequestID: 42,
  PlatformID: index + 101,
  EventType: "issue_comment",
  Author: "contributor",
  Body: `Comment ${index + 1}\n\n${"Discussion text. ".repeat(100)}`,
  Summary: "",
  MetadataJSON: "",
  DedupeKey: `comment-${index + 101}`,
  CreatedAt: `2026-01-01T12:${String(index).padStart(2, "0")}:00Z`,
})) as PREvent[];
const timelineProps = {
  events: comments,
  provider: "github",
  platformHost: "github.com",
  repoOwner: "example",
  repoName: "widgets",
  repoPath: "example/widgets",
  number: 42,
  itemType: "pull" as const,
  initialEntryLimit: 1,
  itemIdentity: "example/widgets/42",
};

function focusedComment(): string | null {
  return document.activeElement?.getAttribute("data-comment-id") ?? null;
}

it("reveals provider comment IDs beyond the initial batch and repeats navigation", async () => {
  runtime = makeAppRuntime();
  navigate(`${route}#issuecomment-101`);
  await render(EventTimelineTestHarness, { props: { runtime, timelineProps } });
  await expect.poll(focusedComment).toBe("1");
  const target = document.activeElement as HTMLElement;
  expect(target.textContent).toContain("Comment 1");
  expect(target.getBoundingClientRect().top).toBeGreaterThanOrEqual(0);
  expect(target.getBoundingClientRect().top).toBeLessThan(window.innerHeight);

  navigate(`${route}#issuecomment-112`);
  await expect.poll(focusedComment).toBe("12");
  (document.activeElement as HTMLElement).blur();
  window.scrollTo(0, document.body.scrollHeight);
  navigate(`${route}#issuecomment-112`);
  await expect.poll(focusedComment).toBe("12");
  expect(document.activeElement!.getBoundingClientRect().top).toBeLessThan(window.innerHeight);
});

it("waits for discussion data and lets the latest navigation win", async () => {
  runtime = makeAppRuntime();
  navigate(`${route}#issuecomment-101`);
  const view = await render(EventTimelineTestHarness, {
    props: { runtime, timelineProps: { ...timelineProps, events: [] } },
  });
  navigate(`${route}#issuecomment-112`);
  await view.rerender({ runtime, timelineProps });
  await expect.poll(focusedComment).toBe("12");
  navigate(`${route}#issuecomment-101`);
  navigate("/host/github.com/pulls/github/example/widgets/99#issuecomment-101");
  await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
  expect(focusedComment()).not.toBe("1");
});

it("reports a missing comment without focusing an unrelated event", async () => {
  runtime = makeAppRuntime();
  navigate(`${route}#issuecomment-999`);
  await render(EventTimelineTestHarness, { props: { runtime, timelineProps } });
  await expect.poll(() => document.body.textContent).toContain("Comment not found in this discussion.");
  expect(focusedComment()).toBeNull();
});

it("reveals a filtered review reply and expands compact comments", async () => {
  runtime = makeAppRuntime();
  const root = { ...comments[0]!, EventType: "review_comment", ThreadID: "thread-example" };
  const reply = { ...comments[1]!, EventType: "review_comment", ThreadID: "thread-example" };
  navigate(`${route}#discussion_r102`);
  const view = await render(EventTimelineTestHarness, {
    props: { runtime, timelineProps: { ...timelineProps, events: [], orderingEvents: [root, reply], filtered: true } },
  });
  await expect.poll(focusedComment).toBe("2");
  expect(document.activeElement?.textContent).toContain("Comment 2");
  await view.rerender({
    runtime,
    timelineProps: { ...timelineProps, events: [root, reply], activityViewMode: "compact" },
  });
  navigate(`${route}#discussion_r101`);
  await expect.poll(focusedComment).toBe("1");
  expect(document.activeElement?.textContent).toContain("Comment 1");
});

it("cancels a pending scroll when another item opens", async () => {
  runtime = makeAppRuntime();
  navigate(`${route}#issuecomment-101`);
  await render(EventTimelineTestHarness, { props: { runtime, timelineProps } });
  await expect.poll(focusedComment).toBe("1");
  (document.activeElement as HTMLElement).blur();
  const callbacks: FrameRequestCallback[] = [];
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
    callbacks.push(callback);
    return callbacks.length;
  });
  const cancel = vi.spyOn(window, "cancelAnimationFrame").mockImplementation(() => {});
  navigate(`${route}#issuecomment-112`);
  await expect.poll(() => callbacks.length).toBeGreaterThan(0);
  navigate("/host/github.com/pulls/github/example/widgets/99#issuecomment-112");
  await expect.poll(() => cancel.mock.calls.length).toBeGreaterThan(0);
  for (const callback of callbacks) callback(performance.now());
  await Promise.resolve();
  expect(focusedComment()).toBeNull();
});

it("reveals a reply inside a provider-hidden thread", async () => {
  runtime = makeAppRuntime();
  const root = {
    ...comments[0]!,
    EventType: "review_comment",
    ThreadID: "hidden-thread",
    MetadataJSON: JSON.stringify({ provider_hidden: true, provider_hidden_reason: "OUTDATED" }),
  };
  const reply = { ...comments[1]!, EventType: "review_comment", ThreadID: "hidden-thread" };
  navigate(`${route}#discussion_r102`);
  await render(EventTimelineTestHarness, {
    props: { runtime, timelineProps: { ...timelineProps, events: [root, reply] } },
  });
  await expect.poll(focusedComment).toBe("2");
  expect(document.activeElement?.textContent).toContain("Comment 2");
});
