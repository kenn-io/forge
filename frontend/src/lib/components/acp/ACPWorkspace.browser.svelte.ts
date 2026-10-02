// The message gutter is pure CSS: hover/focus reveal, a reserved column so it
// never reflows the transcript, and a container query that drops it in narrow
// panes. jsdom has no layout or container queries, so this runs in Chromium.

import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";

import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import type { TerminalSessionOptions } from "../terminal/terminal-session.js";
import ACPWorkspace from "./ACPWorkspace.svelte";

const socket = vi.hoisted(() => ({ options: undefined as TerminalSessionOptions | undefined, sent: [] as string[] }));
const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));

vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtimeCapture.current }));
vi.mock("../terminal/terminal-session.js", async () => {
  const { Effect } = await import("effect");
  return {
    makeTerminalSessionController: (options: TerminalSessionOptions) => {
      socket.options = options;
      return {
        program: Effect.never,
        send: (data: string | Uint8Array) => {
          if (typeof data === "string") socket.sent.push(data);
        },
        isConnected: () => true,
      };
    },
  };
});

const nextFrame = () => new Promise((resolve) => requestAnimationFrame(resolve));

async function renderChat(width: number) {
  const host = document.createElement("div");
  // Mirrors the pooled session host: a full-size flex column.
  host.style.cssText = `width: ${width}px; height: 600px; display: flex; flex-direction: column;`;
  document.body.append(host);
  await render(ACPWorkspace, { target: host, props: { websocketPath: "/ws/chat" } });
  await expect.poll(() => socket.options).toBeDefined();
  socket.options!.onOpen?.();
  socket.options!.onMessage(
    JSON.stringify({
      messages: [
        { role: "user", text: "Summarize the change", createdAt: "2026-09-28T21:09:00Z" },
        { role: "assistant", text: "The change adds a gutter.", createdAt: "2026-09-28T21:10:00Z" },
      ],
      messageOffset: 0,
      messageCount: 2,
      configOptions: [],
      configuring: false,
      permissions: [],
      busy: false,
      connected: true,
      error: "",
    }),
  );
  await nextFrame();
  await nextFrame();
  const article = host.querySelector<HTMLElement>('article[aria-label="Assistant"]')!;
  return {
    host,
    article,
    cell: article.querySelector<HTMLElement>(".cell")!,
    gutter: article.querySelector<HTMLElement>(".gutter")!,
  };
}

beforeEach(() => {
  socket.options = undefined;
  socket.sent = [];
  runtimeCapture.current = makeAppRuntime();
});

afterEach(async () => {
  document.body.replaceChildren();
  if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
  runtimeCapture.current = undefined;
});

describe("ACPWorkspace message gutter (browser)", () => {
  it("reveals time and copy left of the message on hover and focus without reflow", async () => {
    const { host, article, cell, gutter } = await renderChat(900);
    const conversation = host.querySelector<HTMLElement>(".conversation")!;
    expect(getComputedStyle(gutter).display).toBe("flex");
    expect(getComputedStyle(gutter).opacity).toBe("0");
    const before = article.getBoundingClientRect();

    await userEvent.hover(cell);
    await expect.poll(() => getComputedStyle(gutter).opacity).toBe("1");
    const after = article.getBoundingClientRect();
    expect(after.height).toBe(before.height);
    expect(after.width).toBe(before.width);
    const gutterRect = gutter.getBoundingClientRect();
    expect(gutterRect.right).toBeLessThanOrEqual(cell.getBoundingClientRect().left);
    expect(gutterRect.left).toBeGreaterThanOrEqual(conversation.getBoundingClientRect().left);
    expect(gutter.querySelector("time")?.textContent).not.toBe("");

    await userEvent.unhover(cell);
    await expect.poll(() => getComputedStyle(gutter).opacity).toBe("0");
    gutter.querySelector<HTMLButtonElement>('button[aria-label="Copy message"]')!.focus();
    await expect.poll(() => getComputedStyle(gutter).opacity).toBe("1");
  });

  it("shows time and copy as a visible line below the message in a narrow pane", async () => {
    const { cell, gutter } = await renderChat(420);
    const body = cell.querySelector<HTMLElement>(".message-body")!;
    const copy = gutter.querySelector<HTMLButtonElement>('button[aria-label="Copy message"]')!;

    expect(getComputedStyle(gutter).position).toBe("static");
    expect(getComputedStyle(gutter).opacity).toBe("1");
    expect(gutter.querySelector("time")?.textContent).not.toBe("");
    expect(copy.getBoundingClientRect().width).toBeGreaterThan(0);
    expect(gutter.getBoundingClientRect().top).toBeGreaterThanOrEqual(body.getBoundingClientRect().bottom);
    expect(Math.abs(gutter.getBoundingClientRect().left - body.getBoundingClientRect().left)).toBeLessThanOrEqual(1);
  });
});

