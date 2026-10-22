import { VideoPlayer } from "@kenn-io/kit-ui";
import { mount, unmount } from "svelte";

// Rendered markdown players (markdown.ts::normalizePlayer) become kit-ui
// VideoPlayers. The player's own control bar has an expand control beside
// full screen, which opens kit-ui's expanded view on the video and pages
// through the page's images, diagrams, and other videos. Rendered markdown
// is plain HTML that re-renders at any time, so an observer upgrades each
// new player and unmounts players whose markup went away.

const PLAYER_SELECTOR = ".markdown-video-frame > video.markdown-video";
const EXPAND_LABEL = "Open video in expanded view";

export interface MarkdownVideoPlayerOptions {
  /** Suspends app keyboard handling while the expanded view is open;
   * returns the restore function (MediaViewer's `onViewerOpen`). */
  onViewerOpen: () => () => void;
}

export interface MarkdownVideoPlayerController {
  /** Stop watching and unmount every player this controller mounted. */
  disconnect: () => void;
}

export function initMarkdownVideoPlayers(
  root: HTMLElement,
  options: MarkdownVideoPlayerOptions,
): MarkdownVideoPlayerController {
  const players = new Map<Element, ReturnType<typeof mount>>();
  let disconnected = false;
  let scheduled = false;

  const upgrade = (video: HTMLVideoElement) => {
    const frame = video.parentElement!;
    // Set when the source has a file name (markdown.ts::normalizePlayer).
    const fileName = video.dataset["fileName"];
    const instance = mount(VideoPlayer, {
      target: frame,
      anchor: video,
      props: {
        sources: playerSources(video),
        label: fileName ?? "Video",
        // An idle player must not fetch (see normalizePlayer).
        preload: "none",
        expandLabel: fileName ? `${EXPAND_LABEL}: ${fileName}` : EXPAND_LABEL,
        onViewerOpen: options.onViewerOpen,
        class: "markdown-video-player",
      },
    });
    video.remove();
    const player = frame.querySelector(":scope > .kit-video-player");
    if (player) players.set(player, instance);
  };

  const sync = () => {
    // A re-render or the load-failure notice removed these players.
    for (const [player, instance] of players) {
      if (player.isConnected) continue;
      players.delete(player);
      void unmount(instance);
    }
    for (const video of root.querySelectorAll<HTMLVideoElement>(PLAYER_SELECTOR)) upgrade(video);
  };

  const schedule = () => {
    if (disconnected || scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      if (!disconnected) sync();
    });
  };

  const observer = new MutationObserver(schedule);
  observer.observe(root, { childList: true, subtree: true });
  sync();

  return {
    disconnect() {
      disconnected = true;
      observer.disconnect();
      for (const instance of players.values()) void unmount(instance);
      players.clear();
    },
  };
}

// The <video>'s own src, then its <source> elements, in the order the
// browser would try them.
function playerSources(video: HTMLVideoElement): { src: string; type?: string }[] {
  const sources: { src: string; type?: string }[] = [];
  const own = video.getAttribute("src");
  if (own) sources.push({ src: own });
  for (const source of video.querySelectorAll("source[src]")) {
    const type = source.getAttribute("type");
    sources.push(type ? { src: source.getAttribute("src")!, type } : { src: source.getAttribute("src")! });
  }
  return sources;
}
