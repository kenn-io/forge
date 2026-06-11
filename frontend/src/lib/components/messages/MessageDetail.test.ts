import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { MessageDetailData } from "../../api/messages/types";
import type { MessageLinkInput } from "../../messages/messageLinks";
import type { IssueRef, IssueSummary, KataAPI, SearchResponse } from "../../messages/types";
import MessageDetail from "./MessageDetail.svelte";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function makeDetail(overrides: Partial<MessageDetailData> = {}): MessageDetailData {
  return {
    id: 1001,
    conversation_id: 1001,
    subject: "Project kickoff",
    from: "alice@example.com",
    to: ["bob@example.com", "carol@example.com"],
    cc: [],
    bcc: [],
    sent_at: "2026-05-15T09:00:00Z",
    snippet: "Let's get this started.",
    labels: ["Inbox", "Work"],
    has_attachments: false,
    size_bytes: 2048,
    deleted_at: null,
    body: "Hi team,\n\nLet us begin.\n\nBest,\nAlice",
    attachments: [],
    ...overrides,
  };
}

function permalinkOf(id: number): string {
  return `messages:msgvault:${id}`;
}

function remoteImageURL(id: number, token: string, index: string): string {
  return `/api/v1/msgvault/messages/${id}/remote-image/${token}/${index}`;
}

function renderDetail(
  detail: MessageDetailData | null,
  opts: {
    loading?: boolean;
    error?: string | null;
    compact?: boolean;
    imagesLoaded?: boolean;
    remoteImageToken?: string;
    htmlSanitizationFailed?: boolean;
    remoteImageCount?: number;
    viewMode?: "html" | "text";
    onViewModeChange?: (id: number, mode: "html" | "text") => void;
    onLoadImages?: (id: number, token: string) => void;
    reverseLinks?: IssueRef[];
    onOpenIssue?: (uid: string) => void;
    kata?: Pick<KataAPI, "search">;
    onLinkMessage?: (issueUid: string, input: MessageLinkInput) => Promise<{ qualified_id: string }>;
  } = {},
) {
  return render(MessageDetail, {
    props: {
      detail,
      loading: opts.loading ?? false,
      error: opts.error ?? null,
      permalinkOf,
      remoteImageURL,
      compact: opts.compact ?? false,
      imagesLoaded: opts.imagesLoaded ?? false,
      remoteImageToken: opts.remoteImageToken ?? "",
      htmlSanitizationFailed: opts.htmlSanitizationFailed ?? false,
      remoteImageCount: opts.remoteImageCount ?? 0,
      viewMode: opts.viewMode ?? "html",
      ...(opts.onViewModeChange !== undefined ? { onViewModeChange: opts.onViewModeChange } : {}),
      ...(opts.onLoadImages !== undefined ? { onLoadImages: opts.onLoadImages } : {}),
      ...(opts.reverseLinks !== undefined ? { reverseLinks: opts.reverseLinks } : {}),
      ...(opts.onOpenIssue !== undefined ? { onOpenIssue: opts.onOpenIssue } : {}),
      ...(opts.kata !== undefined ? { kata: opts.kata } : {}),
      ...(opts.onLinkMessage !== undefined ? { onLinkMessage: opts.onLinkMessage } : {}),
    },
  });
}

const sampleDetail: MessageDetailData = makeDetail({
  id: 2001,
  conversation_id: 2001,
  subject: "HTML test message",
  from: "alice@example.com",
  to: ["bob@example.com"],
  snippet: "Test",
  labels: [],
  size_bytes: 512,
  body: "Plain text fallback.",
});

function fakeRef(overrides: Partial<IssueRef> = {}): IssueRef {
  return {
    uid: overrides.uid ?? "issue-sort-inbox",
    short_id: overrides.short_id ?? "sort",
    qualified_id: overrides.qualified_id ?? "Inbox#sort",
    title: overrides.title ?? "Sort messages by date",
    status: overrides.status ?? "open",
    ...overrides,
  };
}

