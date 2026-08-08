import type { ConfigRepo } from "../api/types.js";
import { canonicalProvider } from "../api/provider-routes.js";

function sameText(left: string | undefined, right: string | undefined): boolean {
  return (left ?? "").trim().toLowerCase() === (right ?? "").trim().toLowerCase();
}

function escapeRegex(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// Repository name globs use the same wildcard forms accepted by Go's
// path.Match. Repository names cannot contain '/', so * can safely match any
// remaining characters here.
function nameGlobMatches(pattern: string, value: string): boolean {
  let source = "^";
  for (let index = 0; index < pattern.length; index += 1) {
    const char = pattern[index]!;
    if (char === "*") {
      source += ".*";
      continue;
    }
    if (char === "?") {
      source += ".";
      continue;
    }
    if (char === "\\" && index + 1 < pattern.length) {
      source += escapeRegex(pattern[++index]!);
      continue;
    }
    if (char === "[") {
      const close = pattern.indexOf("]", index + 1);
      if (close !== -1) {
        let contents = pattern.slice(index + 1, close);
        if (contents.startsWith("^")) contents = `^${contents.slice(1)}`;
        source += `[${contents.replace(/\\/g, "\\\\")}]`;
        index = close;
        continue;
      }
    }
    source += escapeRegex(char);
  }
  try {
    return new RegExp(`${source}$`, "i").test(value);
  } catch {
    return false;
  }
}

function hiddenEntryMatches(repo: ConfigRepo, hidden: ConfigRepo): boolean {
  if (
    canonicalProvider(repo.provider) !== canonicalProvider(hidden.provider) ||
    !sameText(repo.platform_host, hidden.platform_host) ||
    !sameText(repo.owner, hidden.owner)
  ) {
    return false;
  }
  if (hidden.is_glob) return nameGlobMatches(hidden.name, repo.name);
  return sameText(repo.name, hidden.name);
}

export function isConfigRepoHiddenFromUI(repo: ConfigRepo, configured: ConfigRepo[]): boolean {
  return configured.some((candidate) => candidate.hide_from_ui && hiddenEntryMatches(repo, candidate));
}

export function interactiveConfigRepos(configured: ConfigRepo[]): ConfigRepo[] {
  return configured.filter((repo) => !repo.is_glob && !isConfigRepoHiddenFromUI(repo, configured));
}

// The catalog is authoritative for configured rows that resolve to a tracked
// repository, including provider-side renames. Only a zero-match exact row is
// demonstrably absent from that catalog and safe to expose as a fallback.
export function unresolvedInteractiveConfigRepos(configured: ConfigRepo[]): ConfigRepo[] {
  return interactiveConfigRepos(configured).filter((repo) => repo.matched_repo_count === 0);
}
