// A rendered markdown player that cannot load its video becomes a short
// notice with a link to the original attachment, so a reader never faces a
// dead player. Media errors do not bubble, so one capturing listener covers
// every rendered markdown body.
export function initMarkdownVideoFallback(root: Document): () => void {
  const onError = (event: Event) => {
    const video = failedMarkdownVideo(event.target);
    if (video) replaceWithNotice(video);
  };
  root.addEventListener("error", onError, true);
  return () => root.removeEventListener("error", onError, true);
}

function failedMarkdownVideo(target: EventTarget | null): HTMLVideoElement | null {
  if (target instanceof HTMLVideoElement) return isMarkdownVideo(target) ? target : null;
  // Each <source> reports its own failure; the player has failed once the
  // browser has tried its last one.
  if (!(target instanceof HTMLSourceElement)) return null;
  const video = target.parentElement;
  if (!(video instanceof HTMLVideoElement) || !isMarkdownVideo(video)) return null;
  const sources = video.querySelectorAll("source");
  return sources[sources.length - 1] === target ? video : null;
}

// Players sit in their own frame (markdown.ts::normalizeMarkdownVideos), so
// swapping one never disturbs the nodes the markdown host inserted.
function isMarkdownVideo(video: HTMLVideoElement): boolean {
  return (
    video.classList.contains("markdown-video") && !!video.parentElement?.classList.contains("markdown-video-frame")
  );
}

function replaceWithNotice(video: HTMLVideoElement): void {
  const doc = video.ownerDocument;
  const notice = doc.createElement("span");
  notice.className = "markdown-video-error";
  notice.append("Video could not be loaded. ");
  const original = originalSource(video);
  if (original) {
    const link = doc.createElement("a");
    link.href = original;
    link.textContent = "Open the original";
    notice.append(link);
  }
  video.replaceWith(notice);
}

// The address a reader can open: the attachment behind the media route, or
// the player's own address when the browser loads it directly.
function originalSource(video: HTMLVideoElement): string | null {
  const sources = video.querySelectorAll("source");
  const src = video.getAttribute("src") ?? sources[sources.length - 1]?.getAttribute("src");
  if (!src) return null;
  try {
    const url = new URL(src, video.ownerDocument.baseURI);
    const proxied = url.pathname.endsWith("/markdown-media") ? url.searchParams.get("source") : null;
    const original = proxied === null ? url : new URL(proxied);
    return original.protocol === "https:" || original.protocol === "http:" ? original.href : null;
  } catch {
    return null;
  }
}
