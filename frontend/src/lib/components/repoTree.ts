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

export interface VisibleRow {
  node: RepoTreeNodeData;
  depth: number;
  hasChildren: boolean;
  expanded: boolean;
}

export interface VisibleRowsOptions {
  isCollapsed: (id: string) => boolean;
  query?: string;
}

export function visibleRows(
  tree: readonly HostNode[],
  { isCollapsed, query }: VisibleRowsOptions,
): VisibleRow[] {
  const q = query?.trim().toLowerCase() ?? "";
  const filtering = q !== "";
  const matches = (leaf: RepoLeaf) =>
    !filtering || leaf.value.toLowerCase().includes(q);

  // Prune to owners/hosts that still have a matching leaf.
  const pruned = tree
    .map((host) => ({
      ...host,
      children: host.children
        .map((owner) => ({
          ...owner,
          children: owner.children.filter(matches),
        }))
        .filter((owner) => owner.children.length > 0),
    }))
    .filter((host) => host.children.length > 0);

  const expandedOf = (id: string) => filtering || !isCollapsed(id);
  const singleHost = pruned.length === 1;
  const rows: VisibleRow[] = [];

  for (const host of pruned) {
    const ownerDepth = singleHost ? 0 : 1;
    if (!singleHost) {
      const hostExpanded = expandedOf(host.id);
      rows.push({ node: host, depth: 0, hasChildren: true, expanded: hostExpanded });
      if (!hostExpanded) continue;
    }
    for (const owner of host.children) {
      if (owner.children.length === 1) {
        rows.push({
          node: owner.children[0]!,
          depth: ownerDepth,
          hasChildren: false,
          expanded: false,
        });
        continue;
      }
      const ownerExpanded = expandedOf(owner.id);
      rows.push({
        node: owner,
        depth: ownerDepth,
        hasChildren: true,
        expanded: ownerExpanded,
      });
      if (!ownerExpanded) continue;
      for (const leaf of owner.children) {
        rows.push({
          node: leaf,
          depth: ownerDepth + 1,
          hasChildren: false,
          expanded: false,
        });
      }
    }
  }
  return rows;
}
