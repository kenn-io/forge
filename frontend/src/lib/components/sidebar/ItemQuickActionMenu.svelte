<script lang="ts">
  import { Menu, MenuContent, MenuItem, MenuSeparator, type MenuAnchorPoint } from "@kenn-io/kit-ui";
  import ZapIcon from "@lucide/svelte/icons/zap";
  import type { QuickAction } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { quickActionItemLabel, runQuickActionOnItems, type QuickActionItem } from "../../stores/bulk-quick-action.js";
  import {
    quickActionDisabledReason,
    quickActionTarget,
    sortQuickActionsByLabel,
  } from "../../stores/workspace-quick-actions.js";
  import ConfirmDialog from "../shared/ConfirmDialog.svelte";

  // Right-click menu for PR and issue rows: runs a quick action on the
  // right-clicked item, or on every selected item when it is part of a
  // multi-row selection.
  interface Props {
    open: boolean;
    anchor: MenuAnchorPoint | undefined;
    returnFocus: HTMLElement | undefined;
    targets: readonly QuickActionItem[];
    /** Plural noun for the items, such as "pull requests". */
    noun: string;
    selectionActive: boolean;
    onClearSelection: () => void;
  }

  let {
    open = $bindable(),
    anchor,
    returnFocus,
    targets,
    noun,
    selectionActive,
    onClearSelection,
  }: Props = $props();

  const runtime = getAppRuntime();
  const { settings } = getStores();

  const quickActions = $derived(sortQuickActionsByLabel(settings.getQuickActions()));
  const launchTargets = $derived(settings.getLaunchTargets());
  let confirm = $state.raw<{ action: QuickAction; targets: readonly QuickActionItem[] } | null>(null);
  let running = $state(false);

  function run(action: QuickAction, items: readonly QuickActionItem[]): void {
    running = true;
    runQuickActionOnItems(
      runtime,
      items,
      action,
      settings.getWorkspaceSettings().default_execution_target ?? "",
      () => {
        running = false;
        confirm = null;
        onClearSelection();
      },
    );
  }

  function choose(action: QuickAction): void {
    const items = [...targets];
    if (items.length === 1) run(action, items);
    else confirm = { action, targets: items };
  }

  function confirmMessage(action: QuickAction, items: readonly QuickActionItem[]): string {
    const labels = items.slice(0, 3).map(quickActionItemLabel);
    const rest = items.length - labels.length;
    return `Run "${action.label}" on ${labels.join(", ")}${rest > 0 ? ` and ${rest} more` : ""}?`;
  }

  function agentLabel(action: QuickAction): string {
    return quickActionTarget(action, launchTargets)?.label ?? action.agent;
  }
</script>

<Menu bind:open {anchor} {returnFocus} class="item-quick-action-menu-root">
  <MenuContent
    ariaLabel={targets.length > 1 ? `Quick actions for ${targets.length} ${noun}` : "Quick actions"}
    class="item-quick-action-menu"
  >
    {#each quickActions as action (action.label)}
      {@const reason = quickActionDisabledReason(action, launchTargets)}
      <MenuItem disabled={reason !== "" || running} textValue={action.label} onselect={() => choose(action)}>
        {#snippet icon()}<ZapIcon size={14} />{/snippet}
        {#snippet trailing()}{reason || agentLabel(action)}{/snippet}
        {targets.length > 1 ? `${action.label} on ${targets.length} ${noun}...` : action.label}
      </MenuItem>
    {:else}
      <MenuItem disabled onselect={() => {}}>No quick actions configured</MenuItem>
    {/each}
    {#if selectionActive}
      <MenuSeparator />
      <MenuItem onselect={onClearSelection}>Clear selection</MenuItem>
    {/if}
  </MenuContent>
</Menu>

<ConfirmDialog
  open={confirm !== null}
  title={`Run "${confirm?.action.label ?? ""}" on ${confirm?.targets.length ?? 0} ${noun}?`}
  message={confirm ? confirmMessage(confirm.action, confirm.targets) : ""}
  hint={confirm
    ? `Each gets a workspace, reusing one that already exists, and ${agentLabel(confirm.action)} starts with the action's prompt.`
    : undefined}
  confirmLabel={`Run on ${confirm?.targets.length ?? 0} ${noun}`}
  pendingLabel="Starting…"
  busy={running}
  frameId="item-quick-action-confirm"
  onCancel={() => { confirm = null; }}
  onConfirm={() => { if (confirm) run(confirm.action, confirm.targets); }}
/>
