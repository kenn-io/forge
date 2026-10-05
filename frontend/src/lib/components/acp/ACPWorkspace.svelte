<script lang="ts">
  import { Data, Effect } from "effect";
  import { onDestroy, tick, untrack } from "svelte";
  import ArrowUp from "@lucide/svelte/icons/arrow-up";
  import Square from "@lucide/svelte/icons/square";
  import Settings from "@lucide/svelte/icons/settings-2";
  import CornerDownLeft from "@lucide/svelte/icons/corner-down-left";
  import X from "@lucide/svelte/icons/x";
  import ListPlus from "@lucide/svelte/icons/list-plus";
  import ChatOptionMenu from "./ChatOptionMenu.svelte";
  import ChatSessionOptions from "./ChatSessionOptions.svelte";
  import { Button, Card, Spinner, Tooltip } from "@kenn-io/kit-ui";
  import { AlertIcon } from "../../icons.js";
  import { kbdGlyph } from "../keyboard/useKbdLabel.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import ChatMessageView from "./ChatMessageView.svelte";
  import ChatToolGroup from "./ChatToolGroup.svelte";
  import ChatElicitation from "./ChatElicitation.svelte";
  import ChatCommandMenu from "./ChatCommandMenu.svelte";
  import ChatDockRail from "./ChatDockRail.svelte";
  import { insertCommand, matchCommands, pendingInputHint, slashToken } from "./chat-commands.js";
  import { chatRows } from "./chat-timeline.js";
  import { runningSubagents, subagentChildCounts } from "./chat-subagents.js";
  import { makeChatSession } from "./chat-session.js";
  import { mediaDataURL } from "./chat-content.js";
  import type { AgentCommand, ChatContent, ChatState, PromptMode } from "./chat-types.js";

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
  let images = $state.raw<{ id: number; name: string; content: ChatContent | null }[]>([]);
  let imageSequence = 0;
  const imageReads = new Map<number, () => void>();
  let pendingImageIDs = new Set<number>();
  let pending = $state<{id: string; text: string; mode: PromptMode; images?: readonly ChatContent[]} | null>(null);
  class ImagePasteError extends Data.TaggedError("ImagePasteError")<{ name: string }> {}
  onDestroy(() => { for (const interrupt of imageReads.values()) interrupt(); });
  let scroll = $state<HTMLDivElement | null>(null);
  let follow = $state(true);
  let settingsOpen = $state(false);
  let settingPending = $state(false);
  let textarea = $state<HTMLTextAreaElement | null>(null);
  let caret = $state(0);
  let commandMenuDismissed = $state(false);
  let commandHighlight = $state(0);
  const uid = $props.id();
  const commandMenuId = `${uid}-commands`;
  const elicitations = $derived(chatState?.elicitations ?? []);
  const notices = $derived(chatState?.notices ?? []);
  const commands = $derived(chatState?.commands ?? []);
  const slash = $derived(slashToken(draft, caret));
  const commandMatches = $derived(slash && !commandMenuDismissed && !disabled && chatState?.connected ? matchCommands(commands, slash.query) : []);
  const activeCommand = $derived(Math.min(commandHighlight, commandMatches.length - 1));
  const inputHint = $derived(pendingInputHint(commands, draft));
  const primaryOptions = $derived(chatState?.configOptions.filter(option => !option.category || option.category === "model" || option.category === "thought_level") ?? []);
  const otherOptions = $derived(chatState?.configOptions.filter(option => option.category && option.category !== "model" && option.category !== "thought_level") ?? []);
  const optionsDisabled = $derived(!connected || !chatState?.connected || chatState.configuring || settingPending || disabled);
  let resyncPending = false;
  let historyLoading = $state(false);
  // Transcript index of the first loaded message; earlier pages load on demand.
  const firstLoaded = $derived(chatState?.messageOffset ?? 0);
  const lastIndex = $derived(firstLoaded + (chatState?.messages.length ?? 0) - 1);
  const rows = $derived(chatRows(chatState?.messages ?? [], firstLoaded));
  // A running turn never blocks the composer: prompts sent while busy steer
  // the turn or queue behind it on the host.
  const canSend = $derived(connected && chatState?.connected && !pending && !disabled && (draft.trim().length > 0 || images.length > 0) && images.every(image => image.content));
  const running = $derived(!!chatState?.busy || !!chatState?.steering);
  const stopping = $derived(!!chatState?.stopping);
  const steeringSupported = $derived(!!chatState?.steeringSupported);
  const busyMode = $derived<PromptMode>(steeringSupported ? "steer" : "queue");
  const queue = $derived(chatState?.queue ?? []);
  const queuePaused = $derived(!!chatState?.queuePaused);
  const childCounts = $derived(subagentChildCounts(chatState?.messages ?? []));
  const subagents = $derived(runningSubagents(chatState?.messages ?? [], childCounts));
  const composerPlaceholder = $derived(!running ? `Ask ${label}…` : steeringSupported ? "Steer the reply, or queue a follow-up…" : "Queue a follow-up…");
  const queueShortcutLabel = kbdGlyph({ key: "Enter", alt: true });

  function settlePending() {
    if (pending && draft === pending.text) draft = "";
    images = images.filter(image => !pendingImageIDs.has(image.id));
    pendingImageIDs.clear();
    pending = null;
  }
  $effect(() => {
    const path = websocketPath;
    const initialStatus = status;
    return untrack(() => {
      const session = makeChatSession({
        path, initialStatus,
        onState: (next) => {
          if (scroll && chatState && (next.messageOffset ?? 0) < firstLoaded) keepReadingPosition(scroll);
          const exited = !next.connected && chatState?.connected !== false;
          chatState = next;
          settingPending = false;
          if (exited) onExit?.(-1);
          const accepted = (id: string) =>
            next.messages.some((message) => message.submissionId === id) || !!next.queue?.some((queued) => queued.id === id);
          if (resyncPending) {
            resyncPending = false;
            if (pending && !accepted(pending.id)) {
              session.send({ type: "prompt", ...pending });
            }
          }
          if (pending && accepted(pending.id)) settlePending();
        },
        // Covers a retried prompt the host already had, which may sit outside
        // the transcript window this chat can see.
        onAccepted: ({ command, id }) => {
          if (command === "prompt" && pending?.id === id) settlePending();
        },
        onError: (message, failed) => {
          error = message;
          // Only the failed request settles; an unrelated error must not make
          // an accepted prompt look unsent.
          if (failed?.command === "prompt" && failed.id === pending?.id) pending = null;
          if (failed?.command === "config") settingPending = false;
        },
        onConnection: (value) => { connected = value; if (value) resyncPending = true; onConnectionChange?.(value); },
        onHistoryLoading: (value) => { historyLoading = value; },
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
  // Older messages are inserted above what the reader is looking at. Pin the
  // first row still in view to its on-screen position once they render (and
  // for the next frames, while inserted rows settle). This is explicit because
  // Safari has no scroll anchoring.
  function keepReadingPosition(element: HTMLDivElement) {
    const top = element.getBoundingClientRect().top;
    const anchor = [...element.querySelectorAll<HTMLElement>(".messages > :not(.earlier)")].find(
      (row) => row.getBoundingClientRect().bottom > top,
    );
    if (!anchor) return;
    const offset = anchor.getBoundingClientRect().top;
    const restore = () => {
      if (anchor.isConnected) element.scrollTop += anchor.getBoundingClientRect().top - offset;
    };
    void tick().then(() => {
      restore();
      requestAnimationFrame(() => {
        restore();
        requestAnimationFrame(restore);
      });
    });
  }
  function loadEarlier() {
    if (firstLoaded > 0) connection?.loadEarlier(100);
  }
  function onConversationScroll() {
    if (!scroll) return;
    follow = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 80;
    if (scroll.scrollTop < 200) loadEarlier();
  }
  function send(mode: PromptMode) {
    if (!canSend) return;
    // getRandomValues is available on plain HTTP origins as well as HTTPS.
    const id = Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("");
    const content = images.flatMap(image => image.content ? [image.content] : []);
    const submission = { id, text: draft, mode, ...(content.length ? { images: content } : {}) };
    error = "";
    if (connection?.send({type: "prompt", ...submission})) { pending = submission; pendingImageIDs = new Set(images.map(image => image.id)); follow = true; }
  }
  function removeImage(id: number) {
    imageReads.get(id)?.();
    imageReads.delete(id);
    images = images.filter(image => image.id !== id);
  }
  function paste(event: ClipboardEvent) {
    const files = Array.from(event.clipboardData?.files ?? []).filter(file => file.type.startsWith("image/"));
    if (!files.length) return;
    event.preventDefault();
    event.stopPropagation();
    error = "";
    for (const file of files) {
      const id = ++imageSequence;
      const name = file.name || `Pasted image ${id}`;
      images = [...images, { id, name, content: null }];
      const read = Effect.callback<ChatContent, ImagePasteError>(resume => {
        const reader = new FileReader();
        reader.onload = () => {
          const dataURL = reader.result as string;
          resume(Effect.succeed({ type: "image", mimeType: file.type, data: dataURL.slice(dataURL.indexOf(",") + 1), name }));
        };
        reader.onerror = () => resume(Effect.fail(new ImagePasteError({ name })));
        reader.readAsDataURL(file);
        return Effect.sync(() => { reader.onload = null; reader.onerror = null; if (reader.readyState === FileReader.LOADING) reader.abort(); });
      });
      const execution = runtime.runCommand(read.pipe(Effect.tap(content => Effect.sync(() => { imageReads.delete(id); images = images.map(image => image.id === id ? { ...image, content } : image); }))), {
        operation: "read pasted image",
        safeContext: {},
        onFailure: () => { imageReads.delete(id); images = images.filter(image => image.id !== id); error = `Could not read ${name}. Try pasting it again.`; },
      });
      imageReads.set(id, execution.interrupt);
    }
  }
  function configure(id: string, value: string) {
    error = "";
    settingPending = connection?.send({ type: "config", id, value }) ?? false;
  }
  function syncCaret() {
    caret = textarea?.selectionStart ?? draft.length;
  }
  async function pickCommand(command: AgentCommand) {
    if (!slash) return;
    const next = insertCommand(draft, slash, command.name);
    draft = next.text;
    caret = next.caret;
    await tick();
    textarea?.focus();
    textarea?.setSelectionRange(next.caret, next.caret);
  }
  async function highlightCommand(index: number) {
    commandHighlight = index;
    await tick();
    document.getElementById(`${commandMenuId}-${index}`)?.scrollIntoView({ block: "nearest" });
  }
  function commandKeydown(event: KeyboardEvent): boolean {
    const count = commandMatches.length;
    const command = commandMatches[activeCommand];
    if (!command || event.isComposing) return false;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      void highlightCommand((activeCommand + (event.key === "ArrowDown" ? 1 : count - 1)) % count);
    } else if ((event.key === "Enter" || event.key === "Tab") && !event.shiftKey) {
      void pickCommand(command);
    } else if (event.key === "Escape") {
      event.stopPropagation();
      commandMenuDismissed = true;
    } else {
      return false;
    }
    event.preventDefault();
    return true;
  }
  // A click on empty space in the pane (gaps between messages, margins beside
  // short lines, padding) sends focus to the composer. Clicks on text, media,
  // or controls keep their normal behavior, a drag-selection stays intact for
  // copying, and touch input skips it so a tap does not raise the on-screen
  // keyboard. The click's own pointer type decides, since touchscreen laptops
  // report a fine primary pointer; the media query is the fallback for
  // browsers whose clicks carry no pointer type.
  const interactiveTarget = "a[href], audio[controls], video[controls], button, input, select, textarea, summary, label, iframe, [contenteditable]:not([contenteditable='false']), [tabindex], [role='button'], [role='link'], [role='menuitem'], [role='option'], [role='radio'], [role='checkbox'], [role='tab']";
  const mediaTarget = "img, svg, canvas, audio, video";
  // The click target is the deepest element under the pointer, so text in a
  // nested element would have made that element the target: only the target's
  // own text nodes can sit under the pointer.
  function overText(target: Element, x: number, y: number) {
    const range = document.createRange();
    return [...target.childNodes].some((node) => {
      if (node.nodeType !== Node.TEXT_NODE || !node.textContent?.trim()) return false;
      range.selectNodeContents(node);
      return [...range.getClientRects()].some((rect) => x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom);
    });
  }
  function focusComposerFromClick(event: MouseEvent & { currentTarget: HTMLElement }) {
    if (event.defaultPrevented || event.button !== 0 || !textarea || textarea.disabled) return;
    if (!(event.target instanceof Element)) return;
    const control = event.target.closest(interactiveTarget);
    if (control && event.currentTarget.contains(control)) return;
    if (event.target.closest(mediaTarget) || overText(event.target, event.clientX, event.clientY)) return;
    if (window.getSelection()?.isCollapsed === false) return;
    const touch = event instanceof PointerEvent && event.pointerType ? event.pointerType === "touch" : window.matchMedia("(pointer: coarse)").matches;
    if (touch) return;
    textarea.focus({ preventScroll: true });
  }
  function keydown(event: KeyboardEvent) {
    if (commandKeydown(event)) return;
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing && !window.matchMedia("(pointer: coarse)").matches) {
      event.preventDefault();
      send(!running ? "send" : event.altKey ? "queue" : busyMode);
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<section class="acp-workspace" aria-label={`${label} chat`} onclick={focusComposerFromClick}>
  <div class="chat-status">
    <div class="chat-status__inner">
      <span class="chat-status__text" role="status">
        {#if !connected && status === "running"}<Spinner size={14} />Connecting agent…
        {:else if !chatState?.connected}Agent disconnected
        {:else if stopping}Stopping…
        {:else if chatState.permissions.length || elicitations.length}Needs your answer
        {:else if running}<Spinner size={14} />{label} is replying…
        {:else}{label}{/if}
      </span>
      <!-- A degraded start stays quiet in the header: the chat works, so it is
           not an error and never takes space from the conversation. -->
      {#if notices.length}
        <span class="chat-notice">
          <Tooltip align="end" focusable openDelayMs={0}>
            <AlertIcon size={14} role="img" aria-label={notices.join(" ")} />
            {#snippet content()}{#each notices as notice, index (index)}<p class="chat-notice__text">{notice}</p>{/each}{/snippet}
          </Tooltip>
        </span>
      {/if}
    </div>
  </div>
  <div class="conversation" bind:this={scroll} onscroll={onConversationScroll}>
    <!-- The log is not live: streamed blocks would be read piecemeal. The
         region below announces the finished reply once the turn ends. -->
    <div class="messages" role="log" aria-label="Conversation" aria-live="off" aria-busy={chatState?.busy ?? false}>
      {#if firstLoaded > 0}
        <div class="earlier">
          <Button size="sm" surface="soft" disabled={historyLoading || !connected} onclick={loadEarlier}>
            {#if historyLoading}<Spinner size={12} label="" />Loading earlier messages…{:else}Load earlier messages{/if}
          </Button>
        </div>
      {/if}
      {#if rows.length === 0}<p class="empty">Send a message to start working in this workspace.</p>{/if}
      {#each rows as row (row.id)}
        {#if row.kind === "tools"}<ChatToolGroup messages={row.messages} {childCounts} streaming={!!chatState?.busy && row.messages.at(-1) === chatState.messages.at(-1)} />
        {:else}<ChatMessageView message={row.message} streaming={!!chatState?.busy && row.id === lastIndex} />{/if}
      {/each}
      <!-- Questions read as part of the conversation, in the reply column,
           right after the message that asked them. -->
      {#each elicitations as elicitation (elicitation.id)}
        <Card level="default" padding="md" class="request">
          <ChatElicitation {elicitation} disabled={!connected || disabled} onrespond={(response) => connection?.send({ type: "elicitation", id: elicitation.id, ...response })} />
        </Card>
      {/each}
      {#each chatState?.permissions ?? [] as permission (permission.id)}
        <Card level="default" padding="md" class="request">
          <p class="permission-title">{permission.title || "Agent requests permission"}</p>
          <div class="permission-actions">
            {#each permission.options as option (option.optionId)}
              <Button size="sm" disabled={!connected || disabled} onclick={() => connection?.send({type: "permission", id: permission.id, optionId: option.optionId})}>{option.name}</Button>
            {/each}
          </div>
        </Card>
      {/each}
    </div>
  </div>
  <div class="kit-sr-only" aria-live="polite" aria-atomic="true">{#if chatState && !chatState.busy}{chatState.messages.at(-1)?.role === "assistant" ? chatState.messages.at(-1)?.text : ""}{/if}</div>
  {#if !follow}<button class="latest" type="button" onclick={() => { follow = true; }}>Latest messages</button>{/if}
  {#if chatState?.error}
    <div class="error" role="alert">
      <p>{chatState.error}{#if chatState.errorCode != null}{" "}<span class="error-code">ACP {chatState.errorCode}</span>{/if}</p>
      {#if chatState.errorData}<pre>{chatState.errorData}</pre>{/if}
    </div>
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="dock">
    {#if chatState?.plan?.length || subagents.length || queue.length}
      <ChatDockRail
        plan={chatState?.plan ?? []}
        {subagents}
        {queue}
        {queuePaused}
        disabled={!connected || disabled}
        suppressed={commandMatches.length > 0}
        onresume={() => connection?.send({ type: "resume" })}
        onunqueue={(id) => connection?.send({ type: "unqueue", id })}
      />
    {/if}
    <form class="composer" onsubmit={(event) => { event.preventDefault(); send(running ? busyMode : "send"); }}>
      {#if commandMatches.length}
        <ChatCommandMenu id={commandMenuId} commands={commandMatches} active={activeCommand} onpick={pickCommand} />
      {/if}
      {#if settingsOpen}
        <div class="settings-sheet">
          <strong>Agent settings</strong>
          <p>Remembered for this client on this host across workspaces.</p>
          <ChatSessionOptions options={otherOptions} disabled={optionsDisabled || !!chatState?.busy} onchange={configure} />
        </div>
      {/if}
      {#if images.length}
        <div class="attachments" aria-label="Attached images">
          {#each images as image (image.id)}
            <div class="attachment">
              {#if image.content}<img src={mediaDataURL(image.content, "image")} alt={image.name} />{:else}<Spinner size={18} />{/if}
              <button type="button" aria-label={`Remove ${image.name}`} title={`Remove ${image.name}`} onclick={() => removeImage(image.id)}><X size={14} aria-hidden="true" /></button>
            </div>
          {/each}
        </div>
      {/if}
      <div class="input">
        <textarea
          bind:this={textarea}
          aria-label="Message agent"
          aria-autocomplete="list"
          aria-controls={commandMatches.length ? commandMenuId : undefined}
          aria-activedescendant={commandMatches.length ? `${commandMenuId}-${activeCommand}` : undefined}
          aria-describedby={inputHint ? `${uid}-input-hint` : undefined}
          placeholder={composerPlaceholder}
          bind:value={draft}
          onpaste={paste}
          onkeydown={keydown}
          oninput={() => { commandMenuDismissed = false; commandHighlight = 0; syncCaret(); }}
          onkeyup={syncCaret}
          onclick={syncCaret}
          rows="2"
          disabled={disabled || !chatState?.connected}
        ></textarea>
        <!-- Placeholder-style hint after an inserted command, which a native
             placeholder cannot show once the field holds text. -->
        {#if inputHint}<div class="input-hint" id={`${uid}-input-hint`}><span class="input-hint__typed" aria-hidden="true">{draft}</span>{inputHint}</div>{/if}
      </div>
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
          {#if running && steeringSupported}
            <button type="button" class="tb-chip" aria-keyshortcuts="Alt+Enter" title={`Queue for after this reply (${queueShortcutLabel})`} disabled={!canSend} onclick={() => send("queue")}><ListPlus size={14} aria-hidden="true" /><span>Queue</span></button>
          {/if}
          {#if running || stopping}<button type="button" class="round stop" aria-label="Stop reply" title={stopping ? "Stopping…" : "Stop reply"} disabled={stopping || !connected || disabled} onclick={() => connection?.send({type: "cancel"})}><Square size={13} fill="currentColor" strokeWidth={0} /></button>{/if}
          {#if !running}
            <button type="submit" class="round send" aria-label="Send" title="Send (Enter) · new line (Shift+Enter)" disabled={!canSend}>{#if pending}<Spinner size={14} />{:else}<ArrowUp size={16} />{/if}</button>
          {:else if steeringSupported}
            <button type="submit" class="round send" aria-label="Steer reply" title="Steer reply (Enter) · new line (Shift+Enter)" disabled={!canSend}>{#if pending}<Spinner size={14} />{:else}<CornerDownLeft size={16} />{/if}</button>
          {:else}
            <button type="submit" class="round send" aria-label="Queue message" title="Queue message (Enter) · new line (Shift+Enter)" disabled={!canSend}>{#if pending}<Spinner size={14} />{:else}<ListPlus size={16} />{/if}</button>
          {/if}
        </div>
      </div>
    </form>
  </div>
</section>

<style>
  /* One reading column for everything in the pane: the conversation, notices,
     requests, and the composer share its width and edges. */
  .acp-workspace { --acp-column: 64rem; --acp-gutter: 5.5rem; container: acp-pane / inline-size; display: flex; flex-direction: column; min-width: 0; min-height: 0; height: 100%; background: var(--bg-primary); color: var(--text-primary); }
  .chat-status { padding-block: var(--space-4); font-size: var(--font-size-sm); color: var(--text-secondary); border-bottom: 1px solid var(--border-muted); }
  .chat-status__inner, .chat-status__text { display: flex; align-items: center; gap: var(--space-3); }
  .chat-status__text { min-width: 0; }
  .chat-notice { display: flex; margin-left: auto; color: var(--accent-amber); }
  .chat-notice :global(.kit-tooltip-trigger) { align-items: center; justify-content: center; }
  .chat-notice__text { margin: 0; }
  .chat-notice__text + .chat-notice__text { margin-top: var(--space-3); }
  .conversation { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; container: acp-conversation / inline-size; }
  .messages { display: flex; flex-direction: column; gap: var(--space-6); max-width: var(--acp-column); margin: 0 auto; padding: var(--space-6); overflow-wrap: anywhere; }
  .empty { color: var(--text-secondary); }
  .dock, .error, .chat-status__inner { width: 100%; max-width: var(--acp-column); margin-inline: auto; padding-inline: var(--space-6); }
  .messages > :global(.request) { align-self: flex-start; width: 100%; max-width: 36rem; }
  .permission-title { margin: 0; color: var(--text-primary); overflow-wrap: anywhere; }
  .permission-actions { display: flex; flex-wrap: wrap; gap: var(--space-2); margin-top: var(--space-4); }
  /* Block axis only: the shared column rule above centers errors with
     margin-inline and owns their inline padding. */
  .error { color: var(--accent-red); margin-block: 0; padding-block: var(--space-4); overflow-wrap: anywhere; }
  .error p { margin: 0; }
  .error-code { font-family: var(--font-mono); font-size: var(--font-size-xs); color: var(--text-secondary); }
  .error pre { max-height: 10rem; overflow: auto; margin: var(--space-3) 0 0; color: var(--text-primary); font-size: var(--font-size-xs); white-space: pre-wrap; }
  .earlier { display: flex; justify-content: center; }
  .latest { align-self: center; padding: var(--space-3) var(--space-5); color: var(--text-primary); border: 1px solid var(--border-default); background: var(--bg-surface); border-radius: var(--radius-md); font: inherit; }
  @media (pointer: coarse) {
    .messages, .dock, .error, .chat-status__inner { padding-inline: var(--space-4); }
    .earlier :global(button) { min-height: var(--mobile-chrome-hit-target); }
    .chat-notice :global(.kit-tooltip-trigger) { min-width: 24px; min-height: 24px; }
  }
  /* Reserve the message time/copy gutter (ChatMessageView .gutter) only when
     the pane is wide enough; narrow panes go without it. Declared after the
     touch padding so a wide touch pane still reserves the gutter. */
  @container acp-conversation (min-width: 40rem) {
    .messages { padding-left: calc(var(--space-4) + var(--acp-gutter)); }
  }
  @container acp-pane (min-width: 40rem) {
    .dock, .error, .chat-status__inner { padding-left: calc(var(--space-4) + var(--acp-gutter)); }
  }
  .attachments { display: flex; flex-wrap: wrap; gap: var(--space-3); padding: var(--space-3); }
  .attachment { position: relative; display: flex; align-items: center; justify-content: center; width: 5rem; height: 5rem; border: 1px solid var(--border-default); border-radius: var(--radius-md); }
  .attachment img { width: 100%; height: 100%; object-fit: contain; border-radius: inherit; }
  .attachment button { position: absolute; top: 0; right: 0; display: flex; align-items: center; justify-content: center; width: 1.5rem; height: 1.5rem; padding: 0; border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-surface); color: var(--text-primary); cursor: pointer; }
  .attachment button:focus-visible { outline: 2px solid var(--accent-blue); outline-offset: 2px; }
  @media (pointer: coarse) { .attachment button { width: var(--mobile-chrome-hit-target); height: var(--mobile-chrome-hit-target); } }
  .dock {
    position: relative;
    flex: none;
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    padding-block: var(--space-2) var(--space-5);
  }
  .composer {
    position: relative;
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
  .input { position: relative; }
  .input-hint {
    position: absolute;
    inset: 0;
    overflow: hidden;
    padding: var(--space-5) var(--space-5) var(--space-3);
    color: var(--text-secondary);
    line-height: 1.5;
    white-space: pre-wrap;
    overflow-wrap: break-word;
    pointer-events: none;
  }
  .input-hint__typed { visibility: hidden; }
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
    /* Enter inserts a newline on touch keyboards, so the key hints do not apply. */
    .messages { gap: var(--space-4); padding-block: var(--space-4); font-size: var(--font-size-phone-prose); }
    .messages :global(.markdown) { font-size: inherit; }
    .permission-title { font-size: var(--font-size-sm); }
    textarea, .input-hint { padding-block: var(--space-3); font-size: var(--font-size-touch-field); }
    textarea { min-height: var(--mobile-chrome-hit-target); }
    .toolbar { padding-block: var(--space-2); }
    .dock { padding: var(--space-2) var(--space-4) max(var(--space-4), env(safe-area-inset-bottom)); }
  }
</style>
