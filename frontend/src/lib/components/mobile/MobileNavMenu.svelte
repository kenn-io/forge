<script lang="ts">
  import { Modal } from "@kenn-io/kit-ui";
  import MenuIcon from "@lucide/svelte/icons/menu";
  import { MonitorIcon } from "../../icons.ts";
  import ForgeSelector from "../layout/ForgeSelector.svelte";
  import { getMobileNavMenuContext, mobileNavModes, mobileNavSelectedPath } from "./mobile-nav-menu.js";

  const nav = getMobileNavMenuContext();
  let open = $state(false);

  const selectedPath = $derived(nav ? mobileNavSelectedPath(nav.page()) : "");
  const modes = $derived(nav ? mobileNavModes(nav.isModeVisible) : []);

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
            <button
              type="button"
              aria-current={mode.path === selectedPath ? "page" : undefined}
              onclick={() => choose(() => nav.onNavigate(mode.path))}
            >{mode.label}</button>
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
    display: flex;
    flex-direction: column;
  }

  .mobile-nav-sheet button {
    min-height: var(--mobile-chrome-hit-target);
    display: flex;
    align-items: center;
    gap: 0.625rem;
    padding: 0.625rem 0.875rem;
    border: 0;
    border-bottom: thin solid var(--border-muted);
    color: var(--text-primary);
    background: transparent;
    font: inherit;
    font-size: var(--font-size-md);
    text-align: left;
  }

  .mobile-nav-sheet nav button[aria-current="page"] {
    font-weight: 700;
    background: var(--bg-hover);
    box-shadow: inset 3px 0 0 var(--accent-blue);
  }

  .mobile-nav-sheet button.mobile-nav-sheet__desktop {
    border-bottom: 0;
    color: var(--text-secondary);
  }
</style>
