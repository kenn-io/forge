import type { KataTaskSummary } from "./taskTypes.js";

// A routing target scoped to the daemon that produced it. Identical UIDs can
// exist on different daemons, so a target without daemon_id is only complete
// within the surface's active daemon; cross-surface callers must set
// daemon_id (or resolve through an isolated pinned selection) before routing.
export interface KataIssueNavigationTarget {
  uid: string;
  status: KataTaskSummary["status"];
  project_uid: string;
  daemon_id?: string | undefined;
}

export type OpenKataIssue = (target: KataIssueNavigationTarget) => void;
