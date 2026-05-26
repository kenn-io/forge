import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { compile } from "svelte/compiler";
import { afterEach, describe, expect, it, vi } from "vitest";
import componentSource from "./EventTimeline.svelte?raw";
import EventTimeline from "./EventTimeline.svelte";
import { STORES_KEY } from "../../context.js";
import type { DiffResult, PREvent } from "../../api/types.js";
import type { DiffStore } from "../../stores/diff.svelte.js";

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
    ThreadID: null,
    Resolvable: false,
    Resolved: false,
    ...overrides,
  } as PREvent;
}

function makeReviewThreadEvent(overrides: Partial<PREvent> = {}): PREvent {
  return makeEvent({
    EventType: "review_comment",
    Body: "Please keep this setup explicit.",
    Summary: "",
    diff_thread: {
      id: "thread-1",
      path: "src/review.ts",
      side: "right",
      start_side: "right",
      start_line: 10,
      line: 11,
      new_line: 11,
      line_type: "add",
      body: "Please keep this setup explicit.",
      author_login: "alice",
      resolved: false,
      can_resolve: true,
      created_at: "2024-06-01T12:00:00Z",
      updated_at: "2024-06-01T12:00:00Z",
    },
    ...overrides,
  } as Partial<PREvent>);
}

function makeDiffStore(overrides: Partial<DiffStore> = {}): DiffStore {
  const diff: DiffResult = {
    stale: false,
    whitespace_only_count: 0,
    files: [{
      path: "src/review.ts",
      old_path: "src/review.ts",
      status: "modified",
      is_binary: false,
      is_whitespace_only: false,
      additions: 2,
      deletions: 0,
      hunks: [{
        old_start: 9,
        old_count: 1,
        new_start: 9,
        new_count: 3,
        lines: [
          { type: "context", old_num: 9, new_num: 9, content: "const client = setup();" },
          { type: "add", new_num: 10, content: "client.enableReviews();" },
          { type: "add", new_num: 11, content: "client.publishThreads();" },
          { type: "context", old_num: 10, new_num: 12, content: "return client;" },
        ],
      }],
    }],
  };

  return {
    getDiff: () => diff,
    isDiffLoading: () => false,
    getCurrentPR: () => ({ owner: "acme", name: "widget", number: 7 }),
    loadDiff: vi.fn(),
    requestScrollToLine: vi.fn(),
    ...overrides,
  } as unknown as DiffStore;
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

  it("groups discussion comments with the root comment first and reverse-chronological replies", () => {
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 4,
            EventType: "issue_comment",
            Author: "root",
            Body: "Newest threaded reply",
            ThreadID: "disc-1",
            CreatedAt: "2024-06-01T12:03:00Z",
          }),
          makeEvent({
            ID: 3,
            EventType: "issue_comment",
            Author: "root",
            Body: "Middle threaded reply",
            ThreadID: "disc-1",
            CreatedAt: "2024-06-01T12:02:00Z",
          }),
          makeEvent({
            ID: 2,
            EventType: "issue_comment",
            Author: "root",
            Body: "Oldest threaded reply",
            ThreadID: "disc-1",
            CreatedAt: "2024-06-01T12:01:00Z",
          }),
          makeEvent({
            ID: 1,
            EventType: "issue_comment",
            Author: "root",
            Body: "Main threaded comment",
            ThreadID: "disc-1",
            CreatedAt: "2024-06-01T12:00:00Z",
          }),
          makeEvent({
            ID: 5,
            EventType: "commit",
            Summary: "abcdef1234567890",
            Body: "Add fixture",
            CreatedAt: "2024-06-01T11:59:00Z",
          }),
        ],
      },
    });

    expect(container.querySelectorAll(".event")).toHaveLength(2);
    expect(container.querySelectorAll(".thread-reply")).toHaveLength(3);
    expect(screen.getByRole("list", { name: "Threaded replies" })).toBeTruthy();

    const threadText = container.querySelector(".event-card")?.textContent ?? "";
    expect(threadText.indexOf("Main threaded comment")).toBeLessThan(
      threadText.indexOf("Newest threaded reply"),
    );
    expect(threadText.indexOf("Newest threaded reply")).toBeLessThan(
      threadText.indexOf("Middle threaded reply"),
    );
    expect(threadText.indexOf("Middle threaded reply")).toBeLessThan(
      threadText.indexOf("Oldest threaded reply"),
    );
  });

  it("renders positioned discussion threads with the same root and reply ordering", () => {
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 12,
            EventType: "issue_comment",
            Author: "author",
            Body: "Pushed an update",
            ThreadID: "disc-positioned",
            CreatedAt: "2024-06-01T12:02:00Z",
          }),
          makeEvent({
            ID: 10,
            EventType: "issue_comment",
            Author: "reviewer",
            Body: "This needs a named helper",
            ThreadID: "disc-positioned",
            diff_thread: {
              id: "disc-positioned",
              provider_comment_id: "10",
              path: "src/review.ts",
              old_path: "src/review.ts",
              side: "right",
              line: 11,
              new_line: 11,
              line_type: "add",
              diff_head_sha: "head-sha",
              commit_sha: "head-sha",
              body: "This needs a named helper",
              author_login: "reviewer",
              resolved: false,
              can_resolve: false,
              created_at: "2024-06-01T12:00:00Z",
              updated_at: "2024-06-01T12:00:00Z",
            },
            CreatedAt: "2024-06-01T12:00:00Z",
          }),
          makeEvent({
            ID: 11,
            EventType: "issue_comment",
            Author: "reviewer",
            Body: "The wrapper should stay close to the call site",
            ThreadID: "disc-positioned",
            CreatedAt: "2024-06-01T12:01:00Z",
          }),
        ],
        provider: "gitlab",
        platformHost: "gitlab.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
      },
      context: new Map([
        [STORES_KEY, {
          diff: makeDiffStore(),
          diffReviewDraft: {
            setRouteContext: vi.fn(),
            isSubmitting: () => false,
          },
        }],
      ]),
    });

    expect(screen.getByText("src/review.ts:11")).toBeTruthy();
    expect(screen.getByText("client.publishThreads();")).toBeTruthy();
    expect(container.querySelectorAll(".thread-reply")).toHaveLength(2);

    const threadText = container.querySelector(".event-card")?.textContent ?? "";
    expect(threadText.indexOf("This needs a named helper")).toBeLessThan(
      threadText.indexOf("Pushed an update"),
    );
    expect(threadText.indexOf("Pushed an update")).toBeLessThan(
      threadText.indexOf("The wrapper should stay close to the call site"),
    );
  });

  it("can collapse and expand threaded replies", async () => {
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 2,
            EventType: "issue_comment",
            Author: "root",
            Body: "Threaded reply",
            ThreadID: "disc-1",
            CreatedAt: "2024-06-01T12:01:00Z",
          }),
          makeEvent({
            ID: 1,
            EventType: "issue_comment",
            Author: "root",
            Body: "Main threaded comment",
            ThreadID: "disc-1",
            CreatedAt: "2024-06-01T12:00:00Z",
          }),
        ],
      },
    });

    expect(container.querySelectorAll(".thread-reply")).toHaveLength(1);

    await fireEvent.click(screen.getByRole("button", { name: /hide 1 reply/i }));
    expect(container.querySelectorAll(".thread-reply")).toHaveLength(0);
    expect(screen.getByRole("button", { name: /show 1 reply/i })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: /show 1 reply/i }));
    expect(container.querySelectorAll(".thread-reply")).toHaveLength(1);
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
            ID: 1,
            EventType: "comment_deleted",
            Author: "maintainer",
            Summary: "deleted a comment from reviewer",
            MetadataJSON: JSON.stringify({
              deleted_comment_author: "reviewer",
            }),
          }),
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
          makeEvent({
            ID: 5,
            EventType: "assigned",
            Author: "wesm",
            Summary: "self-assigned this",
            MetadataJSON: JSON.stringify({
              assignee: "wesm",
            }),
          }),
        ],
      },
    });

    expect(screen.queryByText("Comment deleted")).toBeNull();
    expect(screen.getByText("maintainer")).toBeTruthy();
    expect(screen.getByText("deleted a comment from reviewer")).toBeTruthy();
    const deletedHeader = document.querySelector(".event-header--compact");
    const deletedChildren = Array.from(deletedHeader?.children ?? []);
    expect(deletedChildren).toHaveLength(3);
    expect(deletedChildren[0]?.classList.contains("event-author")).toBe(true);
    expect(deletedChildren[1]?.classList.contains("system-event-summary")).toBe(true);
    expect(deletedChildren[1]?.classList.contains("system-event-summary--sentence")).toBe(true);
    expect(deletedChildren[2]?.classList.contains("event-time")).toBe(true);
    expect(screen.getByText("Title changed")).toBeTruthy();
    expect(screen.getByText("Base changed")).toBeTruthy();
    expect(screen.getByText("Referenced")).toBeTruthy();
    expect(screen.getByText("Related bug")).toBeTruthy();
    expect(screen.queryByText("Assigned")).toBeNull();
    expect(screen.getByText("self-assigned this")).toBeTruthy();
    expect(document.querySelectorAll(".event--compact").length).toBe(5);
  });

  it("renders cross-repository events as internal item references when metadata identifies the source item", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2024-06-01T16:00:00Z"));

    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 4,
            EventType: "cross_referenced",
            Summary: "Referenced from kenn-io/kit#1",
            CreatedAt: "2024-06-01T14:00:00Z",
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
    assert(screen.getByText("2h ago")).toBeTruthy();
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

  it("shows review thread diff context and exposes a jump action", async () => {
    const jumpToReviewThread = vi.fn();
    const diff = makeDiffStore();

    render(EventTimeline, {
      props: {
        events: [makeReviewThreadEvent()],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        jumpToReviewThread,
      },
      context: new Map([
        [STORES_KEY, {
          diff,
          diffReviewDraft: {
            setRouteContext: vi.fn(),
            isSubmitting: () => false,
          },
        }],
      ]),
    });

    expect(screen.getByText("src/review.ts:10-11")).toBeTruthy();
    expect(screen.getByText("client.publishThreads();")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Jump to diff" }));

    expect(jumpToReviewThread).toHaveBeenCalledWith(
      expect.objectContaining({ id: "thread-1", path: "src/review.ts" }),
    );
  });

  it("marks review thread context outdated when the line is absent from the loaded diff", () => {
    const diff = makeDiffStore({
      getDiff: () => ({
        stale: false,
        whitespace_only_count: 0,
        files: [],
      }),
    });

    render(EventTimeline, {
      props: {
        events: [makeReviewThreadEvent()],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
      },
      context: new Map([
        [STORES_KEY, {
          diff,
          diffReviewDraft: {
            setRouteContext: vi.fn(),
            isSubmitting: () => false,
          },
        }],
      ]),
    });

    expect(screen.getByText("Outdated")).toBeTruthy();
    expect(screen.getByText("Diff context is no longer present in the loaded diff.")).toBeTruthy();
  });
});
