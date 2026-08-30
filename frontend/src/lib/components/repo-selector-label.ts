import { createRepoLabelFormatter, repoPath, type RepoLabelIdentity } from "../utils/repo-label.js";

export interface RepoSelectorOption extends RepoLabelIdentity {
  value: string;
}

export interface RepoSelectorLabel {
  primary: string;
  owner?: string;
  name?: string;
  qualifier?: string;
  full: string;
}

function splitRepoPath(path: string): Pick<RepoSelectorLabel, "owner" | "name"> {
  const separator = path.lastIndexOf("/");
  if (separator <= 0 || separator === path.length - 1) return {};
  return {
    owner: path.slice(0, separator),
    name: path.slice(separator + 1),
  };
}

function labelFromCanonicalValue(value: string): RepoSelectorLabel | null {
  const providerSeparator = value.indexOf("|");
  if (providerSeparator <= 0) return null;

  const provider = value.slice(0, providerSeparator);
  const concreteValue = value.slice(providerSeparator + 1);
  const hostSeparator = concreteValue.indexOf("/");
  if (hostSeparator <= 0 || hostSeparator === concreteValue.length - 1) return null;

  const platformHost = concreteValue.slice(0, hostSeparator);
  const path = concreteValue.slice(hostSeparator + 1);
  return {
    primary: path,
    ...splitRepoPath(path),
    full: `${provider}/${platformHost}/${path}`,
  };
}

export function repoSelectorLabel(value: string, options: readonly RepoSelectorOption[]): RepoSelectorLabel {
  const selected = options.find((option) => option.value === value);
  if (!selected) {
    return labelFromCanonicalValue(value) ?? { primary: value, full: value };
  }

  const path = repoPath(selected);
  const formatted = createRepoLabelFormatter(options, { showOrgNames: true }).format(selected);
  const pathSuffix = `/${path}`;
  const qualifier =
    formatted !== path && formatted.endsWith(pathSuffix) ? formatted.slice(0, -pathSuffix.length) : undefined;
  const fullPrefix = [selected.provider.trim(), selected.platformHost.trim()].filter(Boolean).join("/");

  return {
    primary: path,
    ...splitRepoPath(path),
    ...(qualifier ? { qualifier } : {}),
    full: fullPrefix ? `${fullPrefix}/${path}` : path,
  };
}
