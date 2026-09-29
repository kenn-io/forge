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

const commitEvent = (id: number, subject: string): PREvent =>
  ({
    ...forcePushEvent,
    ID: id,
    EventType: "commit",
    Summary: `abcdef${id}000000000`,
    Body: subject,
    MetadataJSON: "",
    DedupeKey: `commit-${id}`,
  }) as PREvent;

let runtime: OwnedAppRuntime | null = null;

afterEach(async () => {
  cleanup();
  if (runtime) await Effect.runPromise(runtime.disposeEffect);
  runtime = null;
});

describe("EventTimeline timestamp layout", () => {
  it("keeps a force-push timestamp in the card's trailing metadata slot", async () => {
    runtime = makeAppRuntime();
    const wrapper = document.createElement("div");
    wrapper.style.width = "760px";
    document.body.appendChild(wrapper);

    await render(EventTimelineTestHarness, {
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

  it("keeps a short system event on one row", async () => {
    runtime = makeAppRuntime();
    const wrapper = document.createElement("div");
    wrapper.style.width = "760px";
    document.body.appendChild(wrapper);

    await render(EventTimelineTestHarness, {
      target: wrapper,
      props: {
        runtime,
        timelineProps: { events: [forcePushEvent] },
      },
    });

    const card = wrapper.querySelector<HTMLElement>(".kit-comment-card.event-card--compact");
    const parts = [".kit-card__eyebrow", ".kit-card__title", ".system-event-summary", ".kit-card__meta"].map(
      (selector) => card?.querySelector<HTMLElement>(selector)?.getBoundingClientRect(),
    );
    expect(parts.every(Boolean)).toBe(true);
    const middles = parts.map((rect) => rect!.top + rect!.height / 2);
    expect(Math.max(...middles) - Math.min(...middles)).toBeLessThanOrEqual(3);

    wrapper.remove();
  });

  it("packs consecutive one-line commits into tight rows", async () => {
    runtime = makeAppRuntime();
    const wrapper = document.createElement("div");
    wrapper.style.width = "760px";
    document.body.appendChild(wrapper);

    await render(EventTimelineTestHarness, {
      target: wrapper,
      props: {
        runtime,
        timelineProps: { events: [commitEvent(2, "feat: add cache store"), commitEvent(3, "fix: expire entries")] },
      },
    });

    const cards = [...wrapper.querySelectorAll<HTMLElement>(".event-card--commit")];
    expect(cards).toHaveLength(2);
    const [first, second] = cards.map((card) => card.getBoundingClientRect());
    expect(first!.height).toBeLessThanOrEqual(56);
    expect(second!.top - first!.bottom).toBeLessThanOrEqual(8);

    wrapper.remove();
  });
});
