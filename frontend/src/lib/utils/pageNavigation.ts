// Full-page navigation seam. Leaving the SPA replaces the whole document, so
// components call this instead of window.location directly and browser tests
// can observe the destination without unloading the test page.
export function currentInAppPath(): string {
  return window.location.pathname + window.location.search;
}

export function navigateToURL(url: string): void {
  window.location.assign(url);
}
