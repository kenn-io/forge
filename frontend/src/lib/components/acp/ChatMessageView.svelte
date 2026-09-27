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
    for (const image of fragment.querySelectorAll("img")) image.remove()
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
  import type { ChatMessage } from "./chat-types.js"

  let {
    message,
    agent,
    streaming = false,
  }: { message: ChatMessage; agent: string; streaming?: boolean } = $props()
  const user = $derived(message.role === "user")
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

<article class:user aria-label={user ? "You" : "Assistant"}>
  {#if user}
    <p class="message-body bubble" data-kit-tone="info">{message.text}</p>
  {:else}
    <div class="message-body">
      <Markdown
        source={message.text}
        renderer={streaming ? streamingChatMarkdown : chatMarkdown}
        class="markdown"
      />
    </div>
  {/if}
  <div class="message-meta" class:pinned={!!copyError || copied}>
    {#if !user}<span class="who">{agent}</span>{/if}
    {#if timestamp}
      <time datetime={message.createdAt} title={timestamp.toLocaleString()}
        >{timestamp.toLocaleTimeString([], {
          hour: "numeric",
          minute: "2-digit",
        })}</time
      >
    {/if}
    {#if !streaming}
      <CopyButton
        {copied}
        onclick={() => copy()}
        ariaLabel="Copy message"
        copiedAriaLabel="Copy message"
        title="Copy message"
      />
    {/if}
    <span class="copy-feedback" role="status">{copied ? "Copied" : ""}</span>
    {#if copyError}<span class="copy-error" role="alert">{copyError}</span>{/if}
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
  /* The Kit info tone: 9% tint, 30% border, 72% ink. The squared corner
     points at the composer the message came from. */
  .bubble {
    max-width: 85%;
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
  .message-body :global(.markdown > :first-child) {
    margin-top: 0;
  }
  .message-body :global(.markdown > :last-child) {
    margin-bottom: 0;
  }
  .message-meta {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-height: 22px;
    margin-top: var(--space-1);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
    opacity: 0;
    transition: opacity var(--transition-fast) ease-out;
  }
  .user .message-meta {
    flex-direction: row-reverse;
  }
  .who {
    font-weight: 500;
  }
  article:hover .message-meta,
  .message-meta:focus-within,
  .message-meta.pinned {
    opacity: 1;
  }
  .copy-error {
    color: var(--accent-red);
  }
  .copy-feedback:empty {
    display: none;
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

  @media (hover: none) {
    .message-meta {
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .message-meta {
      transition: none;
    }
  }
</style>
