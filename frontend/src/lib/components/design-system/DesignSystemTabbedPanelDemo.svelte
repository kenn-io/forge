<script lang="ts">
  import {
    activateTabbedPanelTab,
    appendTabbedPanelTabToLeaf,
    moveTabbedPanelTabBefore,
    splitTabbedPanelTabIntoLeaf,
    TabbedPanelTree,
    type TabbedPanelDirection,
    type TabbedPanelNode,
    updateTabbedPanelSplitRatio,
  } from "@middleman/ui";

  import {
    createTabbedPanelDemoNode,
    tabbedPanelDemoCopy as panelCopy,
    tabbedPanelDemoTabs as tabs,
  } from "./tabbedPanelDemoData.ts";

  let activeTabKey = $state("overview");
  let node = $state<TabbedPanelNode>(createTabbedPanelDemoNode());

  function updateNode(next: TabbedPanelNode | null): void {
    if (next) node = next;
  }

  function selectTab(tabKey: string): void {
    activeTabKey = tabKey;
    updateNode(activateTabbedPanelTab(node, tabKey));
  }

  function moveTabBefore(sourceTabKey: string, targetTabKey: string): void {
    updateNode(moveTabbedPanelTabBefore(node, sourceTabKey, targetTabKey));
    selectTab(sourceTabKey);
  }

  function appendTabToLeaf(sourceTabKey: string, leafID: string): void {
    updateNode(appendTabbedPanelTabToLeaf(node, sourceTabKey, leafID));
    selectTab(sourceTabKey);
  }

  function splitTab(
    sourceTabKey: string,
    leafID: string,
    direction: TabbedPanelDirection,
    placement: "before" | "after",
  ): void {
    updateNode(splitTabbedPanelTabIntoLeaf(node, sourceTabKey, leafID, direction, placement));
    selectTab(sourceTabKey);
  }

  function updateRatio(splitID: string, ratio: number): void {
    updateNode(updateTabbedPanelSplitRatio(node, splitID, ratio));
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
    onMoveTabBefore={moveTabBefore}
    onAppendTabToLeaf={appendTabToLeaf}
    onSplitTab={splitTab}
    onRatioChange={updateRatio}
  >
    {#snippet renderTab(tabKey, active)}
      {@const copy = panelCopy[tabKey]}
      <article
        class={["panel-surface", { active }]}
        data-testid={`design-system-panel-${tabKey}`}
      >
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
    background:
      linear-gradient(180deg, color-mix(in srgb, var(--bg-surface) 92%, transparent), var(--bg-surface)),
      var(--bg-surface);
  }

  .panel-surface p,
  .panel-surface h3,
  .panel-surface span {
    margin: 0;
  }

  .panel-surface p {
    color: var(--accent-blue);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .panel-surface h3 {
    color: var(--text-primary);
    font-size: var(--font-size-xl);
    line-height: 1.15;
  }

  .panel-surface span {
    color: var(--text-secondary);
    font-size: var(--font-size-md);
    line-height: 1.45;
    max-width: 42ch;
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
    color: var(--text-secondary);
    background: color-mix(in srgb, var(--bg-primary) 36%, transparent);
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    line-height: 1.25;
  }

  .tab-initial {
    width: 14px;
    height: 14px;
    display: inline-grid;
    place-items: center;
    border-radius: 3px;
    background: color-mix(in srgb, var(--accent-blue) 16%, transparent);
    color: var(--accent-blue);
    font-size: var(--font-size-xs);
    font-weight: 800;
  }
</style>
