<script lang="ts">
  import {
    autoReposition,
    Button,
    dismissable,
    floatingPopoverStyle,
  } from "@kenn-io/kit-ui";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import PackagePlusIcon from "@lucide/svelte/icons/package-plus";
  import ZapIcon from "@lucide/svelte/icons/zap";
  import LaunchTargetName from "../terminal/LaunchTargetName.svelte";
  import { Effect } from "effect";
  import { tick } from "svelte";
  import type { LaunchTarget, QuickAction } from "../../api/types.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { sortQuickActionsByLabel } from "../../stores/workspace-quick-actions.js";

  interface Props {
    label: string;
    busyLabel?: string;
    launchTargets: LaunchTarget[];
    busy?: boolean;
    disabled?: boolean;
    disabledReason?: string;
    descriptionId?: string;
    surface?: "soft" | "solid";
    size?: "sm" | "md";
    primaryType?: "button" | "submit";
    onCreate: (targetKey?: string) => void;
    /** Configured quick actions; the lightning segment renders only when non-empty. */
    quickActions?: QuickAction[];
    /** Create the workspace, launch the action's agent, and deliver its prompt. */
    onQuickAction?: (action: QuickAction) => void;
  }

  type MenuKind = "agents" | "quick";

  let {
    label,
    busyLabel = "Creating…",
    launchTargets,
    busy = false,
    disabled = false,
    disabledReason = "",
    descriptionId = "",
    surface = "soft",
    size = "md",
    primaryType = "button",
    onCreate,
    quickActions = [],
    onQuickAction,
  }: Props = $props();
  const runtime = getAppRuntime();

  let open = $state(false);
  let menuKind = $state<MenuKind>("agents");
  let root = $state<HTMLDivElement>();
  let trigger = $state<HTMLButtonElement>();
  let menu = $state<HTMLUListElement>();
  let menuStyle = $state("");
  let openExecution: AppExecution<void, never> | null = null;
  const agentTargets = $derived(
    launchTargets.filter((target) => target.kind === "agent" && target.available),
  );
  const blocked = $derived(disabled || busy);
  const showQuickActions = $derived(quickActions.length > 0 && onQuickAction !== undefined);
  const sortedQuickActions = $derived(sortQuickActionsByLabel(quickActions));

  function quickActionTarget(action: QuickAction): LaunchTarget | undefined {
    return launchTargets.find((target) => target.key === action.agent && target.kind === "agent");
  }

  function quickActionDisabledReason(action: QuickAction): string {
    const target = quickActionTarget(action);
    if (!target) return `Agent "${action.agent}" is not configured`;
    if (!target.available) return target.disabled_reason || `Agent "${action.agent}" is not available`;
    return "";
  }

  function enabledItems(): HTMLButtonElement[] {
    return Array.from(menu?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') ?? []);
  }

  function position(): void {
    if (!trigger || !menu) return;
    menuStyle = floatingPopoverStyle({
      trigger: trigger.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: menu.offsetWidth,
      popoverHeight: menu.offsetHeight,
      // The agent menu hangs off the trailing chevron, so its right edge
      // lines up with the control. The quick-actions segment sits mid-control,
      // so its menu opens from the segment's own left edge instead.
      align: menuKind === "quick" ? "start" : "end",
      triggerGap: 2,
    });
  }

  function openMenu(kind: MenuKind = "agents"): void {
    if (blocked) return;
    if (kind === "agents" && agentTargets.length === 0) return;
    if (kind === "quick" && !showQuickActions) return;
    trigger = root?.querySelector<HTMLButtonElement>(triggerSelector(kind)) ?? undefined;
    openExecution?.interrupt();
    menuKind = kind;
    open = true;
    openExecution = runtime.runCommand(
      Effect.promise(() => tick()).pipe(
        Effect.andThen(Effect.sync(() => {
          position();
          enabledItems()[0]?.focus();
        })),
      ),
      {
        operation: "open workspace launch menu",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  function closeMenu(): void {
    openExecution?.interrupt();
    openExecution = null;
    open = false;
  }

  function portalMenu(node: HTMLElement): () => void {
    const host = root?.closest<HTMLElement>(".kit-modal-panel") ?? document.body;
    host.appendChild(node);
    return () => node.remove();
  }

  // Both segments carry the shared create-options-button class for sizing, so
  // lookups must use the segment-specific class or they resolve to whichever
  // segment renders first.
  function triggerSelector(kind: MenuKind): string {
    return kind === "quick" ? ".create-quick-actions-button" : ".create-agent-options-button";
  }

  function optionsButton(node: HTMLElement, kind: MenuKind = "agents"): () => void {
    const button = node.querySelector<HTMLButtonElement>(triggerSelector(kind));
    if (!button) return () => {};
    button.setAttribute("aria-haspopup", "menu");
    function handle(event: KeyboardEvent): void {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        void openMenu(kind);
      } else if (event.key === "Tab" && open) {
        closeMenu();
      }
    }
    button.addEventListener("keydown", handle);
    return () => button.removeEventListener("keydown", handle);
  }

  function selectTarget(targetKey: string): void {
    closeMenu();
    onCreate(targetKey);
  }

  function selectQuickAction(action: QuickAction): void {
    closeMenu();
    onQuickAction?.(action);
  }

  function handleItemKeydown(event: KeyboardEvent): void {
    const items = enabledItems();
    const current = items.indexOf(event.currentTarget as HTMLButtonElement);
    let next = current;
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      (event.currentTarget as HTMLButtonElement).click();
      return;
    }
    if (event.key === "ArrowDown") next = (current + 1) % items.length;
    else if (event.key === "ArrowUp") next = (current - 1 + items.length) % items.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = items.length - 1;
    else if (event.key === "Tab") {
      closeMenu();
      return;
    } else {
      return;
    }
    event.preventDefault();
    items[next]?.focus();
  }

  $effect(() => {
    if (!open) return;

    function dismissPointerDown(event: PointerEvent): void {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (root?.contains(target) || menu?.contains(target)) return;
      closeMenu();
    }

    const cleanups = [
      dismissable({
        owners: () => [root, menu],
        dismiss: closeMenu,
        escapeFocus: () => trigger,
      }),
      autoReposition(() => [menu], position),
    ];
    document.addEventListener("pointerdown", dismissPointerDown);
    return () => {
      cleanups.forEach((cleanup) => cleanup());
      document.removeEventListener("pointerdown", dismissPointerDown);
    };
  });

  $effect(() => {
    if (blocked) open = false;
  });
</script>

<div class="workspace-create-split workspace-create-split--{size}" bind:this={root}>
  <Button
    type={primaryType}
    class="create-primary"
    tone="info"
    {surface}
    {size}
    ariaLabel={busy ? busyLabel : label}
    ariaDescribedby={descriptionId || undefined}
    title={disabledReason || label}
    disabled={blocked}
    onclick={() => {
      if (primaryType === "button") onCreate(undefined);
    }}
  >
    <PackagePlusIcon size="14" strokeWidth="2.2" aria-hidden="true" />
    <span>{busy ? busyLabel : label}</span>
  </Button>
  {#if showQuickActions}
    <span class="create-options create-quick-actions" {@attach (node) => optionsButton(node, "quick")}>
      <Button
        surface={surface === "solid" ? "solid" : "soft"}
        tone="info"
        {size}
        class="create-options-button create-quick-actions-button"
        ariaLabel="Quick actions"
        title={disabledReason || "Create, launch an agent, and send a preset prompt"}
        ariaExpanded={open && menuKind === "quick"}
        disabled={blocked}
        onclick={() => {
          if (open && menuKind === "quick") closeMenu();
          else void openMenu("quick");
        }}
      >
        <ZapIcon size="12" strokeWidth="2.2" aria-hidden="true" />
      </Button>
    </span>
  {/if}
  <span class="create-options" {@attach optionsButton}>
    <Button
      surface={surface === "solid" ? "solid" : "soft"}
      tone="info"
      {size}
      class="create-options-button create-agent-options-button"
      ariaLabel={`${label} options`}
      title={disabledReason || "Create and launch an agent"}
      ariaExpanded={open && menuKind === "agents"}
      disabled={blocked || agentTargets.length === 0}
      onclick={() => {
        if (open && menuKind === "agents") closeMenu();
        else void openMenu("agents");
      }}
    >
      <ChevronDownIcon size="12" strokeWidth="2" aria-hidden="true" />
    </Button>
  </span>
  {#if open && menuKind === "quick"}
    <ul
      bind:this={menu}
      class="create-menu create-menu--quick kit-popover-card"
      role="menu"
      aria-label="Quick actions"
      style={menuStyle}
      {@attach portalMenu}
    >
      {#each sortedQuickActions as action (action.label)}
        {@const reason = quickActionDisabledReason(action)}
        <li role="none">
          <button
            type="button"
            role="menuitem"
            disabled={reason !== ""}
            title={reason || action.prompt}
            onclick={() => selectQuickAction(action)}
            onkeydown={handleItemKeydown}
          >
            <LaunchTargetName
              target={{ kind: "agent", key: action.agent }}
              label={action.label}
              iconSize={12}
              fallbackIcon
            />
            <span class={["quick-action-agent", reason !== "" && "quick-action-agent--unavailable"]}>
              {reason || quickActionTarget(action)?.label || action.agent}
            </span>
          </button>
        </li>
      {/each}
    </ul>
  {:else if open}
    <ul
      bind:this={menu}
      class="create-menu kit-popover-card"
      role="menu"
      aria-label="Create and launch"
      style={menuStyle}
      {@attach portalMenu}
    >
      {#each agentTargets as target (target.key)}
        <li role="none">
          <button
            type="button"
            role="menuitem"
            disabled={!target.available}
            title={target.disabled_reason || target.label}
            onclick={() => selectTarget(target.key)}
            onkeydown={handleItemKeydown}
          >
            <LaunchTargetName {target} label={target.label} iconSize={12} />
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  /* The chevron segment is a square cap on the primary control, so its width
   * tracks the kit control height for the requested size. Heights themselves
   * come from kit: hardcoding one here made the split button taller than the
   * `sm` siblings it sits beside in the detail action rows. */
  .workspace-create-split {
    --create-options-width: var(--kit-control-height, 28px);

    display: inline-flex;
    min-width: 0;
    max-width: 100%;
  }

  .workspace-create-split--sm {
    --create-options-width: 24px;
  }

  .workspace-create-split :global(.create-primary) {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    padding-inline: var(--space-4);
    border-radius: var(--kit-control-radius, var(--radius-sm)) 0 0 var(--kit-control-radius, var(--radius-sm));
  }

  .workspace-create-split :global(.create-primary span) {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Sized by its button: phone hit-target rules widen the button beyond the
   * square cap, and a fixed wrapper width would let it overflow the control. */
  .create-options {
    display: inline-flex;
    flex: 0 0 auto;
  }

  .create-options :global(.create-options-button) {
    flex-shrink: 0;
    width: var(--create-options-width);
    padding: 0;
    border-left: 0;
    border-radius: 0 var(--kit-control-radius, var(--radius-sm)) var(--kit-control-radius, var(--radius-sm)) 0;
  }

  /* The quick-actions segment sits between the primary control and the
   * chevron, so it is square on both sides. */
  .create-quick-actions :global(.create-quick-actions-button) {
    border-radius: 0;
  }

  /* A trigger stays visibly down while its menu is open. The kit's soft
   * info surface has no expanded state of its own (its solid info and soft
   * success surfaces do), so this mirrors that recipe: a deeper tint and a
   * firmer border, plus an inset edge so the segment reads as pressed. */
  .create-options :global(.create-options-button.kit-button--soft[aria-expanded="true"]) {
    background: color-mix(in srgb, var(--accent-blue) 26%, transparent);
    border-color: color-mix(in srgb, var(--accent-blue) 48%, transparent);
    box-shadow: inset 0 1px 2px color-mix(in srgb, var(--accent-blue) 35%, transparent);
  }

  .create-options :global(.create-options-button.kit-button--solid[aria-expanded="true"]) {
    box-shadow: inset 0 1px 2px color-mix(in srgb, #000 30%, transparent);
  }

  .create-menu--quick button {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
  }

  .quick-action-agent {
    flex: 0 0 auto;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .quick-action-agent--unavailable {
    flex: 0 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-faint);
  }

  .create-menu {
    position: fixed;
    z-index: var(--z-popover, 1001);
    min-width: 180px;
    max-width: calc(100vw - 16px);
    margin: 0;
    padding: var(--space-2);
    list-style: none;
  }

  .create-menu button {
    width: 100%;
    min-height: 30px;
    padding: 0 var(--space-3);
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-xs);
    text-align: left;
    cursor: pointer;
  }

  .create-menu button:hover:not(:disabled),
  .create-menu button:focus-visible {
    background: var(--bg-surface-hover);
  }

  .create-menu button:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: -1px;
  }

  .create-menu button:disabled {
    color: var(--text-faint);
    cursor: default;
  }
</style>
