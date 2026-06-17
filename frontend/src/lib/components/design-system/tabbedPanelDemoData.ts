// Single source of truth for the tabbed-panel demo configuration.
//
// Two callers render this exact data and must not drift apart:
//   - DesignSystemTabbedPanelDemo.svelte ships it on /design-system through the
//     @middleman/ui barrel,
//   - DesignSystemPanelHarness.svelte mounts the real TabbedPanelTree from its
//     source files so the browser-tier panel test stays deterministic on a cold
//     optimizer (the barrel re-exports tiptap/pierre/lucide and reloads mid-run).
//
// The type imports below come from the source layout module rather than the
// barrel for the same reason: with verbatimModuleSyntax they erase at runtime,
// so this module contributes only plain data to the harness's graph, never the
// heavy barrel.
import type {
  TabbedPanelDescriptor,
  TabbedPanelNode,
} from "../../../../../packages/ui/src/components/shared/tabbed-panel-layout.ts";

export interface TabbedPanelDemoCopy {
  eyebrow: string;
  title: string;
  body: string;
  details?: string[];
}

export const tabbedPanelDemoTabs: TabbedPanelDescriptor[] = [
  { key: "overview", label: "Overview", status: "success" },
  { key: "activity", label: "Activity", status: "running" },
  { key: "terminal", label: "Terminal", status: "warning" },
];

export const tabbedPanelDemoCopy: Record<string, TabbedPanelDemoCopy> = {
  overview: {
    eyebrow: "PR #442",
    title: "Resizable split view",
    body: "Conversation and files stay side by side with a persisted divider.",
    details: ["Conversation", "Files", "Checks", "Review threads", "Merge queue", "Release notes"],
  },
  activity: {
    eyebrow: "Workspace",
    title: "Review activity",
    body: "New comments, CI updates, and review decisions land in one panel.",
    details: [
      "09:42 Review requested",
      "10:15 CI started",
      "10:21 Lint passed",
      "10:24 Unit tests passed",
      "10:27 E2E tests passed",
      "10:31 Comment added",
      "10:40 Changes requested",
      "11:03 Fix pushed",
      "11:12 Review approved",
    ],
  },
  terminal: {
    eyebrow: "Shell",
    title: "Local session",
    body: "A compact terminal surface can live beside PR context.",
    details: ["$ git status", "$ bun run lint", "$ bun run typecheck", "$ git diff --stat", "$ gh pr checks"],
  },
};

// A fresh node per call: both callers mutate it via $state (activate / reorder /
// split / resize), so they must not share one object instance.
export function createTabbedPanelDemoNode(): TabbedPanelNode {
  return {
    type: "split",
    id: "demo-root",
    direction: "horizontal",
    ratio: 0.58,
    first: {
      type: "leaf",
      id: "demo-left",
      tabs: ["overview", "activity"],
      activeTabKey: "overview",
    },
    second: {
      type: "leaf",
      id: "demo-right",
      tabs: ["terminal"],
      activeTabKey: "terminal",
    },
  };
}
