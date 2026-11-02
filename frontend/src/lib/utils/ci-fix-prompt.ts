import type { CICheck } from "../api/types.js";

/** Drag type whose data is a ready-to-send agent prompt; agent chats insert it into the composer. */
export const AGENT_PROMPT_DRAG_MIME = "application/x-kenn-forge-agent-prompt";

export interface CIFixPullRequest {
  owner: string;
  name: string;
  number: number;
  url: string;
  headBranch: string;
  headRepoCloneURL: string;
  headSha: string;
}

// Check names come from the PR's CI configuration; keep each on one line so
// a name cannot add lines that read as instructions.
function oneLine(value: string): string {
  return value.replace(/\s+/g, " ").trim();
}

/** `gh run view` for a GitHub Actions job URL, which reads only the failed steps' log. */
function githubActionsLogCommand(url: string): string | null {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return null;
  }
  const match = /^\/([^/]+)\/([^/]+)\/actions\/runs\/(\d+)\/job\/(\d+)\/?$/.exec(parsed.pathname);
  if (!match) return null;
  const [, owner, repo, run, job] = match;
  const repoArg = parsed.host === "github.com" ? `${owner}/${repo}` : `${parsed.host}/${owner}/${repo}`;
  return `gh run view ${run} --job ${job} --log-failed --repo ${repoArg}`;
}

/** A deterministic prompt asking an agent to fix one failed check and push the fix to the PR. */
export function ciFixPrompt(check: CICheck, pr: CIFixPullRequest, otherFailed: readonly CICheck[]): string {
  const checkName = oneLine(check.name);
  const app = oneLine(check.app);
  const headBranch = oneLine(pr.headBranch);
  const headRepo = oneLine(pr.headRepoCloneURL);
  const others = otherFailed.map((other) => `"${oneLine(other.name)}"`);
  const logCommand = githubActionsLogCommand(check.url);

  const facts = [
    `- Pull request: ${pr.url || `${pr.owner}/${pr.name}#${pr.number}`}`,
    check.url && `- Check details: ${check.url}`,
    pr.headSha && `- Failed on commit: ${pr.headSha}`,
    headBranch && `- Head branch: ${headBranch}${headRepo ? ` on ${headRepo}` : ""}`,
    others.length > 0 && `- Other failing checks: ${others.join(", ")}`,
  ].filter(Boolean);

  const readStep = logCommand
    ? `Read the failure first: \`${logCommand}\`. Find the first real error, not the errors that follow from it.`
    : "Read the failure log from the check details first. Find the first real error, not the errors that follow from it.";
  const steps = [
    readStep,
    ...(others.length > 0 ? ["If this check only reports the result of other checks, fix those checks instead."] : []),
    "Confirm this checkout is on the head branch, at or after the failed commit. If it is not, stop and ask.",
    "Reproduce the failure locally with the narrowest command that runs the failing step.",
    "Fix the root cause with the smallest change. Do not skip, disable, or loosen tests or checks to make them pass. If this pull request did not cause the failure (it also fails on the base branch, or passes on an unchanged rerun), report the evidence and stop without changing code.",
    "Rerun the reproduction and the repository's relevant checks until they pass.",
    "Commit with a message that explains why, then push to the head branch above so the pull request updates. That branch may live in a fork rather than origin. Do not force-push or open a new pull request.",
    "Reply with the root cause, the fix, the commands that prove it, and the pushed commit.",
  ];

  return [
    `Fix the failing CI check "${checkName}"${app ? ` (${app})` : ""} on pull request ${pr.owner}/${pr.name}#${pr.number}.`,
    "",
    ...facts,
    "",
    ...steps.map((step, index) => `${index + 1}. ${step}`),
  ].join("\n");
}
