<!-- Copied from the shared console chat; imports and touch sizing adapted for Forge. -->
<script module lang="ts">
  import {
    createMarkdownRenderer,
    type MarkdownRenderer,
  } from "@kenn-io/kit-ui/utils/markdown"

  const markdown = createMarkdownRenderer()
  const chatMarkdown: MarkdownRenderer = {
    render: async (source) => decorate(await markdown.render(source)),
    renderSync: (source) => decorate(markdown.renderSync(source)),
  }
  const streamingChatMarkdown: MarkdownRenderer = {
    render: async (source) => chatMarkdown.renderSync(source),
    renderSync: (source) => chatMarkdown.renderSync(source),
  }
  // Kit owns parsing, sanitization and prose styling. Add link destinations
  // on hover and focusable table scrolling for narrow layouts.
  function decorate(html: string): string {
    const container = document.createElement("template")
    container.innerHTML = html
    const fragment = container.content
    // Embedded images can render without fetching agent-provided addresses.
    for (const image of fragment.querySelectorAll("img")) {
      if (!/^data:image\/[a-z0-9.+-]+;base64,[a-z0-9+/=\s]+$/i.test(image.getAttribute("src") ?? "")) image.remove()
    }
    for (const table of fragment.querySelectorAll("table")) {
      const scroll = document.createElement("div")
      scroll.className = "table-scroll"
      scroll.tabIndex = 0
      scroll.setAttribute("role", "region")
      scroll.setAttribute("aria-label", "Scrollable table")
      table.replaceWith(scroll)
      scroll.append(table)
    }
    for (const link of fragment.querySelectorAll("a[href]")) {
      link.setAttribute("title", link.getAttribute("href") ?? "")
      link.setAttribute("target", "_blank")
      link.setAttribute("rel", "noopener noreferrer")
    }
    return container.innerHTML
  }
</script>

<script lang="ts">
  import { copyToClipboard } from "@kenn-io/kit-ui/utils/clipboard"
  import { onDestroy } from "svelte"
  import { CopyButton } from "@kenn-io/kit-ui"
  import { Markdown } from "@kenn-io/kit-ui"
  import Brain from "@lucide/svelte/icons/brain"
  import ChevronDown from "@lucide/svelte/icons/chevron-down"
  import ChatContentBlock from "./ChatContentBlock.svelte"
  import type { ChatMessage } from "./chat-types.js"

  let { message, streaming = false }: { message: ChatMessage; streaming?: boolean } =
    $props()
  const user = $derived(message.role === "user")
  const thought = $derived(message.role === "thought")
  const id = $props.id()
  let thoughtOpen = $state(false)
  let copied = $state(false)
  let copyError = $state("")
  let resetCopy: ReturnType<typeof setTimeout> | undefined
  const timestamp = $derived(
    message.createdAt ? new Date(message.createdAt) : null,
  )
  onDestroy(() => clearTimeout(resetCopy))
  async function copy(): Promise<void> {
    copyError = ""
    if (await copyToClipboard(message.text)) {
      copied = true
      clearTimeout(resetCopy)
      resetCopy = setTimeout(() => (copied = false), 1800)
    } else {
      copyError = "Could not copy message."
    }
  }
</script>

