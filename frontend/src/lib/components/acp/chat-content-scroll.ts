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
      if (position && element.getClientRects().length) {
        element.scrollTop = position.top;
        element.scrollLeft = position.left;
      }
    };
    const save = () => {
      if (element.getClientRects().length) {
        positions.set(key, { top: element.scrollTop, left: element.scrollLeft });
      }
    };
    restore();
    element.addEventListener("scroll", save);
    // Closed raw disclosures have no layout until their native toggle opens.
    const disclosure = element.closest("details");
    disclosure?.addEventListener("toggle", restore);
    return () => {
      save();
      element.removeEventListener("scroll", save);
      disclosure?.removeEventListener("toggle", restore);
    };
  };
}
