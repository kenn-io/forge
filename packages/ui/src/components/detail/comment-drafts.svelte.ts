type CommentDraftTarget = "issue" | "pull";

let drafts = $state<Record<string, string>>({});
let pendingSubmitCounts = $state<Record<string, number>>({});
let submitErrors = $state<Record<string, string>>({});

export function getCommentDraftKey(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  platformHost?: string | undefined,
): string {
  const repoKey = platformHost ? `${platformHost}/${owner}/${name}` : `${owner}/${name}`;
  return `${target}:${repoKey}/${number}`;
}

function getLegacyCommentDraftKey(target: CommentDraftTarget, owner: string, name: string, number: number): string {
  return getCommentDraftKey(target, owner, name, number);
}

function getCommentDraftKeys(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  platformHost?: string | undefined,
): {
  primary: string;
  legacy: string | undefined;
} {
  return {
    primary: getCommentDraftKey(target, owner, name, number, platformHost),
    legacy: platformHost ? getLegacyCommentDraftKey(target, owner, name, number) : undefined,
  };
}

export function getCommentDraft(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  platformHost?: string | undefined,
): string {
  const keys = getCommentDraftKeys(target, owner, name, number, platformHost);
  return drafts[keys.primary] ?? (keys.legacy ? drafts[keys.legacy] : undefined) ?? "";
}

export function getCommentDraftByKey(key: string): string {
  return drafts[key] ?? "";
}

export function setCommentDraft(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  body: string,
  platformHost?: string | undefined,
): void {
  const keys = getCommentDraftKeys(target, owner, name, number, platformHost);
  if (body === "") {
    const next = { ...drafts };
    delete next[keys.primary];
    if (keys.legacy) {
      delete next[keys.legacy];
    }
    drafts = next;
    return;
  }
  drafts = {
    ...drafts,
    [keys.primary]: body,
  };
}

export function clearCommentDraft(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  platformHost?: string | undefined,
): void {
  const keys = getCommentDraftKeys(target, owner, name, number, platformHost);
  const next = { ...drafts };
  delete next[keys.primary];
  if (keys.legacy) {
    delete next[keys.legacy];
  }
  drafts = next;
}

export function isCommentSubmitPending(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  platformHost?: string | undefined,
): boolean {
  const keys = getCommentDraftKeys(target, owner, name, number, platformHost);
  return (pendingSubmitCounts[keys.primary] ?? (keys.legacy ? pendingSubmitCounts[keys.legacy] : undefined) ?? 0) > 0;
}

export function beginCommentSubmit(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  platformHost?: string | undefined,
): void {
  const key = getCommentDraftKey(target, owner, name, number, platformHost);
  pendingSubmitCounts = {
    ...pendingSubmitCounts,
    [key]: (pendingSubmitCounts[key] ?? 0) + 1,
  };
}

export function finishCommentSubmit(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  platformHost?: string | undefined,
): void {
  const key = getCommentDraftKey(target, owner, name, number, platformHost);
  const nextCount = (pendingSubmitCounts[key] ?? 0) - 1;
  if (nextCount <= 0) {
    const next = { ...pendingSubmitCounts };
    delete next[key];
    pendingSubmitCounts = next;
    return;
  }
  pendingSubmitCounts = {
    ...pendingSubmitCounts,
    [key]: nextCount,
  };
}

export function getCommentSubmitError(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  platformHost?: string | undefined,
): string | null {
  const keys = getCommentDraftKeys(target, owner, name, number, platformHost);
  return submitErrors[keys.primary] ?? (keys.legacy ? submitErrors[keys.legacy] : undefined) ?? null;
}

export function setCommentSubmitError(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  error: string,
  platformHost?: string | undefined,
): void {
  const key = getCommentDraftKey(target, owner, name, number, platformHost);
  submitErrors = {
    ...submitErrors,
    [key]: error,
  };
}

export function clearCommentSubmitError(
  target: CommentDraftTarget,
  owner: string,
  name: string,
  number: number,
  platformHost?: string | undefined,
): void {
  const keys = getCommentDraftKeys(target, owner, name, number, platformHost);
  const next = { ...submitErrors };
  delete next[keys.primary];
  if (keys.legacy) {
    delete next[keys.legacy];
  }
  submitErrors = next;
}
