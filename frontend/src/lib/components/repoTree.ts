export interface RepoTreeOption {
  value: string; // `${platformHost}/${repoPath}`
  owner: string;
  name: string;
  provider: string; // canonical, lowercase
  platformHost: string;
}

export interface RepoLeaf {
  kind: "repo";
  id: string;
  label: string;
  value: string;
}

export interface OwnerNode {
  kind: "owner";
  id: string;
  label: string;
  children: RepoLeaf[];
}

export interface HostNode {
  kind: "host";
  id: string;
  label: string;
  provider: string;
  platformHost: string;
  children: OwnerNode[];
}

export type RepoTreeNodeData = HostNode | OwnerNode | RepoLeaf;

function stripHostPrefix(value: string, platformHost: string): string {
  const prefix = `${platformHost}/`;
  if (value.startsWith(prefix)) return value.slice(prefix.length);
  // Defensive fallback: drop everything up to and including the first slash.
  return value.replace(/^[^/]+\//, "");
}

export function buildRepoTree(
  options: readonly RepoTreeOption[],
): HostNode[] {
  const hosts = new Map<string, HostNode>();

  for (const option of options) {
    const repoPath = stripHostPrefix(option.value, option.platformHost);
    const segments = repoPath.split("/");
    const name = segments[segments.length - 1] ?? repoPath;
    const ownerPath = segments.slice(0, -1).join("/");
    if (ownerPath === "") continue; // malformed value with no owner segment

    let host = hosts.get(option.platformHost);
    if (!host) {
      host = {
        kind: "host",
        id: option.platformHost,
        label: option.platformHost,
        provider: option.provider,
        platformHost: option.platformHost,
        children: [],
      };
      hosts.set(option.platformHost, host);
    }

    let owner = host.children.find((node) => node.label === ownerPath);
    if (!owner) {
      owner = {
        kind: "owner",
        id: `${option.platformHost}/${ownerPath}`,
        label: ownerPath,
        children: [],
      };
      host.children.push(owner);
    }

    owner.children.push({
      kind: "repo",
      id: option.value,
      label: name,
      value: option.value,
    });
  }

  const sorted = [...hosts.values()].sort((a, b) =>
    a.label.localeCompare(b.label),
  );
  for (const host of sorted) {
    host.children.sort((a, b) => a.label.localeCompare(b.label));
    for (const owner of host.children) {
      owner.children.sort((a, b) => a.label.localeCompare(b.label));
    }
  }
  return sorted;
}
