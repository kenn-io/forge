<script lang="ts">
  interface Props {
    status: "misconfigured" | "down" | "unauthorized";
    statusDetail?: string | undefined;
    /**
     * When provided, the banner renders an inline "Configure" button that
     * fires this callback. Owners pass it when a setup dialog is mounted
     * upstream (App.svelte -> MessagesWorkspace -> MessagesBanner); a callerless
     * banner (e.g. from a standalone unit test) renders no button.
     */
    onConfigure?: (() => void) | undefined;
  }

  let { status, statusDetail, onConfigure }: Props = $props();

  const COPY: Record<Props["status"], string> = {
    misconfigured: "Messages are misconfigured - check message source settings",
    down: "Messages unavailable - retrying on next refresh.",
    unauthorized: "Messages key rejected - check `api_key_env`.",
  };

  function bannerText(): string {
    const base = COPY[status];
    if (status === "misconfigured" && statusDetail) {
      return `${base} (${statusDetail})`;
    }
    return base;
  }
</script>

<div class="messages-banner" role="alert">
  <span class="messages-banner-text">
    {bannerText()}
  </span>
  {#if onConfigure}
    <button
      type="button"
      class="banner-configure"
      aria-label="Configure messages"
      onclick={onConfigure}
    >
      Configure
    </button>
  {/if}
</div>

<style>
  .messages-banner {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 16px;
    background: var(--bg-surface);
    border-bottom: 1px solid var(--border-default);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .messages-banner-text {
    min-width: 0;
    flex: 1;
  }

  .banner-configure {
    flex-shrink: 0;
    padding: 4px 10px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-primary);
    color: var(--text-primary);
    font-size: var(--font-size-xs);
    font-weight: 500;
    cursor: pointer;
    transition: background 0.1s, border-color 0.1s;
  }

  .banner-configure:hover {
    background: var(--bg-surface-hover);
    border-color: var(--accent-blue);
  }

  .banner-configure:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 1px;
  }
</style>
