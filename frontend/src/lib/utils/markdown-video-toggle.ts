// The header above each rendered markdown player collapses and expands it
// (markdown.ts::markdownVideoHeader). Rendered markdown is plain HTML, so one
// delegated listener covers every body; app.css hides what follows a
// collapsed header.
export function initMarkdownVideoToggle(root: Document): () => void {
  const onClick = (event: MouseEvent) => {
    if (!(event.target instanceof Element)) return;
    const header = event.target.closest("button.markdown-video-header");
    const frame = header?.parentElement?.parentElement;
    if (!header?.parentElement?.classList.contains("markdown-video-bar")) return;
    if (!frame?.classList.contains("markdown-video-frame")) return;
    const collapsing = header.getAttribute("aria-expanded") !== "false";
    header.setAttribute("aria-expanded", String(!collapsing));
    // A hidden player keeps playing its sound.
    if (collapsing) frame.querySelector("video")?.pause();
  };
  root.addEventListener("click", onClick);
  return () => root.removeEventListener("click", onClick);
}
