import type { Route, DetailTab, getPage } from "../router.svelte.js";
import type { PullSelection } from "../pulls.svelte.js";
import type { IssueSelection } from "../issues.svelte.js";
import type { RoutedItemRef } from "../../routes.js";
import type { KeySpec } from "./keyspec.js";
import type { StoreInstances } from "../../types.js";

export type { KeySpec, StoreInstances };

export type ScopeTag = "global" | "view-pulls" | "view-issues" | "detail" | "detail-pr" | "detail-issue";

export interface Context {
  page: ReturnType<typeof getPage>;
  route: Route;
  selectedPR: PullSelection | null;
  selectedIssue: IssueSelection | null;
  isDiffView: boolean;
  detailTab: DetailTab;
  sidebarTargetAvailable: boolean;
}

export interface PreviewBlock {
  title: string;
  subtitle?: string;
  body?: string;
  badge?: string;
}

export interface Action {
  id: string;
  label: string;
  scope: ScopeTag;
  binding: KeySpec | KeySpec[] | null;
  priority: number;
  when: (ctx: Context) => boolean;
  visible?: (ctx: Context) => boolean;
  handler: (ctx: Context) => void | Promise<void>;
  preview?: (ctx: Context) => PreviewBlock;
}

export interface CheatsheetEntry {
  id: string;
  label: string;
  binding: KeySpec | KeySpec[];
  scope: ScopeTag;
  conditionBadge?: string;
}

export interface RecentsState {
  version: 1;
  items: Array<{
    kind: "pr" | "issue";
    ref: RoutedItemRef;
    lastSelectedAt: string;
  }>;
}
