import { Effect, Option, Queue } from "effect";

// Idle players fetch nothing before play (markdown.ts::normalizePlayer), so
// on their own they show an empty frame that reads 0:00. Each player that
// comes into view gets its length and first frame from a detached copy of
// it, one copy at a time for the whole page: the copy reads until its first
// frame, the frame becomes the player's poster, and the copy drops its
// source, which ends its request. The player itself still fetches nothing
// until it is played.

// The rendered <video>, and the kit-ui player's own <video> that replaces it
// once mounted (markdown-video-player.ts::initMarkdownVideoPlayers). The
// player copies the rendered one's poster, so a preview shown before the
// swap carries over.
const PLAYER_SELECTOR = ".markdown-video-frame video";

// A copy that has no first frame by then gives up, so one slow source never
// holds the queue. Whatever the copy learned by then still shows.
const PREVIEW_TIMEOUT = "15 seconds";

// Wider frames are scaled down; the poster only fills the content column.
const POSTER_MAX_WIDTH = 1280;

interface MarkdownVideoPreview {
  readonly duration: number | null;
  // A blob: URL for the player's poster attribute.
  readonly poster: string | null;
}

export const observeMarkdownVideoPreviews = (target: HTMLElement) =>
  Effect.gen(function* () {
    const pending = yield* Queue.unbounded<HTMLVideoElement>();
    // Previews by source, kept for the session so a body that renders again
    // shows its players at once instead of fetching again. A source that
    // gave no preview stays recorded too: asking again would fetch again.
    const previews = new Map<string, MarkdownVideoPreview>();
    let onPlayerRemoved: (() => void) | null = null;

    const register = (player: HTMLVideoElement) => {
      const key = sourceKey(player);
      if (key === null) return;
      const known = previews.get(key);
      if (known) showPreview(player, known);
      else visibility.observe(player);
    };
    // Collapsed players have no box, so they wait until they are expanded.
    // A player the reader scrolled past before its turn is skipped and waits
    // to come back into view, so the player in view does not wait behind it.
    const inView = new WeakSet<HTMLVideoElement>();
    const queued = new WeakSet<HTMLVideoElement>();
    const visibility = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (!(entry.target instanceof HTMLVideoElement)) continue;
        const player = entry.target;
        if (!entry.isIntersecting) {
          inView.delete(player);
          continue;
        }
        inView.add(player);
        if (queued.has(player)) continue;
        queued.add(player);
        Queue.offerUnsafe(pending, player);
      }
    });
    const mutations = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) for (const player of playersIn(node)) register(player);
        for (const node of record.removedNodes) for (const player of playersIn(node)) visibility.unobserve(player);
      }
      onPlayerRemoved?.();
    });
    yield* Effect.acquireRelease(
      Effect.sync(() => {
        mutations.observe(target, { childList: true, subtree: true });
        for (const player of target.querySelectorAll<HTMLVideoElement>(PLAYER_SELECTOR)) register(player);
      }),
      () =>
        Effect.sync(() => {
          mutations.disconnect();
          visibility.disconnect();
          for (const { poster } of previews.values()) if (poster) URL.revokeObjectURL(poster);
        }),
    );

    // Resolves once the player leaves the page, so its copy stops reading.
    const playerRemoved = (player: HTMLVideoElement) =>
      Effect.callback<void>((resume) => {
        onPlayerRemoved = () => {
          if (!player.isConnected) resume(Effect.void);
        };
        return Effect.sync(() => {
          onPlayerRemoved = null;
        });
      });

    const previewNext = Effect.gen(function* () {
      const player = yield* Queue.take(pending);
      queued.delete(player);
      if (!inView.has(player)) return;
      visibility.unobserve(player);
      const key = sourceKey(player);
      if (key === null || !player.isConnected || hasStarted(player)) return;
      const known = previews.get(key);
      if (known) {
        showPreview(player, known);
        return;
      }
      const preview = yield* Effect.raceFirst(
        loadPreview(player).pipe(Effect.map(Option.some)),
        playerRemoved(player).pipe(Effect.as(Option.none<MarkdownVideoPreview>())),
      );
      if (Option.isNone(preview)) return;
      previews.set(key, preview.value);
      if (player.isConnected) showPreview(player, preview.value);
    });
    return yield* Effect.forever(previewNext);
  });

function playersIn(node: Node): HTMLVideoElement[] {
  if (!(node instanceof Element)) return [];
  if (node instanceof HTMLVideoElement && node.matches(PLAYER_SELECTOR)) return [node];
  return [...node.querySelectorAll<HTMLVideoElement>(PLAYER_SELECTOR)];
}

function sourceKey(player: HTMLVideoElement): string | null {
  const sources = [
    player.getAttribute("src"),
    ...[...player.querySelectorAll("source")].map((s) => s.getAttribute("src")),
  ];
  const key = sources.filter((src) => src !== null).join(" ");
  return key === "" ? null : key;
}

// A player the reader already started shows its own frames and length.
// paused clears as soon as play() is called, while readyState and
// networkState stay at nothing until the player's own load begins.
function hasStarted(player: HTMLVideoElement): boolean {
  return (
    !player.paused ||
    player.readyState > HTMLMediaElement.HAVE_NOTHING ||
    player.networkState === HTMLMediaElement.NETWORK_LOADING
  );
}

