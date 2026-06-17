export interface RepoLabelIdentity {
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

  for (const repo of repos) {
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
  }

  return {
    format(repo: RepoLabelIdentity): string {
      const name = repo.name.trim();
      const path = repoPath(repo);
      const host = repo.platformHost.trim();

      if (!options.showOrgNames) {
        if (hostNeeded(hostsByRepoPath, path) && host) return `${host}/${path}`;
        if ((repoPathsByName.get(name)?.size ?? 0) <= 1) {
          return name || path || host;
        }
        return path || name;
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

function hostNeeded(hostsByRepoPath: ReadonlyMap<string, ReadonlySet<string>>, path: string): boolean {
  return (hostsByRepoPath.get(path)?.size ?? 0) > 1;
}
