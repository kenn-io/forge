// Decides whether a surface should use phone-only presentation (mobile
// tokens, focus routes for PR/issue detail, the /m shell). This is about the
// device, not just the width: a phone rotated to landscape is still a phone,
// so the desktop split-pane layout must not appear there.

export const PHONE_PORTRAIT_MAX_WIDTH = 640;

// Upper bound for a handheld in landscape. Tablets and desktop-mode browsers
// report wider viewports and keep the desktop presentation.
export const PHONE_LANDSCAPE_MAX_WIDTH = 1024;

export interface PhoneLikeViewportInput {
  viewportWidth: number;
  hasCoarsePointer: boolean;
  hasMobileUserAgent: boolean;
}

export function isPhoneLikeViewport({
  viewportWidth,
  hasCoarsePointer,
  hasMobileUserAgent,
}: PhoneLikeViewportInput): boolean {
  if (viewportWidth <= PHONE_PORTRAIT_MAX_WIDTH) {
    return hasCoarsePointer || hasMobileUserAgent;
  }
  // Past the portrait breakpoint, require both signals so a touch laptop or
  // a tablet with a desktop user agent does not become phone-like.
  return viewportWidth <= PHONE_LANDSCAPE_MAX_WIDTH && hasCoarsePointer && hasMobileUserAgent;
}
