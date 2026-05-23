import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { compile } from "svelte/compiler";
import { afterEach, describe, expect, it, vi } from "vitest";
import componentSource from "./EventTimeline.svelte?raw";
import EventTimeline from "./EventTimeline.svelte";
import type { PREvent } from "../../api/types.js";

const compiledCss = compile(
  componentSource,
  { filename: "EventTimeline.svelte" },
).css?.code ?? "";

function makeEvent(overrides: Partial<PREvent> = {}): PREvent {
  return {
    ID: 1,
    MergeRequestID: 42,
    PlatformID: null,
    EventType: "force_push",
    Author: "alice",
    Body: "",
    Summary: "aaaaaaa -> bbbbbbb",
    MetadataJSON: JSON.stringify({
      before_sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      after_sha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    }),
    DedupeKey: "force-push-1",
    CreatedAt: "2024-06-01T12:00:00Z",
    ...overrides,
  } as PREvent;
}

function findCompiledStyleRule(
  selector: string,
  exclude: string[] = [],
): CSSStyleDeclaration {
  const style = document.createElement("style");
  style.textContent = compiledCss;
  document.head.appendChild(style);

  for (const rule of Array.from(style.sheet?.cssRules ?? [])) {
    if (!("selectorText" in rule) || !("style" in rule)) continue;
    const selectorText = String(rule.selectorText);
    if (
      selectorText.includes(selector)
      && exclude.every((part) => !selectorText.includes(part))
    ) {
      return rule.style as CSSStyleDeclaration;
    }
  }
  throw new Error(`Could not find compiled style rule for ${selector}`);
}

