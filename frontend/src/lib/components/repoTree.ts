export interface RepoTreeOption {
  value: string; // `${provider}|${platformHost}/${repoPath}`
  owner: string;
  name: string;
  provider: string; // canonical, lowercase
  platformHost: string;
}

export interface RepoLeaf {
  kind: "repo";
  id: string;
  label: string;
  displayLabel?: string;
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
  const providerSeparator = value.indexOf("|");
  const concreteValue = providerSeparator === -1 ? value : value.slice(providerSeparator + 1);
  const prefix = `${platformHost}/`;
  if (concreteValue.startsWith(prefix)) return concreteValue.slice(prefix.length);
  // Defensive fallback: drop everything up to and including the first slash.
  return concreteValue.replace(/^[^/]+\//, "");
}

function providerQualifiedLeafLabel(option: RepoTreeOption, name: string, needsProvider: boolean): string | undefined {
  return needsProvider ? `${option.provider}/${name}` : undefined;
}

function slashDisplayValue(value: string): string {
  const providerSeparator = value.indexOf("|");
  if (providerSeparator === -1) return value;
  return `${value.slice(0, providerSeparator)}/${value.slice(providerSeparator + 1)}`;
}

function leafMatches(leaf: RepoLeaf, ownerLabel: string, query: string): boolean {
  if (query === "") return true;
  const visibleLabel = leaf.displayLabel ?? leaf.label;
  return [
    leaf.value,
    slashDisplayValue(leaf.value),
    visibleLabel,
    `${ownerLabel}/${visibleLabel}`,
    `${ownerLabel}/${leaf.label}`,
  ].some((label) => label.toLowerCase().includes(query));
}

export function buildRepoTree(options: readonly RepoTreeOption[]): HostNode[] {
  const hosts = new Map<string, HostNode>();
  const providersByHost = new Map<string, Set<string>>();

  for (const option of options) {
    let providers = providersByHost.get(option.platformHost);
    if (!providers) {
      providers = new Set<string>();
      providersByHost.set(option.platformHost, providers);
    }
    providers.add(option.provider);
  }

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
    } else if (host.provider !== option.provider) {
      host.provider = "";
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

    const displayLabel = providerQualifiedLeafLabel(
      option,
      name,
      (providersByHost.get(option.platformHost)?.size ?? 0) > 1,
    );
    owner.children.push({
      kind: "repo",
      id: option.value,
      label: name,
      ...(displayLabel ? { displayLabel } : {}),
      value: option.value,
    });
  }

  const sorted = [...hosts.values()].sort((a, b) => a.label.localeCompare(b.label));
  for (const host of sorted) {
    host.children.sort((a, b) => a.label.localeCompare(b.label));
    for (const owner of host.children) {
      owner.children.sort((a, b) => (a.displayLabel ?? a.label).localeCompare(b.displayLabel ?? b.label));
    }
  }
  return sorted;
}

export interface VisibleRow {
  node: RepoTreeNodeData;
  depth: number;
  hasChildren: boolean;
  expanded: boolean;
  /**
   * Overrides the visible label when set. Used for flattened single-repo owners,
   * which render `owner/repo` instead of the bare repo name so two owners that
   * each have one identically-named repo stay distinguishable. `node` is
   * unchanged (identity, value, and selection still use the leaf).
   */
  displayLabel?: string;
}

export interface VisibleRowsOptions {
  isCollapsed: (id: string) => boolean;
  query?: string;
}

export function visibleRows(tree: readonly HostNode[], { isCollapsed, query }: VisibleRowsOptions): VisibleRow[] {
  const q = query?.trim().toLowerCase() ?? "";
  const filtering = q !== "";

  // Prune to the owners/hosts that still have a matching leaf, but keep a
  // reference to the ORIGINAL (unpruned) node alongside the matching leaves.
  // Rendering uses `matchingLeaves` (visibility); selection and tri-state use
  // the original node so a filtered parent still reflects and toggles its FULL
  // subtree, and single-repo-owner flattening keys on the true repo count.
  const pruned = tree
    .map((host) => ({
      original: host,
      owners: host.children
        .map((owner) => ({
          original: owner,
          matchingLeaves: owner.children.filter((leaf) => leafMatches(leaf, owner.label, q)),
        }))
        .filter((owner) => owner.matchingLeaves.length > 0),
    }))
    .filter((host) => host.owners.length > 0);

  const expandedOf = (id: string) => filtering || !isCollapsed(id);
  const singleHost = pruned.length === 1;
  const rows: VisibleRow[] = [];

  for (const host of pruned) {
    const ownerDepth = singleHost ? 0 : 1;
    if (!singleHost) {
      const hostExpanded = expandedOf(host.original.id);
      rows.push({
        node: host.original,
        depth: 0,
        hasChildren: true,
        expanded: hostExpanded,
      });
      if (!hostExpanded) continue;
    }
    for (const owner of host.owners) {
      // Flatten only owners that genuinely have a single repo, not owners
      // narrowed to one match by the active filter. Show `owner/repo` as the
      // label so two single-repo owners with the same repo name stay distinct.
      if (owner.original.children.length === 1) {
        const leaf = owner.original.children[0]!;
        rows.push({
          node: leaf,
          depth: ownerDepth,
          hasChildren: false,
          expanded: false,
          displayLabel: `${owner.original.label}/${leaf.displayLabel ?? leaf.label}`,
        });
        continue;
      }
      const ownerExpanded = expandedOf(owner.original.id);
      rows.push({
        node: owner.original,
        depth: ownerDepth,
        hasChildren: true,
        expanded: ownerExpanded,
      });
      if (!ownerExpanded) continue;
      for (const leaf of owner.matchingLeaves) {
        rows.push({
          node: leaf,
          depth: ownerDepth + 1,
          hasChildren: false,
          expanded: false,
          ...(leaf.displayLabel ? { displayLabel: leaf.displayLabel } : {}),
        });
      }
    }
  }
  return rows;
}

export type SelectionState = "checked" | "partial" | "unchecked";

export function collectLeafValues(node: RepoTreeNodeData): string[] {
  if (node.kind === "repo") return [node.value];
  const values: string[] = [];
  for (const child of node.children) values.push(...collectLeafValues(child));
  return values;
}

export function nodeSelectionState(node: RepoTreeNodeData, active: ReadonlySet<string>): SelectionState {
  const leaves = collectLeafValues(node);
  if (leaves.length === 0) return "unchecked";
  let selected = 0;
  for (const value of leaves) if (active.has(value)) selected += 1;
  if (selected === 0) return "unchecked";
  if (selected === leaves.length) return "checked";
  return "partial";
}

export function toggleSubtree(node: RepoTreeNodeData, activeValues: readonly string[]): string[] {
  const leaves = collectLeafValues(node);
  if (nodeSelectionState(node, new Set(activeValues)) === "checked") {
    const remove = new Set(leaves);
    return activeValues.filter((value) => !remove.has(value));
  }
  const next = [...activeValues];
  const present = new Set(activeValues);
  for (const value of leaves) if (!present.has(value)) next.push(value);
  return next;
}
