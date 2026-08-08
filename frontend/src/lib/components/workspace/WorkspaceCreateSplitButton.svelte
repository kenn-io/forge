<script lang="ts">
  import {
    autoReposition,
    Button,
    dismissable,
    floatingPopoverStyle,
  } from "@kenn-io/kit-ui";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import PackagePlusIcon from "@lucide/svelte/icons/package-plus";
  import { Effect } from "effect";
  import { tick } from "svelte";
  import type { LaunchTarget } from "../../api/types.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";

  interface Props {
    label: string;
    busyLabel?: string;
    launchTargets: LaunchTarget[];
    busy?: boolean;
    disabled?: boolean;
    disabledReason?: string;
    descriptionId?: string;
    surface?: "soft" | "solid";
    primaryType?: "button" | "submit";
    onCreate: (targetKey?: string) => void;
  }

  let {
    label,
    busyLabel = "Creating…",
    launchTargets,
    busy = false,
    disabled = false,
    disabledReason = "",
    descriptionId = "",
    surface = "soft",
    primaryType = "button",
    onCreate,
  }: Props = $props();
  const runtime = getAppRuntime();

  let open = $state(false);
  let root = $state<HTMLDivElement>();
  let trigger = $state<HTMLButtonElement>();
  let menu = $state<HTMLUListElement>();
  let menuStyle = $state("");
  let openExecution: AppExecution<void, never> | null = null;
  const agentTargets = $derived(
    launchTargets.filter((target) => target.kind === "agent" && target.available),
  );
  const blocked = $derived(disabled || busy);

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
      align: "end",
      triggerGap: 2,
    });
  }

  function openMenu(): void {
    if (blocked || agentTargets.length === 0) return;
    trigger ??= root?.querySelector<HTMLButtonElement>(".create-options-button") ?? undefined;
    openExecution?.interrupt();
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

  function optionsButton(node: HTMLElement): () => void {
    const button = node.querySelector<HTMLButtonElement>(".create-options-button");
    if (!button) return () => {};
    button.setAttribute("aria-haspopup", "menu");
    function handle(event: KeyboardEvent): void {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        void openMenu();
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
      autoReposition(() => menu, position),
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

<div class="workspace-create-split" bind:this={root}>
  <Button
    type={primaryType}
    class="create-primary"
    tone="info"
    {surface}
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
  <span class="create-options" {@attach optionsButton}>
    <Button
      surface={surface === "solid" ? "solid" : "soft"}
      tone="info"
      class="create-options-button"
      ariaLabel={`${label} options`}
      title={disabledReason || "Create and launch an agent"}
      ariaExpanded={open}
      disabled={blocked || agentTargets.length === 0}
      onclick={() => {
        if (open) closeMenu();
        else void openMenu();
      }}
    >
      <ChevronDownIcon size="12" strokeWidth="2" aria-hidden="true" />
    </Button>
  </span>
  {#if open}
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
            {target.label}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .workspace-create-split {
    display: inline-flex;
    min-width: 0;
    max-width: 100%;
  }

  .workspace-create-split :global(.create-primary) {
    flex: 1 1 auto;
    min-width: 0;
    min-height: 30px;
    overflow: hidden;
    padding-inline: var(--space-4);
    border-radius: var(--radius-sm) 0 0 var(--radius-sm);
  }

  .workspace-create-split :global(.create-primary span) {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .create-options {
    display: inline-flex;
    flex: 0 0 30px;
    width: 30px;
  }

  .create-options :global(.create-options-button) {
    flex-shrink: 0;
    width: 30px;
    min-height: 30px;
    padding: 0;
    border-left: 0;
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
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