describe("ACPWorkspace notices and errors (browser)", () => {
  it("keeps errors and the notice icon in the reading column", async () => {
    const { host } = await renderChat(1400);
    socket.options!.onMessage(
      JSON.stringify({
        messages: [],
        messageOffset: 0,
        messageCount: 0,
        configOptions: [],
        configuring: false,
        permissions: [],
        busy: false,
        connected: true,
        notices: ["Forge tools are unavailable."],
        error: "Agent exited",
      }),
    );
    await expect.element(page.getByRole("alert")).toBeVisible();
    // The error box shares the composer dock's column, not the pane's left edge.
    const error = host.querySelector<HTMLElement>(".error")!.getBoundingClientRect();
    const dock = host.querySelector<HTMLElement>(".dock")!.getBoundingClientRect();
    expect(dock.left).toBeGreaterThan(host.getBoundingClientRect().left + 100);
    expect(Math.abs(error.left - dock.left)).toBeLessThanOrEqual(1);
    expect(Math.abs(error.right - dock.right)).toBeLessThanOrEqual(1);
    // The notice icon ends the header row at the column's right edge.
    const headerElement = host.querySelector<HTMLElement>(".chat-status__inner")!;
    const header = headerElement.getBoundingClientRect();
    const contentRight = header.right - parseFloat(getComputedStyle(headerElement).paddingRight);
    const icon = page.getByRole("img", { name: "Forge tools are unavailable." }).element().getBoundingClientRect();
    expect(Math.abs(icon.right - contentRight)).toBeLessThanOrEqual(1);
    expect(icon.top).toBeGreaterThanOrEqual(header.top);
    expect(icon.bottom).toBeLessThanOrEqual(header.bottom);
  });
});

