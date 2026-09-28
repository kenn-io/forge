<script lang="ts">
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import ArrowUp from "@lucide/svelte/icons/arrow-up";
  import Square from "@lucide/svelte/icons/square";
  import Settings from "@lucide/svelte/icons/settings-2";
  import ChatOptionMenu from "./ChatOptionMenu.svelte";
  import ChatSessionOptions from "./ChatSessionOptions.svelte";
  import { Button, Spinner } from "@kenn-io/kit-ui";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import ChatMessageView from "./ChatMessageView.svelte";
  import ChatToolGroup from "./ChatToolGroup.svelte";
  import { chatRows } from "./chat-timeline.js";
  import { makeChatSession } from "./chat-session.js";
  import type { ChatState } from "./chat-types.js";

  let { websocketPath, label = "Agent", status = "running", active = true, disabled = false, onConnectionChange, onExit }: {
    websocketPath: string; label?: string; status?: string; active?: boolean; disabled?: boolean;
    onConnectionChange?: (connected: boolean) => void;
    onExit?: (code: number) => void;
  } = $props();
  const runtime = getAppRuntime();
  let chatState = $state.raw<ChatState | null>(null);
  let connection = $state<ReturnType<typeof makeChatSession> | null>(null);
  let connected = $state(false);
  let error = $state("");
  let draft = $state("");
  let pending = $state<{id: string; text: string} | null>(null);
  let scroll = $state<HTMLDivElement | null>(null);
  let follow = $state(true);
  let settingsOpen = $state(false);
  let settingPending = $state(false);
  const textEncoder = new TextEncoder();
  const draftBytes = $derived(textEncoder.encode(draft).byteLength);
  const primaryOptions = $derived(chatState?.configOptions.filter(option => !option.category || option.category === "model" || option.category === "thought_level") ?? []);
  const otherOptions = $derived(chatState?.configOptions.filter(option => option.category && option.category !== "model" && option.category !== "thought_level") ?? []);
  const optionsDisabled = $derived(!connected || !chatState?.connected || chatState.configuring || settingPending || disabled);
  let resyncPending = false;
  const rows = $derived(chatRows(chatState?.messages ?? []));
  const canSend = $derived(connected && chatState?.connected && !chatState.busy && !chatState.configuring && !settingPending && !pending && !disabled && draft.trim().length > 0 && draftBytes <= 65536);

  $effect(() => {
    const path = websocketPath;
    const initialStatus = status;
    return untrack(() => {
      const session = makeChatSession({
        path, initialStatus,
        onState: (next) => {
          const exited = !next.connected && chatState?.connected !== false;
          chatState = next;
          settingPending = false;
          if (exited) onExit?.(-1);
          if (resyncPending) {
            resyncPending = false;
            if (pending && !next.messages.some((message) => message.submissionId === pending?.id)) {
              session.send({ type: "prompt", ...pending });
            }
          }
          if (pending && next.messages.some((message) => message.submissionId === pending?.id)) {
            if (draft === pending.text) draft = "";
            pending = null;
          }
        },
        onError: (message) => { error = message; pending = null; settingPending = false; },
        onConnection: (value) => { connected = value; if (value) resyncPending = true; onConnectionChange?.(value); },
      });
      connection = session;
      const execution = runtime.runCommand(Effect.scoped(session.program), {
        operation: "connect workspace chat",
        safeContext: {},
        onFailure: () => { error = "Could not connect to the agent."; },
      });
      return execution.interrupt;
    });
  });
  $effect(() => {
    if (!chatState) return;
    if (scroll && active && follow) scroll.scrollTop = scroll.scrollHeight;
  });
  function send() {
    if (!canSend) return;
    // getRandomValues is available on plain HTTP origins as well as HTTPS.
    const id = Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("");
    const submission = { id, text: draft };
    error = "";
    if (connection?.send({type: "prompt", ...submission})) { pending = submission; follow = true; }
  }
  function configure(id: string, value: string) {
    error = "";
    settingPending = connection?.send({ type: "config", id, value }) ?? false;
  }
  function keydown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing && !window.matchMedia("(pointer: coarse)").matches) {
      event.preventDefault(); send();
    }
  }
