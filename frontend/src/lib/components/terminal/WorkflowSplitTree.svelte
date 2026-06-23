<script lang="ts">
  import type { Snippet } from "svelte";
  import XIcon from "@lucide/svelte/icons/x";
  import MoveIcon from "@lucide/svelte/icons/move";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import SparklesIcon from "@lucide/svelte/icons/sparkles";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import HouseIcon from "@lucide/svelte/icons/house";
  import { TabbedPanelTree, type TabbedPanelDescriptor } from "@middleman/ui";
  import type { SplitDirection, WorkflowNode, WorkflowTabKey } from "./terminal-layout";
  import {
    clearActiveTerminalDrag,
    readWorkflowTabDrag,
    startWorkflowTabDrag,
  } from "./terminal-drag";

  export interface WorkflowTabDescriptor extends TabbedPanelDescriptor {
    key: WorkflowTabKey;
    kind: "home" | "terminal" | "agent" | "plain_shell";
    renamable?: boolean | undefined;
    movableToTerminal?: boolean | undefined;
    closable?: boolean | undefined;
  }

  interface Props {
    workspaceId: string;
    node: WorkflowNode;
    tabs: WorkflowTabDescriptor[];
    activeTabKey: WorkflowTabKey;
    renderTab: Snippet<[WorkflowTabKey, boolean]>;
    disabled?: boolean;
    onSelectTab?: ((tabKey: WorkflowTabKey) => void) | undefined;
    onMoveTabBefore?:
      | ((sourceTabKey: WorkflowTabKey, targetTabKey: WorkflowTabKey) => void)
      | undefined;
    onAppendTabToLeaf?: ((sourceTabKey: WorkflowTabKey, leafID: string) => void) | undefined;
    onSplitTab?:
      | ((
          sourceTabKey: WorkflowTabKey,
          leafID: string,
          direction: SplitDirection,
          placement: "before" | "after",
        ) => void)
      | undefined;
    onMoveTabToTerminal?: ((tabKey: WorkflowTabKey) => void) | undefined;
    onCloseTab?: ((tabKey: WorkflowTabKey) => void) | undefined;
    onRenameTab?: ((tabKey: WorkflowTabKey) => void) | undefined;
    onRatioChange?: ((splitID: string, ratio: number) => void) | undefined;
  }

  const {
    workspaceId,
    node,
    tabs,
    activeTabKey,
    renderTab: renderWorkflowTab,
    disabled = false,
    onSelectTab,
    onMoveTabBefore,
    onAppendTabToLeaf,
    onSplitTab,
    onMoveTabToTerminal,
    onCloseTab,
    onRenameTab,
    onRatioChange,
  }: Props = $props();

  function workflowTabFrom(tabKey: string): WorkflowTabKey {
    return tabKey as WorkflowTabKey;
  }

  function splitDirectionFrom(direction: string): SplitDirection {
    return direction === "vertical" ? "vertical" : "horizontal";
  }

  function tabKind(tab: TabbedPanelDescriptor): WorkflowTabDescriptor["kind"] {
    return (tab as WorkflowTabDescriptor).kind;
  }

  function isRenamable(tab: TabbedPanelDescriptor): boolean {
    return (tab as WorkflowTabDescriptor).renamable === true;
  }

  function isMovableToTerminal(tab: TabbedPanelDescriptor): boolean {
    return (tab as WorkflowTabDescriptor).movableToTerminal === true;
  }

  function isClosable(tab: TabbedPanelDescriptor): boolean {
    return (tab as WorkflowTabDescriptor).closable === true;
  }
</script>

<TabbedPanelTree
  dragScope={workspaceId}
  {node}
  {tabs}
  {activeTabKey}
  {disabled}
  tablistLabel="Workflow group tabs"
  leafLabel="Workflow group"
  dropTargetsLabel="Workflow group drop targets"
  resizeLabel="Resize workflow split"
  onSelectTab={(tabKey) => {
    if (disabled) return;
    onSelectTab?.(workflowTabFrom(tabKey));
  }}
  onMoveTabBefore={(source, target) =>
    !disabled && onMoveTabBefore?.(workflowTabFrom(source), workflowTabFrom(target))}
  onAppendTabToLeaf={(source, leafID) => !disabled && onAppendTabToLeaf?.(workflowTabFrom(source), leafID)}
  onSplitTab={(source, leafID, direction, placement) =>
    !disabled && onSplitTab?.(workflowTabFrom(source), leafID, splitDirectionFrom(direction), placement)}
  onTabDoubleClick={(tabKey) => {
    if (disabled) return;
    onRenameTab?.(workflowTabFrom(tabKey));
  }}
  onRatioChange={(splitID, ratio) => {
    if (disabled) return;
    onRatioChange?.(splitID, ratio);
  }}
  onStartTabDrag={(event, tab) =>
    !disabled && startWorkflowTabDrag(event, {
      workspaceId,
      tabKey: workflowTabFrom(tab.key),
    })}
  onReadDraggedTab={(event) => disabled ? null : readWorkflowTabDrag(event, workspaceId)}
  onClearDrag={clearActiveTerminalDrag}
>
  {#snippet renderTab(tabKey, active)}
    {@render renderWorkflowTab(workflowTabFrom(tabKey), active)}
  {/snippet}

  {#snippet tabIcon(tab)}
    {#if tabKind(tab) === "home"}
      <HouseIcon size="13" strokeWidth="2" />
    {:else if tabKind(tab) === "plain_shell" || tabKind(tab) === "terminal"}
      <TerminalIcon size="13" strokeWidth="2" />
    {:else}
      <SparklesIcon size="13" strokeWidth="2" />
    {/if}
  {/snippet}

  {#snippet tabActions(tab)}
    {@const tabKey = workflowTabFrom(tab.key)}
    {#if isRenamable(tab)}
      <button
        class="tabbed-panel-tab-tool"
        title="Rename"
        aria-label={`Rename ${tab.label}`}
        disabled={disabled}
        onclick={() => {
          if (disabled) return;
          onRenameTab?.(tabKey);
        }}
      >
        <PencilIcon size="11" strokeWidth="2.2" aria-hidden="true" />
      </button>
    {/if}
    {#if isMovableToTerminal(tab)}
      <button
        class="tabbed-panel-tab-tool"
        title="Move to terminal"
        aria-label={`Move ${tab.label} to terminal`}
        disabled={disabled}
        onclick={() => {
          if (disabled) return;
          onMoveTabToTerminal?.(tabKey);
        }}
      >
        <MoveIcon size="11" strokeWidth="2.2" aria-hidden="true" />
      </button>
    {/if}
    {#if isClosable(tab)}
      <button
        class="tabbed-panel-tab-tool"
        title="Close"
        aria-label={`Close ${tab.label}`}
        disabled={disabled}
        onclick={() => {
          if (disabled) return;
          onCloseTab?.(tabKey);
        }}
      >
        <XIcon size="11" strokeWidth="2.3" aria-hidden="true" />
      </button>
    {/if}
  {/snippet}
</TabbedPanelTree>
