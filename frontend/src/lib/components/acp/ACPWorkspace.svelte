<script lang="ts">
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import { Button, Spinner } from "@kenn-io/kit-ui";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import ChatMessageView from "./ChatMessageView.svelte";
  import ChatToolGroup from "./ChatToolGroup.svelte";
  import { chatRows } from "./chat-timeline.js";
  import { makeChatSession } from "./chat-session.js";
  import type { ChatState } from "./chat-types.js";

  let { websocketPath, label = "Agent", status = "running", active = true, disabled = false, onConnectionChange }: {
    websocketPath: string; label?: string; status?: string; active?: boolean; disabled?: boolean;
    onConnectionChange?: (connected: boolean) => void;
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
  let resyncPending = false;
  const rows = $derived(chatRows(chatState?.messages ?? []));
  const canSend = $derived(connected && chatState?.connected && !chatState.busy && !pending && !disabled && draft.trim().length > 0);

  $effect(() => {
    const path = websocketPath;
    const initialStatus = status;
    return untrack(() => {
      const session = makeChatSession({
        path, initialStatus,
        onState: (next) => {
          chatState = next;
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
        onError: (message) => { error = message; pending = null; },
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
    const submission = { id: crypto.randomUUID(), text: draft };
    error = "";
    if (connection?.send({type: "prompt", ...submission})) { pending = submission; follow = true; }
  }
  function keydown(event: KeyboardEvent) {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey) && !event.isComposing) {
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
  {#if error || chatState?.error}<p class="error" role="alert">{error || chatState?.error}</p>{/if}
  <form class="composer" onsubmit={(event) => { event.preventDefault(); send(); }}>
    <textarea aria-label="Message agent" placeholder="Message the agent…" bind:value={draft} onkeydown={keydown} rows="2" disabled={disabled} maxlength={65536}></textarea>
    <div class="composer-actions">
      <span class="hint">Ctrl / ⌘ Enter to send</span>
      {#if chatState?.busy}<Button disabled={!connected || disabled} onclick={() => connection?.send({type: "cancel"})}>Stop reply</Button>{/if}
      <Button type="submit" tone="info" surface="solid" disabled={!canSend}>{pending ? "Sending…" : "Send"}</Button>
    </div>
  </form>
</section>

<style>
  .acp-workspace { display: flex; flex-direction: column; min-width: 0; min-height: 0; height: 100%; background: var(--bg-primary); color: var(--text-primary); }
  .chat-status { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-4) var(--space-6); font-size: var(--font-size-sm); color: var(--text-secondary); border-bottom: 1px solid var(--border-muted); }
  .conversation { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; }
  .messages { display: flex; flex-direction: column; gap: var(--space-6); max-width: 52rem; margin: 0 auto; padding: var(--space-6); overflow-wrap: anywhere; }
  .empty { color: var(--text-secondary); }
  .composer { padding: var(--space-4) var(--space-6) max(var(--space-4), env(safe-area-inset-bottom)); border-top: 1px solid var(--border-default); }
  textarea { box-sizing: border-box; width: 100%; min-height: 4rem; max-height: 12rem; field-sizing: content; resize: vertical; padding: var(--space-4); border: 1px solid var(--border-default); border-radius: var(--radius-md); background: var(--bg-inset); color: var(--text-primary); font: inherit; }
  textarea:focus-visible, .latest:focus-visible { outline: var(--focus-ring); outline-offset: 2px; }
  .composer-actions, .permission-actions { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: var(--space-4); margin-top: var(--space-4); }
  .hint { margin-right: auto; font-size: var(--font-size-xs); color: var(--text-secondary); }
  .permissions { flex-shrink: 0; max-height: 35%; overflow: auto; padding: var(--space-4) var(--space-6); border-top: 1px solid var(--border-default); background: var(--bg-surface); }
  .permission + .permission { margin-top: var(--space-4); }
  .permission strong { overflow-wrap: anywhere; }
  .error { color: var(--accent-red); margin: 0; padding: var(--space-4) var(--space-6); overflow-wrap: anywhere; }
  .latest { align-self: center; padding: var(--space-3) var(--space-5); color: var(--text-primary); border: 1px solid var(--border-default); background: var(--bg-surface); border-radius: var(--radius-md); font: inherit; }
  @media (pointer: coarse) {
    textarea { font-size: max(16px, var(--font-size-md)); resize: none; }
    .hint { display: none; }
    .acp-workspace .composer-actions :global(button), .acp-workspace .permission-actions :global(button), .latest { min-height: 44px; }
    .messages, .composer, .permissions { padding-inline: var(--space-4); }
  }
</style>