describe("EventTimeline", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("renders force-push label, actor, and SHA transition", () => {
    render(EventTimeline, {
      props: {
        events: [makeEvent()],
      },
    });

    const label = screen.getByText("Force-pushed");
    expect(label).toBeTruthy();
    expect(label.getAttribute("style")).toContain("var(--accent-red)");
    expect(screen.getByText("alice")).toBeTruthy();
    expect(screen.getByText("aaaaaaa -> bbbbbbb")).toBeTruthy();
  });

  it("keeps the timeline entry card while rendering body content without a nested card surface", () => {
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            Body: "Timeline body text",
            EventType: "issue_comment",
          }),
        ],
      },
    });

    const cards = container.querySelectorAll(".event-card");
    const wrapper = cards[0];
    const body = container.querySelector(".event-body");
    const bodyWrap = container.querySelector(".event-body-wrap");
    expect(cards).toHaveLength(1);
    expect(wrapper).toBeInstanceOf(HTMLElement);
    expect(body).toBeInstanceOf(HTMLElement);
    expect(bodyWrap).toBeInstanceOf(HTMLElement);

    expect(wrapper!.contains(bodyWrap)).toBe(true);
    expect(bodyWrap!.contains(body)).toBe(true);
    expect(body!.classList.contains("event-card")).toBe(false);

    const cardStyle = findCompiledStyleRule(".event-card");
    const bodyStyle = findCompiledStyleRule(".event-body", [
      ".event-body-wrap",
      ".markdown-body",
    ]);

    expect(cardStyle.getPropertyValue("background")).toBe("var(--bg-surface)");
    expect(cardStyle.getPropertyValue("border")).toBe("1px solid var(--border-muted)");
    expect(cardStyle.getPropertyValue("border-radius")).toBe("var(--radius-md)");
    expect(bodyStyle.getPropertyValue("background")).toBe("");
    expect(bodyStyle.getPropertyValue("border")).toBe("");
    expect(bodyStyle.getPropertyValue("border-radius")).toBe("");
  });

  it("renders commit events as expanded commit detail rows", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2024-06-01T16:00:00Z"));

    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            EventType: "commit",
            Summary: "abcdef1234567890",
            Body: "feat: add timeline filters\n\nLong body",
          }),
        ],
      },
    });

    expect(screen.getByText("abcdef1")).toBeTruthy();
    expect(
      document.querySelector(".commit-body-details")?.textContent?.trim(),
    ).toBe("feat: add timeline filters\n\nLong body");
    expect(screen.getByText("4h ago")).toBeTruthy();
    expect(document.querySelector(".event--compact")).toBeTruthy();
    expect(document.querySelector(".commit-title")).toBeNull();
    expect(
      document.querySelector(".commit-body-details")?.classList.contains("event-body"),
    ).toBe(true);
    expect(
      document
        .querySelector(".event-header--compact")
        ?.lastElementChild
        ?.classList.contains("event-time"),
    ).toBe(true);
  });

  it("expands single-line commit messages when commit details are shown", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2024-06-01T16:00:00Z"));

    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            EventType: "commit",
            Summary: "abcdef1234567890",
            Body: "refactor: simplify worktree mapping application",
          }),
        ],
      },
    });

    expect(screen.getByText("abcdef1")).toBeTruthy();
    expect(
      document.querySelector(".commit-body-details")?.textContent?.trim(),
    ).toBe("refactor: simplify worktree mapping application");
    expect(document.querySelector(".commit-title")).toBeNull();
  });

  it("can hide commit body details while keeping the title row", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2024-06-01T16:00:00Z"));

    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            EventType: "commit",
            Summary: "abcdef1234567890",
            Body: "feat: add timeline filters\n\nLong body",
          }),
        ],
        showCommitDetails: false,
      },
    });

    expect(screen.getByText("abcdef1")).toBeTruthy();
    expect(screen.getByText("feat: add timeline filters")).toBeTruthy();
    expect(screen.getByText("4h ago")).toBeTruthy();
    expect(screen.queryByText("Long body")).toBeNull();
    expect(
      document
        .querySelector(".event-header--compact")
        ?.lastElementChild
        ?.classList.contains("event-time"),
    ).toBe(true);
  });

  it("renders system events as compact rows", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 2,
            EventType: "renamed_title",
            Summary: `"Old" -> "New"`,
            MetadataJSON: JSON.stringify({
              previous_title: "Old",
              current_title: "New",
            }),
          }),
          makeEvent({
            ID: 3,
            EventType: "base_ref_changed",
            Summary: "main -> release",
            MetadataJSON: JSON.stringify({
              previous_ref_name: "main",
              current_ref_name: "release",
            }),
          }),
          makeEvent({
            ID: 4,
            EventType: "cross_referenced",
            Summary: "Referenced from other/repo#77",
            MetadataJSON: JSON.stringify({
              source_owner: "other",
              source_repo: "repo",
              source_number: 77,
              source_title: "Related bug",
              source_url: "https://github.com/other/repo/issues/77",
            }),
          }),
        ],
      },
    });

    expect(screen.getByText("Title changed")).toBeTruthy();
    expect(screen.getByText("Base changed")).toBeTruthy();
    expect(screen.getByText("Referenced")).toBeTruthy();
    expect(screen.getByText("Related bug")).toBeTruthy();
    expect(document.querySelectorAll(".event--compact").length).toBe(3);
  });

  it("renders cross-repository events as internal item references when metadata identifies the source item", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 4,
            EventType: "cross_referenced",
            Summary: "Referenced from kenn-io/kit#1",
            MetadataJSON: JSON.stringify({
              source_type: "PullRequest",
              source_owner: "kenn-io",
              source_repo: "kit",
              source_number: 1,
              source_title: "Add shared git tooling packages",
              source_url: "https://github.com/kenn-io/kit/pull/1",
            }),
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "kenn-io",
        repoName: "middleman",
        repoPath: "kenn-io/middleman",
      },
    });

    const link = screen.getByRole("link", {
      name: "Add shared git tooling packages",
    });
    const assert = expect.soft;
    assert(link.getAttribute("href")).toBe("/pulls/github/kenn-io/kit/1");
    assert(link.classList.contains("item-ref")).toBe(true);
    assert(link.getAttribute("target")).toBeNull();
    assert(link.getAttribute("rel")).toBeNull();
    assert(link.getAttribute("data-provider")).toBe("github");
    assert(link.getAttribute("data-platform-host")).toBe("github.com");
    assert(link.getAttribute("data-owner")).toBe("kenn-io");
    assert(link.getAttribute("data-name")).toBe("kit");
    assert(link.getAttribute("data-repo-path")).toBe("kenn-io/kit");
    assert(link.getAttribute("data-number")).toBe("1");
    assert(link.getAttribute("data-external-url")).toBe("https://github.com/kenn-io/kit/pull/1");
  });

  it("keeps external cross-reference links when item metadata is incomplete", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 4,
            EventType: "cross_referenced",
            Summary: "Referenced from kenn-io/middleman#377",
            MetadataJSON: JSON.stringify({
              source_title: "external reference",
              source_url: "https://github.com/kenn-io/middleman/pull/377",
            }),
          }),
        ],
        provider: "github",
        platformHost: "github.com",
      },
    });

    const link = screen.getByRole("link", { name: "external reference" });
    expect(link.getAttribute("href")).toBe("https://github.com/kenn-io/middleman/pull/377");
    expect(link.classList.contains("item-ref")).toBe(false);
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  });

  it("falls back to non-link cross-reference text when metadata is invalid", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 5,
            EventType: "cross_referenced",
            Summary: "Referenced from other/repo#77",
            MetadataJSON: "null",
          }),
          makeEvent({
            ID: 6,
            EventType: "cross_referenced",
            Summary: "Referenced from other/repo#78",
            MetadataJSON: JSON.stringify({
              source_title: "Related follow-up",
            }),
          }),
        ],
      },
    });

    expect(screen.getByText("Referenced from other/repo#77")).toBeTruthy();
    expect(screen.getByText("Related follow-up")).toBeTruthy();
    expect(document.querySelectorAll(".system-event-link").length).toBe(0);
  });

  it("shows filtered empty copy when filters hide all events", () => {
    render(EventTimeline, {
      props: {
        events: [],
        filtered: true,
      },
    });

    expect(screen.getByText("No activity matches the current filters")).toBeTruthy();
  });

  it("shows inline edit controls for editable issue comments", async () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            Body: "Original comment",
            EventType: "issue_comment",
            PlatformID: 44,
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        onEditComment: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit comment" }));

    expect(screen.getByRole("button", { name: /save/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /cancel/i })).toBeTruthy();
  });

  it("hides inline edit controls when comment editing is unavailable", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            Body: "Original comment",
            EventType: "issue_comment",
            PlatformID: 44,
          }),
        ],
        repoOwner: "acme",
        repoName: "widget",
        onEditComment: undefined,
      },
    });

    expect(screen.queryByRole("button", { name: "Edit comment" })).toBeNull();
  });
});
