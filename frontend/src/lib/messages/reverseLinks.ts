import type { IssueRef, IssueSummary, MessageLinkRef } from "./types";
import { readMessageLinks } from "./messageLinks";

export interface LinkedMessageRow {
  message: MessageLinkRef;
  issues: IssueRef[];
  most_recent_added_at: string;
}

function toIssueRef(issue: IssueSummary): IssueRef {
  return {
    uid: issue.uid,
    short_id: issue.short_id,
    qualified_id: issue.qualified_id,
    title: issue.title,
    status: issue.status,
  };
}

export function findIssuesLinkedToMessage(issues: readonly IssueSummary[], messageId: number): IssueRef[] {
  const matches: IssueRef[] = [];
  for (const issue of issues) {
    const links = readMessageLinks(issue.metadata);
    if (links.some((link) => link.message_id === messageId)) {
      matches.push(toIssueRef(issue));
    }
  }
  return matches;
}

export function buildLinkedMessagesIndex(issues: readonly IssueSummary[]): LinkedMessageRow[] {
  const rows = new Map<number, LinkedMessageRow>();

  for (const issue of issues) {
    const links = readMessageLinks(issue.metadata);
    if (links.length === 0) continue;

    const issueRef = toIssueRef(issue);
    const latestPerMessage = new Map<number, MessageLinkRef>();
    for (const link of links) {
      const prior = latestPerMessage.get(link.message_id);
      if (prior === undefined || link.added_at > prior.added_at) {
        latestPerMessage.set(link.message_id, link);
      }
    }

    for (const link of latestPerMessage.values()) {
      const existing = rows.get(link.message_id);
      if (existing) {
        existing.issues.push(issueRef);
        if (link.added_at > existing.most_recent_added_at) {
          existing.most_recent_added_at = link.added_at;
        }
      } else {
        rows.set(link.message_id, {
          message: link,
          issues: [issueRef],
          most_recent_added_at: link.added_at,
        });
      }
    }
  }

  return Array.from(rows.values()).sort((a, b) =>
    a.most_recent_added_at > b.most_recent_added_at ? -1 : a.most_recent_added_at < b.most_recent_added_at ? 1 : 0,
  );
}
