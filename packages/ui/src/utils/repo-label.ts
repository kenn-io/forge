export interface RepoLabelIdentity {
  provider: string;
  platformHost: string;
  owner: string;
  name: string;
  repoPath?: string | undefined;
}

export interface RepoLabelOptions {
  showOrgNames: boolean;
}

export interface RepoLabelFormatter {
  format(repo: RepoLabelIdentity): string;
}

export function createRepoLabelFormatter(
  repos: Iterable<RepoLabelIdentity>,
  options: RepoLabelOptions,
): RepoLabelFormatter {
  const repoPathsByName = new Map<string, Set<string>>();
  const hostsByRepoPath = new Map<string, Set<string>>();
  const providersByHostRepoPath = new Map<string, Set<string>>();

  for (const repo of repos) {
    const provider = repo.provider.trim();
    const name = repo.name.trim();
    const path = repoPath(repo);
    const host = repo.platformHost.trim();
    if (!name || !path) continue;

    let paths = repoPathsByName.get(name);
    if (!paths) {
      paths = new Set();
      repoPathsByName.set(name, paths);
    }
    paths.add(path);

    let hosts = hostsByRepoPath.get(path);
    if (!hosts) {
      hosts = new Set();
      hostsByRepoPath.set(path, hosts);
    }
    hosts.add(host);

    const hostRepoPath = hostRepoPathKey(host, path);
    let providers = providersByHostRepoPath.get(hostRepoPath);
    if (!providers) {
      providers = new Set();
      providersByHostRepoPath.set(hostRepoPath, providers);
    }
    providers.add(provider);
  }

  return {
    format(repo: RepoLabelIdentity): string {
      const provider = repo.provider.trim();
      const name = repo.name.trim();
      const path = repoPath(repo);
      const host = repo.platformHost.trim();

      if (!options.showOrgNames) {
        if (providerNeeded(providersByHostRepoPath, host, path)) {
          return provider ? providerHostRepoPathLabel(provider, host, path) : hostQualifiedRepoPathLabel(host, path);
        }
        if (hostNeeded(hostsByRepoPath, path) && host) return `${host}/${path}`;
        if ((repoPathsByName.get(name)?.size ?? 0) <= 1) {
          return name || path || host || provider;
        }
        return path || name;
      }

      if (providerNeeded(providersByHostRepoPath, host, path)) {
        return provider ? providerHostRepoPathLabel(provider, host, path) : hostQualifiedRepoPathLabel(host, path);
      }
      return hostNeeded(hostsByRepoPath, path) && host ? `${host}/${path}` : path || name;
    },
  };
}

export function repoPath(repo: RepoLabelIdentity): string {
  const explicit = repo.repoPath?.trim();
  if (explicit) return explicit;
  const owner = repo.owner.trim();
  const name = repo.name.trim();
  if (owner && name) return `${owner}/${name}`;
  return name;
}

export function repoIdentityKey(repo: RepoLabelIdentity): string {
  // The "|" delimiter is the established thread/grouping key format
  // (provider|host|owner/name); activity item and branch keys append
  // ":type:number" to it and ActivityThreaded relies on that shape.
  // None of provider, host, or repo path contains "|", so segments
  // stay unambiguous.
  return [repo.provider.trim(), repo.platformHost.trim(), repoPath(repo)].join("|");
}

function hostNeeded(hostsByRepoPath: ReadonlyMap<string, ReadonlySet<string>>, path: string): boolean {
  return (hostsByRepoPath.get(path)?.size ?? 0) > 1;
}

function providerNeeded(
  providersByHostRepoPath: ReadonlyMap<string, ReadonlySet<string>>,
  host: string,
  path: string,
): boolean {
  return (providersByHostRepoPath.get(hostRepoPathKey(host, path))?.size ?? 0) > 1;
}

function hostRepoPathKey(host: string, path: string): string {
  return `${host}\0${path}`;
}

function providerHostRepoPathLabel(provider: string, host: string, path: string): string {
  return host ? `${provider}/${host}/${path}` : `${provider}/${path}`;
}

function hostQualifiedRepoPathLabel(host: string, path: string): string {
  return host ? `${host}/${path}` : path;
}