describe("MessageDetail", () => {
  it("null detail shows empty state copy", () => {
    renderDetail(null);

    expect(screen.getByText("Select a message to read it.")).toBeTruthy();
  });

  it("loading state renders skeleton, not empty state", () => {
    renderDetail(null, { loading: true });

    expect(screen.queryByText("Select a message to read it.")).toBeNull();
    expect(document.querySelectorAll("[aria-hidden='true']").length).toBeGreaterThan(0);
  });

  it("error state renders the error message and retry hint", () => {
    renderDetail(null, { error: "Connection refused" });

    expect(screen.getByRole("alert").textContent).toContain("Connection refused");
    expect(screen.getByText(/retry/i)).toBeTruthy();
    expect(screen.queryByText("Select a message to read it.")).toBeNull();
  });

  it("renders header fields, labels, body text, and attachments", () => {
    renderDetail(
      makeDetail({
        cc: ["cc@example.com"],
        bcc: ["bcc@example.com"],
        has_attachments: true,
        attachments: [
          { filename: "report.pdf", mime_type: "application/pdf", size_bytes: 12000 },
          { filename: "photo.jpg", mime_type: "image/jpeg", size_bytes: 48000 },
        ],
      }),
    );

    const text = document.body.textContent ?? "";
    expect(text).toContain("alice@example.com");
    expect(text).toContain("bob@example.com");
    expect(text).toContain("carol@example.com");
    expect(text).toContain("cc@example.com");
    expect(text).toContain("bcc@example.com");
    expect(screen.getByRole("heading", { level: 1, name: "Project kickoff" })).toBeTruthy();
    expect(screen.getByText("Inbox")).toBeTruthy();
    expect(screen.getByText("Work")).toBeTruthy();
    expect(screen.getByText(/Let us begin/)).toBeTruthy();
    expect(screen.getByText("report.pdf")).toBeTruthy();
    expect(screen.getByText("photo.jpg")).toBeTruthy();
    expect(screen.getByText(/Download from the message source/)).toBeTruthy();
  });

  it("omits optional rows and sections when data is empty", () => {
    renderDetail(makeDetail({ cc: [], bcc: [], labels: [], attachments: [] }));

    const text = document.body.textContent ?? "";
    expect(text).not.toMatch(/\bCc\b/);
    expect(text).not.toMatch(/\bBcc\b/);
    expect(screen.queryByText("Inbox")).toBeNull();
    expect(screen.queryByText("Work")).toBeNull();
    expect(screen.queryByText(/Download from the message source/)).toBeNull();
  });

  it("preserves body whitespace and linkifies URLs", () => {
    renderDetail(makeDetail({ body: "Line one\n  indented line\nSee www.example.com and https://example.org." }));

    const pre = document.querySelector("pre.msg-body");
    expect(pre?.textContent).toContain("  indented line");
    expect(document.querySelector("a[href='https://www.example.com']")).toBeTruthy();
    const explicit = document.querySelector("a[href='https://example.org']") as HTMLAnchorElement | null;
    expect(explicit).toBeTruthy();
    expect(explicit?.getAttribute("target")).toBe("_blank");
    expect(explicit?.getAttribute("rel")).toBe("noopener noreferrer");
  });

  it("copy permalink writes messages:msgvault:<id> and shows transient copied state", async () => {
    vi.useFakeTimers();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      writable: true,
      configurable: true,
    });

    renderDetail(makeDetail({ id: 42 }));

    await fireEvent.click(screen.getByRole("button", { name: /copy permalink/i }));

    expect(writeText).toHaveBeenCalledWith("messages:msgvault:42");
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /copied/i })).toBeTruthy();
    });
    vi.advanceTimersByTime(2000);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /copy permalink/i })).toBeTruthy();
    });
  });

  it("compact mode suppresses the subject heading and marks the root", () => {
    const { container } = renderDetail(makeDetail({ subject: "Quarterly update" }), { compact: true });

    expect(screen.queryByRole("heading", { level: 1, name: "Quarterly update" })).toBeNull();
    expect(screen.getByText("alice@example.com")).toBeTruthy();
    expect(container.querySelector(".messages-detail")?.classList.contains("compact")).toBe(true);
  });
});

