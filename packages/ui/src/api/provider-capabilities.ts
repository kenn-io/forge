const lockedProviders = new Set(["github", "forgejo", "gitea"]);

export function supportsLocked(provider: string, _host: string, _org: string, _repo: string): boolean {
  return lockedProviders.has(provider.trim().toLowerCase());
}
