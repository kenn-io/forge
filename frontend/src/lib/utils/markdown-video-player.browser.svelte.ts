// Rendered markdown players become kit-ui VideoPlayers whose control bar
// opens the expanded view. Playback needs a real browser and a real clip.

import { Effect, Fiber } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";

import "../../app.css";
import clip from "../../../tests/fixtures/markdown-video.webm?url";
import { renderMarkdownSync } from "./markdown.js";
import { initMarkdownVideoFallback } from "./markdown-video-fallback.js";
import { initMarkdownVideoPlayers } from "./markdown-video-player.svelte.js";
import { observeMarkdownVideoPreviews } from "./markdown-video-preview.js";
import { initMarkdownVideoToggle } from "./markdown-video-toggle.js";

// Loading and seeking a clip can take seconds while other suites share the browser.
const settle = { timeout: 5_000 };
// Decoding a clip can outlast the poll default while the whole suite runs.
const previewed = { timeout: 10_000 };
const expandName = "Open video in expanded view: markdown-video.webm";

let cleanups: Array<() => void> = [];

afterEach(() => {
  for (const cleanup of cleanups) cleanup();
  cleanups = [];
  document.body.replaceChildren();
});

function mount(markdown: string) {
  const body = document.createElement("div");
  body.className = "markdown-body";
  body.innerHTML = renderMarkdownSync(markdown);
  document.body.append(body);
  const restore = vi.fn();
  const onViewerOpen = vi.fn(() => restore);
  const players = initMarkdownVideoPlayers(document.body, { onViewerOpen });
  cleanups.push(initMarkdownVideoToggle(document), initMarkdownVideoFallback(document), () => players.disconnect());
  return { body, onViewerOpen, restore };
}

// The same page wiring as mount.ts: previews and players side by side.
function mountWithPreviews(markdown: string) {
  const mounted = mount(markdown);
  const previews = Effect.runFork(Effect.scoped(observeMarkdownVideoPreviews(document.body)));
  cleanups.push(() => void Effect.runFork(Fiber.interrupt(previews)));
  return mounted;
}

// The visible text of the bar's two clocks.
function barClocks(body: HTMLElement): string[] {
  const clocks = body.querySelectorAll(".kit-video-player__time > :is(media-time, time)");
  return [...clocks].filter((clock) => clock.checkVisibility()).map((clock) => clock.textContent);
}

function inlineVideo(body: HTMLElement): HTMLVideoElement {
  return body.querySelector<HTMLVideoElement>(".markdown-video-frame > .kit-video-player video")!;
}

function expandedVideo(): HTMLVideoElement | null {
  return document.querySelector<HTMLVideoElement>('[role="dialog"] video');
}

async function playInline(video: HTMLVideoElement, from: number) {
  if (video.readyState < HTMLMediaElement.HAVE_METADATA) {
    video.load();
    await new Promise((resolve) => video.addEventListener("loadedmetadata", resolve, { once: true }));
  }
  video.currentTime = from;
  // Muted, because nothing has clicked the page yet.
  video.muted = true;
  await video.play();
}

describe("markdown video player (browser)", () => {
  it("names the player and its expand control after the file, and leaves it idle", async () => {
    const { body } = mount(`<video src="${clip}"></video>`);

    await expect.poll(() => page.getByRole("button", { name: expandName }).query(), settle).not.toBeNull();
    // The header above the player still names the file.
    expect(body.querySelector(".markdown-video-name")?.textContent).toBe("markdown-video.webm");
    // An idle player must not fetch.
    expect(inlineVideo(body).preload).toBe("none");
  });

  it("shows the previewed length in the control bar without loading the player", async () => {
    const { body } = mountWithPreviews(`<video src="${clip}"></video>`);

    await expect.poll(() => barClocks(body), previewed).toEqual(["0:00", "0:03"]);
    const video = inlineVideo(body);
    expect(video.readyState).toBe(HTMLMediaElement.HAVE_NOTHING);
    expect(video.networkState).not.toBe(HTMLMediaElement.NETWORK_LOADING);
  });

  it("gives a re-rendered player the length the page already read", async () => {
    const { body } = mountWithPreviews(`<video src="${clip}"></video>`);
    await expect.poll(() => barClocks(body), previewed).toEqual(["0:00", "0:03"]);

    body.innerHTML = renderMarkdownSync(`<video src="${clip}"></video>`);

    await expect.poll(() => barClocks(body), settle).toEqual(["0:00", "0:03"]);
  });

  it("keeps zero in the control bar when the preview finds no length", async () => {
    const { body } = mountWithPreviews(
      `<video src="/missing-markdown-video.webm"></video>\n\n<video src="${clip}"></video>`,
    );
    const [missing, found] = [...body.querySelectorAll<HTMLElement>(".markdown-video-frame")];

    // One copy at a time, in page order, so the missing source's turn is over.
    await expect.poll(() => barClocks(found!), previewed).toEqual(["0:00", "0:03"]);
    expect(barClocks(missing!)).toEqual(["0:00", "0:00"]);
  });

  it("keeps the author's poster, muted, and loop settings", async () => {
    const poster = "/markdown-video-poster.png";
    const { body } = mount(`<video src="${clip}" poster="${poster}" muted loop></video>`);
    const video = inlineVideo(body);

    expect(video.getAttribute("poster")).toBe(poster);
    // Set on the player's <video> once it mounts.
    await expect.poll(() => ({ muted: video.muted, loop: video.loop })).toEqual({ muted: true, loop: true });
  });

  it("suspends app keyboard handling while the expanded view is open", async () => {
    const { onViewerOpen, restore } = mount(`<video src="${clip}"></video>`);

    await userEvent.click(page.getByRole("button", { name: expandName }));
    await expect.poll(() => expandedVideo(), settle).not.toBeNull();
    expect(onViewerOpen).toHaveBeenCalledTimes(1);
    expect(restore).not.toHaveBeenCalled();

    await userEvent.keyboard("{Escape}");
    await expect.poll(() => expandedVideo(), settle).toBeNull();
    expect(restore).toHaveBeenCalledTimes(1);
  });

  it("collapsing hides and pauses the player", async () => {
    const { body } = mount(`<video src="${clip}"></video>`);
    const video = inlineVideo(body);
    await playInline(video, 0);

    await userEvent.click(body.querySelector<HTMLButtonElement>(".markdown-video-header")!);

    expect(video.paused).toBe(true);
    expect(body.querySelector(".kit-video-player")!.checkVisibility()).toBe(false);
  });

  it("replaces a player that cannot load with the notice, keeping the header", async () => {
    const { body } = mount(`<video src="/missing-markdown-video.webm"></video>`);
    const video = inlineVideo(body);

    video.load();

    await expect.poll(() => body.querySelector(".markdown-video-error"), settle).not.toBeNull();
    expect(body.querySelector(".kit-video-player")).toBeNull();
    expect(body.querySelector(".markdown-video-header")).not.toBeNull();
  });

  it("gives a re-rendered body a working player of its own", async () => {
    const { body } = mount(`<video src="${clip}"></video>`);

    body.innerHTML = renderMarkdownSync(`<video src="${clip}"></video>`);

    await expect.poll(() => body.querySelectorAll(".kit-video-player").length, settle).toBe(1);
    await userEvent.click(page.getByRole("button", { name: expandName }));
    await expect.poll(() => expandedVideo(), settle).not.toBeNull();
  });
});
