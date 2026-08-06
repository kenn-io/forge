<script lang="ts">
  import type { WorkspaceSession } from "../../api/types.js";

  interface Props {
    sessions: WorkspaceSession[];
    onCommand: (
      command: string,
      payload: Record<string, unknown>,
    ) => void;
    hostKey: string;
  }

  let { sessions, onCommand, hostKey }: Props = $props();

  let showHidden = $state(false);

  const visibleSessions = $derived(
    sessions.filter((s) => !s.isHidden),
  );
  const hiddenSessions = $derived(
    sessions.filter((s) => s.isHidden),
  );
  const hasContent = $derived(
    visibleSessions.length > 0 || hiddenSessions.length > 0,
  );
</script>

{#if hasContent}
  <section class="session-section">
    <div class="section-header">Sessions</div>

    {#each visibleSessions as session (session.key)}
      <div
        class="session-row"
        role="button"
        tabindex="0"
        onclick={() =>
          onCommand("openSession", {
            hostKey,
            sessionKey: session.key,
          })}
        onkeydown={(e: KeyboardEvent) => {
          if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) {
            onCommand("openSession", {
              hostKey,
              sessionKey: session.key,
            });
          }
        }}
      >
        <span class="session-name">{session.name}</span>
        {#if session.worktreeKey === null}
          <button
            class="hide-btn"
            onclick={(e: MouseEvent) => {
              e.stopPropagation();
              onCommand("hideSession", {
                hostKey,
                sessionKey: session.key,
              });
            }}
            title="Hide session"
          >
            &times;
          </button>
        {/if}
      </div>
    {/each}

    {#if hiddenSessions.length > 0}
      <button
        class="hidden-toggle"
        onclick={() => (showHidden = !showHidden)}
      >
        {showHidden
          ? "Hide hidden sessions"
          : `Show ${hiddenSessions.length} hidden`}
      </button>
    {/if}

    {#if showHidden}
      {#each hiddenSessions as session (session.key)}
        <button
          class="session-row session-row--hidden"
          onclick={() =>
            onCommand("openSession", {
              hostKey,
              sessionKey: session.key,
            })}
        >
          <span class="session-name">{session.name}</span>
        </button>
      {/each}
      <button
        class="hidden-toggle"
        onclick={() =>
          onCommand("unhideAllSessions", { hostKey })}
      >
        Unhide all sessions
      </button>
    {/if}
  </section>
{/if}

<style>
  .session-section {
    display: flex;
    flex-direction: column;
  }

  .section-header {
    height: 32px;
    display: flex;
    align-items: center;
    padding: 0 10px;
    font-size: var(--font-size-xs);
    font-weight: 700;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .session-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    height: 32px;
    padding: 0 12px;
    text-align: left;
    background: var(--bg-surface);
    border: none;
    cursor: pointer;
    transition: background 0.1s;
  }

  .session-row:hover {
    background: var(--bg-surface-hover);
  }

  .session-row--hidden {
    opacity: 0.55;
  }

  .session-name {
    font-size: var(--font-size-md);
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .hide-btn {
    flex-shrink: 0;
    width: 20px;
    height: 20px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: var(--font-size-lg);
    color: var(--text-muted);
    background: none;
    border: none;
    border-radius: var(--radius-sm, 4px);
    cursor: pointer;
    opacity: 0;
    transition: opacity 0.1s;
  }

  .session-row:hover .hide-btn {
    opacity: 1;
  }

  .hide-btn:hover {
    color: var(--text-primary);
    background: var(--bg-inset);
  }

  .hidden-toggle {
    display: block;
    width: 100%;
    padding: 4px 12px;
    text-align: left;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    background: none;
    border: none;
    cursor: pointer;
  }

  .hidden-toggle:hover {
    color: var(--text-secondary);
  }
</style>