describe("ACPWorkspace image output (browser)", () => {
  it("decodes tool and Markdown images while tools stay collapsed", async () => {
    await page.viewport(1000, 720);
    const { host } = await renderChat(900);
    const data = btoa(
      '<svg xmlns="http://www.w3.org/2000/svg" width="320" height="120"><rect width="320" height="120" fill="#2563eb"/><text x="24" y="68" fill="white" font-size="24">Image from agent</text></svg>',
    );
    socket.options!.onMessage(
      JSON.stringify({
        messages: [
          { role: "user", text: "Show the image in chat" },
          {
            role: "tool",
            text: "Create image",
            toolCallId: "image",
            status: "completed",
            toolContent: [
              { type: "content", content: { type: "image", mimeType: "image/svg+xml", data, title: "Tool image" } },
            ],
          },
          { role: "assistant", text: `![Markdown image](data:image/svg+xml;base64,${data})` },
        ],
        messageOffset: 0,
        messageCount: 3,
        configOptions: [],
        configuring: false,
        permissions: [],
        busy: false,
        connected: true,
        error: "",
      }),
    );
    await expect.element(page.getByRole("img", { name: "Tool image" })).toBeVisible();
    await expect.element(page.getByRole("img", { name: "Markdown image" })).toBeVisible();
    await expect.poll(() => [...host.querySelectorAll("img")].map((image) => image.naturalWidth)).toEqual([320, 320]);
    expect(host.querySelector('button[aria-controls$="-list"]')?.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("ACPWorkspace grouped activity (browser)", () => {
  it("expands interleaved thoughts with the keyboard in a narrow pane", async () => {
    await page.viewport(420, 720);
    const { host } = await renderChat(420);
    socket.options!.onMessage(
      JSON.stringify({
        messages: [
          { role: "tool", text: "Read options", status: "completed", toolCallId: "read" },
          { role: "thought", text: "Compare the available options." },
          { role: "tool", text: "Check result", status: "failed", toolCallId: "check" },
          { role: "assistant", text: "Here is the result." },
        ],
        messageOffset: 0,
        messageCount: 4,
        configOptions: [],
        configuring: false,
        permissions: [],
        busy: false,
        connected: true,
        error: "",
      }),
    );
    const group = page.getByRole("button", { name: "2 tools · 1 thought · 1 failed" });
    await expect.element(group).toHaveAttribute("aria-expanded", "false");
    await expect.element(page.getByText("Compare the available options.")).not.toBeInTheDocument();
    (group.element() as HTMLButtonElement).focus();
    await userEvent.keyboard("{Enter}");
    const thought = page.getByRole("button", { name: "Thinking" });
    await expect.element(thought).toBeVisible();
    const thoughtCopy = page
      .getByRole("article", { name: "Assistant thinking", exact: true })
      .getByRole("button", { name: "Copy message" });
    await expect.element(thought).toHaveAttribute("aria-expanded", "false");
    await expect.element(thoughtCopy).not.toBeInTheDocument();
    (thought.element() as HTMLButtonElement).focus();
    await userEvent.keyboard("{Enter}");
    const thoughtText = page.getByText("Compare the available options.");
    await expect.element(thoughtText).toBeVisible();
    await expect.element(thoughtCopy).toBeVisible();
    const toolFontSize = getComputedStyle(page.getByText("Read options").element()).fontSize;
    expect(getComputedStyle(thoughtText.element()).fontSize).toBe(toolFontSize);
    expect(getComputedStyle(thought.element()).fontSize).toBe(toolFontSize);
    expect(getComputedStyle(group.element()).fontSize).toBe(toolFontSize);
    const copyBounds = thoughtCopy.element().getBoundingClientRect();
    const textBounds = thoughtText.element().getBoundingClientRect();
    expect(copyBounds.right).toBeLessThanOrEqual(textBounds.left);
    expect(copyBounds.top).toBeLessThan(textBounds.bottom);
    expect(copyBounds.bottom).toBeGreaterThan(textBounds.top);
    await thought.click();
    await expect.element(thoughtCopy).not.toBeInTheDocument();
    const conversation = host.querySelector<HTMLElement>(".conversation")!;
    expect(conversation.scrollWidth).toBeLessThanOrEqual(conversation.clientWidth);
    await group.click();
    await expect.element(page.getByText("Compare the available options.")).not.toBeInTheDocument();
    await expect
      .element(page.getByRole("article", { name: "Assistant", exact: true }).getByText("Here is the result."))
      .toBeVisible();
  });
});

describe("ACPWorkspace transcript paging (browser)", () => {
  const message = (index: number) => ({
    role: index % 2 ? "assistant" : "user",
    text: `Message ${index}: ${"a longer line of transcript text ".repeat((index % 3) + 1)}`,
  });
  const range = (from: number, to: number) => Array.from({ length: to - from }, (_, offset) => message(from + offset));

  it("keeps the reader's place when earlier messages load above", async () => {
    const host = document.createElement("div");
    host.style.cssText = "width: 900px; height: 600px; display: flex; flex-direction: column;";
    document.body.append(host);
    await render(ACPWorkspace, { target: host, props: { websocketPath: "/ws/chat" } });
    await expect.poll(() => socket.options).toBeDefined();
    socket.options!.onOpen?.();
    socket.options!.onMessage(
      JSON.stringify({
        messages: range(200, 240),
        messageOffset: 200,
        messageCount: 240,
        configOptions: [],
        configuring: false,
        permissions: [],
        busy: false,
        connected: true,
        error: "",
      }),
    );
    await nextFrame();
    await nextFrame();
    const conversation = host.querySelector<HTMLElement>(".conversation")!;
    expect(conversation.scrollHeight).toBeGreaterThan(conversation.clientHeight);
    // WebKit has no scroll anchoring; turn Chromium's off so the pane's own
    // compensation is what holds the position.
    conversation.style.overflowAnchor = "none";

    // Scroll near the top: the pane asks for the page before the window.
    conversation.scrollTop = 120;
    conversation.dispatchEvent(new Event("scroll"));
    await expect
      .poll(() => socket.sent.map((frame) => JSON.parse(frame) as unknown))
      .toContainEqual({
        type: "history",
        before: 200,
        limit: 100,
      });
    const anchor = [...host.querySelectorAll<HTMLElement>("article")].find((article) =>
      article.textContent?.includes("Message 203:"),
    )!;
    const before = anchor.getBoundingClientRect().top;

    socket.options!.onMessage(JSON.stringify({ history: { offset: 100, messages: range(100, 200) } }));
    await nextFrame();
    await nextFrame();

    expect(host.querySelector("article")?.textContent).toContain("Message 100:");
    expect(conversation.scrollTop).toBeGreaterThan(1000);
    // Within sub-pixel rounding of the integer scroll offset.
    expect(Math.abs(anchor.getBoundingClientRect().top - before)).toBeLessThan(2);
  });
});
