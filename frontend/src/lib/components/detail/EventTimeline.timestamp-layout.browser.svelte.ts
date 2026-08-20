import { afterEach, describe, expect, it } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";
import { Effect } from "effect";

import "../../../app.css";
import type { PREvent } from "../../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import EventTimelineTestHarness from "./EventTimelineTestHarness.svelte";

const forcePushEvent = {
  ID: 1,
  MergeRequestID: 42,
  PlatformID: null,
  EventType: "force_push",
  Author: "alice",
  Body: "",
  Summary: "7b48e66 -> 94aa952",
  MetadataJSON: JSON.stringify({
    before_sha: "7b48e66000000000000000000000000000000000",
    after_sha: "94aa952000000000000000000000000000000000",
  }),
  DedupeKey: "force-push-1",
  CreatedAt: "2026-08-20T12:00:00Z",
  ThreadID: null,
  Resolvable: false,
  Resolved: false,
} as PREvent;

let runtime: OwnedAppRuntime | null = null;

afterEach(async () => {
  cleanup();
  if (runtime) await Effect.runPromise(runtime.disposeEffect);
  runtime = null;
});

describe("EventTimeline timestamp layout", () => {
  it("keeps a force-push timestamp in the card's trailing metadata slot", () => {
    runtime = makeAppRuntime();
    const wrapper = document.createElement("div");
    wrapper.style.width = "760px";
    document.body.appendChild(wrapper);

    render(EventTimelineTestHarness, {
      target: wrapper,
      props: {
        runtime,
        timelineProps: { events: [forcePushEvent] },
      },
    });

    const card = wrapper.querySelector<HTMLElement>(".kit-comment-card.event-card--compact");
    const timestamp = card?.querySelector<HTMLElement>(".kit-card__meta");
    expect(card).not.toBeNull();
    expect(timestamp).not.toBeNull();

    const cardRect = card!.getBoundingClientRect();
    const timestampRect = timestamp!.getBoundingClientRect();
    expect(cardRect.right - timestampRect.right).toBeLessThanOrEqual(24);

    wrapper.remove();
  });
});
