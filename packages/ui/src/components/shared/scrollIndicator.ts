export interface ScrollIndicatorGeometry {
  scrollable: boolean;
  height: number;
  top: number;
}

const MIN_THUMB_HEIGHT = 24;

export function getScrollIndicatorGeometry(
  viewportHeight: number,
  contentHeight: number,
  scrollTop: number,
): ScrollIndicatorGeometry {
  if (viewportHeight <= 0 || contentHeight <= viewportHeight) {
    return { scrollable: false, height: 0, top: 0 };
  }

  const height = Math.max(MIN_THUMB_HEIGHT, (viewportHeight * viewportHeight) / contentHeight);
  const scrollRange = contentHeight - viewportHeight;
  const travel = viewportHeight - height;
  const clampedScrollTop = Math.min(Math.max(scrollTop, 0), scrollRange);

  return {
    scrollable: true,
    height,
    top: (clampedScrollTop / scrollRange) * travel,
  };
}
