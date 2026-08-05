<script lang="ts">
  import { autoReposition, dismissable, floatingPopoverStyle } from "@kenn-io/kit-ui";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import PackagePlusIcon from "@lucide/svelte/icons/package-plus";
  import { tick } from "svelte";
  import type { LaunchTarget } from "../../api/types.js";

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
    onCreate: (targetKey?: string) => void | Promise<void>;
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

  let open = $state(false);
  let root = $state<HTMLDivElement>();
  let trigger = $state<HTMLButtonElement>();
  let menu = $state<HTMLUListElement>();
  let menuStyle = $state("");
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

  async function openMenu(): Promise<void> {
    if (blocked || agentTargets.length === 0) return;
    open = true;
    await tick();
    position();
    enabledItems()[0]?.focus();
  }

  function closeMenu(): void {
    open = false;
  }

  function portalMenu(node: HTMLElement): () => void {
    const host = root?.closest<HTMLElement>(".kit-modal-panel") ?? document.body;
    host.appendChild(node);
    return () => node.remove();
  }

  function selectTarget(targetKey: string): void {
    closeMenu();
    void onCreate(targetKey);
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

<div class="workspace-create-split" bind:this={root} data-surface={surface}>
  <button
    type={primaryType}
    class="create-primary"
    aria-label={busy ? busyLabel : label}
    aria-describedby={descriptionId || undefined}
    title={disabledReason || label}
    disabled={blocked}
    onclick={() => {
      if (primaryType === "button") void onCreate(undefined);
    }}
  >
    <PackagePlusIcon size="14" strokeWidth="2.2" aria-hidden="true" />
    <span>{busy ? busyLabel : label}</span>
  </button>
  <button
    bind:this={trigger}
    type="button"
    class="create-options"
    aria-label={`${label} options`}
    title={disabledReason || "Create and launch an agent"}
    aria-haspopup="menu"
    aria-expanded={open}
    disabled={blocked || agentTargets.length === 0}
    onclick={() => {
      if (open) closeMenu();
      else void openMenu();
    }}
    onkeydown={(event) => {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        void openMenu();
      } else if (event.key === "Tab" && open) {
        closeMenu();
      }
    }}
  >
    <ChevronDownIcon size="12" strokeWidth="2" aria-hidden="true" />
  </button>
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

  .workspace-create-split > button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 30px;
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-xs);
    font-weight: 600;
    cursor: pointer;
  }

  .create-primary {
    flex: 1 1 auto;
    gap: var(--space-2);
    min-width: 0;
    overflow: hidden;
    padding: 0 var(--space-4);
    border-radius: var(--radius-sm) 0 0 var(--radius-sm);
  }

  .create-primary span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .create-options {
    flex: 0 0 30px;
    width: 30px;
    padding: 0;
    border-left: 0;
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  }

  .workspace-create-split[data-surface="solid"] > button {
    border-color: var(--accent-blue);
    background: var(--accent-blue);
    color: var(--bg-surface);
  }

  .workspace-create-split > button:hover:not(:disabled),
  .workspace-create-split > button:focus-visible,
  .workspace-create-split > button[aria-expanded="true"] {
    border-color: var(--accent-blue);
    color: var(--text-primary);
  }

  .workspace-create-split > button:focus-visible {
    position: relative;
    outline: 2px solid var(--accent-blue);
    outline-offset: -1px;
  }

  .workspace-create-split[data-surface="solid"] > button:hover:not(:disabled),
  .workspace-create-split[data-surface="solid"] > button:focus-visible {
    border-color: color-mix(in srgb, var(--accent-blue) 88%, #000);
    background: color-mix(in srgb, var(--accent-blue) 88%, #000);
    color: var(--bg-surface);
  }

  .workspace-create-split[data-surface="solid"] > button[aria-expanded="true"] {
    border-color: color-mix(in srgb, var(--accent-blue) 78%, #000);
    background: color-mix(in srgb, var(--accent-blue) 78%, #000);
    color: var(--bg-surface);
  }

  .workspace-create-split > button:disabled {
    color: var(--text-faint);
    cursor: default;
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
