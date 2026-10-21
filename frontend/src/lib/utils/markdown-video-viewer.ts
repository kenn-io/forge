import {
  openMediaViewerGallery,
  registerMediaViewerItem,
  type MediaViewerVideoItem,
} from "@kenn-io/kit-ui/utils/media-gallery";

// Rendered markdown players (markdown.ts::normalizePlayer) join kit-ui's page
// gallery, so a player's expand button opens MediaViewer on it and the viewer
// pages through the page's images, diagrams, and other players. Rendered
// markdown is plain HTML that re-renders at any time, so an observer
// registers each new player and one delegated listener handles the buttons.

const PLAYER_SELECTOR = ".markdown-video-frame > video.markdown-video";

export interface MarkdownVideoViewerOptions {
  /** Suspends app keyboard handling while the viewer is open; returns the
   * restore function (MediaViewer's `onViewerOpen`). */
  onViewerOpen: () => () => void;
}

export interface MarkdownVideoViewerController {
  /** Stop watching and close a viewer this controller opened. */
  disconnect: () => void;
}

export function initMarkdownVideoViewer(
  root: HTMLElement,
  options: MarkdownVideoViewerOptions,
): MarkdownVideoViewerController {
  const registered = new WeakSet<HTMLVideoElement>();
  let disconnected = false;
  let scheduled = false;
  let closeOwnedViewer: (() => void) | null = null;

  const register = (video: HTMLVideoElement) => {
    if (registered.has(video)) return;
    registered.add(video);
    registerMediaViewerItem(video, () => expandedVideo(video));
  };

  const registerAll = () => {
    for (const video of root.querySelectorAll<HTMLVideoElement>(PLAYER_SELECTOR)) register(video);
  };

  const schedule = () => {
    if (disconnected || scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      if (!disconnected) registerAll();
    });
  };

  const open = async (video: HTMLVideoElement) => {
    const close: () => void = await openMediaViewerGallery(video, {
      onViewerOpen: options.onViewerOpen,
      onClose: () => {
        if (closeOwnedViewer === close) closeOwnedViewer = null;
      },
    });
    // Disconnected while the viewer loaded: it must not outlive us.
    if (disconnected) close();
    else closeOwnedViewer = close;
  };

  const onClick = (event: MouseEvent) => {
    if (!(event.target instanceof Element)) return;
    const button = event.target.closest("button.markdown-video-expand");
    const frame = button?.parentElement?.parentElement;
    if (!button?.parentElement?.classList.contains("markdown-video-bar")) return;
    if (!frame?.classList.contains("markdown-video-frame")) return;
    // A player that failed to load is gone; its notice has nothing to open.
    const video = frame.querySelector<HTMLVideoElement>(":scope > video.markdown-video");
    if (!video) return;
    register(video);
    void open(video);
  };

  const observer = new MutationObserver(schedule);
  observer.observe(root, { childList: true, subtree: true });
  root.addEventListener("click", onClick);
  registerAll();

  return {
    disconnect() {
      disconnected = true;
      observer.disconnect();
      root.removeEventListener("click", onClick);
      const close = closeOwnedViewer;
      closeOwnedViewer = null;
      close?.();
    },
  };
}

// Called when the viewer opens, for every player it includes. The viewer
// plays its own copy, so the inline player pauses and hands over where it
// was; it takes the position back when the video leaves the viewer.
function expandedVideo(video: HTMLVideoElement): MediaViewerVideoItem {
  const playing = !video.paused && !video.ended;
  video.pause();
  return {
    kind: "video",
    // An idle player (preload="none") may not have picked a source yet.
    src: video.currentSrc || video.src || video.querySelector<HTMLSourceElement>("source[src]")?.src || "",
    label: video.parentElement?.querySelector(".markdown-video-name")?.textContent || "Video",
    currentTime: video.currentTime,
    playing,
    onleave: (state) => {
      // A re-render replaced the player while the viewer was open.
      if (!video.isConnected) return;
      video.currentTime = state.currentTime;
      // A collapsed player stays paused, as collapsing it does.
      if (state.playing && !collapsed(video)) video.play().catch(() => {});
    },
  };
}

function collapsed(video: HTMLVideoElement): boolean {
  return video.parentElement?.querySelector(".markdown-video-header")?.getAttribute("aria-expanded") === "false";
}
