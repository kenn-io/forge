<!--
  Browser-tier test harness. Mounts the real TabbedPanelTree UI primitive with
  the exact same node tree, tabs, labels, scrollPanels flag, and renderTab
  content that DesignSystemTabbedPanelDemo.svelte ships on /design-system: both
  read that configuration from the shared tabbedPanelDemoData.ts (including the
  nine Activity rows whose overflow drives the scroll assertion), so the demo and
  this harness cannot drift apart. It imports TabbedPanelTree and
  activateTabbedPanelTab from their source files rather than the @middleman/ui
  barrel: the barrel re-exports the whole UI package (tiptap, pierre, dozens of
  lucide icons), which the browser project would optimize mid-run and reload
  over, making a cold run flaky. The geometry/scroll under test belongs to the
  real TabbedPanelTree, so this stays a faithful equivalent of the shipped demo
  without the heavy module graph.
-->
<script lang="ts">
  import TabbedPanelTree from "../../../../../packages/ui/src/components/shared/TabbedPanelTree.svelte";
  import {
    activateTabbedPanelTab,
    type TabbedPanelNode,
  } from "../../../../../packages/ui/src/components/shared/tabbed-panel-layout.ts";

  import {
    createTabbedPanelDemoNode,
    tabbedPanelDemoCopy as panelCopy,
    tabbedPanelDemoTabs as tabs,
  } from "./tabbedPanelDemoData.ts";

  let activeTabKey = $state("overview");
  let node = $state<TabbedPanelNode>(createTabbedPanelDemoNode());

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
