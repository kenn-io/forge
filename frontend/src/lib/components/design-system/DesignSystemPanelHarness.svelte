<!--
  Browser-tier test harness. Mounts the real TabbedPanelTree UI primitive with
  the same node tree, tabs, labels, scrollPanels flag, and renderTab content
  that DesignSystemTabbedPanelDemo.svelte ships on /design-system (including the
  nine Activity rows whose overflow drives the scroll assertion). It imports
  TabbedPanelTree and activateTabbedPanelTab from their source files rather than
  the @middleman/ui barrel: the barrel re-exports the whole UI package (tiptap,
  pierre, dozens of lucide icons), which the browser project would optimize
  mid-run and reload over, making a cold run flaky. The geometry/scroll under
  test belongs to the real TabbedPanelTree, so this stays a faithful equivalent
  of the shipped demo without the heavy module graph.
-->
<script lang="ts">
  import TabbedPanelTree from "../../../../../packages/ui/src/components/shared/TabbedPanelTree.svelte";
  import {
    activateTabbedPanelTab,
    type TabbedPanelDescriptor,
    type TabbedPanelNode,
  } from "../../../../../packages/ui/src/components/shared/tabbed-panel-layout.ts";

  const tabs: TabbedPanelDescriptor[] = [
    { key: "overview", label: "Overview", status: "success" },
    { key: "activity", label: "Activity", status: "running" },
    { key: "terminal", label: "Terminal", status: "warning" },
  ];

  const panelCopy: Record<
    string,
    { eyebrow: string; title: string; body: string; details?: string[] }
  > = {
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

  let activeTabKey = $state("overview");
  let node = $state<TabbedPanelNode>({
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
  });

  function selectTab(tabKey: string): void {
    activeTabKey = tabKey;
    const next = activateTabbedPanelTab(node, tabKey);
    if (next) node = next;
  }
</script>

<div class="tabbed-panel-demo" data-testid="design-system-tabbed-panel-demo">
  <TabbedPanelTree
    dragScope="design-system-panel-demo"
    {node}
    {tabs}
    {activeTabKey}
    scrollPanels={true}
    tablistLabel="Design system panel tabs"
    leafLabel="Design system panel group"
    dropTargetsLabel="Design system panel drop targets"
    resizeLabel="Resize design system panel split"
    onSelectTab={selectTab}
  >
    {#snippet renderTab(tabKey, active)}
      {@const copy = panelCopy[tabKey]}
      <article class={["panel-surface", { active }]} data-testid={`design-system-panel-${tabKey}`}>
        <p>{copy?.eyebrow ?? tabKey}</p>
        <h3>{copy?.title ?? tabKey}</h3>
        <span>{copy?.body ?? ""}</span>
        {#if copy?.details}
          <ul>
            {#each copy.details as detail (detail)}
              <li>{detail}</li>
            {/each}
          </ul>
        {/if}
      </article>
    {/snippet}

    {#snippet tabIcon(tab)}
      <span class="tab-initial">{tab.label.slice(0, 1)}</span>
    {/snippet}
  </TabbedPanelTree>
</div>

<style>
  .tabbed-panel-demo {
    height: 300px;
    min-width: 0;
    border: 1px solid var(--border-muted);
    background: var(--bg-primary);
  }

  .panel-surface {
    min-height: 100%;
    padding: 18px;
    display: grid;
    align-content: start;
    gap: 10px;
    background: var(--bg-surface);
  }

  .panel-surface p,
  .panel-surface h3,
  .panel-surface span {
    margin: 0;
  }

  .panel-surface ul {
    display: grid;
    gap: 7px;
    margin: 4px 0 0;
    padding: 0;
    list-style: none;
  }

  .panel-surface li {
    padding: 7px 8px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
  }

  .tab-initial {
    width: 14px;
    height: 14px;
    display: inline-grid;
    place-items: center;
  }
</style>
