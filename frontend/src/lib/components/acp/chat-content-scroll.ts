export type ContentScrollPositions = Map<string, { top: number; left: number }>;

// The owning chat outlives activity groups, which can split around steering.
export function preserveContentScroll(
  positions: ContentScrollPositions,
  entryId: string,
  contentId: readonly (string | number)[],
) {
  const key = JSON.stringify([entryId, ...contentId]);
  return (element: HTMLElement) => {
    const restore = () => {
      const position = positions.get(key);
      if (!position) return true;
      if (!element.getClientRects().length) return false;
      element.scrollTop = position.top;
      element.scrollLeft = position.left;
      return true;
    };
    let pending = !restore();
    // A parked chat has no layout. Observe only until its saved position can
    // be applied, then leave later resizing and scrolling to the reader.
    const observer = pending
      ? new ResizeObserver(() => {
          if (restore()) {
            pending = false;
            observer?.disconnect();
          }
        })
      : undefined;
    observer?.observe(element);
    const save = () => {
      if (!pending && element.getClientRects().length) {
        positions.set(key, { top: element.scrollTop, left: element.scrollLeft });
      }
    };
    element.addEventListener("scroll", save);
    // Closed raw disclosures have no layout until their native toggle opens.
    const disclosure = element.closest("details");
    disclosure?.addEventListener("toggle", restore);
    return () => {
      save();
      observer?.disconnect();
      element.removeEventListener("scroll", save);
      disclosure?.removeEventListener("toggle", restore);
    };
  };
}
