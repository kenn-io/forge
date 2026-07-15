import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { compile } from "svelte/compiler";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vite-plus/test";
import componentSource from "./EventTimeline.svelte?raw";
import EventTimeline from "./EventTimeline.svelte";
import { STORES_KEY } from "../../context.js";
import { copyToClipboard } from "@kenn-io/kit-ui";
import type { DiffResult, PREvent } from "../../api/types.js";
import type { DiffStore } from "../../stores/diff.svelte.js";
import { getStackDepth, resetModalStack } from "../../stores/keyboard/modal-stack.svelte.js";

vi.mock("@kenn-io/kit-ui", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@kenn-io/kit-ui")>()),
  copyToClipboard: vi.fn(() => Promise.resolve(true)),
}));

const compiledCss = compile(componentSource, { filename: "EventTimeline.svelte" }).css?.code ?? "";

type GlobalWithResizeObserver = { ResizeObserver?: unknown };
type GlobalWithCSSStyleSheet = {
  CSSStyleSheet?: {
    prototype: CSSStyleSheet & { replaceSync?: (text: string) => void };
  };
};
let originalResizeObserver: unknown;
let originalResizeObserverExisted = false;
let originalReplaceSync: unknown;

beforeAll(() => {
  originalResizeObserverExisted = "ResizeObserver" in globalThis;
  originalResizeObserver = (globalThis as GlobalWithResizeObserver).ResizeObserver;
  class ResizeObserverStub {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  (globalThis as GlobalWithResizeObserver).ResizeObserver = ResizeObserverStub;

  originalReplaceSync = (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype.replaceSync;
  if ((globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype) {
    (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync ??= function replaceSync(): void {};
  }
});

afterAll(() => {
  if (originalResizeObserverExisted) {
    (globalThis as GlobalWithResizeObserver).ResizeObserver = originalResizeObserver;
  } else {
    delete (globalThis as GlobalWithResizeObserver).ResizeObserver;
  }
  if ((globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype) {
    if (originalReplaceSync) {
      (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync = originalReplaceSync as (
        text: string,
      ) => void;
    } else {
      delete (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync;
    }
  }
});

afterEach(() => {
  vi.mocked(copyToClipboard).mockClear();
  resetModalStack();
});

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
    files: [
      {
        path: "src/review.ts",
        old_path: "src/review.ts",
        status: "modified",
        is_binary: false,
        is_whitespace_only: false,
        additions: 2,
        deletions: 0,
        hunks: [
          {
            old_start: 9,
            old_count: 1,
            new_start: 9,
            new_count: 3,
            lines: [
              {
                type: "context",
                old_num: 9,
                new_num: 9,
                content: "const client = setup();",
              },
              {
                type: "add",
                new_num: 10,
                content: "client.enableReviews();",
              },
              {
                type: "add",
                new_num: 11,
                content: "client.publishThreads();",
              },
              {
                type: "context",
                old_num: 10,
                new_num: 12,
                content: "return client;",
              },
            ],
          },
        ],
      },
    ],
  };

  return {
    getDiff: () => diff,
    isDiffLoading: () => false,
    getCurrentPR: () => ({ owner: "acme", name: "widget", number: 7 }),
    getTabWidth: () => 4,
    loadDiff: vi.fn(),
    requestScrollToLine: vi.fn(),
    ...overrides,
  } as unknown as DiffStore;
}

function findCompiledStyleRule(selector: string, exclude: string[] = []): CSSStyleDeclaration {
  const style = document.createElement("style");
  style.textContent = compiledCss;
  document.head.appendChild(style);
  const selectorParts = selector.split(/\s+/).filter(Boolean);

  for (const rule of Array.from(style.sheet?.cssRules ?? [])) {
    if (!("selectorText" in rule) || !("style" in rule)) continue;
    const selectorText = String(rule.selectorText);
    if (
      selectorParts.every((part) => selectorText.includes(part)) &&
      exclude.every((part) => !selectorText.includes(part))
    ) {
      return rule.style as CSSStyleDeclaration;
    }
  }
  throw new Error(`Could not find compiled style rule for ${selector}`);
}

async function expectPierreTimelineText(pattern: RegExp): Promise<void> {
  await waitFor(() => {
    const host = document.querySelector(".thread-code .pierre-diff");
    expect(host?.shadowRoot?.textContent).toMatch(pattern);
  });
}

async function expectSuggestionPierreText(pattern: RegExp): Promise<void> {
  await waitFor(() => {
    const host = document.querySelector(".review-suggestion .pierre-diff");
    expect(host?.shadowRoot?.textContent).toMatch(pattern);
  });
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

  it("renders lifecycle event labels with actor bylines", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 3,
            EventType: "merged",
            Author: "merge-admin",
            Summary: "merged this",
            CreatedAt: "2024-06-01T12:03:00Z",
          }),
          makeEvent({
            ID: 2,
            EventType: "closed",
            Author: "reviewer",
            Summary: "closed this",
            CreatedAt: "2024-06-01T12:02:00Z",
          }),
          makeEvent({
            ID: 1,
            EventType: "reopened",
            Author: "maintainer",
            Summary: "reopened this",
            CreatedAt: "2024-06-01T12:01:00Z",
          }),
        ],
      },
    });

    expect(screen.getByText("Merged")).toBeTruthy();
    expect(screen.getByText("Closed")).toBeTruthy();
    expect(screen.getByText("Reopened")).toBeTruthy();
    const authors = Array.from(document.querySelectorAll(".event-author")).map((element) =>
      element.textContent?.replace(/\s+/g, " ").trim(),
    );
    expect(authors).toContain("by merge-admin");
    expect(authors).toContain("by reviewer");
    expect(authors).toContain("by maintainer");
    expect(document.querySelector(".event-author-prefix")?.textContent).toBe("by");
    expect(document.querySelector(".event-author--lifecycle")?.textContent?.replace(/\s+/g, " ").trim()).toBe(
      "by merge-admin",
    );
    const prefixStyle = findCompiledStyleRule(".event-author-prefix");
    expect(prefixStyle.color).toBe("var(--text-muted)");
    expect(prefixStyle.fontWeight).toBe("400");
    expect(compiledCss).toContain(".event-author--lifecycle");
    expect(compiledCss).toContain("margin-left: calc(var(--focus-detail-space-xs, 0.46rem) * -0.5)");
    expect(screen.queryByText("merged this")).toBeNull();
    expect(screen.queryByText("closed this")).toBeNull();
    expect(screen.queryByText("reopened this")).toBeNull();
    expect(document.querySelectorAll(".event--compact")).toHaveLength(3);
  });

  it("uses merged status styling for merged lifecycle events", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            EventType: "merged",
            Summary: "merged this",
          }),
        ],
      },
    });

    const label = screen.getByText("Merged");
    const dot = document.querySelector(".dot");
    expect(label.getAttribute("style")).toContain("var(--accent-purple)");
    expect(dot?.getAttribute("style")).toContain("var(--accent-purple)");
  });

  it("renders compact activity lifecycle rows with actor bylines", () => {
    const { container } = render(EventTimeline, {
      props: {
        activityViewMode: "compact",
        events: [
          makeEvent({
            EventType: "merged",
            Author: "alice",
            Summary: "merged this",
          }),
        ],
      },
    });

    const row = container.querySelector<HTMLElement>(".event-card--compact-row");
    const rowText = row?.textContent?.replace(/\s+/g, " ").trim() ?? "";
    expect(rowText).toContain("Merged by alice");
    expect(rowText).not.toContain("merged this");
    expect(row?.querySelector(".event-author-prefix")?.textContent).toBe("by");
    expect(row?.querySelector(".compact-event-summary")?.textContent?.trim()).toBe("");
  });

  it("collapses duplicate merge lifecycle rows into the single authored transition", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 3,
            EventType: "merged",
            Author: "mariusvniekerk",
            Summary: "merged this",
            CreatedAt: "2024-06-01T12:03:00Z",
            DedupeKey: "timeline-merge-provider",
          }),
          makeEvent({
            ID: 2,
            EventType: "closed",
            Author: "mariusvniekerk",
            Summary: "closed this",
            CreatedAt: "2024-06-01T12:03:00Z",
            DedupeKey: "timeline-close-provider",
          }),
          makeEvent({
            ID: 1,
            EventType: "merged",
            Author: "",
            Summary: "merged this",
            CreatedAt: "2024-06-01T12:03:00Z",
            DedupeKey: "timeline-merge-fallback",
          }),
        ],
      },
    });

    expect(screen.getAllByText("Merged")).toHaveLength(1);
    expect(screen.queryByText("Closed")).toBeNull();
    expect(screen.queryByText("closed this")).toBeNull();
    expect(document.querySelector(".event--compact")?.textContent).toContain("by mariusvniekerk");
    expect(document.querySelectorAll(".event--compact")).toHaveLength(1);
  });

  it("uses the authored close transition when an anonymous merged row is coalesced", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 1,
            EventType: "merged",
            Author: "",
            Summary: "merged this",
            CreatedAt: "2024-06-01T12:03:00Z",
            DedupeKey: "timeline-merge-fallback",
          }),
          makeEvent({
            ID: 2,
            EventType: "closed",
            Author: "merge-admin",
            Summary: "closed this",
            CreatedAt: "2024-06-01T12:03:00Z",
            DedupeKey: "timeline-close-provider",
          }),
        ],
      },
    });

    expect(screen.getAllByText("Merged")).toHaveLength(1);
    expect(screen.queryByText("Closed")).toBeNull();
    expect(document.querySelector(".event--compact")?.textContent).toContain("by merge-admin");
    expect(screen.queryByText("merged this")).toBeNull();
    expect(document.querySelectorAll(".event--compact")).toHaveLength(1);
  });

  it("keeps pre-merge close lifecycle rows when the PR was reopened before merging", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 3,
            EventType: "merged",
            Author: "mariusvniekerk",
            Summary: "merged this",
            CreatedAt: "2024-06-01T12:01:00Z",
          }),
          makeEvent({
            ID: 2,
            EventType: "reopened",
            Author: "mariusvniekerk",
            Summary: "reopened this",
            CreatedAt: "2024-06-01T12:00:45Z",
          }),
          makeEvent({
            ID: 1,
            EventType: "closed",
            Author: "mariusvniekerk",
            Summary: "closed this",
            CreatedAt: "2024-06-01T12:00:30Z",
          }),
        ],
      },
    });

    expect(screen.getByText("Merged")).toBeTruthy();
    expect(screen.getByText("Reopened")).toBeTruthy();
    expect(screen.getByText("Closed")).toBeTruthy();
    const authors = Array.from(document.querySelectorAll(".event-author")).map((element) =>
      element.textContent?.replace(/\s+/g, " ").trim(),
    );
    expect(authors.filter((author) => author === "by mariusvniekerk")).toHaveLength(3);
    expect(screen.queryByText("closed this")).toBeNull();
    expect(document.querySelectorAll(".event--compact")).toHaveLength(3);
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
    const bodyStyle = findCompiledStyleRule(".event-body", [".event-body-wrap", ".markdown-body"]);

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
    expect(threadText.indexOf("Main threaded comment")).toBeLessThan(threadText.indexOf("Newest threaded reply"));
    expect(threadText.indexOf("Newest threaded reply")).toBeLessThan(threadText.indexOf("Middle threaded reply"));
    expect(threadText.indexOf("Middle threaded reply")).toBeLessThan(threadText.indexOf("Oldest threaded reply"));
  });

  it("renders positioned discussion threads with the same root and reply ordering", async () => {
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
        [
          STORES_KEY,
          {
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    expect(screen.getByText("src/review.ts:11")).toBeTruthy();
    await expectPierreTimelineText(/client\.publishThreads\(\);/);
    expect(container.querySelectorAll(".thread-reply")).toHaveLength(2);

    const threadText = container.querySelector(".event-card")?.textContent ?? "";
    expect(threadText.indexOf("This needs a named helper")).toBeLessThan(threadText.indexOf("Pushed an update"));
    expect(threadText.indexOf("Pushed an update")).toBeLessThan(
      threadText.indexOf("The wrapper should stay close to the call site"),
    );
  });

  it("renders GitHub suggestion fences as applicable diff blocks", async () => {
    const applySuggestion = vi.fn(async () => true);
    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            Body: ["This can return directly.", "", "```suggestion", "return client.publishThreads();", "```"].join(
              "\r\n",
            ),
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              diff_head_sha: "abc123",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        currentHeadSHA: "abc123",
        onApplySuggestion: applySuggestion,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    expect(screen.getByText("This can return directly.")).toBeTruthy();
    expect(screen.queryByText("```suggestion")).toBeNull();
    await expectSuggestionPierreText(/return client\.publishThreads\(\);/);

    await fireEvent.click(screen.getByRole("button", { name: "Commit suggestion" }));
    expect(applySuggestion).toHaveBeenCalledWith({
      suggestions: [
        {
          threadID: "thread-1",
          replacement: "return client.publishThreads();",
        },
      ],
    });
  });

  it("blocks concurrent individual and batch suggestion submissions", async () => {
    let resolveApplication: ((value: boolean) => void) | undefined;
    const applySuggestion = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          resolveApplication = resolve;
        }),
    );
    const baseThread = makeReviewThreadEvent().diff_thread!;
    const first = makeReviewThreadEvent({
      ID: 1,
      CreatedAt: "2024-06-01T12:01:00Z",
      Body: ["First suggestion.", "", "```suggestion", "return firstSuggestion();", "```"].join("\n"),
      diff_thread: { ...baseThread, id: "thread-1", diff_head_sha: "abc123" },
    });
    const second = makeReviewThreadEvent({
      ID: 2,
      CreatedAt: "2024-06-01T12:02:00Z",
      Body: ["Second suggestion.", "", "```suggestion", "return secondSuggestion();", "```"].join("\n"),
      diff_thread: { ...baseThread, id: "thread-2", diff_head_sha: "abc123" },
    });
    const { container } = render(EventTimeline, {
      props: {
        events: [first, second],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        currentHeadSHA: "abc123",
        onApplySuggestion: applySuggestion,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    await waitFor(() => {
      expect(screen.getAllByRole("button", { name: "Add suggestion to batch" })).toHaveLength(2);
    });
    await fireEvent.click(screen.getAllByRole("button", { name: "Add suggestion to batch" })[1]!);
    await fireEvent.click(screen.getAllByRole("button", { name: "Commit suggestion" })[0]!);
    await waitFor(() => expect(applySuggestion).toHaveBeenCalledTimes(1));

    const suggestionActions = Array.from(
      container.querySelectorAll<HTMLButtonElement>(".review-suggestion__actions button"),
    );
    expect(suggestionActions.every((button) => button.disabled)).toBe(true);
    expect((screen.getByRole("button", { name: "Commit batch" }) as HTMLButtonElement).disabled).toBe(true);

    await fireEvent.click(suggestionActions[2]!);
    await fireEvent.click(screen.getByRole("button", { name: "Commit batch" }));
    expect(applySuggestion).toHaveBeenCalledTimes(1);

    resolveApplication?.(true);
    await waitFor(() => {
      expect(
        Array.from(container.querySelectorAll<HTMLButtonElement>(".review-suggestion__actions button")).every(
          (button) => !button.disabled,
        ),
      ).toBe(true);
    });
  });

  it("shows an inline error only when suggestion application reports a durable conflict", async () => {
    const applySuggestion = vi.fn(async () => ({
      ok: false,
      error: "pull request state changed",
    }));
    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            Body: ["This can return directly.", "", "```suggestion", "return client.publishThreads();", "```"].join(
              "\n",
            ),
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              diff_head_sha: "abc123",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        currentHeadSHA: "abc123",
        onApplySuggestion: applySuggestion,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    expect(screen.queryByText("pull request state changed")).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Commit suggestion" }));

    await waitFor(() => {
      expect(screen.getByText("pull request state changed")).toBeTruthy();
    });
  });

  it("does not reuse a stale detail error for a generic suggestion failure", async () => {
    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            Body: ["Try this.", "", "```suggestion", "return publish();", "```"].join("\n"),
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              diff_head_sha: "abc123",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        currentHeadSHA: "abc123",
        onApplySuggestion: vi.fn(async () => false),
      },
      context: new Map([
        [
          STORES_KEY,
          {
            detail: { getDetailError: () => "unrelated previous error" },
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Commit suggestion" }));
    await waitFor(() => expect(screen.queryByText("unrelated previous error")).toBeNull());
  });

  it("keeps hidden selected suggestions in the batch apply request", async () => {
    const applySuggestion = vi.fn(async () => true);
    const baseThread = makeReviewThreadEvent().diff_thread!;
    const first = makeReviewThreadEvent({
      ID: 1,
      CreatedAt: "2024-06-01T12:01:00Z",
      Body: ["First suggestion.", "", "```suggestion", "return firstSuggestion();", "```"].join("\n"),
      diff_thread: {
        ...baseThread,
        id: "thread-1",
        diff_head_sha: "abc123",
      },
    });
    const second = makeReviewThreadEvent({
      ID: 2,
      CreatedAt: "2024-06-01T12:02:00Z",
      Body: ["Second suggestion.", "", "```suggestion", "return secondSuggestion();", "```"].join("\n"),
      diff_thread: {
        ...baseThread,
        id: "thread-2",
        diff_head_sha: "abc123",
      },
    });
    const props = {
      events: [first, second],
      provider: "github",
      platformHost: "github.com",
      repoOwner: "acme",
      repoName: "widget",
      repoPath: "acme/widget",
      number: 7,
      currentHeadSHA: "abc123",
      onApplySuggestion: applySuggestion,
    };
    const context = new Map([
      [
        STORES_KEY,
        {
          diff: makeDiffStore(),
          diffReviewDraft: {
            setRouteContext: vi.fn(),
            isSubmitting: () => false,
          },
        },
      ],
    ]);
    const { rerender } = render(EventTimeline, { props, context });

    await waitFor(() => {
      expect(screen.getAllByRole("button", { name: "Add suggestion to batch" })).toHaveLength(2);
    });
    for (const button of screen.getAllByRole("button", { name: "Add suggestion to batch" })) {
      await fireEvent.click(button);
    }
    expect(screen.getByText("2 suggestions in batch")).toBeTruthy();

    await rerender({ ...props, events: [first] });
    await fireEvent.click(screen.getByRole("button", { name: "Commit batch" }));

    expect(applySuggestion).toHaveBeenCalledTimes(1);
    const suggestions = [...applySuggestion.mock.calls[0]![0].suggestions].sort((a, b) =>
      a.threadID.localeCompare(b.threadID),
    );
    expect(suggestions).toEqual([
      {
        threadID: "thread-1",
        replacement: "return firstSuggestion();",
      },
      {
        threadID: "thread-2",
        replacement: "return secondSuggestion();",
      },
    ]);
  });

  it("clears a batch error before a successful retry", async () => {
    const applySuggestion = vi
      .fn()
      .mockImplementationOnce(async () => ({
        ok: false,
        error: "pull request state changed",
      }))
      .mockImplementationOnce(async () => true);
    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            Body: ["Batch this.", "", "```suggestion", "return publish();", "```"].join("\n"),
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              diff_head_sha: "abc123",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        currentHeadSHA: "abc123",
        onApplySuggestion: applySuggestion,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Add suggestion to batch" }));
    await fireEvent.click(screen.getByRole("button", { name: "Commit batch" }));
    await waitFor(() => expect(screen.getByText("pull request state changed")).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Commit batch" }));
    await waitFor(() => expect(screen.queryByText("pull request state changed")).toBeNull());
    expect(applySuggestion).toHaveBeenCalledTimes(2);
  });

  it("disables suggestion application when the reviewed head is missing", async () => {
    const applySuggestion = vi.fn(async () => true);
    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            Body: ["This can return directly.", "", "```suggestion", "return client.publishThreads();", "```"].join(
              "\n",
            ),
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        onApplySuggestion: applySuggestion,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    await expectSuggestionPierreText(/return client\.publishThreads\(\);/);
    const button = screen.getByRole("button", { name: "Commit suggestion" }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.title).toBe("The suggestion is missing a reviewed head commit");
  });

  it("disables suggestion application when the reviewed head is stale", async () => {
    const applySuggestion = vi.fn(async () => true);
    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            Body: ["This can return directly.", "", "```suggestion", "return client.publishThreads();", "```"].join(
              "\n",
            ),
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              diff_head_sha: "old-head",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        currentHeadSHA: "new-head",
        onApplySuggestion: applySuggestion,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    await expectSuggestionPierreText(/return client\.publishThreads\(\);/);
    const commitButton = screen.getByRole("button", { name: "Commit suggestion" }) as HTMLButtonElement;
    const batchButton = screen.getByRole("button", { name: "Add suggestion to batch" }) as HTMLButtonElement;
    expect(commitButton.disabled).toBe(true);
    expect(commitButton.title).toBe("The suggestion was reviewed on an older head commit");
    expect(batchButton.disabled).toBe(true);
  });

  it("disables suggestion application when the current head is unknown", async () => {
    const applySuggestion = vi.fn(async () => true);
    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            Body: ["This can return directly.", "", "```suggestion", "return client.publishThreads();", "```"].join(
              "\n",
            ),
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              diff_head_sha: "abc123",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        onApplySuggestion: applySuggestion,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    await expectSuggestionPierreText(/return client\.publishThreads\(\);/);
    const commitButton = screen.getByRole("button", { name: "Commit suggestion" }) as HTMLButtonElement;
    const batchButton = screen.getByRole("button", { name: "Add suggestion to batch" }) as HTMLButtonElement;
    expect(commitButton.disabled).toBe(true);
    expect(commitButton.title).toBe("The current pull request head is not known yet");
    expect(batchButton.disabled).toBe(true);
  });

  it("drops suggestions batched on an older head from batch submit and keeps them removable", async () => {
    const applySuggestion = vi.fn(async () => true);
    const baseThread = makeReviewThreadEvent().diff_thread!;
    const staleEvent = makeReviewThreadEvent({
      ID: 1,
      CreatedAt: "2024-06-01T12:01:00Z",
      Body: ["First suggestion.", "", "```suggestion", "return firstSuggestion();", "```"].join("\n"),
      diff_thread: {
        ...baseThread,
        id: "thread-1",
        diff_head_sha: "abc123",
      },
    });
    const freshEvent = makeReviewThreadEvent({
      ID: 2,
      CreatedAt: "2024-06-01T12:02:00Z",
      Body: ["Second suggestion.", "", "```suggestion", "return secondSuggestion();", "```"].join("\n"),
      diff_thread: {
        ...baseThread,
        id: "thread-2",
        diff_head_sha: "new-head",
      },
    });
    const props = {
      events: [staleEvent, freshEvent],
      provider: "github",
      platformHost: "github.com",
      repoOwner: "acme",
      repoName: "widget",
      repoPath: "acme/widget",
      number: 7,
      currentHeadSHA: "abc123",
      onApplySuggestion: applySuggestion,
    };
    const context = new Map([
      [
        STORES_KEY,
        {
          diff: makeDiffStore(),
          diffReviewDraft: {
            setRouteContext: vi.fn(),
            isSubmitting: () => false,
          },
        },
      ],
    ]);
    const { rerender } = render(EventTimeline, { props, context });

    const batchButtons = await waitFor(() => {
      const buttons = screen.getAllByRole("button", { name: "Add suggestion to batch" });
      expect(buttons).toHaveLength(2);
      return buttons as HTMLButtonElement[];
    });
    expect(batchButtons[0]!.disabled).toBe(false);
    await fireEvent.click(batchButtons[0]!);
    expect(screen.getByText("1 suggestion in batch")).toBeTruthy();

    // The PR head moves past the first suggestion's reviewed head.
    await rerender({ ...props, currentHeadSHA: "new-head" });
    expect(screen.queryByText("1 suggestion in batch")).toBeNull();

    // The now-fresh suggestion can still be batched and committed alone.
    await fireEvent.click(screen.getByRole("button", { name: "Add suggestion to batch" }));
    expect(screen.getByText("1 suggestion in batch")).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Commit batch" }));
    expect(applySuggestion).toHaveBeenCalledTimes(1);
    expect(applySuggestion).toHaveBeenCalledWith({
      suggestions: [
        {
          threadID: "thread-2",
          replacement: "return secondSuggestion();",
        },
      ],
    });

    // The stale batched suggestion stays removable even though it cannot apply.
    const removeButton = screen.getByRole("button", { name: "Remove from batch" }) as HTMLButtonElement;
    expect(removeButton.disabled).toBe(false);
    await fireEvent.click(removeButton);
    expect(screen.queryByRole("button", { name: "Remove from batch" })).toBeNull();
  });

  it("marks the suggestion preview outdated and reloads when the cached diff predates the current head", async () => {
    const applySuggestion = vi.fn(async () => true);
    const loadDiff = vi.fn();
    const diffStore = makeDiffStore({
      getDiff: () =>
        ({
          ...(makeDiffStore().getDiff() as DiffResult),
          diff_head_sha: "old-head",
        }) as DiffResult,
      getCurrentPR: () => ({
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
        number: 7,
      }),
      loadDiff,
    } as unknown as Partial<DiffStore>);
    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            Body: ["This can return directly.", "", "```suggestion", "return client.publishThreads();", "```"].join(
              "\n",
            ),
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              diff_head_sha: "new-head",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        currentHeadSHA: "new-head",
        onApplySuggestion: applySuggestion,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff: diffStore,
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    const commitButton = await waitFor(() => {
      const button = screen.getByRole("button", { name: "Commit suggestion" }) as HTMLButtonElement;
      expect(button).toBeTruthy();
      return button;
    });
    expect(commitButton.disabled).toBe(true);
    expect(commitButton.title).toBe("The original diff context is not available");
    const batchButton = screen.getByRole("button", { name: "Add suggestion to batch" }) as HTMLButtonElement;
    expect(batchButton.disabled).toBe(true);
    expect(screen.queryByRole("button", { name: "Commit batch" })).toBeNull();
    await waitFor(() => {
      expect(loadDiff).toHaveBeenCalledTimes(1);
    });
  });

  it("keeps suggestion apply enabled when the cached diff matches the current head", async () => {
    const applySuggestion = vi.fn(async () => true);
    const diffStore = makeDiffStore({
      getDiff: () =>
        ({
          ...(makeDiffStore().getDiff() as DiffResult),
          diff_head_sha: "new-head",
        }) as DiffResult,
    } as unknown as Partial<DiffStore>);
    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            Body: ["This can return directly.", "", "```suggestion", "return client.publishThreads();", "```"].join(
              "\n",
            ),
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              diff_head_sha: "new-head",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        currentHeadSHA: "new-head",
        onApplySuggestion: applySuggestion,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff: diffStore,
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    await expectSuggestionPierreText(/return client\.publishThreads\(\);/);
    const commitButton = screen.getByRole("button", { name: "Commit suggestion" }) as HTMLButtonElement;
    expect(commitButton.disabled).toBe(false);
  });

  it("renders threaded comments as separate compact rows with one-line previews", async () => {
    const { container } = render(EventTimeline, {
      props: {
        activityViewMode: "compact",
        events: [
          makeEvent({
            ID: 4,
            EventType: "issue_comment",
            Author: "reviewer",
            Body: "```ts\nreturn cache.get(key);\n```",
            ThreadID: "compact-discussion",
            CreatedAt: "2024-06-01T12:03:00Z",
          }),
          makeEvent({
            ID: 3,
            EventType: "issue_comment",
            Author: "reviewer",
            Body: "Reply with [a link](https://example.test)\n\nand details",
            ThreadID: "compact-discussion",
            CreatedAt: "2024-06-01T12:02:00Z",
          }),
          makeEvent({
            ID: 1,
            EventType: "issue_comment",
            Author: "author",
            Body: "Root **comment**\n\n```ts\nconst hidden = true;\n```",
            ThreadID: "compact-discussion",
            CreatedAt: "2024-06-01T12:00:00Z",
          }),
        ],
      },
    });

    expect(container.querySelectorAll(".event-card--compact-row")).toHaveLength(3);
    expect(container.querySelectorAll(".thread-reply")).toHaveLength(0);
    expect(container.textContent).toContain("Root comment");
    expect(container.textContent).toContain("Reply with a link");
    expect(container.textContent).toContain("return cache.get(key);");
    expect(container.textContent).not.toContain("const hidden = true");
    const copyButtons = screen.getAllByLabelText("Copy comment");
    expect(copyButtons).toHaveLength(3);
    await fireEvent.click(copyButtons[1]!);
    expect(copyToClipboard).toHaveBeenCalledWith("Reply with [a link](https://example.test)\n\nand details");

    expect(screen.queryByText("and details")).toBeNull();
    const expandableRows = container.querySelectorAll<HTMLButtonElement>(".compact-event-toggle");
    expect(expandableRows.length).toBe(3);
    await fireEvent.click(expandableRows[1]!);

    expect(screen.getByText("and details")).toBeTruthy();
    expect(expandableRows[1]?.getAttribute("aria-expanded")).toBe("true");
  });

  it("expands compact commit rows to show the full commit message", async () => {
    const { container } = render(EventTimeline, {
      props: {
        activityViewMode: "compact",
        events: [
          makeEvent({
            EventType: "commit",
            Author: "alice",
            Summary: "abcdef1234567890",
            Body: "feat: add cache store\n\nCache entries now expire after refresh.",
          }),
        ],
      },
    });

    const row = container.querySelector<HTMLElement>(".event-card--compact-row");
    expect(row?.textContent).toContain("feat: add cache store");
    expect(row?.textContent).not.toContain("Cache entries now expire after refresh.");

    const toggle = row?.querySelector<HTMLButtonElement>(".compact-event-toggle");
    expect(toggle).toBeTruthy();
    await fireEvent.click(toggle!);

    expect(row?.textContent).toContain("Cache entries now expire after refresh.");
    expect(toggle?.getAttribute("aria-expanded")).toBe("true");
  });

  it("keeps compact commit details collapsed when commit details are hidden", () => {
    const { container } = render(EventTimeline, {
      props: {
        activityViewMode: "compact",
        showCommitDetails: false,
        events: [
          makeEvent({
            EventType: "commit",
            Author: "alice",
            Summary: "abcdef1234567890",
            Body: "feat: add cache store\n\nCache entries now expire after refresh.",
          }),
        ],
      },
    });

    const row = container.querySelector<HTMLElement>(".event-card--compact-row");
    expect(row?.textContent).toContain("feat: add cache store");
    expect(row?.textContent).not.toContain("Cache entries now expire after refresh.");
    expect(row?.querySelector(".compact-event-toggle")).toBeNull();
    expect(row?.querySelector(".commit-body-details")).toBeNull();
  });

  it("renders compact review verdicts and review comment context", () => {
    const { container } = render(EventTimeline, {
      props: {
        activityViewMode: "compact",
        events: [
          makeEvent({
            ID: 2,
            EventType: "review_comment",
            Author: "reviewer",
            Body: "This branch needs a guard clause.",
            Summary: "",
            CreatedAt: "2024-06-01T12:01:00Z",
            diff_thread: {
              id: "compact-review-thread",
              provider_comment_id: "2",
              path: "src/review.ts",
              old_path: "src/review.ts",
              side: "right",
              line: 42,
              new_line: 42,
              line_type: "add",
              diff_head_sha: "head-sha",
              commit_sha: "head-sha",
              body: "This branch needs a guard clause.",
              author_login: "reviewer",
              resolved: false,
              can_resolve: false,
              created_at: "2024-06-01T12:01:00Z",
              updated_at: "2024-06-01T12:01:00Z",
            },
          } as Partial<PREvent>),
          makeEvent({
            ID: 1,
            EventType: "review",
            Author: "maintainer",
            Body: "Please handle the cache fallback before merge.",
            Summary: "CHANGES_REQUESTED",
            CreatedAt: "2024-06-01T12:00:00Z",
          }),
        ],
      },
    });

    expect(container.querySelectorAll(".event-card--compact-row")).toHaveLength(2);
    expect(screen.getByText("Changes requested - Please handle the cache fallback before merge.")).toBeTruthy();
    expect(container.textContent).not.toContain("CHANGES_REQUESTED");
    expect(screen.getByText("src/review.ts:42")).toBeTruthy();
    expect(screen.getByText("This branch needs a guard clause.")).toBeTruthy();
  });

  it("keeps review thread replies available when compact rows expand", async () => {
    const { container } = render(EventTimeline, {
      props: {
        activityViewMode: "compact",
        events: [
          makeReviewThreadEvent({
            ID: 1,
            Body: "First compact review comment",
            CreatedAt: "2024-06-01T12:01:00Z",
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              provider_comment_id: "1",
              body: "First compact review comment",
            },
          }),
          makeReviewThreadEvent({
            ID: 2,
            Body: "Second compact review comment",
            CreatedAt: "2024-06-01T12:02:00Z",
            diff_thread: {
              ...makeReviewThreadEvent().diff_thread!,
              provider_comment_id: "2",
              body: "Second compact review comment",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        canReplyToThreads: true,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            detail: {
              replyToDiscussion: vi.fn().mockResolvedValue(true),
              getDetailError: vi.fn(),
            },
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    const rows = Array.from(container.querySelectorAll<HTMLElement>(".event-card--compact-row"));
    expect(rows).toHaveLength(2);
    await fireEvent.click(rows[0]!.querySelector<HTMLButtonElement>(".compact-event-toggle")!);
    await fireEvent.click(rows[1]!.querySelector<HTMLButtonElement>(".compact-event-toggle")!);

    expect(container.querySelectorAll(".thread-reply-action--inline")).toHaveLength(2);
    await fireEvent.click(rows[1]!.querySelector<HTMLButtonElement>(".thread-reply-action--inline")!);

    expect(container.querySelectorAll(".thread-reply-panel")).toHaveLength(1);
    expect(rows[0]!.querySelector(".thread-reply-panel")).toBeNull();
    expect(rows[1]!.querySelector(".thread-reply-panel")).toBeTruthy();
    expect(screen.getByRole("button", { name: /cancel/i })).toBeTruthy();
  });

  it("keeps normal review thread reply composer visible when thread entries regroup", async () => {
    const rootComment = makeReviewThreadEvent({
      ID: 1,
      Body: "Root review thread comment",
      CreatedAt: "2024-06-01T12:01:00Z",
      diff_thread: {
        ...makeReviewThreadEvent().diff_thread!,
        id: "thread-regroup",
        provider_comment_id: "1",
        body: "Root review thread comment",
      },
    });
    const replyComment = makeReviewThreadEvent({
      ID: 2,
      Body: "Existing reply after regroup",
      CreatedAt: "2024-06-01T12:02:00Z",
      diff_thread: {
        ...makeReviewThreadEvent().diff_thread!,
        id: "thread-regroup",
        provider_comment_id: "2",
        body: "Existing reply after regroup",
      },
    });
    const props = {
      events: [rootComment],
      provider: "github",
      platformHost: "github.com",
      repoOwner: "acme",
      repoName: "widget",
      repoPath: "acme/widget",
      number: 7,
      canReplyToThreads: true,
    } as const;
    const { container, rerender } = render(EventTimeline, {
      props,
      context: new Map([
        [
          STORES_KEY,
          {
            detail: {
              replyToDiscussion: vi.fn().mockResolvedValue(true),
              getDetailError: vi.fn(),
            },
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    await fireEvent.click(container.querySelector<HTMLButtonElement>(".thread-reply-action--inline")!);
    expect(container.querySelectorAll(".thread-reply-panel")).toHaveLength(1);

    await rerender({ ...props, events: [rootComment, replyComment] });

    expect(container.querySelectorAll(".thread-reply-panel")).toHaveLength(1);
    expect(screen.getByText("Existing reply after regroup")).toBeTruthy();
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
    expect(document.querySelector(".commit-body-details")?.textContent?.trim()).toBe(
      "feat: add timeline filters\n\nLong body",
    );
    expect(screen.getByText("4h ago")).toBeTruthy();
    expect(document.querySelector(".event--compact")).toBeTruthy();
    expect(document.querySelector(".commit-title")).toBeNull();
    expect(document.querySelector(".commit-body-details")?.classList.contains("event-body")).toBe(true);
    expect(document.querySelector(".event-header--compact")?.lastElementChild?.classList.contains("event-time")).toBe(
      true,
    );
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
    expect(document.querySelector(".commit-body-details")?.textContent?.trim()).toBe(
      "refactor: simplify worktree mapping application",
    );
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
    expect(document.querySelector(".event-header--compact")?.lastElementChild?.classList.contains("event-time")).toBe(
      true,
    );
  });

  it("renders force pushes as boundaries between commit generations", () => {
    const oldHead = "cccccccccccccccccccccccccccccccccccccccc";
    const newHead = "ffffffffffffffffffffffffffffffffffffffff";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 7,
            EventType: "force_push",
            Author: "alice",
            Summary: "ccccccc -> fffffff",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: oldHead,
              after_sha: newHead,
            }),
          }),
          makeEvent({
            ID: 6,
            EventType: "commit",
            Summary: newHead,
            Body: "new C3 after rebase",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
          makeEvent({
            ID: 3,
            EventType: "commit",
            Summary: oldHead,
            Body: "old C3 before rebase",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
          makeEvent({
            ID: 5,
            EventType: "commit",
            Summary: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
            Body: "new C2 after rebase",
            CreatedAt: "2024-06-01T10:02:00Z",
          }),
          makeEvent({
            ID: 2,
            EventType: "commit",
            Summary: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            Body: "old C2 before rebase",
            CreatedAt: "2024-06-01T10:02:00Z",
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("new C3 after rebase")).toBeLessThan(text.indexOf("ccccccc -> fffffff"));
    expect(text.indexOf("new C2 after rebase")).toBeLessThan(text.indexOf("ccccccc -> fffffff"));
    expect(text.indexOf("ccccccc -> fffffff")).toBeLessThan(text.indexOf("old C3 before rebase"));
    expect(text.indexOf("ccccccc -> fffffff")).toBeLessThan(text.indexOf("old C2 before rebase"));
  });

  it("orders force-push generations from commit ancestry even when database IDs are not generation order", () => {
    const oldHead = "cccccccccccccccccccccccccccccccccccccccc";
    const newHead = "ffffffffffffffffffffffffffffffffffffffff";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 70,
            EventType: "force_push",
            Author: "alice",
            Summary: "ccccccc -> fffffff",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: oldHead,
              after_sha: newHead,
            }),
          }),
          makeEvent({
            ID: 20,
            EventType: "commit",
            Summary: newHead,
            Body: "new head after rebase despite lower database id",
            CreatedAt: "2024-06-01T10:03:00Z",
            MetadataJSON: JSON.stringify({ commit_order: 3 }),
          }),
          makeEvent({
            ID: 30,
            EventType: "commit",
            Summary: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
            Body: "new base after rebase despite lower database id",
            CreatedAt: "2024-06-01T10:02:00Z",
            MetadataJSON: JSON.stringify({ commit_order: 2 }),
          }),
          makeEvent({
            ID: 90,
            EventType: "commit",
            Summary: oldHead,
            Body: "old head before rebase with higher database id",
            CreatedAt: "2024-06-01T10:03:00Z",
            MetadataJSON: JSON.stringify({ commit_order: 1 }),
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("new head after rebase despite lower database id")).toBeLessThan(
      text.indexOf("ccccccc -> fffffff"),
    );
    expect(text.indexOf("new base after rebase despite lower database id")).toBeLessThan(
      text.indexOf("ccccccc -> fffffff"),
    );
    expect(text.indexOf("ccccccc -> fffffff")).toBeLessThan(
      text.indexOf("old head before rebase with higher database id"),
    );
  });

  it("orders force-push generations by stable commit keys when list positions collide", () => {
    const oldHead = "cccccccccccccccccccccccccccccccccccccccc";
    const newHead = "ffffffffffffffffffffffffffffffffffffffff";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 70,
            EventType: "force_push",
            Author: "alice",
            Summary: "ccccccc -> fffffff",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: oldHead,
              after_sha: newHead,
            }),
          }),
          makeEvent({
            ID: 20,
            EventType: "commit",
            Summary: newHead,
            Body: "new head after same-length rebase",
            CreatedAt: "2024-06-01T10:03:00Z",
            MetadataJSON: JSON.stringify({ commit_order: 2, commit_order_key: 4 }),
          }),
          makeEvent({
            ID: 30,
            EventType: "commit",
            Summary: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
            Body: "new base after same-length rebase",
            CreatedAt: "2024-06-01T10:02:00Z",
            MetadataJSON: JSON.stringify({ commit_order: 1, commit_order_key: 3 }),
          }),
          makeEvent({
            ID: 90,
            EventType: "commit",
            Summary: oldHead,
            Body: "old head before same-length rebase",
            CreatedAt: "2024-06-01T10:03:00Z",
            MetadataJSON: JSON.stringify({ commit_order: 2, commit_order_key: 2 }),
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("new head after same-length rebase")).toBeLessThan(text.indexOf("ccccccc -> fffffff"));
    expect(text.indexOf("new base after same-length rebase")).toBeLessThan(text.indexOf("ccccccc -> fffffff"));
    expect(text.indexOf("ccccccc -> fffffff")).toBeLessThan(text.indexOf("old head before same-length rebase"));
  });

  it("keeps later commits in chronological order after force-push generations", () => {
    const oldHead = "cccccccccccccccccccccccccccccccccccccccc";
    const newHead = "ffffffffffffffffffffffffffffffffffffffff";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 9,
            EventType: "commit",
            Summary: "9999999999999999999999999999999999999999",
            Body: "follow-up after force push",
            CreatedAt: "2024-06-01T14:00:00Z",
          }),
          makeEvent({
            ID: 8,
            EventType: "issue_comment",
            Summary: "",
            Body: "comment after force push",
            CreatedAt: "2024-06-01T13:00:00Z",
          }),
          makeEvent({
            ID: 7,
            EventType: "force_push",
            Author: "alice",
            Summary: "ccccccc -> fffffff",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: oldHead,
              after_sha: newHead,
            }),
          }),
          makeEvent({
            ID: 6,
            EventType: "commit",
            Summary: newHead,
            Body: "new head after rebase",
            CreatedAt: "2024-06-01T10:00:00Z",
          }),
          makeEvent({
            ID: 3,
            EventType: "commit",
            Summary: oldHead,
            Body: "old head before rebase",
            CreatedAt: "2024-06-01T10:00:00Z",
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("follow-up after force push")).toBeLessThan(text.indexOf("comment after force push"));
    expect(text.indexOf("comment after force push")).toBeLessThan(text.indexOf("new head after rebase"));
    expect(text.indexOf("new head after rebase")).toBeLessThan(text.indexOf("ccccccc -> fffffff"));
    expect(text.indexOf("ccccccc -> fffffff")).toBeLessThan(text.indexOf("old head before rebase"));
  });

  it("keeps consecutive force pushes between their commit generations", () => {
    const oldHead = "3333333333333333333333333333333333333333";
    const firstHead = "6666666666666666666666666666666666666666";
    const secondHead = "9999999999999999999999999999999999999999";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 10,
            EventType: "force_push",
            Summary: "6666666 -> 9999999",
            CreatedAt: "2024-06-01T13:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: firstHead,
              after_sha: secondHead,
            }),
          }),
          makeEvent({
            ID: 9,
            EventType: "commit",
            Summary: secondHead,
            Body: "second generation head",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
          makeEvent({
            ID: 7,
            EventType: "force_push",
            Summary: "3333333 -> 6666666",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: oldHead,
              after_sha: firstHead,
            }),
          }),
          makeEvent({
            ID: 6,
            EventType: "commit",
            Summary: firstHead,
            Body: "first generation head",
            CreatedAt: "2024-06-01T14:00:00Z",
          }),
          makeEvent({
            ID: 3,
            EventType: "commit",
            Summary: oldHead,
            Body: "original generation head",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("second generation head")).toBeLessThan(text.indexOf("6666666 -> 9999999"));
    expect(text.indexOf("6666666 -> 9999999")).toBeLessThan(text.indexOf("first generation head"));
    expect(text.indexOf("first generation head")).toBeLessThan(text.indexOf("3333333 -> 6666666"));
    expect(text.indexOf("3333333 -> 6666666")).toBeLessThan(text.indexOf("original generation head"));
  });

  it("keeps consecutive same-timestamp force pushes between their commit generations", () => {
    const oldHead = "3333333333333333333333333333333333333333";
    const firstHead = "6666666666666666666666666666666666666666";
    const secondHead = "9999999999999999999999999999999999999999";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 10,
            EventType: "force_push",
            Summary: "6666666 -> 9999999",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: firstHead,
              after_sha: secondHead,
            }),
          }),
          makeEvent({
            ID: 9,
            EventType: "commit",
            Summary: secondHead,
            Body: "same timestamp second generation head",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
          makeEvent({
            ID: 7,
            EventType: "force_push",
            Summary: "3333333 -> 6666666",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: oldHead,
              after_sha: firstHead,
            }),
          }),
          makeEvent({
            ID: 6,
            EventType: "commit",
            Summary: firstHead,
            Body: "same timestamp first generation head",
            CreatedAt: "2024-06-01T14:00:00Z",
          }),
          makeEvent({
            ID: 3,
            EventType: "commit",
            Summary: oldHead,
            Body: "same timestamp original generation head",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("same timestamp second generation head")).toBeLessThan(text.indexOf("6666666 -> 9999999"));
    expect(text.indexOf("6666666 -> 9999999")).toBeLessThan(text.indexOf("same timestamp first generation head"));
    expect(text.indexOf("same timestamp first generation head")).toBeLessThan(text.indexOf("3333333 -> 6666666"));
    expect(text.indexOf("3333333 -> 6666666")).toBeLessThan(text.indexOf("same timestamp original generation head"));
  });

  it("preserves natural same-timestamp ordering for unrelated timeline events", () => {
    const oldHead = "3333333333333333333333333333333333333333";
    const firstHead = "6666666666666666666666666666666666666666";
    const secondHead = "9999999999999999999999999999999999999999";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 10,
            EventType: "force_push",
            Summary: "6666666 -> 9999999",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: firstHead,
              after_sha: secondHead,
            }),
          }),
          makeEvent({
            ID: 9,
            EventType: "commit",
            Summary: secondHead,
            Body: "same timestamp natural second generation",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
          makeEvent({
            ID: 8,
            EventType: "issue_comment",
            Summary: "",
            Body: "same timestamp reviewer note",
            CreatedAt: "2024-06-01T12:00:00Z",
          }),
          makeEvent({
            ID: 7,
            EventType: "force_push",
            Summary: "3333333 -> 6666666",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: oldHead,
              after_sha: firstHead,
            }),
          }),
          makeEvent({
            ID: 6,
            EventType: "commit",
            Summary: firstHead,
            Body: "same timestamp natural first generation",
            CreatedAt: "2024-06-01T14:00:00Z",
          }),
          makeEvent({
            ID: 3,
            EventType: "commit",
            Summary: oldHead,
            Body: "same timestamp natural original generation",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("same timestamp natural second generation")).toBeLessThan(text.indexOf("6666666 -> 9999999"));
    expect(text.indexOf("6666666 -> 9999999")).toBeLessThan(text.indexOf("same timestamp reviewer note"));
    expect(text.indexOf("same timestamp reviewer note")).toBeLessThan(
      text.indexOf("same timestamp natural first generation"),
    );
    expect(text.indexOf("same timestamp natural first generation")).toBeLessThan(text.indexOf("3333333 -> 6666666"));
  });

  it("keeps same-timestamp unrelated events outside force-push boundary buckets", () => {
    const oldHead = "3333333333333333333333333333333333333333";
    const newHead = "6666666666666666666666666666666666666666";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 15,
            EventType: "issue_comment",
            Summary: "",
            Body: "same timestamp reviewer note between IDs",
            CreatedAt: "2024-06-01T12:00:00Z",
          }),
          makeEvent({
            ID: 10,
            EventType: "commit",
            Summary: newHead,
            Body: "same timestamp generated commit below comment ID",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
          makeEvent({
            ID: 20,
            EventType: "force_push",
            Summary: "3333333 -> 6666666",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: oldHead,
              after_sha: newHead,
            }),
          }),
          makeEvent({
            ID: 5,
            EventType: "commit",
            Summary: oldHead,
            Body: "same timestamp original generation",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("same timestamp generated commit below comment ID")).toBeLessThan(
      text.indexOf("3333333 -> 6666666"),
    );
    expect(text.indexOf("3333333 -> 6666666")).toBeLessThan(text.indexOf("same timestamp reviewer note between IDs"));
    expect(text.indexOf("same timestamp reviewer note between IDs")).toBeLessThan(
      text.indexOf("same timestamp original generation"),
    );
  });

  it("uses hidden force-push events to order visible commit generations", () => {
    const oldHead = "3333333333333333333333333333333333333333";
    const newHead = "6666666666666666666666666666666666666666";
    const visibleEvents = [
      makeEvent({
        ID: 3,
        EventType: "commit",
        Summary: oldHead,
        Body: "visible old generation head",
        CreatedAt: "2024-06-01T11:00:00Z",
      }),
      makeEvent({
        ID: 6,
        EventType: "commit",
        Summary: newHead,
        Body: "visible new generation head",
        CreatedAt: "2024-06-01T10:00:00Z",
      }),
    ];
    const { container } = render(EventTimeline, {
      props: {
        events: visibleEvents,
        orderingEvents: [
          ...visibleEvents,
          makeEvent({
            ID: 7,
            EventType: "force_push",
            Summary: "3333333 -> 6666666",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: oldHead,
              after_sha: newHead,
            }),
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("visible new generation head")).toBeLessThan(text.indexOf("visible old generation head"));
    expect(screen.queryByText("3333333 -> 6666666")).toBeNull();
  });

  it("falls back to after-sha when the old force-push anchor was never imported", () => {
    const missingOldHead = "3333333333333333333333333333333333333333";
    const newHead = "6666666666666666666666666666666666666666";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 7,
            EventType: "force_push",
            Summary: "3333333 -> 6666666",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: missingOldHead,
              after_sha: newHead,
            }),
          }),
          makeEvent({
            ID: 6,
            EventType: "commit",
            Summary: newHead,
            Body: "fresh import new head",
            CreatedAt: "2024-06-01T10:02:00Z",
          }),
          makeEvent({
            ID: 5,
            EventType: "commit",
            Summary: "5555555555555555555555555555555555555555",
            Body: "fresh import earlier current commit",
            CreatedAt: "2024-06-01T10:01:00Z",
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("fresh import new head")).toBeLessThan(text.indexOf("3333333 -> 6666666"));
    expect(text.indexOf("fresh import earlier current commit")).toBeLessThan(text.indexOf("3333333 -> 6666666"));
  });

  it("orders fallback force-push boundaries after earlier anchored boundaries", () => {
    const originalHead = "3333333333333333333333333333333333333333";
    const firstHead = "6666666666666666666666666666666666666666";
    const missingSecondBefore = "8888888888888888888888888888888888888888";
    const secondHead = "9999999999999999999999999999999999999999";
    const { container } = render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            ID: 10,
            EventType: "force_push",
            Summary: "8888888 -> 9999999",
            CreatedAt: "2024-06-01T13:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: missingSecondBefore,
              after_sha: secondHead,
            }),
          }),
          makeEvent({
            ID: 9,
            EventType: "commit",
            Summary: secondHead,
            Body: "fallback second generation head",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
          makeEvent({
            ID: 7,
            EventType: "force_push",
            Summary: "3333333 -> 6666666",
            CreatedAt: "2024-06-01T12:00:00Z",
            MetadataJSON: JSON.stringify({
              before_sha: originalHead,
              after_sha: firstHead,
            }),
          }),
          makeEvent({
            ID: 6,
            EventType: "commit",
            Summary: firstHead,
            Body: "anchored first generation head",
            CreatedAt: "2024-06-01T14:00:00Z",
          }),
          makeEvent({
            ID: 3,
            EventType: "commit",
            Summary: originalHead,
            Body: "anchored original generation head",
            CreatedAt: "2024-06-01T10:03:00Z",
          }),
        ],
      },
    });

    const text = container.textContent ?? "";
    expect(text.indexOf("fallback second generation head")).toBeLessThan(text.indexOf("8888888 -> 9999999"));
    expect(text.indexOf("8888888 -> 9999999")).toBeLessThan(text.indexOf("anchored first generation head"));
    expect(text.indexOf("anchored first generation head")).toBeLessThan(text.indexOf("3333333 -> 6666666"));
    expect(text.indexOf("3333333 -> 6666666")).toBeLessThan(text.indexOf("anchored original generation head"));
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

  it("keeps compact cross-reference summaries navigable", () => {
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
        activityViewMode: "compact",
      },
    });

    const link = screen.getByRole("link", {
      name: "Add shared git tooling packages",
    });
    const assert = expect.soft;
    assert(link.closest(".event-card--compact-row")).toBeTruthy();
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

    const link = screen.getByRole("link", {
      name: "external reference",
    });
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

  it("confirms comment deletion with author and excerpt before calling the provider", async () => {
    const onDeleteComment = vi.fn().mockResolvedValue(null);
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            Author: "alice",
            Body: "Remove this detailed comment after confirming the target.",
            EventType: "issue_comment",
            PlatformID: 44,
          }),
        ],
        onDeleteComment,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Delete comment" }));

    const dialog = screen.getByRole("dialog", { name: "Delete comment?" });
    expect(getStackDepth()).toBe(1);
    expect(dialog.textContent).toContain("alice");
    expect(dialog.textContent).toContain("Remove this detailed comment");
    expect(onDeleteComment).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onDeleteComment).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "Delete comment?" })).toBeNull();
    expect(getStackDepth()).toBe(0);
  });

  it("submits comment deletion once and closes after success", async () => {
    let finishDelete: (error: string | null) => void = () => {};
    const onDeleteComment = vi.fn(
      () =>
        new Promise<string | null>((resolve) => {
          finishDelete = resolve;
        }),
    );
    const comment = makeEvent({ Body: "Delete me", EventType: "issue_comment", PlatformID: 44 });
    render(EventTimeline, { props: { events: [comment], onDeleteComment } });

    await fireEvent.click(screen.getByRole("button", { name: "Delete comment" }));
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(onDeleteComment).toHaveBeenCalledTimes(1);
    expect(onDeleteComment).toHaveBeenCalledWith(comment);
    expect(screen.getByRole("button", { name: "Deleting..." }).hasAttribute("disabled")).toBe(true);

    finishDelete(null);
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Delete comment?" })).toBeNull();
    });
  });

  it("keeps the confirmation open with the provider error after deletion fails", async () => {
    const onDeleteComment = vi.fn().mockResolvedValue("provider denied deletion");
    render(EventTimeline, {
      props: {
        events: [makeEvent({ Body: "Keep me", EventType: "issue_comment", PlatformID: 44 })],
        onDeleteComment,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Delete comment" }));
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => {
      expect(screen.getByText("provider denied deletion")).toBeTruthy();
    });
    expect(screen.getByRole("dialog", { name: "Delete comment?" })).toBeTruthy();
  });

  it("shows a stable error when the deletion callback rejects", async () => {
    const onDeleteComment = vi.fn().mockRejectedValue(new Error("transport exploded"));
    render(EventTimeline, {
      props: {
        events: [makeEvent({ Body: "Keep me", EventType: "issue_comment", PlatformID: 44 })],
        onDeleteComment,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Delete comment" }));
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => {
      expect(screen.getByText("transport exploded")).toBeTruthy();
    });
    expect(screen.getByRole("dialog", { name: "Delete comment?" })).toBeTruthy();
  });

  it("hides comment deletion when the callback is unavailable", () => {
    render(EventTimeline, {
      props: {
        events: [makeEvent({ Body: "Keep me", EventType: "issue_comment", PlatformID: 44 })],
        onDeleteComment: undefined,
      },
    });

    expect(screen.queryByRole("button", { name: "Delete comment" })).toBeNull();
  });

  it("copies a direct comment link when the event exposes one", async () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            Body: "Original comment",
            DirectURL: "https://github.com/acme/widget/pull/7#issuecomment-44",
            EventType: "issue_comment",
            PlatformID: 44,
          }),
        ],
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Copy direct link" }));

    await waitFor(() => {
      expect(copyToClipboard).toHaveBeenCalledWith("https://github.com/acme/widget/pull/7#issuecomment-44");
    });
  });

  it("does not render a direct link action for comments without a direct URL", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            Body: "Original comment",
            EventType: "issue_comment",
            PlatformID: 44,
          }),
        ],
      },
    });

    expect(screen.queryByRole("button", { name: "Copy direct link" })).toBeNull();
  });

  it("shows review thread diff context and exposes a jump action", async () => {
    const jumpToReviewThread = vi.fn();
    const diff = makeDiffStore();

    const { container } = render(EventTimeline, {
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
        [
          STORES_KEY,
          {
            diff,
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    expect(screen.getByText("src/review.ts:10-11")).toBeTruthy();
    await expectPierreTimelineText(/client\.publishThreads\(\);/);
    expect(container.querySelector(".event-body-wrap--with-thread .event-actions")).toBeTruthy();

    const threadedActions = findCompiledStyleRule(".event-body-wrap--with-thread");
    expect(threadedActions.getPropertyValue("display")).toBe("flow-root");

    const threadedActionButtons = findCompiledStyleRule(".event-body-wrap--with-thread .event-actions");
    expect(threadedActionButtons.getPropertyValue("position")).toBe("static");
    expect(threadedActionButtons.getPropertyValue("float")).toBe("right");

    await fireEvent.click(screen.getByRole("button", { name: "Jump to diff" }));

    expect(jumpToReviewThread).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "thread-1",
        path: "src/review.ts",
      }),
    );
  });

  it("quotes review thread snippet paths before building synthetic patch text", async () => {
    const path = "src/review.ts\n--- a/forged.ts\n+++ b/forged.ts";
    const diff = makeDiffStore({
      getDiff: () => ({
        stale: false,
        whitespace_only_count: 0,
        files: [
          {
            path,
            old_path: path,
            status: "modified",
            is_binary: false,
            is_whitespace_only: false,
            additions: 1,
            deletions: 0,
            hunks: [
              {
                old_start: 9,
                old_count: 1,
                new_start: 9,
                new_count: 2,
                lines: [
                  {
                    type: "context",
                    old_num: 9,
                    new_num: 9,
                    content: "const client = setup();",
                  },
                  {
                    type: "add",
                    new_num: 10,
                    content: "client.publishThreads();",
                  },
                ],
              },
            ],
          },
        ],
      }),
    });

    render(EventTimeline, {
      props: {
        events: [
          makeReviewThreadEvent({
            diff_thread: {
              id: "thread-1",
              path,
              side: "right",
              start_side: "right",
              start_line: 10,
              line: 10,
              new_line: 10,
              line_type: "add",
              body: "Please keep this setup explicit.",
              author_login: "alice",
              resolved: false,
              can_resolve: true,
              created_at: "2024-06-01T12:00:00Z",
              updated_at: "2024-06-01T12:00:00Z",
            },
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            diff,
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    await expectPierreTimelineText(/client\.publishThreads\(\);/);
  });

  it("shows a reply composer for review threads when thread replies are available", async () => {
    const { container } = render(EventTimeline, {
      props: {
        events: [makeReviewThreadEvent()],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        canReplyToThreads: true,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            detail: {
              replyToDiscussion: vi.fn().mockResolvedValue(true),
              getDetailError: vi.fn(),
            },
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    expect(container.querySelector(".event-card--reply-inline")).toBeTruthy();
    expect(container.querySelector(".thread-controls--reply-only")).toBeNull();
    expect(container.querySelector(".thread-reply-action--inline")).toBeTruthy();

    const inlineReplyCard = findCompiledStyleRule(".event-card--reply-inline");
    expect(inlineReplyCard.getPropertyValue("display")).toBe("flow-root");

    const inlineReplyBody = findCompiledStyleRule(".event-body--with-inline-reply");
    expect(inlineReplyBody.getPropertyValue("display")).toBe("block");

    const inlineReplyFloat = findCompiledStyleRule(".event-body--with-inline-reply .thread-reply-inline-float");
    expect(inlineReplyFloat.getPropertyValue("float")).toBe("right");
    expect(inlineReplyFloat.getPropertyValue("clear")).toBe("right");
    expect(inlineReplyFloat.getPropertyValue("margin-left")).toBe("var(--focus-detail-space-sm, 10px)");

    const inlineReplyAction = findCompiledStyleRule(".event-body--with-inline-reply .thread-reply-action--inline");
    expect(inlineReplyAction.getPropertyValue("display")).toBe("inline-flex");
    expect(inlineReplyAction.getPropertyValue("color")).toBe("var(--text-secondary)");

    await fireEvent.click(screen.getByRole("button", { name: "Reply" }));

    expect(screen.getByRole("button", { name: /cancel/i })).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "Reply" })).toHaveLength(2);
  });

  it("does not expose replies when a timeline item lacks a local review thread", () => {
    render(EventTimeline, {
      props: {
        events: [
          makeEvent({
            EventType: "review_comment",
            Body: "Provider thread without local diff metadata",
            ThreadID: "PRRT_provider_thread",
          }),
        ],
        provider: "github",
        platformHost: "github.com",
        repoOwner: "acme",
        repoName: "widget",
        repoPath: "acme/widget",
        number: 7,
        canReplyToThreads: true,
      },
      context: new Map([
        [
          STORES_KEY,
          {
            detail: {
              replyToDiscussion: vi.fn().mockResolvedValue(true),
              getDetailError: vi.fn(),
            },
            diff: makeDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    expect(screen.queryByRole("button", { name: "Reply" })).toBeNull();
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
        [
          STORES_KEY,
          {
            diff,
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
          },
        ],
      ]),
    });

    expect(screen.getByText("Outdated")).toBeTruthy();
    expect(screen.getByText("Diff context is no longer present in the loaded diff.")).toBeTruthy();
  });
});
