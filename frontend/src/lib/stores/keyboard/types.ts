import type { Route, DetailTab, getPage } from "../router.svelte.js";
import type { PullSelection } from "@middleman/ui/stores/pulls";
import type { IssueSelection } from "@middleman/ui/stores/issues";
import type { RoutedItemRef } from "@middleman/ui/routes";
import type { KeySpec } from "@middleman/ui/stores/keyboard/keyspec";
import type { StoreInstances } from "@middleman/ui";

export type { KeySpec, StoreInstances };

export type ScopeTag = "global" | "view-pulls" | "view-issues" | "detail" | "detail-pr" | "detail-issue";

export interface Context {
  page: ReturnType<typeof getPage>;
  route: Route;
  selectedPR: PullSelection | null;
  selectedIssue: IssueSelection | null;
  isDiffView: boolean;
  detailTab: DetailTab;
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