</script>

<section class="acp-workspace" aria-label={`${label} chat`}>
  <div class="chat-status" role="status">
    {#if !connected && status === "running"}<Spinner size={14} />Connecting to {label}…
    {:else if !chatState?.connected}Agent disconnected
    {:else if chatState.busy}<Spinner size={14} />{label} is working
    {:else}{label}{/if}
  </div>
  <div class="conversation" bind:this={scroll} onscroll={() => { if (scroll) follow = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 80; }}>
    <div class="messages" role="log" aria-label="Conversation" aria-live="polite" aria-busy={chatState?.busy ?? false}>
      {#if rows.length === 0}<p class="empty">Send a message to start working in this workspace.</p>{/if}
      {#each rows as row (row.id)}
        {#if row.kind === "tools"}<ChatToolGroup messages={row.messages} />
        {:else}<ChatMessageView message={row.message} agent={label} streaming={!!chatState?.busy && row.id === (chatState?.messages.length ?? 0) - 1} />{/if}
      {/each}
    </div>
  </div>
  {#if !follow}<button class="latest" type="button" onclick={() => { follow = true; }}>Latest messages</button>{/if}
  {#if chatState?.permissions.length}
    <div class="permissions" aria-label="Agent permissions">
      {#each chatState.permissions as permission (permission.id)}
        <div class="permission">
          <strong>{permission.title || "Agent requests permission"}</strong>
          <div class="permission-actions">
            {#each permission.options as option (option.optionId)}
              <Button disabled={!connected || disabled} onclick={() => connection?.send({type: "permission", id: permission.id, optionId: option.optionId})}>{option.name}</Button>
            {/each}
          </div>
        </div>
      {/each}
    </div>
  {/if}
  {#if chatState?.historyTruncated}<p class="history-notice" role="status">Earlier chat messages were removed to keep this session responsive.</p>{/if}
  {#if error || chatState?.error}<p class="error" role="alert">{error || chatState?.error}</p>{/if}
  {#if draftBytes > 65536}<p class="error" role="alert">Message must not exceed 65,536 bytes.</p>{/if}
  <div class="dock">
    <form class="composer" onsubmit={(event) => { event.preventDefault(); send(); }}>
      {#if settingsOpen}
        <div class="settings-sheet">
          <strong>Agent settings</strong>
          <p>Remembered for this client on this host across workspaces.</p>
          <ChatSessionOptions options={otherOptions} disabled={optionsDisabled || !!chatState?.busy} onchange={configure} />
        </div>
      {/if}
      <textarea aria-label="Message agent" placeholder={`Ask ${label}…`} bind:value={draft} onkeydown={keydown} rows="2" disabled={disabled || !chatState?.connected}></textarea>
      <div class="toolbar">
        <div class="chips">
          {#each primaryOptions as option (option.id)}
            <ChatOptionMenu {option} disabled={optionsDisabled} onchange={(value) => configure(option.id, value)} />
          {/each}
          {#if otherOptions.length}
            <button type="button" class="tb-chip" aria-label="Agent settings" aria-expanded={settingsOpen} title="Agent settings" onclick={() => settingsOpen = !settingsOpen}><Settings size={14} /></button>
          {/if}
        </div>
        <div class="send-cluster">
          {#if chatState?.busy}<button type="button" class="round stop" aria-label="Stop reply" title="Stop reply" disabled={!connected || disabled} onclick={() => connection?.send({type: "cancel"})}><Square size={13} fill="currentColor" strokeWidth={0} /></button>{/if}
          <button type="submit" class="round send" aria-label="Send" title="Send (Enter) · new line (Shift+Enter)" disabled={!canSend}>{#if pending}<Spinner size={14} />{:else}<ArrowUp size={16} />{/if}</button>
        </div>
      </div>
    </form>
  </div>
</section>

<style>
  .acp-workspace { display: flex; flex-direction: column; min-width: 0; min-height: 0; height: 100%; background: var(--bg-primary); color: var(--text-primary); }
  .chat-status { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-4) var(--space-6); font-size: var(--font-size-sm); color: var(--text-secondary); border-bottom: 1px solid var(--border-muted); }
  .conversation { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; }
  .messages { display: flex; flex-direction: column; gap: var(--space-6); max-width: 52rem; margin: 0 auto; padding: var(--space-6); overflow-wrap: anywhere; }
  .empty { color: var(--text-secondary); }
  .permissions { flex-shrink: 0; max-height: 35%; overflow: auto; padding: var(--space-4) var(--space-6); border-top: 1px solid var(--border-default); background: var(--bg-surface); }
  .permission + .permission { margin-top: var(--space-4); }
  .permission-actions { display: flex; flex-wrap: wrap; gap: var(--space-3); margin-top: var(--space-3); }
  .permission strong { overflow-wrap: anywhere; }
  .error { color: var(--accent-red); margin: 0; padding: var(--space-4) var(--space-6); overflow-wrap: anywhere; }
  .history-notice { color: var(--text-secondary); margin: 0; padding: var(--space-4) var(--space-6); overflow-wrap: anywhere; }
  .latest { align-self: center; padding: var(--space-3) var(--space-5); color: var(--text-primary); border: 1px solid var(--border-default); background: var(--bg-surface); border-radius: var(--radius-md); font: inherit; }
  @media (pointer: coarse) {
    .messages, .permissions { padding-inline: var(--space-4); }
  }
  .dock {
    position: relative;
    flex: none;
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    padding: var(--space-2) var(--space-5) var(--space-5);
  }
  .composer {
    container-type: inline-size;
    display: flex;
    flex-direction: column;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    transition: border-color var(--transition-fast) ease-out;
  }
  .composer:hover {
    border-color: color-mix(
      in srgb,
      var(--text-muted) 60%,
      var(--border-default)
    );
  }
  .composer:focus-within {
    border-color: var(--accent-blue);
  }
  textarea {
    box-sizing: border-box;
    width: 100%;
    min-height: 56px;
    max-height: 40vh;
    padding: var(--space-5) var(--space-5) var(--space-3);
    border: 0;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    line-height: 1.5;
    resize: none;
    field-sizing: content;
  }
  textarea::placeholder {
    color: var(--text-secondary);
  }
  textarea:focus-visible {
    outline: none;
  }
  textarea:disabled {
    cursor: not-allowed;
  }
  .toolbar {
    display: flex;
    align-items: flex-end;
    gap: var(--space-3);
    padding: var(--space-3) var(--space-3) var(--space-3) var(--space-4);
  }
  /* Effort and app tools keep their width. The agent and model menus start
     from zero and grow only to their own content, so their names truncate
     before any control leaves the row; the row wraps only when even their
     minimum widths cannot fit. Full names are in the tooltips. */
  .chips {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1);
  }
  .chips > :global(.kit-menu) {
    display: flex;
    flex: 1 1 0;
    min-width: 0;
    max-width: max-content;
  }
  .chips > :global(.kit-menu.opt--fixed) {
    flex: none;
  }
  /* Shrinking stops before the agent mark and its caret, or the model's
     first letters, are cut. The menu wrapper holds the floor so the chip
     inside cannot spill into its neighbour. */
  .chips > :global(.kit-menu:first-child) {
    min-width: 44px;
  }
  .chips > :global(.kit-menu.opt--model) {
    min-width: 40px;
  }
  .chips > :global(.kit-menu) > :global(.tb-chip) {
    flex: 1 1 auto;
  }

  /* Toolbar chips: quiet ghost controls that gain a surface on hover. */
  .acp-workspace :global(.tb-chip) {
    flex: 0 1 auto;
    min-width: 0;
    overflow: hidden;
    display: inline-flex;
    align-items: center;
    justify-content: flex-start;
    gap: var(--space-2);
    height: 26px;
    min-height: 26px;
    padding: 0 var(--space-3);
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
    cursor: pointer;
    transition:
      background var(--transition-fast) ease-out,
      border-color var(--transition-fast) ease-out,
      color var(--transition-fast) ease-out;
  }
  .acp-workspace :global(.tb-chip:hover:not(:disabled)) {
    background: var(--bg-surface-hover);
    border-color: transparent;
    color: var(--text-primary);
  }
  .acp-workspace :global(.tb-chip[aria-expanded="true"]) {
    background: var(--bg-inset);
    border-color: var(--border-default);
    color: var(--text-primary);
  }
  .acp-workspace :global(.tb-chip:focus-visible) {
    outline: var(--focus-ring);
    outline-offset: 1px;
  }
  .acp-workspace :global(.tb-chip.tb-chip--tools) {
    flex: none;
  }
  .acp-workspace :global(.tb-chip--agent) {
    color: var(--text-primary);
    font-weight: var(--font-weight-medium);
  }
  .acp-workspace :global(.tb-chip--large) {
    height: 28px;
    padding: 0 var(--space-4);
    border-color: var(--border-default);
    background: var(--bg-surface);
  }
  .acp-workspace :global(.tb-chip__label) {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .acp-workspace :global(.tb-chip--agent .tb-chip__label) {
    max-width: 12ch;
  }

  /* In a narrow composer the effort bars and the agent mark carry their
     meaning on their own; names stay in the tooltips and accessible labels. */
  @container (max-width: 320px) {
    .acp-workspace :global(.opt--effort .tb-chip__label) {
      display: none;
    }
  }
  @container (max-width: 260px) {
    .acp-workspace :global(.tb-chip--agent:not(.tb-chip--large) .tb-chip__label),
    .acp-workspace :global(.tb-chip--tools .tb-chip__label) {
      display: none;
    }
  }
  .acp-workspace :global(.tb-chip--large .tb-chip__label) {
    max-width: none;
  }
  .acp-workspace :global(.opt--model .tb-chip__label) {
    max-width: 16ch;
  }
  .acp-workspace :global(.tb-chip__caret) {
    flex: none;
    color: var(--text-secondary);
  }

  .settings-sheet { padding: var(--space-5); border-bottom: 1px solid var(--border-muted); }
  .settings-sheet p { margin: var(--space-2) 0 var(--space-4); font-size: var(--font-size-xs); color: var(--text-secondary); }
  .send-cluster {
    flex: none;
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  /* The one round control in the panel: send and stop read as the verbs. */
  .round {
    display: inline-grid;
    place-items: center;
    width: 30px;
    height: 30px;
    padding: 0;
    border-radius: 50%;
    font: inherit;
    cursor: pointer;
    transition:
      background var(--transition-fast) ease-out,
      color var(--transition-fast) ease-out;
  }
  .send {
    border: 1px solid var(--accent-blue);
    background: var(--accent-blue);
    color: var(--bg-surface);
  }
  .send:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-blue) 86%, var(--text-primary));
  }
  .send:disabled {
    border-color: var(--border-default);
    background: var(--bg-inset);
    color: var(--text-secondary);
  }
  .stop {
    border: 1px solid transparent;
    background: transparent;
    color: var(--text-primary);
  }
  .stop:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-red) 10%, var(--bg-surface));
    color: color-mix(in srgb, var(--accent-red) 80%, var(--text-primary));
  }
  .round:focus-visible {
    outline: var(--focus-ring);
    outline-offset: 2px;
  }

  @media (pointer: coarse) {
    .acp-workspace :global(.tb-chip), .round, .permission-actions :global(button) { min-width: var(--mobile-chrome-hit-target); min-height: var(--mobile-chrome-hit-target); }
    .messages { gap: var(--space-4); padding-block: var(--space-4); font-size: var(--font-size-phone-prose); }
    .messages :global(.markdown) { font-size: inherit; }
    .permissions { font-size: var(--font-size-sm); }
    textarea { min-height: var(--mobile-chrome-hit-target); padding-block: var(--space-3); font-size: var(--font-size-touch-field); }
    .toolbar { padding-block: var(--space-2); }
    .dock { padding: var(--space-2) var(--space-4) max(var(--space-4), env(safe-area-inset-bottom)); }
  }
</style>
