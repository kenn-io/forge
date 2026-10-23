// Browser-tier coverage for markdown video previews: only a real browser
// decodes the clip, draws its frame, and applies cross-origin canvas rules.

import { Effect, Fiber } from "effect";
import { afterEach, describe, expect, it } from "vite-plus/test";

import "../../app.css";
import clipPath from "../../../tests/fixtures/markdown-video.webm?url";
import { renderMarkdownSync } from "./markdown.js";
import { observeMarkdownVideoPreviews } from "./markdown-video-preview.js";

const clip = new URL(clipPath, window.location.href).href;

// Decoding a clip can outlast the 1s poll default while the whole suite runs.
const LOAD = { timeout: 10_000 };

// The same clip from the other loopback name: another origin that sends no
// CORS headers.
function crossOriginClip(): string {
  const url = new URL(clip);
  url.hostname = url.hostname === "localhost" ? "127.0.0.1" : "localhost";
  return url.href;
}

let root: HTMLElement;
let observer: Fiber.Fiber<never> | undefined;

function mount(sources: readonly string[]): HTMLVideoElement[] {
  root = document.createElement("div");
  root.className = "markdown-body";
  root.innerHTML = renderMarkdownSync(sources.map((src) => `<video src="${src}"></video>`).join("\n\n"));
  document.body.append(root);
  observer = Effect.runFork(Effect.scoped(observeMarkdownVideoPreviews(root)));
  return [...root.querySelectorAll<HTMLVideoElement>("video.markdown-video")];
}

function durationOf(player: HTMLVideoElement): string | null {
  return player.parentElement?.querySelector(".markdown-video-duration")?.textContent ?? null;
}

afterEach(async () => {
  if (observer) await Effect.runPromise(Fiber.interrupt(observer));
  observer = undefined;
  root.remove();
});

describe("markdown video previews (browser)", () => {
  it("shows the length and first frame of a player that has not loaded", async () => {
    const [player] = mount([clip]);

    await expect.poll(() => durationOf(player!), LOAD).toBe("0:03");
    await expect.poll(() => player!.getAttribute("poster") ?? "", LOAD).toMatch(/^blob:/);
    const poster = new Image();
    poster.src = player!.getAttribute("poster")!;
    await poster.decode();
    expect(poster.naturalWidth).toBeGreaterThan(0);
    expect(player!.readyState).toBe(HTMLMediaElement.HAVE_NOTHING);
    expect(player!.networkState).not.toBe(HTMLMediaElement.NETWORK_LOADING);
  });

  it("shows only the length when another origin withholds the frame", async () => {
    const [player] = mount([crossOriginClip()]);

    await expect.poll(() => durationOf(player!), LOAD).toBe("0:03");
    expect(player!.hasAttribute("poster")).toBe(false);
  });

  it("waits for a collapsed player to be expanded", async () => {
    const [collapsed, open] = mount([`${clip}#collapsed`, `${clip}#open`]);
    const header = collapsed!.parentElement!.querySelector<HTMLButtonElement>(".markdown-video-header")!;
    header.setAttribute("aria-expanded", "false");

    await expect.poll(() => durationOf(open!), LOAD).toBe("0:03");
    expect(durationOf(collapsed!)).toBeNull();

    header.setAttribute("aria-expanded", "true");
    await expect.poll(() => durationOf(collapsed!), LOAD).toBe("0:03");
  });

  it("shows a known preview at once when the body renders again", async () => {
    const [first] = mount([clip]);
    await expect.poll(() => durationOf(first!), LOAD).toBe("0:03");
    const poster = first!.getAttribute("poster");

    root.innerHTML = renderMarkdownSync(`<video src="${clip}"></video>`);
    const again = root.querySelector<HTMLVideoElement>("video.markdown-video")!;
    // Mutation records arrive as a microtask, before any media event could.
    await Promise.resolve();

    expect(durationOf(again)).toBe("0:03");
    expect(again.getAttribute("poster")).toBe(poster);
  });

  it("leaves a player the reader already started alone", async () => {
    const [started, idle] = mount([`${clip}#started`, `${clip}#idle`]);
    started!.muted = true;
    await started!.play();

    await expect.poll(() => durationOf(idle!), LOAD).toBe("0:03");
    expect(started!.hasAttribute("poster")).toBe(false);
    expect(durationOf(started!)).toBeNull();
  });
});
