// A rendered markdown player hands its playback to kit-ui's expanded view and
// takes it back on close. Playback needs a real browser and a real clip.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";

import "../../app.css";
import clip from "../../../tests/fixtures/markdown-video.webm?url";
import { renderMarkdownSync } from "./markdown.js";
import { initMarkdownVideoToggle } from "./markdown-video-toggle.js";
import { initMarkdownVideoViewer } from "./markdown-video-viewer.js";

// Loading and seeking a clip can take seconds while other suites share the browser.
const settle = { timeout: 5_000 };

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
  const viewer = initMarkdownVideoViewer(document.body, { onViewerOpen });
  // Chromium blocks unmuted autoplay when the full suite shares the browser,
  // so the viewer's player is muted before it gets metadata and starts.
  const muter = new MutationObserver(() => {
    const expanded = expandedVideo();
    if (expanded) expanded.muted = true;
  });
  muter.observe(document.body, { childList: true, subtree: true });
  cleanups.push(
    initMarkdownVideoToggle(document),
    () => viewer.disconnect(),
    () => muter.disconnect(),
  );
  return { body, onViewerOpen, restore };
}

function playback(video: HTMLVideoElement) {
  return { time: video.currentTime, playing: !video.paused && !video.ended };
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

describe("markdown video expanded view (browser)", () => {
  it("continues inline playback in the viewer and resumes inline from where the viewer closed", async () => {
    const { body, onViewerOpen, restore } = mount(`<video src="${clip}"></video>`);
    const video = body.querySelector<HTMLVideoElement>("video.markdown-video")!;
    const header = body.querySelector<HTMLButtonElement>(".markdown-video-header")!;
    await playInline(video, 0.5);

    await userEvent.click(page.getByRole("button", { name: "Open video in expanded view: markdown-video.webm" }));

    await expect.poll(() => expandedVideo(), settle).not.toBeNull();
    expect(onViewerOpen).toHaveBeenCalledTimes(1);
    expect(header.getAttribute("aria-expanded")).toBe("true");
    expect(playback(video).playing).toBe(false);
    const handedOver = video.currentTime;
    const expanded = expandedVideo()!;
    expect(expanded.currentSrc).toBe(video.currentSrc);
    await expect.poll(() => playback(expanded).playing, settle).toBe(true);
    expect(expanded.currentTime).toBeGreaterThanOrEqual(handedOver);
    await expect.poll(() => expanded.currentTime, settle).toBeGreaterThan(handedOver + 0.5);

    await userEvent.keyboard("{Escape}");

    await expect.poll(() => expandedVideo(), settle).toBeNull();
    expect(restore).toHaveBeenCalledTimes(1);
    expect(video.currentTime).toBeGreaterThan(handedOver + 0.5);
    // Under load the short clip can reach its end before the poll looks; a
    // player that never resumed would still be paused short of the end.
    await expect.poll(() => !video.paused || video.ended, settle).toBe(true);
  });

  it("leaves a collapsed player paused at the viewer's position", async () => {
    const { body } = mount(`<video src="${clip}"></video>`);
    const video = body.querySelector<HTMLVideoElement>("video.markdown-video")!;
    await userEvent.click(body.querySelector<HTMLButtonElement>(".markdown-video-header")!);
    expect(video.checkVisibility()).toBe(false);

    await userEvent.click(page.getByRole("button", { name: "Open video in expanded view: markdown-video.webm" }));
    await expect.poll(() => expandedVideo(), settle).not.toBeNull();
    const expanded = expandedVideo()!;
    await expanded.play();
    await expect.poll(() => expanded.currentTime, settle).toBeGreaterThan(0.5);

    await userEvent.keyboard("{Escape}");

    await expect.poll(() => expandedVideo(), settle).toBeNull();
    expect(video.currentTime).toBeGreaterThan(0.5);
    expect(video.paused).toBe(true);
  });

  it("keeps the expand button beside the header in the bar of a collapsed player", async () => {
    const { body } = mount(`<video src="${clip}"></video>`);
    const expand = body.querySelector<HTMLButtonElement>(".markdown-video-expand")!;

    await userEvent.click(body.querySelector<HTMLButtonElement>(".markdown-video-header")!);

    expect(expand.checkVisibility()).toBe(true);
    expect(body.querySelector("video")!.checkVisibility()).toBe(false);
  });
});
