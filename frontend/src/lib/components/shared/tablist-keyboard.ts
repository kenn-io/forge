// Roving focus for a horizontal tablist (the WAI-ARIA tabs pattern): the
// strip is one tab stop, Left and Right move between tabs and select the one
// they land on, Home and End jump to the ends. Tab itself then leaves the strip
// for the active panel instead of walking every tab.
//
// Returns the index the key moves to, or null when the key is not a tablist
// navigation key so the caller leaves the event alone.
export function tablistKeyTarget(key: string, index: number, count: number): number | null {
  if (count <= 0) return null;
  switch (key) {
    case "ArrowRight":
      return (index + 1) % count;
    case "ArrowLeft":
      return (index - 1 + count) % count;
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return null;
  }
}
