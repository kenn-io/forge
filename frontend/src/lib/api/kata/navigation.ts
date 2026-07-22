import type { KataTaskSummary } from "./taskTypes.js";

export interface KataIssueNavigationTarget {
  uid: string;
  status: KataTaskSummary["status"];
  project_uid: string;
  daemon_id?: string | undefined;
}

export type OpenKataIssue = (target: KataIssueNavigationTarget) => void;
