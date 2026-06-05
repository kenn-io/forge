const ACCENT_COLORS = [
  "var(--accent-blue)",
  "var(--accent-amber)",
  "var(--accent-green)",
  "var(--accent-red)",
  "var(--accent-purple)",
  "var(--accent-teal)",
] as const;

export function repoColor(repoName: string): string {
  let hash = 0;
  for (let i = 0; i < repoName.length; i++) {
    hash = ((hash << 5) - hash + repoName.charCodeAt(i)) | 0;
  }
  const idx = ((hash % ACCENT_COLORS.length) + ACCENT_COLORS.length) % ACCENT_COLORS.length;
  return ACCENT_COLORS[idx]!;
}
