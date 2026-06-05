const MENU_GAP = 6;
const MENU_MAX_WIDTH = 420;
const VIEWPORT_MARGIN = 16;
const MENU_MIN_WIDTH = 220;

interface MenuPositionInput {
  caretRect: {
    left: number;
    top: number;
    bottom: number;
    width: number;
  };
  viewportWidth: number;
  viewportHeight: number;
  menuHeight: number;
}

interface MenuPosition {
  top: number;
  left: number;
  width: number;
  maxWidth: number;
}

export function computeCommentEditorMenuPosition(input: MenuPositionInput): MenuPosition {
  const availableWidth = Math.max(0, input.viewportWidth - VIEWPORT_MARGIN * 2);
  const maxWidth = Math.min(MENU_MAX_WIDTH, availableWidth);
  const width = Math.max(Math.min(maxWidth, availableWidth), Math.min(MENU_MIN_WIDTH, availableWidth));

  const belowTop = input.caretRect.bottom + MENU_GAP;
  const aboveTop = input.caretRect.top - MENU_GAP - input.menuHeight;
  const fitsBelow = belowTop + input.menuHeight <= input.viewportHeight - VIEWPORT_MARGIN;

  const unclampedLeft = input.caretRect.left + input.caretRect.width;
  const left = Math.max(VIEWPORT_MARGIN, Math.min(unclampedLeft, input.viewportWidth - VIEWPORT_MARGIN - width));

  return {
    top: fitsBelow ? belowTop : Math.max(VIEWPORT_MARGIN, aboveTop),
    left,
    width,
    maxWidth,
  };
}
