// Rendered markdown players become kit-ui VideoPlayers whose control bar
// opens the expanded view. Playback needs a real browser and a real clip.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";

import "../../app.css";
import clip from "../../../tests/fixtures/markdown-video.webm?url";
import { renderMarkdownSync } from "./markdown.js";
import { initMarkdownVideoFallback } from "./markdown-video-fallback.js";
import { initMarkdownVideoPlayers } from "./markdown-video-player.js";
import { initMarkdownVideoToggle } from "./markdown-video-toggle.js";

// Loading and seeking a clip can take seconds while other suites share the browser.
const settle = { timeout: 5_000 };
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
  // Chromium blocks unmuted autoplay when the full suite shares the browser,
  // so the viewer's player is muted before it gets metadata and starts.
  const muter = new MutationObserver(() => {
    const expanded = expandedVideo();
    if (expanded) expanded.muted = true;
  });
  muter.observe(document.body, { childList: true, subtree: true });
  cleanups.push(
    initMarkdownVideoToggle(document),
    initMarkdownVideoFallback(document),
    () => players.disconnect(),
    () => muter.disconnect(),
  );
  return { body, onViewerOpen, restore };
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
  it("puts the expand control in the player's bar, beside full screen", async () => {
    const { body } = mount(`<video src="${clip}"></video>`);
    const bar = body.querySelector('[role="group"][aria-label="Video controls"]')!;

    // Video.js labels its buttons once they connect.
    const names = () =>
      Array.from(bar.querySelectorAll('[role="button"], button'), (button) => button.getAttribute("aria-label"));
    await expect.poll(() => names().slice(-2), settle).toEqual([expandName, "Enter fullscreen"]);
    // The header above the player still names the file.
    expect(body.querySelector(".markdown-video-name")?.textContent).toBe("markdown-video.webm");
    // An idle player must not fetch.
    expect(inlineVideo(body).preload).toBe("none");
  });

  it("continues inline playback in the viewer and resumes inline from where the viewer closed", async () => {
    const { body, onViewerOpen, restore } = mount(`<video src="${clip}"></video>`);
    const video = inlineVideo(body);
    await playInline(video, 0.5);

    await userEvent.click(page.getByRole("button", { name: expandName }));

    await expect.poll(() => expandedVideo(), settle).not.toBeNull();
    expect(onViewerOpen).toHaveBeenCalledTimes(1);
    expect(video.paused).toBe(true);
    const handedOver = video.currentTime;
    const expanded = expandedVideo()!;
    await expect.poll(() => expanded.currentSrc, settle).toBe(video.currentSrc);
    await expect.poll(() => !expanded.paused, settle).toBe(true);
    await expect.poll(() => expanded.currentTime, settle).toBeGreaterThan(handedOver + 0.5);

    await userEvent.keyboard("{Escape}");

    await expect.poll(() => expandedVideo(), settle).toBeNull();
    expect(restore).toHaveBeenCalledTimes(1);
    expect(video.currentTime).toBeGreaterThan(handedOver + 0.5);
    // Under load the short clip can reach its end before the poll looks; a
    // player that never resumed would still be paused short of the end.
    await expect.poll(() => !video.paused || video.ended, settle).toBe(true);
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
