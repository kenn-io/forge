// Markdown video against the real daemon: a synthetic WebM clip served as a
// GitHub attachment through the credentialed media route.

import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test, type Locator, type Page, type Request } from "@playwright/test";
import { startIsolatedE2EServerWithOptions, type IsolatedE2EServer } from "./support/e2eServer";

const fixtures = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../fixtures");
const clip = path.join(fixtures, "markdown-video.webm");
// Large enough that the browser fetches it in several cut-short pieces.
const largeClip = path.join(fixtures, "markdown-video-large.webm");

test.describe.configure({ mode: "serial", timeout: 60_000 });

let server: IsolatedE2EServer;

test.beforeAll(async () => {
  test.setTimeout(120_000);
  server = await startIsolatedE2EServerWithOptions({ markdownVideo: clip });
});

test.afterAll(async () => {
  await server?.stop();
});

function route(pathname: string): string {
  return new URL(pathname, server.info.base_url).toString();
}

// Starts playback through the media route. The diff view rebuilds its
// comment bubbles once just after load, which replaces a player started in
// that window; a player that keeps getting replaced still fails here.
async function startPlayback(video: Locator): Promise<void> {
  await expect(async () => {
    await video.evaluate((v: HTMLVideoElement) => {
      v.muted = true;
      return v.play();
    });
    await expect
      .poll(() => video.evaluate((v: HTMLVideoElement) => v.currentTime), { timeout: 1_000 })
      .toBeGreaterThan(0.2);
  }).toPass();
}

// Plays and seeks, which needs the route's byte ranges.
async function expectPlayable(video: Locator): Promise<void> {
  // kit-ui's player draws the controls; the browser's stay off.
  const player = video.locator("xpath=ancestor::div[contains(concat(' ', @class, ' '), ' kit-video-player ')][1]");
  await expect(player.getByRole("button", { name: "Open video in expanded view" })).toBeVisible();
  await expect(video).not.toHaveAttribute("controls");
  await expect(video).not.toHaveAttribute("autoplay");
  await startPlayback(video);
  expect(await video.evaluate((v: HTMLVideoElement) => v.duration)).toBeCloseTo(3, 0);
  const seekedTo = await video.evaluate(
    (v: HTMLVideoElement) =>
      new Promise<number>((resolve) => {
        v.pause();
        v.addEventListener("seeked", () => resolve(v.currentTime), { once: true });
        v.currentTime = 2;
      }),
  );
  expect(seekedTo).toBeCloseTo(2, 0);
}

async function expectContained(video: Locator, container: Locator): Promise<void> {
  const [videoBox, containerBox] = await Promise.all([video.boundingBox(), container.boundingBox()]);
  expect(videoBox).not.toBeNull();
  expect(containerBox).not.toBeNull();
  expect(videoBox!.x).toBeGreaterThanOrEqual(containerBox!.x - 1);
  expect(videoBox!.x + videoBox!.width).toBeLessThanOrEqual(containerBox!.x + containerBox!.width + 1);
}

test("plays an issue description video", async ({ page }) => {
  await page.goto(route("/issues/github/acme/widgets/14"));
  const body = page
    .locator(".markdown-body")
    .filter({ has: page.locator("video") })
    .first();
  const video = body.locator(".markdown-video-frame video");

  await expectPlayable(video);
  await expectContained(video, body);
});

// Counts video requests a page starts and how many are open at once. Firefox
// reads ahead on every player it preloads, so a page with a few videos would
// hold every connection the browser allows to the daemon.
function trackMediaRequests(page: Page) {
  const open = new Set<Request>();
  const counts = { started: 0, open: 0, mostOpen: 0 };
  page.on("request", (request) => {
    if (request.resourceType() !== "media") return;
    open.add(request);
    counts.started++;
    counts.open = open.size;
    counts.mostOpen = Math.max(counts.mostOpen, open.size);
  });
  const end = (request: Request) => {
    open.delete(request);
    counts.open = open.size;
  };
  page.on("requestfinished", end);
  page.on("requestfailed", end);
  return counts;
}

async function expectPreviewed(frame: Locator, options?: { timeout: number }): Promise<void> {
  await expect(frame.locator(".markdown-video-duration")).toHaveText("0:03", options);
  await expect(frame.locator("video")).toHaveAttribute("poster", /^blob:/, options);
}

test("shows a player's length and first frame before it is played", async ({ page }) => {
  const media = trackMediaRequests(page);
  await page.goto(route("/issues/github/acme/widgets/14"));
  const frame = page.locator(".markdown-body .markdown-video-frame").first();
  const video = frame.locator("video");

  await expectPreviewed(frame);
  await expect.poll(() => media.open).toBe(0);
  // The preview came from a copy; the player itself has fetched nothing.
  expect(await video.evaluate((v: HTMLVideoElement) => v.readyState)).toBe(0);
  const length = frame.locator(".kit-video-player__known-clock").last();
  await expect(length).toBeVisible();
  await expect(length).toHaveText("0:03");

  await startPlayback(video);
});

test("previews a description's players one at a time and holds no request afterwards", async ({ page }) => {
  const media = trackMediaRequests(page);
  await page.goto(route("/pulls/github/acme/widgets/1"));
  const frames = page.locator(".markdown-body .markdown-video-frame");
  await expect(frames).toHaveCount(5);

  for (const frame of await frames.all()) {
    await frame.scrollIntoViewIfNeeded();
    await expectPreviewed(frame);
  }
  await expect.poll(() => media.open).toBe(0);
  expect(media.started).toBeGreaterThanOrEqual(5);
  expect(media.mostOpen).toBe(1);
});

