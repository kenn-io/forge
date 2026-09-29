// The message gutter is pure CSS: hover/focus reveal, a reserved column so it
// never reflows the transcript, and a container query that drops it in narrow
// panes. jsdom has no layout or container queries, so this runs in Chromium.

import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { userEvent } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";

import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import type { TerminalSessionOptions } from "../terminal/terminal-session.js";
import ACPWorkspace from "./ACPWorkspace.svelte";

const socket = vi.hoisted(() => ({ options: undefined as TerminalSessionOptions | undefined }));
const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));

vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtimeCapture.current }));
vi.mock("../terminal/terminal-session.js", async () => {
  const { Effect } = await import("effect");
  return {
    makeTerminalSessionController: (options: TerminalSessionOptions) => {
      socket.options = options;
      return { program: Effect.never, send: () => undefined, isConnected: () => true };
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
      configOptions: [],
      configuring: false,
      historyTruncated: false,
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

  it("omits the gutter in a narrow pane", async () => {
    const { gutter } = await renderChat(420);
    expect(getComputedStyle(gutter).display).toBe("none");
  });
});
