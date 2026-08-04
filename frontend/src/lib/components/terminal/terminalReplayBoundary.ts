export function supportsReplayBoundary(websocketPath: string | null | undefined): boolean {
  if (!websocketPath) return false;
  try {
    const pathname = new URL(websocketPath, window.location.href).pathname;
    return (
      !pathname.includes("/fleet/hosts/") && /\/workspaces\/[^/]+\/runtime\/sessions\/[^/]+\/terminal$/.test(pathname)
    );
  } catch {
    return false;
  }
}