test("plays to the end when the daemon cuts its download short", async ({ page }) => {
  const cutServer = await startIsolatedE2EServerWithOptions({ markdownVideo: largeClip, markdownVideoInterrupt: true });
  try {
    let playing = false;
    let cutsWhilePlaying = 0;
    page.on("requestfailed", (request) => {
      if (playing && request.resourceType() === "media") cutsWhilePlaying++;
    });
    await page.goto(new URL("/issues/github/acme/widgets/14", cutServer.info.base_url).toString());
    const frame = page.locator(".markdown-body .markdown-video-frame").first();
    const video = frame.locator("video");
    // Firefox previews from a copy of the whole clip, so it waits out every cut.
    await expectPreviewed(frame, { timeout: 15_000 });

    playing = true;
    await startPlayback(video);
    await video.evaluate((v: HTMLVideoElement) => v.pause());
    // The player's own download was cut, while playing or while paused.
    await expect.poll(() => cutsWhilePlaying).toBeGreaterThan(0);
    await video.evaluate((v: HTMLVideoElement) => v.play());

    await expect.poll(() => video.evaluate((v: HTMLVideoElement) => v.ended), { timeout: 15_000 }).toBe(true);
    await expect(frame.locator(".markdown-video-error")).toHaveCount(0);
  } finally {
    await cutServer.stop();
  }
});

test("keeps the player size when the first play loads the video", async ({ page }) => {
  await page.goto(route("/issues/github/acme/widgets/14"));
  const video = page.locator(".markdown-body .markdown-video-frame video").first();
  await expect(video).toBeVisible();
  const before = await video.boundingBox();

  await startPlayback(video);

  expect(await video.boundingBox()).toEqual(before);
});

test("plays a video in a diff review comment", async ({ page }) => {
  await page.goto(route("/pulls/github/acme/widgets/1/files"));
  const bubble = page
    .locator(".review-thread-body")
    .filter({ has: page.locator("video") })
    .first();
  const video = bubble.locator(".markdown-video-frame video");

  await expectPlayable(video);
  await expectContained(video, bubble);
});

test("keeps the player inside the content width on a phone", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(route("/issues/github/acme/widgets/14"));
  const body = page
    .locator(".markdown-body")
    .filter({ has: page.locator("video") })
    .first();
  const video = body.locator(".markdown-video-frame video");
  await startPlayback(video);

  await expectContained(video, body);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});

test("replaces a player that cannot load with a link to the original", async ({ page }) => {
  const missing = "https://github.com/user-attachments/assets/e2e-missing-video";
  await page.goto(route("/issues/github/acme/widgets/14"));
  // Match on the issue text: the body loses its video once the notice replaces it.
  const body = page.locator(".markdown-body").filter({ hasText: "Screen recording" }).first();
  const video = body.locator(".markdown-video-frame video");
  await expect(video).toBeVisible();

  // The kit-ui player lists the address as a <source>.
  await video.evaluate((v: HTMLVideoElement, missing: string) => {
    const source = v.querySelector("source")!;
    const url = new URL(source.getAttribute("src")!, window.location.href);
    url.searchParams.set("source", missing);
    source.setAttribute("src", `${url.pathname}${url.search}`);
    v.load();
    v.play().catch(() => {});
  }, missing);

  await expect(body.getByText("Video could not be loaded.")).toBeVisible();
  await expect(body.getByRole("link", { name: "Open the original" })).toHaveAttribute("href", missing);
  await expect(body.locator("video")).toHaveCount(0);
});

test("collapses and expands a player from its header", async ({ page }) => {
  await page.goto(route("/issues/github/acme/widgets/14"));
  const frame = page.locator(".markdown-body .markdown-video-frame").first();
  const header = frame.getByRole("button", { name: /^Video/ });
  const video = frame.locator("video");
  const chevron = header.locator(".markdown-video-chevron");
  await expect(header).toHaveAttribute("aria-expanded", "true");
  await expect(video).toBeVisible();
  await startPlayback(video);

  await header.click();

  await expect(header).toHaveAttribute("aria-expanded", "false");
  await expect(video).toBeHidden();
  expect(await video.evaluate((v: HTMLVideoElement) => v.paused)).toBe(true);
  // A half-turn matrix once the quick rotation settles.
  await expect
    .poll(() => chevron.evaluate((svg) => getComputedStyle(svg).transform))
    .toBe("matrix(-1, 0, 0, -1, 0, 0)");

  await header.focus();
  await page.keyboard.press("Enter");

  await expect(header).toHaveAttribute("aria-expanded", "true");
  await expect(video).toBeVisible();
  await expect.poll(() => chevron.evaluate((svg) => getComputedStyle(svg).transform)).toBe("none");
});

test("turns the header chevron without animation for reduced motion", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto(route("/issues/github/acme/widgets/14"));
  const header = page.locator(".markdown-body .markdown-video-header").first();
  const chevron = header.locator(".markdown-video-chevron");
  await expect(header).toBeVisible();

  expect(await chevron.evaluate((svg) => getComputedStyle(svg).transitionDuration)).toBe("0s");
  await header.click();
  expect(await chevron.evaluate((svg) => getComputedStyle(svg).transform)).toBe("matrix(-1, 0, 0, -1, 0, 0)");
});