const loadPreview = (player: HTMLVideoElement) =>
  Effect.acquireUseRelease(
    Effect.sync(() => detachedCopy(player)),
    readPreview,
    (copy) => Effect.sync(() => dropSource(copy)),
  );

// The copy picks a source the way the player would and shares its CORS mode,
// so it reads what the player will. It sits outside the document: its load
// errors must not reach markdown-video-fallback.ts, which would replace the
// player.
function detachedCopy(player: HTMLVideoElement): HTMLVideoElement {
  const copy = player.ownerDocument.createElement("video");
  copy.muted = true;
  // WebKit reaches the first frame only with auto; "metadata" stops at the
  // length.
  copy.preload = "auto";
  const crossOrigin = player.getAttribute("crossorigin");
  if (crossOrigin !== null) copy.setAttribute("crossorigin", crossOrigin);
  for (const source of player.querySelectorAll("source")) copy.append(source.cloneNode());
  const src = player.getAttribute("src");
  if (src !== null) copy.setAttribute("src", src);
  return copy;
}

// Removing every source and loading again aborts the copy's request in every
// browser, which closes or frees the connection it held.
function dropSource(copy: HTMLVideoElement): void {
  copy.removeAttribute("src");
  copy.replaceChildren();
  copy.load();
}

const readPreview = (copy: HTMLVideoElement) =>
  Effect.gen(function* () {
    yield* firstFrameOrFailure(copy).pipe(Effect.timeoutOption(PREVIEW_TIMEOUT));
    const duration =
      copy.readyState >= HTMLMediaElement.HAVE_METADATA && Number.isFinite(copy.duration) ? copy.duration : null;
    const poster = copy.readyState >= HTMLMediaElement.HAVE_CURRENT_DATA ? yield* posterFrom(copy) : null;
    return { duration, poster } satisfies MarkdownVideoPreview;
  });

// Each <source> reports its own failure and the copy has failed once its
// last one has, as in markdown-video-fallback.ts.
const firstFrameOrFailure = (copy: HTMLVideoElement) =>
  Effect.callback<void>((resume) => {
    const onFrame = () => resume(Effect.void);
    const onError = (event: Event) => {
      const sources = copy.querySelectorAll("source");
      if (event.target === copy || event.target === sources[sources.length - 1]) resume(Effect.void);
    };
    copy.addEventListener("loadeddata", onFrame);
    copy.addEventListener("error", onError, true);
    return Effect.sync(() => {
      copy.removeEventListener("loadeddata", onFrame);
      copy.removeEventListener("error", onError, true);
    });
  });

// A source on another origin without CORS taints the canvas, and reading it
// back throws; those players show their length without a frame.
const posterFrom = (copy: HTMLVideoElement) =>
  Effect.try(() => {
    const scale = Math.min(1, POSTER_MAX_WIDTH / copy.videoWidth);
    const canvas = copy.ownerDocument.createElement("canvas");
    canvas.width = Math.round(copy.videoWidth * scale);
    canvas.height = Math.round(copy.videoHeight * scale);
    const context = canvas.getContext("2d");
    if (!context) throw new Error("no 2d canvas context");
    context.drawImage(copy, 0, 0, canvas.width, canvas.height);
    // Reading a pixel throws for a tainted canvas before any encoding work.
    context.getImageData(0, 0, 1, 1);
    return canvas;
  }).pipe(
    Effect.flatMap((canvas) =>
      Effect.callback<Blob | null>((resume) => {
        canvas.toBlob((blob) => resume(Effect.succeed(blob)), "image/jpeg", 0.85);
      }),
    ),
    Effect.map((blob) => (blob === null ? null : URL.createObjectURL(blob))),
    Effect.orElseSucceed(() => null),
  );

function showPreview(player: HTMLVideoElement, preview: MarkdownVideoPreview): void {
  // The reader can start the player while its copy is still reading.
  if (hasStarted(player)) return;
  // An author's own poster wins, as it does in the browser.
  if (preview.poster !== null && !player.hasAttribute("poster")) player.setAttribute("poster", preview.poster);
  if (preview.duration !== null) showDuration(player, preview.duration);
}

function showDuration(player: HTMLVideoElement, seconds: number): void {
  const header = player.closest(".markdown-video-frame")?.querySelector(":scope > .markdown-video-header");
  if (!header) return;
  let label = header.querySelector("time.markdown-video-duration");
  if (!label) {
    label = player.ownerDocument.createElement("time");
    label.className = "markdown-video-duration";
    header.querySelector(".markdown-video-chevron")?.before(label);
  }
  label.setAttribute("datetime", `PT${Math.round(seconds)}S`);
  label.textContent = formatDuration(seconds);
}

// The clock players show: m:ss, or h:mm:ss from an hour.
export function formatDuration(seconds: number): string {
  const total = Math.round(seconds);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = String(total % 60).padStart(2, "0");
  return hours > 0 ? `${hours}:${String(minutes).padStart(2, "0")}:${secs}` : `${minutes}:${secs}`;
}
