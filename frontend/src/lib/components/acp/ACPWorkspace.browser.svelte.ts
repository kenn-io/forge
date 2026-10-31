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
import { fixtureMessages, fixtureState } from "./chat-test-fixtures.js";
import ACPWorkspace from "./ACPWorkspace.svelte";
import { STORES_KEY } from "../../context.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";

let settings: ReturnType<typeof createSettingsStore>;

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

function encodeFixture(frame: Record<string, unknown>) {
  return JSON.stringify("messages" in frame ? fixtureState(frame) : frame);
}

const nextFrame = () => new Promise((resolve) => requestAnimationFrame(resolve));

async function renderChat(width: number) {
  const host = document.createElement("div");
  // Mirrors the pooled session host: a full-size flex column.
  host.style.cssText = `width: ${width}px; height: 600px; display: flex; flex-direction: column;`;
  document.body.append(host);
  await render(ACPWorkspace, {
    context: new Map([[STORES_KEY, { settings }]]),
    target: host,
    props: { websocketPath: "/ws/chat" },
  });
  await expect.poll(() => socket.options).toBeDefined();
  socket.options!.onOpen?.();
  socket.options!.onMessage(
    encodeFixture({
      messages: [
        { role: "user", text: "Summarize the change", createdAt: "2026-09-28T21:09:00Z" },
        { role: "assistant", text: "The change adds a gutter.", createdAt: "2026-09-28T21:10:00Z" },
      ],
      messageCursor: 1,
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
  settings = createSettingsStore();
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

describe("ACPWorkspace click to focus composer (browser)", () => {
  // The primary-pointer media query describes the device, not the click:
  // touchscreen laptops report a fine pointer, tablets with a mouse a coarse one.
  function primaryPointer(coarse: boolean) {
    vi.stubGlobal(
      "matchMedia",
      vi.fn((query: string) => ({ matches: query === "(pointer: coarse)" && coarse })),
    );
  }

  // Points on the reply's one short line and in the empty space to its right,
  // relative to the block that holds the line.
  async function renderReply() {
    const { host, cell } = await renderChat(900);
    const walker = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
    let text = walker.nextNode();
    while (text && !text.textContent?.includes("The change adds a gutter.")) text = walker.nextNode();
    const block = text!.parentElement!;
    const range = document.createRange();
    range.selectNodeContents(text!);
    const line = range.getBoundingClientRect();
    const box = block.getBoundingClientRect();
    const y = line.top - box.top + line.height / 2;
    const onText = { x: line.left - box.left + 4, y };
    const empty = { x: box.width - 4, y };
    expect(box.left + empty.x).toBeGreaterThan(line.right + 20);
    return { block, onText, empty, composer: host.querySelector("textarea")! };
  }

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("focuses on a click in the empty space beside a short line", async () => {
    primaryPointer(false);
    const { block, empty, composer } = await renderReply();

    await userEvent.click(block, { position: empty });
    expect(document.activeElement).toBe(composer);
  });

  it("leaves focus alone on a click on message text", async () => {
    primaryPointer(false);
    const { block, onText, composer } = await renderReply();

    await userEvent.click(block, { position: onText });
    expect(document.activeElement).not.toBe(composer);
  });

  it("skips a touch tap on a device whose primary pointer is fine", async () => {
    primaryPointer(false);
    const { block, empty, composer } = await renderReply();
    const box = block.getBoundingClientRect();

    block.dispatchEvent(
      new PointerEvent("click", {
        bubbles: true,
        button: 0,
        pointerType: "touch",
        clientX: box.left + empty.x,
        clientY: box.top + empty.y,
      }),
    );
    expect(document.activeElement).not.toBe(composer);
  });

  it("focuses on a mouse click on a device whose primary pointer is coarse", async () => {
    primaryPointer(true);
    const { block, empty, composer } = await renderReply();

    await userEvent.click(block, { position: empty });
    expect(document.activeElement).toBe(composer);
  });
});

describe("ACPWorkspace notices and errors (browser)", () => {
  it("keeps errors and the notice icon in the reading column", async () => {
    const { host } = await renderChat(1400);
    socket.options!.onMessage(
      encodeFixture({
        messages: [],
        messageCursor: 1,
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
    // The notice icon sits in the composer's control row, before the send button.
    const composer = host.querySelector<HTMLElement>(".composer")!.getBoundingClientRect();
    const send = page.getByRole("button", { name: "Send", exact: true }).element().getBoundingClientRect();
    const icon = page.getByRole("img", { name: "Forge tools are unavailable." }).element().getBoundingClientRect();
    expect(icon.right).toBeLessThanOrEqual(send.left);
    expect(icon.left).toBeGreaterThanOrEqual(composer.left);
    expect(icon.top).toBeGreaterThanOrEqual(composer.top);
    expect(icon.bottom).toBeLessThanOrEqual(composer.bottom);
  });
});

describe("ACPWorkspace turn status (browser)", () => {
  const crowdedToolbar = {
    messages: [{ role: "user", text: "Summarize the change" }],
    messageCursor: 1,
    messageOffset: 0,
    messageCount: 1,
    configOptions: [
      {
        id: "model",
        name: "Model",
        category: "model",
        type: "select",
        currentValue: "fast",
        options: [{ value: "fast", name: "Fast model with a long name" }],
      },
      {
        id: "effort",
        name: "Effort",
        category: "thought_level",
        type: "select",
        currentValue: "high",
        options: [{ value: "high", name: "High" }],
      },
    ],
    configuring: false,
    permissions: [],
    busy: false,
    connected: true,
    error: "",
  };

  async function pushTurn(state: Record<string, unknown>) {
    socket.options!.onMessage(encodeFixture({ ...crowdedToolbar, ...state }));
    await nextFrame();
    await nextFrame();
  }

  function layout(host: HTMLElement) {
    return {
      composer: host.querySelector<HTMLElement>(".composer")!.getBoundingClientRect().toJSON() as DOMRect,
      conversation: host.querySelector<HTMLElement>(".conversation")!.getBoundingClientRect().toJSON() as DOMRect,
      primary: host.querySelector<HTMLElement>(".round.send")!.getBoundingClientRect().toJSON() as DOMRect,
    };
  }

  // A pane with room for the chips and one at phone width, where they crowd
  // the send button and any added width would wrap the row.
  it.each([900, 360])("shows only a spinner by the stop button while a %ipx chat replies", async (width) => {
    const { host } = await renderChat(width);
    await pushTurn({ busy: false, steeringSupported: true });
    const idle = layout(host);

    await pushTurn({ busy: true, steeringSupported: true });
    await expect.element(page.getByRole("button", { name: "Stop reply" })).toBeVisible();
    expect(layout(host)).toEqual(idle);

    const stop = page.getByRole("button", { name: "Stop reply" }).element().getBoundingClientRect();
    const spinner = host.querySelector<HTMLElement>(".toolbar .kit-spinner")!.getBoundingClientRect();
    expect(spinner.width).toBeGreaterThan(0);
    expect(spinner.right).toBeLessThanOrEqual(stop.left);
    expect(stop.left - spinner.right).toBeLessThanOrEqual(8);

    // Announced, but clipped to nothing on screen.
    const status = host.querySelector<HTMLElement>('.toolbar [role="status"]')!;
    expect(status.textContent.trim()).toBe("Replying…");
    expect(getComputedStyle(status).clipPath).toBe("inset(50%)");
    expect(status.getBoundingClientRect().width).toBeLessThanOrEqual(1);

    // Stopping adds visible text to the row, and must still fit beside the
    // queue, stop, and steer controls.
    await pushTurn({ busy: true, steeringSupported: true, stopping: true });
    expect(layout(host)).toEqual(idle);

    await pushTurn({ busy: false, steeringSupported: true });
    expect(host.querySelector(".toolbar .kit-spinner")).toBeNull();
    expect(layout(host)).toEqual(idle);
  });

  it("starts the conversation at the top of the pane", async () => {
    const { host } = await renderChat(900);
    const conversation = host.querySelector<HTMLElement>(".conversation")!.getBoundingClientRect();
    expect(conversation.top).toBe(host.getBoundingClientRect().top);
  });

  it.each([
    ["Stopping…", { busy: true, stopping: true }],
    ["Needs your answer", { busy: true, permissions: [{ id: "p1", title: "Run a command", options: [] }] }],
    ["Agent disconnected", { connected: false }],
  ])("shows %s as visible text in the composer's control row", async (text, state) => {
    const { host } = await renderChat(360);
    await pushTurn(state);
    const status = host.querySelector<HTMLElement>('.toolbar [role="status"]')!;
    expect(status.textContent.trim()).toBe(text);
    const box = status.getBoundingClientRect();
    const composer = host.querySelector<HTMLElement>(".composer")!.getBoundingClientRect();
    expect(box.width).toBeGreaterThan(1);
    expect(getComputedStyle(status).clipPath).toBe("none");
    expect(box.left).toBeGreaterThanOrEqual(composer.left);
    expect(box.right).toBeLessThanOrEqual(host.querySelector<HTMLElement>(".round.send")!.getBoundingClientRect().left);
    expect(box.bottom).toBeLessThanOrEqual(composer.bottom);
  });

  it("keeps the send controls inside a narrow composer at the largest chat font", async () => {
    const { host } = await renderChat(320);
    settings.setACPSettings({ font_family: "monospace", font_size: 32 });
    await pushTurn({
      busy: true,
      steeringSupported: true,
      permissions: [{ id: "p1", title: "Run a command", options: [] }],
    });
    const status = host.querySelector<HTMLElement>('.toolbar [role="status"]')!;
    expect(status.textContent.trim()).toBe("Needs your answer");
    const composer = host.querySelector<HTMLElement>(".composer")!.getBoundingClientRect();
    const steer = page.getByRole("button", { name: "Steer reply" }).element().getBoundingClientRect();
    expect(steer.right).toBeLessThanOrEqual(composer.right);
    expect(status.getBoundingClientRect().right).toBeLessThanOrEqual(
      page.getByRole("button", { name: "Queue", exact: true }).element().getBoundingClientRect().left,
    );
    expect(host.scrollWidth).toBeLessThanOrEqual(host.clientWidth);
  });

  it("wraps the parked explanation inside the composer of a narrow pane", async () => {
    const host = document.createElement("div");
    host.style.cssText = "width: 320px; height: 600px; display: flex; flex-direction: column;";
    document.body.append(host);
    await render(ACPWorkspace, {
      context: new Map([[STORES_KEY, { settings }]]),
      target: host,
      props: { websocketPath: "/ws/chat", status: "parked" },
    });
    const status = host.querySelector<HTMLElement>('.toolbar [role="status"]')!;
    expect(status.textContent.trim()).toBe("Stopped while idle; it restarts when the workspace shows");
    const box = status.getBoundingClientRect();
    const composer = host.querySelector<HTMLElement>(".composer")!.getBoundingClientRect();
    expect(box.left).toBeGreaterThanOrEqual(composer.left);
    expect(box.right).toBeLessThanOrEqual(host.querySelector<HTMLElement>(".round.send")!.getBoundingClientRect().left);
    expect(box.bottom).toBeLessThanOrEqual(composer.bottom);
    expect(host.scrollWidth).toBeLessThanOrEqual(host.clientWidth);
  });
});

describe("ACPWorkspace image output (browser)", () => {
  it("loads embedded and HTTP Markdown images while tools stay collapsed", async () => {
    await page.viewport(1000, 720);
    const { host } = await renderChat(900);
    const data = btoa(
      '<svg xmlns="http://www.w3.org/2000/svg" width="320" height="120"><rect width="320" height="120" fill="#2563eb"/><text x="24" y="68" fill="white" font-size="24">Image from agent</text></svg>',
    );
    const imageURL = new URL("../../../../public/favicon.svg", import.meta.url).href;
    socket.options!.onMessage(
      encodeFixture({
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
          {
            role: "assistant",
            text: `![Markdown image](data:image/svg+xml;base64,${data})\n\n![URL image](${imageURL})`,
          },
        ],
        messageCursor: 1,
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
    await expect.element(page.getByRole("img", { name: "URL image" })).toBeVisible();
    const urlImage = host.querySelector<HTMLImageElement>('img[alt="URL image"]')!;
    expect(urlImage.getAttribute("src")).toBe(imageURL);
    await expect.poll(() => urlImage.naturalWidth).toBeGreaterThan(0);
    await expect
      .poll(() =>
        [...host.querySelectorAll('img:not([alt="URL image"])')].map(
          (image) => (image as HTMLImageElement).naturalWidth,
        ),
      )
      .toEqual([320, 320]);
    expect(host.querySelector('button[aria-controls$="-list"]')?.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("ACPWorkspace grouped activity (browser)", () => {
  it("fits toolbar labels at the maximum ACP font size", async () => {
    await renderChat(900);
    settings.setACPSettings({ font_family: "serif", font_size: 32 });
    socket.options!.onMessage(
      encodeFixture({
        messages: [],
        messageCursor: 1,
        messageOffset: 0,
        messageCount: 0,
        configOptions: [
          {
            id: "model",
            name: "Model",
            category: "model",
            type: "select",
            currentValue: "fast",
            options: [{ value: "fast", name: "Fast model" }],
          },
        ],
        configuring: false,
        permissions: [],
        busy: false,
        connected: true,
        error: "",
      }),
    );
    const button = page.getByRole("button", { name: "Model: Fast model" });
    await expect.element(button).toBeVisible();
    const bounds = button.element().getBoundingClientRect();
    const label = button.element().querySelector(".tb-chip__label")!.getBoundingClientRect();
    expect(label.top).toBeGreaterThanOrEqual(bounds.top);
    expect(label.bottom).toBeLessThanOrEqual(bounds.bottom);
  });

  it("finds a model by typing in the model chip", async () => {
    await renderChat(900);
    socket.options!.onMessage(
      encodeFixture({
        messages: [],
        messageCursor: 1,
        messageOffset: 0,
        messageCount: 0,
        configOptions: [
          {
            id: "model",
            name: "Model",
            category: "model",
            type: "select",
            currentValue: "fast",
            options: [
              { value: "fast", name: "Fast model" },
              { value: "deep", name: "Deep model" },
              { group: "preview", name: "Preview", options: [{ value: "draft", name: "Draft model" }] },
            ],
          },
        ],
        configuring: false,
        permissions: [],
        busy: false,
        connected: true,
        error: "",
      }),
    );
    await page.getByRole("button", { name: "Model: Fast model" }).click();
    await userEvent.keyboard("draf");
    await expect.element(page.getByText("Deep model")).not.toBeInTheDocument();
    await userEvent.keyboard("{Enter}");
    await expect
      .poll(() => socket.sent.map((frame) => JSON.parse(frame) as unknown))
      .toContainEqual({ type: "config", id: "model", value: "draft" });
  });

  it("keeps the configured composer size under the app touch-field rule", async () => {
    const { host } = await renderChat(900);
    host.id = "app";
    document.documentElement.classList.add("kit-type-touch");
    try {
      const composer = page.getByRole("textbox", { name: "Message agent" }).element();
      settings.setACPSettings({ font_family: "", font_size: 26 });
      await expect.poll(() => getComputedStyle(composer).fontSize).toBe("26px");
      settings.setACPSettings({ font_family: "", font_size: 13 });
      await expect.poll(() => getComputedStyle(composer).fontSize).toBe("16px");
    } finally {
      document.documentElement.classList.remove("kit-type-touch");
    }
  });

  it("applies ACP appearance to messages, code, tools, and composer without changing the app font", async () => {
    const { host } = await renderChat(900);
    const appFont = getComputedStyle(document.body).fontFamily;
    const composer = page.getByRole("textbox", { name: "Message agent" });
    expect(getComputedStyle(composer.element()).fontSize).toBe("13px");
    expect(getComputedStyle(host.querySelector(".markdown")!).fontSize).toBe("13px");
    settings.setTerminalSettings({ ...settings.getTerminalSettings(), font_family: "monospace", font_size: 11 });
    settings.setACPSettings({ font_family: "serif", font_size: 26 });
    socket.options!.onMessage(
      encodeFixture({
        messages: [
          { role: "user", text: "Review the code" },
          { role: "tool", text: "Read options", status: "completed", toolCallId: "read" },
          { role: "thought", text: "Compare the available options." },
          { role: "assistant", text: "Here is the result.\n\n```js\nconst value = 1;\n```" },
        ],
        messageCursor: 1,
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
    await expect.element(page.getByText("Here is the result.")).toBeVisible();
    await page.getByRole("button", { name: "1 tool · 1 thought" }).click();
    await page.getByRole("button", { name: "Thinking", exact: true }).click();
    for (const element of [
      composer.element(),
      page.getByText("Review the code").element(),
      page.getByText("Here is the result.").element(),
    ]) {
      expect(getComputedStyle(element).fontSize).toBe("26px");
      expect(getComputedStyle(element).fontFamily).toBe("serif");
    }
    for (const text of ["Read options", "Compare the available options."]) {
      expect(getComputedStyle(page.getByText(text).element()).fontSize).toBe("22px");
      expect(getComputedStyle(page.getByText(text).element()).fontFamily).toBe("serif");
    }
    const code = host.querySelector(".markdown pre code")!;
    expect(getComputedStyle(code).fontFamily).toBe("serif");
    expect(getComputedStyle(code.parentElement!).fontSize).toBe("24px");
    expect(getComputedStyle(document.body).fontFamily).toBe(appFont);
    settings.setACPSettings({ font_family: "", font_size: 13 });
    await expect.poll(() => getComputedStyle(composer.element()).fontSize).toBe("13px");
    expect(getComputedStyle(composer.element()).fontFamily).toBe(appFont);
    expect(getComputedStyle(code).fontFamily).toContain("monospace");
  });

  it("expands interleaved thoughts with the keyboard in a narrow pane", async () => {
    await page.viewport(420, 720);
    const { host } = await renderChat(420);
    socket.options!.onMessage(
      encodeFixture({
        messages: [
          { role: "tool", text: "Read options", status: "completed", toolCallId: "read" },
          { role: "thought", text: "Compare the available options." },
          { role: "tool", text: "Check result", status: "failed", toolCallId: "check" },
          { role: "assistant", text: "Here is the result." },
        ],
        messageCursor: 1,
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
    id: `entry-${index}`,
    previousId: index === 0 ? "" : `entry-${index - 1}`,
    version: 1,
    role: index % 2 ? "assistant" : "user",
    text: `Message ${index}: ${"a longer line of transcript text ".repeat((index % 3) + 1)}`,
  });
  const range = (from: number, to: number) => Array.from({ length: to - from }, (_, offset) => message(from + offset));

  it("renders separate tool calls when an agent reuses a call ID", async () => {
    const { host } = await renderChat(900);
    const tool = { role: "tool", toolCallId: "edit", createdAt: "2026-10-19T16:00:00Z", status: "completed" };
    socket.options!.onMessage(
      encodeFixture({
        messages: [
          { role: "user", text: "First turn" },
          { ...tool, text: "First edit" },
          { role: "user", text: "Second turn" },
          { ...tool, text: "Second edit" },
          { ...tool, text: "Third edit" },
        ],
        messageCursor: 1,
        messageOffset: 0,
        messageCount: 5,
        configOptions: [],
        configuring: false,
        permissions: [],
        busy: false,
        connected: true,
        error: "",
      }),
    );
    await expect.element(page.getByRole("button", { name: "1 tool", exact: true })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "2 tools", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "1 tool", exact: true }).click();
    await page.getByRole("button", { name: "2 tools", exact: true }).click();
    expect(host.textContent).toContain("First edit");
    expect(host.textContent).toContain("Second edit");
    expect(host.textContent).toContain("Third edit");
  });

  it("keeps an expanded thought group mounted as tools arrive and agent call IDs are reused", async () => {
    const { host } = await renderChat(900);
    const initial = fixtureState({
      messages: [{ role: "thought", text: "Considering the files" }],
      messageOffset: 0,
      messageCount: 1,
      configOptions: [],
      configuring: false,
      permissions: [],
      busy: true,
      connected: true,
      error: "",
    });
    socket.options!.onMessage(JSON.stringify(initial));
    await page.getByRole("button", { name: "1 thought", exact: true }).click();
    const group = host.querySelector<HTMLElement>(".tool-group")!;
    socket.options!.onMessage(
      JSON.stringify({
        updates: {
          ...initial,
          lastMessageId: "entry-1",
          messageCount: 2,
          messageCursor: 2,
          more: false,
          messages: fixtureMessages(
            [{ role: "tool", text: "First edit", toolCallId: "edit", status: "completed", version: 2 }],
            1,
          ),
        },
      }),
    );
    await expect
      .element(page.getByRole("button", { name: "1 tool · 1 thought", exact: true }))
      .toHaveAttribute("aria-expanded", "true");
    socket.options!.onMessage(
      JSON.stringify({
        updates: {
          ...initial,
          lastMessageId: "entry-3",
          messageCount: 4,
          messageCursor: 3,
          more: false,
          messages: fixtureMessages(
            [
              { role: "user", text: "Continue", version: 3 },
              { role: "tool", text: "Next edit", toolCallId: "edit", status: "completed", version: 3 },
            ],
            2,
          ),
        },
      }),
    );
    await expect.element(page.getByRole("button", { name: "1 tool", exact: true })).toBeVisible();
    expect(group.isConnected).toBe(true);
    await expect
      .element(page.getByRole("button", { name: "1 tool · 1 thought", exact: true }))
      .toHaveAttribute("aria-expanded", "true");
    expect(group.textContent).toContain("First edit");
  });

  it.each(["tool", "text"])(
    "keeps expanded details and the %s row reading position through steering and batched updates",
    async (anchorKind) => {
      const { host } = await renderChat(900);
      const tool = {
        role: "tool",
        toolCallId: "inspect-files",
        text: "Inspect files",
        status: "in_progress",
        toolContent: [{ type: "terminal", output: "Initial output" }],
      };
      const state = {
        messages: [...range(0, 9), { role: "thought", text: "Inspect the relevant files" }, tool, ...range(11, 40)],
        messageCursor: 1,
        messageOffset: 0,
        messageCount: 40,
        configOptions: [],
        configuring: false,
        permissions: [],
        busy: true,
        connected: true,
        error: "",
      };
      socket.options!.onMessage(encodeFixture(state));
      await nextFrame();
      await page.getByRole("button", { name: /1 tool · 1 thought · running/ }).click();
      await page.getByRole("button", { name: /Inspect files/ }).click();
      await expect.element(page.getByText("Initial output", { exact: true })).toBeVisible();
      const conversation = host.querySelector<HTMLElement>(".conversation")!;
      conversation.style.overflowAnchor = "none";
      const anchor =
        anchorKind === "tool"
          ? host.querySelector<HTMLElement>(".tool-group")!
          : [...host.querySelectorAll<HTMLElement>("article")].find((article) =>
              article.textContent?.includes("Message 4:"),
            )!;
      conversation.scrollTop += anchor.getBoundingClientRect().top - conversation.getBoundingClientRect().top - 2;
      conversation.dispatchEvent(new Event("scroll"));
      await nextFrame();
      const before = anchor.getBoundingClientRect().top;

      const updateState = { ...state, messageCursor: 2, lastMessageId: "entry-39", messageCount: 41 };
      socket.options!.onMessage(
        encodeFixture({
          updates: {
            ...updateState,
            more: true,
            messages: [
              { id: "steering", previousId: "", version: 2, role: "user", text: "Steer the work" },
              { ...message(0), previousId: "steering", version: 2 },
            ],
          },
        }),
      );
      await nextFrame();
      await expect.element(page.getByText("Initial output", { exact: true })).toBeVisible();
      socket.options!.onMessage(
        encodeFixture({
          updates: {
            ...updateState,
            more: false,
            messages: [
              {
                ...tool,
                id: "entry-10",
                previousId: "entry-9",
                version: 2,
                status: "completed",
                toolContent: [{ type: "terminal", output: "Final output" }],
              },
            ],
          },
        }),
      );
      expect(socket.sent).toEqual([]);
      await expect.element(page.getByText("Final output", { exact: true })).toBeVisible();
      await nextFrame();
      await nextFrame();
      expect(anchor.isConnected).toBe(true);
      if (anchorKind === "text") expect(anchor.textContent).toContain("Message 4:");
      expect(Math.abs(anchor.getBoundingClientRect().top - before)).toBeLessThan(2);
      await expect
        .element(page.getByRole("button", { name: "1 tool · 1 thought", exact: true }))
        .toHaveAttribute("aria-expanded", "true");
      await expect
        .element(page.getByRole("button", { name: /Inspect files/ }))
        .toHaveAttribute("aria-expanded", "true");
    },
  );

  it.each(["prepend", "split"])(
    "keeps expanded tools and the reading position when activity groups %s",
    async (change) => {
      const { host } = await renderChat(900);
      const tool = (index: number) => ({
        id: `entry-${index}`,
        previousId: `entry-${index - 1}`,
        version: 1,
        role: "tool",
        toolCallId: `call-${index}`,
        text: `Inspect ${index}`,
        status: "completed",
        toolContent: [{ type: "terminal", output: `Output ${index}\n${"line of output\n".repeat(35)}` }],
      });
      const initial = {
        messages: [tool(10), tool(11), ...range(12, 40)],
        messageOffset: 10,
        messageCount: 40,
        messageCursor: 1,
        lastMessageId: "entry-39",
        configOptions: [],
        configuring: false,
        permissions: [],
        busy: false,
        connected: true,
        error: "",
      };
      socket.options!.onMessage(encodeFixture(initial));
      await page.getByRole("button", { name: "2 tools", exact: true }).click();
      await page.getByRole("button", { name: /Inspect 10/ }).click();
      await page.getByRole("button", { name: /Inspect 11/ }).click();
      const conversation = host.querySelector<HTMLElement>(".conversation")!;
      conversation.style.overflowAnchor = "none";
      const readingRow = [...host.querySelectorAll<HTMLElement>(".tool-group li")].find((row) =>
        row.textContent?.includes("Inspect 11"),
      )!;
      conversation.scrollTop += readingRow.getBoundingClientRect().top - conversation.getBoundingClientRect().top + 2;
      conversation.dispatchEvent(new Event("scroll"));
      await nextFrame();
      const before = readingRow.getBoundingClientRect().top;
      const originalGroup = readingRow.closest(".tool-group");
      if (change === "prepend") {
        host
          .querySelector<HTMLButtonElement>(".earlier button")!
          .dispatchEvent(new MouseEvent("click", { bubbles: true }));
        await expect
          .poll(() => socket.sent.map((frame) => JSON.parse(frame)))
          .toContainEqual({ type: "history", beforeId: "entry-10", limit: 100 });
        socket.options!.onMessage(encodeFixture({ history: { beforeId: "entry-10", offset: 9, messages: [tool(9)] } }));
      } else {
        socket.options!.onMessage(
          encodeFixture({
            updates: {
              ...initial,
              messageCount: 41,
              messageCursor: 2,
              more: false,
              messages: [
                { id: "steer", previousId: "entry-10", version: 2, role: "user", text: "Focus on the second file" },
                { ...tool(11), previousId: "steer", version: 2 },
              ],
            },
          }),
        );
      }
      await expect.element(page.getByRole("button", { name: /Inspect 11/ })).toHaveAttribute("aria-expanded", "true");
      await nextFrame();
      await nextFrame();
      const restoredRow = [...host.querySelectorAll<HTMLElement>(".tool-group li")].find((row) =>
        row.textContent?.includes("Inspect 11"),
      )!;
      expect(restoredRow.textContent).toContain("Output 11");
      expect(Math.abs(restoredRow.getBoundingClientRect().top - before)).toBeLessThan(2);
      if (change === "prepend") expect(restoredRow.closest(".tool-group")).toBe(originalGroup);
      else expect(restoredRow.closest(".tool-group")).not.toBe(originalGroup);
    },
  );

  it.each([false, true])("keeps expanded details and inner scroll through steering (hidden: %s)", async (hidden) => {
    const { host } = await renderChat(900);
    const thought = {
      id: "entry-11",
      previousId: "entry-10",
      version: 1,
      role: "thought",
      text: "Compare the file changes before editing.",
    };
    const tool = {
      id: "entry-12",
      previousId: "entry-11",
      version: 1,
      role: "tool",
      toolCallId: "inspect",
      text: "Inspect changes",
      status: "completed",
      rawInput: JSON.stringify({ paths: Array.from({ length: 25 }, (_, index) => `src/file-${index}.ts`) }, null, 2),
      rawOutput: JSON.stringify(
        { results: Array.from({ length: 40 }, (_, index) => `Finished file ${index}`) },
        null,
        2,
      ),
      toolContent: [
        {
          type: "terminal",
          terminalId: "inspect-output",
          output: Array.from({ length: 80 }, (_, index) => `Line ${index}: ${"long output ".repeat(40)}`).join("\n"),
        },
        {
          type: "diff",
          path: "src/example.ts",
          oldText: "",
          newText: Array.from({ length: 80 }, (_, index) => `const value${index} = ${index};`).join("\n"),
        },
      ],
    };
    const initial = {
      messages: [
        {
          id: "entry-10",
          previousId: "entry-9",
          version: 1,
          role: "tool",
          text: "Prepare inspection",
          status: "completed",
        },
        thought,
        tool,
        ...range(13, 40),
      ],
      messageOffset: 10,
      messageCount: 40,
      messageCursor: 1,
      lastMessageId: "entry-39",
      configOptions: [],
      configuring: false,
      permissions: [],
      busy: false,
      connected: true,
      error: "",
    };
    socket.options!.onMessage(encodeFixture(initial));
    await page.getByRole("button", { name: "2 tools · 1 thought", exact: true }).click();
    await page.getByRole("button", { name: "Thinking", exact: true }).click();
    await page.getByRole("button", { name: /Inspect changes/ }).click();
    await page.getByText("Raw input", { exact: true }).click();
    await page.getByText("Raw output", { exact: true }).click();
    const rawDetails = () => [...host.querySelectorAll<HTMLDetailsElement>("details.raw")];
    await expect.poll(() => rawDetails().map((details) => details.open)).toEqual([true, true]);
    const scrollRegions = () => [
      host.querySelector<HTMLElement>(".terminal-output pre")!,
      host.querySelector<HTMLElement>(".diff ol")!,
      ...rawDetails().map((details) => details.querySelector<HTMLElement>("pre")!),
    ];
    for (const region of scrollRegions()) {
      expect(region.scrollHeight).toBeGreaterThan(region.clientHeight + 90);
      region.scrollTop = 90;
      region.dispatchEvent(new Event("scroll"));
    }
    const terminalOutput = scrollRegions()[0]!;
    expect(terminalOutput.scrollWidth).toBeGreaterThan(terminalOutput.clientWidth + 120);
    terminalOutput.scrollLeft = 120;
    terminalOutput.dispatchEvent(new Event("scroll"));
    await nextFrame();
    const priorScroll = scrollRegions().map((region) => ({ top: region.scrollTop, left: region.scrollLeft }));
    const oldThought = host.querySelector('article[aria-label="Assistant thinking"]')!;
    const output = rawDetails()[1]!.querySelector("pre")!;
    const conversation = host.querySelector<HTMLElement>(".conversation")!;
    conversation.style.overflowAnchor = "none";
    conversation.scrollTop += output.getBoundingClientRect().top - conversation.getBoundingClientRect().top + 2;
    conversation.dispatchEvent(new Event("scroll"));
    await nextFrame();
    const before = output.getBoundingClientRect().top;
    if (hidden) host.style.display = "none";
    socket.options!.onMessage(
      encodeFixture({
        updates: {
          ...initial,
          messageCount: 41,
          messageCursor: 2,
          more: false,
          messages: [
            { id: "steer", previousId: "entry-10", version: 2, role: "user", text: "Explain the changes" },
            { ...thought, previousId: "steer", version: 2 },
          ],
        },
      }),
    );
    if (hidden) {
      await nextFrame();
      await nextFrame();
      expect(terminalOutput.isConnected).toBe(false);
      expect(scrollRegions()[0]!.getClientRects()).toHaveLength(0);
      host.style.display = "flex";
    }
    await expect
      .element(page.getByRole("button", { name: "Thinking", exact: true }))
      .toHaveAttribute("aria-expanded", "true");
    await expect.poll(() => rawDetails().map((details) => details.open)).toEqual([true, true]);
    await nextFrame();
    await nextFrame();
    expect(oldThought.isConnected).toBe(false);
    expect(terminalOutput.isConnected).toBe(false);
    await expect
      .poll(() => scrollRegions().map((region) => ({ top: region.scrollTop, left: region.scrollLeft })))
      .toEqual(priorScroll);
    expect(host.querySelector(".thought-body")!.textContent).toContain(thought.text);
    if (!hidden)
      expect(Math.abs(rawDetails()[1]!.querySelector("pre")!.getBoundingClientRect().top - before)).toBeLessThan(2);
  });

  it("keeps the reader's place when earlier messages load above", async () => {
    const host = document.createElement("div");
    host.style.cssText = "width: 900px; height: 600px; display: flex; flex-direction: column;";
    document.body.append(host);
    await render(ACPWorkspace, {
      context: new Map([[STORES_KEY, { settings }]]),
      target: host,
      props: { websocketPath: "/ws/chat" },
    });
    await expect.poll(() => socket.options).toBeDefined();
    socket.options!.onOpen?.();
    socket.options!.onMessage(
      encodeFixture({
        messages: range(200, 240),
        messageCursor: 1,
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
        beforeId: "entry-200",
        limit: 100,
      });
    const anchor = [...host.querySelectorAll<HTMLElement>("article")].find((article) =>
      article.textContent?.includes("Message 203:"),
    )!;
    const before = anchor.getBoundingClientRect().top;

    socket.options!.onMessage(
      encodeFixture({ history: { beforeId: "entry-200", offset: 100, messages: range(100, 200) } }),
    );
    await nextFrame();
    await nextFrame();

    expect(host.querySelector("article")?.textContent).toContain("Message 100:");
    expect(conversation.scrollTop).toBeGreaterThan(1000);
    // Within sub-pixel rounding of the integer scroll offset.
    expect(Math.abs(anchor.getBoundingClientRect().top - before)).toBeLessThan(2);
  });

  it("keeps a pending and a refused prompt in view without a host update", async () => {
    const host = document.createElement("div");
    host.style.cssText = "width: 900px; height: 600px; display: flex; flex-direction: column;";
    document.body.append(host);
    await render(ACPWorkspace, {
      context: new Map([[STORES_KEY, { settings }]]),
      target: host,
      props: { websocketPath: "/ws/chat" },
    });
    await expect.poll(() => socket.options).toBeDefined();
    socket.options!.onOpen?.();
    socket.options!.onMessage(
      encodeFixture({
        messages: range(200, 240),
        messageCursor: 1,
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
    const conversation = host.querySelector<HTMLElement>(".conversation")!;
    const gap = () => conversation.scrollHeight - conversation.scrollTop - conversation.clientHeight;
    await expect.poll(gap).toBeLessThan(2);

    const composer = page.getByRole("textbox", { name: "Message agent" });
    await userEvent.fill(composer, "A long follow-up\n\n".repeat(20));
    await userEvent.keyboard("{Enter}");
    await expect.poll(() => host.querySelector(".outgoing")?.textContent).toContain("Sending…");
    await expect.poll(gap).toBeLessThan(2);

    const { id } = socket.sent.map((frame) => JSON.parse(frame) as { id: string })[0]!;
    socket.options!.onMessage(JSON.stringify({ commandError: "The agent is not running.", command: "prompt", id }));
    await expect.poll(() => host.querySelector(".outgoing__refused")).not.toBeNull();
    await expect.poll(gap).toBeLessThan(2);
  });
});

describe("ACPWorkspace queued coordinator prompts (browser)", () => {
  async function openQueue(width: number) {
    const { host } = await renderChat(width);
    socket.options!.onMessage(
      encodeFixture({
        messages: [],
        messageCursor: 1,
        messageOffset: 0,
        messageCount: 0,
        configOptions: [],
        configuring: false,
        permissions: [],
        busy: true,
        connected: true,
        error: "",
        queue: [
          {
            id: "c1",
            text: "Refactor the parser",
            sender: { kind: "coordinator", name: "release-planner-with-a-long-name" },
          },
        ],
      }),
    );
    await nextFrame();
    await userEvent.click(page.getByRole("button", { name: /1 queued/ }));
    await nextFrame();
    const row = host.querySelector<HTMLElement>('ol[aria-label="Queued messages"] li')!;
    return { row, name: row.querySelector<HTMLElement>(".row-sender-name")! };
  }

  it("names the coordinator in a wide pane", async () => {
    const { row, name } = await openQueue(900);
    expect(getComputedStyle(name).display).not.toBe("none");
    expect(row.textContent).toContain("Coordinator release-planner-with-a-long-name");
  });

  it("says only Coordinator in a phone-width pane, keeping the message and buttons visible", async () => {
    const { row, name } = await openQueue(360);
    expect(getComputedStyle(name).display).toBe("none");
    const rowBox = row.getBoundingClientRect();
    expect(row.querySelector<HTMLElement>(".row-text")!.getBoundingClientRect().width).toBeGreaterThan(80);
    for (const button of row.querySelectorAll<HTMLElement>("button")) {
      expect(button.getBoundingClientRect().right).toBeLessThanOrEqual(rowBox.right + 0.5);
    }
  });
});
