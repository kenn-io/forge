import { canonicalProvider, resolvedPlatformHost } from "../../api/provider-routes.js";
import { repoIdentityKey, type RepoLabelIdentity } from "../../utils/repo-label.js";

type CommentDraftTarget = "issue" | "pull";

const storagePrefix = "kenn-forge:comment-draft:";
const drafts = $state<Record<string, string>>({});
const pendingSubmitCounts = $state<Record<string, number>>({});

export function getCommentDraftKey(target: CommentDraftTarget, ref: RepoLabelIdentity & { number: number }): string {
  const provider = canonicalProvider(ref.provider);
  const repository = repoIdentityKey({
    ...ref,
    provider,
    platformHost: resolvedPlatformHost(provider, ref.platformHost),
  });
  return `${target}\u0000${repository}\u0000${ref.number}`;
}

export function getCommentDraft(key: string): string {
  if (drafts[key] !== undefined) return drafts[key];
  try {
    return localStorage.getItem(storagePrefix + key) ?? "";
  } catch {
    return "";
  }
}

export function setCommentDraft(key: string, body: string): void {
  drafts[key] = body;
  try {
    if (body === "") localStorage.removeItem(storagePrefix + key);
    else localStorage.setItem(storagePrefix + key, body);
  } catch {
    // Keep editing in memory when browser storage is unavailable or full.
  }
}

export function clearCommentDraft(key: string): void {
  setCommentDraft(key, "");
}

export function isCommentSubmitPending(key: string): boolean {
  return (pendingSubmitCounts[key] ?? 0) > 0;
}

export function beginCommentSubmit(key: string): void {
  pendingSubmitCounts[key] = (pendingSubmitCounts[key] ?? 0) + 1;
}

export function finishCommentSubmit(key: string): void {
  const nextCount = (pendingSubmitCounts[key] ?? 0) - 1;
  if (nextCount <= 0) delete pendingSubmitCounts[key];
  else pendingSubmitCounts[key] = nextCount;
}
