<script lang="ts">
  import { Modal } from "@kenn-io/kit-ui";
  import CircleDotIcon from "@lucide/svelte/icons/circle-dot";
  import GitPullRequestIcon from "@lucide/svelte/icons/git-pull-request";
  import InboxIcon from "@lucide/svelte/icons/inbox";
  import MenuIcon from "@lucide/svelte/icons/menu";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import { untrack } from "svelte";
  import { MonitorIcon } from "../../icons.ts";
  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";
  import ForgeSelector from "../layout/ForgeSelector.svelte";
  import { getMobileNavMenuContext, mobileNavModes, mobileNavSelectedPath } from "./mobile-nav-menu.js";

  const nav = getMobileNavMenuContext();
  let open = $state(false);

  const selectedPath = $derived(nav ? mobileNavSelectedPath(nav.page()) : "");
  const modes = $derived(nav ? mobileNavModes(nav.isModeVisible) : []);

  const modeIcons = {
    "/m": InboxIcon,
    "/m/pulls": GitPullRequestIcon,
    "/m/issues": CircleDotIcon,
    "/m/workspaces": TerminalIcon,
  } as const;

  // An open sheet owns the keyboard, so Escape closes it instead of running
  // the page's own Escape action underneath.
  $effect(() => {
    if (!open) return;
    return untrack(() => pushModalFrame("mobile-nav-menu", []));
  });

  function choose(action: () => void): void {
    open = false;
    action();
  }
</script>

{#if nav}
  <button
    class="mobile-nav-menu__trigger"
    type="button"
    aria-label="Menu"
    aria-haspopup="dialog"
    aria-expanded={open}
    onclick={() => (open = true)}
  >
    <MenuIcon size="20" strokeWidth="2" aria-hidden="true" />
  </button>

  {#if open}
    <Modal
      title="kenn-forge"
      ariaLabel="Menu"
      closeLabel="Close menu"
      width="min(100%, 38rem)"
      maxWidth="100%"
      onclose={() => (open = false)}
    >
      <div class="mobile-nav-sheet">
        <!-- Renders only when the fleet has another Forge to switch to. -->
        <div class="mobile-nav-sheet__forge">
          <ForgeSelector compact />
        </div>
        <nav aria-label="Phone mode">
          {#each modes as mode (mode.path)}
            {@const ModeIcon = modeIcons[mode.path as keyof typeof modeIcons]}
            <button
              type="button"
              aria-current={mode.path === selectedPath ? "page" : undefined}
              onclick={() => choose(() => nav.onNavigate(mode.path))}
            >
              {#if ModeIcon}<ModeIcon size="22" strokeWidth="1.75" aria-hidden="true" />{/if}
              {mode.label}
            </button>
          {/each}
        </nav>
        <button
          class="mobile-nav-sheet__desktop"
          type="button"
          onclick={() => choose(nav.onDesktopView)}
        >
          <MonitorIcon size="18" strokeWidth="1.75" aria-hidden="true" />
          Open desktop view
        </button>
      </div>
    </Modal>
  {/if}
{/if}

<style>
  /* Stretch to the tallest control in the host row so the trigger lines up
     with each screen's own buttons. */
  .mobile-nav-menu__trigger {
    flex: 0 0 auto;
    align-self: stretch;
    min-width: var(--mobile-chrome-hit-target);
    min-height: var(--mobile-chrome-hit-target);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    background: var(--bg-inset);
  }

  .mobile-nav-menu__trigger:focus-visible,
  .mobile-nav-sheet button:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 2px;
  }

  :global(.kit-modal-overlay:has(.mobile-nav-sheet)) {
    align-items: flex-end;
  }

  :global(.kit-modal-panel:has(.mobile-nav-sheet)) {
    max-height: min(82vh, 44rem);
    border-bottom: 0;
    border-radius: var(--radius-lg) var(--radius-lg) 0 0;
  }

  :global(.kit-modal-body:has(> .mobile-nav-sheet)) {
    padding: 0 0 max(1rem, env(safe-area-inset-bottom));
  }

  .mobile-nav-sheet__forge:not(:has(:global(.forge-selector))) {
    display: none;
  }

  .mobile-nav-sheet__forge {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.75rem 0.875rem;
    border-bottom: thin solid var(--border-muted);
  }

  .mobile-nav-sheet nav {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.625rem;
    padding: 0.875rem;
  }

  .mobile-nav-sheet button {
    display: flex;
    align-items: center;
    justify-content: center;
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    background: var(--bg-inset);
    font: inherit;
    font-size: var(--font-size-md);
  }

  .mobile-nav-sheet nav button {
    min-height: 4.75rem;
    flex-direction: column;
    gap: 0.4375rem;
    padding: 0.75rem 0.5rem;
    font-weight: 600;
  }

  .mobile-nav-sheet nav button :global(svg) {
    color: var(--text-secondary);
  }

  .mobile-nav-sheet nav button[aria-current="page"] {
    border-color: var(--accent-blue);
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 14%, var(--bg-inset));
  }

  .mobile-nav-sheet nav button[aria-current="page"] :global(svg) {
    color: var(--accent-blue);
  }

  .mobile-nav-sheet button.mobile-nav-sheet__desktop {
    width: calc(100% - 1.75rem);
    min-height: var(--mobile-chrome-hit-target);
    gap: 0.5rem;
    margin: 0 0.875rem;
    color: var(--text-secondary);
    background: transparent;
  }
</style>