describe("MessageDetail HTML viewer", () => {
  it("viewMode=text renders plain text without an iframe", () => {
    const { container } = renderDetail(
      { ...sampleDetail, body_html: "<p>x</p>", remote_image_count: 0 },
      {
        viewMode: "text",
      },
    );

    expect(container.querySelector("pre.msg-body")).toBeTruthy();
    expect(container.querySelector("iframe.html-iframe")).toBeFalsy();
  });

  it("viewMode=html with body_html renders iframe with srcdoc and sandbox", () => {
    const { container } = renderDetail(
      { ...sampleDetail, body_html: "<p>x</p>", remote_image_count: 0 },
      {
        viewMode: "html",
      },
    );

    const iframe = container.querySelector("iframe.html-iframe") as HTMLIFrameElement | null;
    expect(iframe).toBeTruthy();
    expect(iframe?.getAttribute("sandbox")).toBe("allow-popups allow-popups-to-escape-sandbox");
    expect(iframe?.getAttribute("title")).toBe("Message body");
    const srcdoc = iframe?.getAttribute("srcdoc") ?? "";
    expect(srcdoc).toMatch(/Content-Security-Policy/);
    expect(srcdoc).toMatch(/img-src/);
    expect(srcdoc).toMatch(/<p>x<\/p>/);
  });

  it("compact HTML mode marks the iframe", () => {
    const { container } = renderDetail(
      { ...sampleDetail, body_html: "<p>x</p>", remote_image_count: 0 },
      {
        compact: true,
        viewMode: "html",
      },
    );

    expect(container.querySelector("iframe.html-iframe")?.classList.contains("compact-iframe")).toBe(true);
  });

  it("sanitization failure renders plain text and an alert", () => {
    const { container } = renderDetail(
      { ...sampleDetail, body_html: "", html_sanitization_failed: true },
      {
        viewMode: "html",
        htmlSanitizationFailed: true,
      },
    );

    expect(container.querySelector("pre.msg-body")).toBeTruthy();
    expect(container.querySelector("iframe.html-iframe")).toBeFalsy();
    expect(screen.getByRole("alert").textContent).toMatch(/Couldn't render HTML/i);
  });

  it("loaded remote images replace data indexes with message source image URLs", () => {
    const token = "deadbeef".repeat(4);
    const { container } = renderDetail(
      {
        ...sampleDetail,
        body_html: '<img data-remote-image-idx="0" alt="banner">',
        remote_image_count: 1,
        remote_image_token: token,
      },
      {
        viewMode: "html",
        imagesLoaded: true,
        remoteImageToken: token,
      },
    );

    const iframe = container.querySelector("iframe.html-iframe") as HTMLIFrameElement | null;
    const srcdoc = iframe?.getAttribute("srcdoc") ?? "";
    expect(srcdoc).toMatch(
      /<img[^>]*src="\/api\/v1\/msgvault\/messages\/2001\/remote-image\/deadbeefdeadbeefdeadbeefdeadbeef\/0"/,
    );
    expect(srcdoc).not.toMatch(/data-remote-image-idx/);
  });

  it("HTML/Text toggle is gated by sanitized HTML and calls onViewModeChange", async () => {
    const onViewModeChange = vi.fn();
    const { rerender } = renderDetail(
      { ...sampleDetail, body_html: "<p>x</p>" },
      {
        viewMode: "html",
        onViewModeChange,
      },
    );

    expect(screen.getByRole("button", { name: "HTML" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Text" }));
    expect(onViewModeChange).toHaveBeenCalledWith(sampleDetail.id, "text");

    await rerender({
      detail: { ...sampleDetail, body_html: "", html_sanitization_failed: true },
      loading: false,
      error: null,
      permalinkOf,
      remoteImageURL,
      htmlSanitizationFailed: true,
      viewMode: "html",
    });
    expect(screen.queryByRole("button", { name: "HTML" })).toBeNull();
  });

  it("remote-image banner gates on count, mode, loaded state, and sanitization failure", () => {
    const detail = { ...sampleDetail, body_html: "<p>x</p>", remote_image_count: 2 };
    const rendered = renderDetail(detail, { viewMode: "html", remoteImageCount: 2 });
    expect(screen.getByRole("status").textContent).toMatch(/2 remote images/i);

    cleanup();
    renderDetail({ ...detail, remote_image_count: 0 }, { viewMode: "html", remoteImageCount: 0 });
    expect(screen.queryByRole("status")).toBeNull();

    cleanup();
    renderDetail(detail, { viewMode: "html", remoteImageCount: 2, imagesLoaded: true });
    expect(screen.queryByRole("status")).toBeNull();

    cleanup();
    renderDetail(detail, { viewMode: "text", remoteImageCount: 2 });
    expect(screen.queryByRole("status")).toBeNull();

    cleanup();
    renderDetail(
      { ...detail, body_html: "", html_sanitization_failed: true },
      { viewMode: "html", remoteImageCount: 2, htmlSanitizationFailed: true },
    );
    expect(screen.queryByRole("status")).toBeNull();

    rendered.unmount();
  });

  it("Load images click calls onLoadImages with id and token", async () => {
    const token = "deadbeef".repeat(4);
    const onLoadImages = vi.fn();
    renderDetail(
      {
        ...sampleDetail,
        body_html: "<p>x</p>",
        remote_image_count: 1,
        remote_image_token: token,
      },
      {
        viewMode: "html",
        remoteImageCount: 1,
        remoteImageToken: token,
        onLoadImages,
      },
    );

    await fireEvent.click(screen.getByRole("button", { name: /Load images/i }));

    expect(onLoadImages).toHaveBeenCalledWith(sampleDetail.id, token);
  });
});

describe("MessageDetail reverse links", () => {
  it("hides reverse-link section unless links and an open callback are supplied", () => {
    renderDetail(makeDetail(), { onOpenIssue: vi.fn() });
    expect(screen.queryByRole("region", { name: /Linked tasks/i })).toBeNull();

    cleanup();
    renderDetail(makeDetail(), { reverseLinks: [], onOpenIssue: vi.fn() });
    expect(screen.queryByRole("region", { name: /Linked tasks/i })).toBeNull();

    cleanup();
    renderDetail(makeDetail(), { reverseLinks: [fakeRef()] });
    expect(screen.queryByRole("region", { name: /Linked tasks/i })).toBeNull();
  });

  it("renders reverse-link pills and opens the selected issue", async () => {
    const onOpenIssue = vi.fn();
    renderDetail(makeDetail(), {
      reverseLinks: [
        fakeRef({ uid: "issue-sort-inbox", qualified_id: "Inbox#sort", title: "Sort messages by date" }),
        fakeRef({ uid: "issue-archive-old", qualified_id: "Inbox#archive", title: "Archive old messages" }),
      ],
      onOpenIssue,
    });

    expect(screen.getByRole("region", { name: /Linked tasks/i })).toBeTruthy();
    expect(document.querySelectorAll(".reverse-pill").length).toBe(2);
    expect(document.body.textContent).toContain("Inbox#sort");
    expect(document.body.textContent).toContain("Inbox#archive");

    await fireEvent.click(screen.getByRole("button", { name: /Inbox#sort.*Sort messages by date/i }));

    expect(onOpenIssue).toHaveBeenCalledWith("issue-sort-inbox");
  });
});

function fakeIssue(overrides: Partial<IssueSummary> = {}): IssueSummary {
  return {
    id: overrides.id ?? 42,
    uid: overrides.uid ?? "uid-42",
    short_id: overrides.short_id ?? "42",
    qualified_id: overrides.qualified_id ?? "Kata#42",
    title: overrides.title ?? "Pick me",
    status: overrides.status ?? "open",
    metadata: overrides.metadata ?? {},
  };
}

function searchResponse(issues: IssueSummary[]): SearchResponse {
  return {
    filters: {
      scope: { kind: "all" },
      status: "open",
      owner: "",
      label: "",
      query: "",
    },
    issues,
    fetched_at: "2026-05-18T00:00:00Z",
  };
}

function makeKata(search: KataAPI["search"] = async () => searchResponse([])): Pick<KataAPI, "search"> {
  return { search };
}

describe("MessageDetail link to task", () => {
  it("hides the link button unless search and link callbacks are supplied with a detail", () => {
    renderDetail(makeDetail(), { kata: makeKata() });
    expect(screen.queryByRole("button", { name: /Link to task/i })).toBeNull();

    cleanup();
    renderDetail(makeDetail(), { onLinkMessage: vi.fn() });
    expect(screen.queryByRole("button", { name: /Link to task/i })).toBeNull();

    cleanup();
    renderDetail(null, { kata: makeKata(), onLinkMessage: vi.fn() });
    expect(screen.queryByRole("button", { name: /Link to task/i })).toBeNull();
  });

  it("opens the task picker when the link button is clicked", async () => {
    renderDetail(makeDetail(), {
      kata: makeKata(),
      onLinkMessage: vi.fn(),
    });

    await fireEvent.click(screen.getByRole("button", { name: /Link to task/i }));

    expect(screen.getByRole("dialog", { name: /Link to task/i })).toBeTruthy();
  });

  it("links the selected task with a snapshot of the current message", async () => {
    vi.useFakeTimers();
    const kata = makeKata(async () =>
      searchResponse([fakeIssue({ id: 42, uid: "uid-42", qualified_id: "Kata#42", title: "Pick me" })]),
    );
    const onLinkMessage = vi.fn().mockResolvedValue({ qualified_id: "Kata#42" });
    const detail = makeDetail({
      id: 1001,
      conversation_id: 2001,
      subject: "Project sync",
      from: "alice@example.com",
      sent_at: "2026-05-15T09:00:00Z",
    });
    renderDetail(detail, { kata, onLinkMessage });

    await fireEvent.click(screen.getByRole("button", { name: /Link to task/i }));
    await fireEvent.input(screen.getByPlaceholderText(/Title or qualified ID/i), { target: { value: "test" } });
    await vi.advanceTimersByTimeAsync(250);
    await fireEvent.click(await waitFor(() => screen.getByRole("button", { name: /Kata#42.*Pick me/i })));
    await fireEvent.click(screen.getByRole("button", { name: /^Link$/ }));

    await waitFor(() => {
      expect(onLinkMessage).toHaveBeenCalledTimes(1);
    });
    expect(onLinkMessage).toHaveBeenCalledWith("uid-42", {
      message_id: 1001,
      conversation_id: 2001,
      subject: "Project sync",
      from: "alice@example.com",
      sent_at: "2026-05-15T09:00:00Z",
    });
  });

  it("shows a success toast after linking", async () => {
    vi.useFakeTimers();
    const kata = makeKata(async () =>
      searchResponse([fakeIssue({ id: 42, uid: "uid-42", qualified_id: "Kata#42", title: "Pick me" })]),
    );
    const onLinkMessage = vi.fn().mockResolvedValue({ qualified_id: "Kata#42" });
    renderDetail(makeDetail({ id: 1001, conversation_id: 2001 }), { kata, onLinkMessage });

    await fireEvent.click(screen.getByRole("button", { name: /Link to task/i }));
    await fireEvent.input(screen.getByPlaceholderText(/Title or qualified ID/i), { target: { value: "test" } });
    await vi.advanceTimersByTimeAsync(250);
    await fireEvent.click(await waitFor(() => screen.getByRole("button", { name: /Kata#42.*Pick me/i })));
    await fireEvent.click(screen.getByRole("button", { name: /^Link$/ }));

    const toast = await waitFor(() => screen.getByRole("status"));
    expect(toast.textContent).toContain("Linked to Kata#42.");
  });

  it("shows link failures as an alert", async () => {
    vi.useFakeTimers();
    const kata = makeKata(async () =>
      searchResponse([fakeIssue({ id: 42, uid: "uid-42", qualified_id: "Kata#42", title: "Pick me" })]),
    );
    const onLinkMessage = vi.fn().mockRejectedValueOnce(new Error("oops"));
    renderDetail(makeDetail({ id: 1001, conversation_id: 2001 }), { kata, onLinkMessage });

    await fireEvent.click(screen.getByRole("button", { name: /Link to task/i }));
    await fireEvent.input(screen.getByPlaceholderText(/Title or qualified ID/i), { target: { value: "test" } });
    await vi.advanceTimersByTimeAsync(250);
    await fireEvent.click(await waitFor(() => screen.getByRole("button", { name: /Kata#42.*Pick me/i })));
    await fireEvent.click(screen.getByRole("button", { name: /^Link$/ }));

    const alert = await waitFor(() => screen.getByRole("alert"));
    expect(alert.textContent).toContain("oops");
  });
});