{#snippet body()}
  {#if message.text}
    <Markdown
      source={message.text}
      renderer={streaming ? streamingChatMarkdown : chatMarkdown}
      class="markdown"
    />
  {/if}
  {#if message.content}<ChatContentBlock content={message.content} />{/if}
{/snippet}

{#snippet copyAction()}
  <CopyButton
    {copied}
    onclick={() => copy()}
    ariaLabel="Copy message"
    copiedAriaLabel="Copy message"
    title="Copy message"
  />
  <span class="kit-sr-only" role="status">{copied ? "Copied" : ""}</span>
  {#if copyError}<span class="copy-error" role="alert">{copyError}</span>{/if}
{/snippet}

<article
  class:user
  aria-label={user ? "You" : thought ? "Assistant thinking" : "Assistant"}
>
  <div class="cell">
    {#if user}
      {#if message.text}<p class="message-body bubble" data-kit-tone="info">{message.text}</p>{/if}
      {#each message.images ?? [] as image}
        <ChatContentBlock content={image} />
      {/each}
    {:else if thought}
      <div class="thought">
        <button
          type="button"
          class="thought-toggle"
          aria-expanded={thoughtOpen}
          aria-controls={`${id}-thought`}
          onclick={() => (thoughtOpen = !thoughtOpen)}
        >
          <Brain size={13} aria-hidden="true" />Thinking<ChevronDown
            size={12}
            class="chevron"
            aria-hidden="true"
          />
        </button>
        {#if thoughtOpen}
          <div class="thought-body" id={`${id}-thought`}>
            {#if !streaming && message.text}
              <div class="thought-copy">{@render copyAction()}</div>
            {/if}
            <div class="message-body">{@render body()}</div>
          </div>
        {/if}
      </div>
    {:else}
      <div class="message-body">{@render body()}</div>
    {/if}
    <!-- Time and copy. Wide panes reveal them on hover or focus in a gutter
         left of the cell, laid over reserved space so nothing reflows; panes
         too narrow for the gutter show them as a quiet line below the message. -->
    {#if timestamp || (!thought && ((!streaming && message.text) || copyError))}
    <div class="gutter" class:pinned={!!copyError || copied}>
    {#if timestamp}
      <time datetime={message.createdAt} title={timestamp.toLocaleString()}
        >{timestamp.toLocaleTimeString([], {
          hour: "numeric",
          minute: "2-digit",
        })}</time
      >
    {/if}
    {#if !thought && !streaming && message.text}{@render copyAction()}{/if}
    </div>
    {/if}
  </div>
</article>

<style>
  article {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  article.user {
    align-items: flex-end;
  }
  .cell {
    position: relative;
    min-width: 0;
  }
  .user .cell {
    max-width: 85%;
  }
  /* The Kit info tone: 9% tint, 30% border, 72% ink. The squared corner
     points at the composer the message came from. */
  .bubble {
    margin: 0;
    padding: var(--space-4) var(--space-5);
    background: var(--kit-tone-band-bg);
    border: 1px solid var(--kit-tone-border);
    border-radius: var(--radius-md) var(--radius-md) var(--radius-sm)
      var(--radius-md);
    color: var(--kit-tone-ink);
    line-height: 1.55;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .message-body:not(.bubble) {
    min-width: 0;
    line-height: 1.6;
  }
  .message-body {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  .thought-toggle {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    min-height: 24px;
    padding: 0 var(--space-2);
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-muted);
    font: inherit;
    font-size: var(--font-size-xs);
    cursor: pointer;
  }
  .thought-toggle:hover {
    color: var(--text-secondary);
  }
  .thought-toggle:focus-visible {
    outline: var(--focus-ring);
    outline-offset: 1px;
  }
  .thought-toggle :global(.chevron) {
    transition: transform var(--transition-fast) ease-out;
  }
  .thought-toggle[aria-expanded="true"] :global(.chevron) {
    transform: rotate(180deg);
  }
  .thought-body {
    display: flex;
    align-items: flex-start;
    gap: var(--space-2);
    margin-top: var(--space-2);
    padding-left: var(--space-5);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }
  .thought-copy {
    flex: 0 0 auto;
  }
  .message-body :global(.markdown > :first-child) {
    margin-top: 0;
  }
  .message-body :global(.markdown > :last-child) {
    margin-bottom: 0;
  }
  /* Narrow panes: one always-visible muted line under the message, on the
     message's own text edge. */
  .gutter {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1) var(--space-2);
    margin-top: var(--space-1);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
  }
  .user .gutter {
    justify-content: flex-end;
  }
  /* Wide panes: the conversation reserves --acp-gutter to the left of every
     cell (ACPWorkspace.svelte .messages) and the gutter sits there, revealed on
     hover or focus. */
  @container acp-conversation (min-width: 40rem) {
    .gutter {
      position: absolute;
      top: 0;
      right: calc(100% + var(--space-3));
      width: var(--acp-gutter);
      margin-top: 0;
      justify-content: flex-end;
      color: var(--text-secondary);
      opacity: 0;
      transition: opacity var(--transition-fast) ease-out;
    }
    .cell:hover .gutter,
    .cell:focus-within .gutter,
    .gutter.pinned {
      opacity: 1;
    }
  }
  .copy-error {
    color: var(--accent-red);
    text-align: right;
  }
  .message-body :global(.markdown .table-scroll) {
    max-width: 100%;
    overflow-x: auto;
    margin-block: var(--space-4);
  }
  .message-body :global(.markdown table) {
    display: table;
    width: 100%;
    overflow-wrap: normal;
    word-break: normal;
    font-size: var(--font-size-sm);
  }
  .message-body :global(.markdown th) {
    white-space: nowrap;
  }
  .message-body :global(.markdown a:focus-visible),
  .message-body :global(.markdown .table-scroll:focus-visible) {
    outline: var(--focus-ring);
    outline-offset: 2px;
  }

  @media (pointer: coarse) {
    .thought-toggle {
      min-height: var(--mobile-chrome-hit-target);
    }
    .gutter :global(button),
    .thought-copy :global(button) {
      min-width: var(--mobile-chrome-hit-target);
      min-height: var(--mobile-chrome-hit-target);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .thought-toggle :global(.chevron),
    .gutter {
      transition: none;
    }
  }
</style>
