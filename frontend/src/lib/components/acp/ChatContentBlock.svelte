<script lang="ts">
  import { Button, CodeBlock, ImagePreview } from "@kenn-io/kit-ui";
  import Download from "@lucide/svelte/icons/download";
  import FileText from "@lucide/svelte/icons/file-text";
  import ImageIcon from "@lucide/svelte/icons/image";
  import Link from "@lucide/svelte/icons/link";
  import Music from "@lucide/svelte/icons/music";
  import {
    decodeBase64,
    formatBytes,
    isImageContent,
    mediaDataURL,
    resourceFilename,
    resourceHref,
    resourceLanguage,
  } from "./chat-content.js";
  import type { ChatContent } from "./chat-types.js";

  import McpApp from "./McpApp.svelte";
  import { decodeAppDescriptor } from "./mcp-app.js";
  let { content }: { content: ChatContent } = $props();

  const app = $derived(decodeAppDescriptor(content));
  const label = $derived(content.title || content.name || content.uri || "");
  const meta = $derived([content.mimeType, formatBytes(content.size)].filter(Boolean).join(" · "));
  const imageSrc = $derived(isImageContent(content) ? mediaDataURL(content, "image") : undefined);
  const audioSrc = $derived(content.type === "audio" ? mediaDataURL(content, "audio") : undefined);
  const href = $derived(resourceHref(content.uri));
  const languageProp = $derived.by((): { language?: string } => {
    const language = resourceLanguage(content);
    return language ? { language } : {};
  });

  function download() {
    if (!content.data) return;
    const blob = new Blob([decodeBase64(content.data) as BlobPart], {
      type: content.mimeType || "application/octet-stream",
    });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = resourceFilename(content);
    anchor.click();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }
</script>

{#snippet placeholder(Icon: typeof ImageIcon, text: string)}
  <div class="card placeholder">
    <Icon size={14} aria-hidden="true" />
    <span>{text}</span>
    {#if meta}<span class="meta">{meta}</span>{/if}
  </div>
{/snippet}

{#if app}
  <McpApp {app} />
{:else if content.type === "text"}
  <p class="text">{content.text ?? ""}</p>
{:else if isImageContent(content)}
  {#if imageSrc}
    <div class="image"><ImagePreview src={imageSrc} alt={label || "Image from agent"} maxHeight="24rem" /></div>
  {:else}
    {@render placeholder(ImageIcon, "Image unavailable")}
  {/if}
{:else if content.type === "audio"}
  {#if audioSrc}
    <audio class="audio" controls src={audioSrc} aria-label={label || "Audio from agent"}></audio>
  {:else}
    {@render placeholder(Music, "Audio unavailable")}
  {/if}
{:else if content.type === "resource_link"}
  <div class="card link-card">
    <Link size={14} aria-hidden="true" />
    <div class="card-body">
      {#if href}
        <a class="title" {href} target="_blank" rel="noopener noreferrer">{content.title || content.name || content.uri}</a>
      {:else}
        <span class="title">{content.title || content.name || content.uri}</span>
      {/if}
      {#if content.uri && (content.title || content.name || !href)}<code class="uri">{content.uri}</code>{/if}
      {#if content.description}<span class="description">{content.description}</span>{/if}
      {#if meta}<span class="meta">{meta}</span>{/if}
    </div>
  </div>
{:else if content.type === "resource"}
  {#if content.text != null}
    <details class="resource">
      <summary><FileText size={14} aria-hidden="true" /><span>{label || "Embedded resource"}</span></summary>
      <CodeBlock
        code={content.text}
        title={content.uri || content.name || "resource"}
        maxHeight="20rem"
        {...languageProp}
      />
    </details>
  {:else if !content.data}
    {@render placeholder(FileText, "Resource unavailable")}
  {:else}
    <div class="card">
      <FileText size={14} aria-hidden="true" />
      <div class="card-body">
        <span class="title">{label || "Embedded resource"}</span>
        {#if meta}<span class="meta">{meta}</span>{/if}
      </div>
      <Button size="sm" onclick={download}><Download size={14} aria-hidden="true" />Download</Button>
    </div>
  {/if}
{:else}
  {@render placeholder(FileText, `Unsupported ${content.type} content`)}
{/if}

<style>
  .text {
    margin: 0;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .image {
    width: fit-content;
    max-width: 100%;
  }
  .audio {
    display: block;
    max-width: 100%;
  }
  .card {
    display: flex;
    align-items: flex-start;
    gap: var(--space-3);
    width: fit-content;
    max-width: min(36rem, 100%);
    padding: var(--space-3) var(--space-4);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }
  .card > :global(svg) {
    flex: none;
    margin-top: var(--space-1);
  }
  .card-body {
    display: flex;
    flex: 1;
    flex-direction: column;
    gap: var(--space-1);
    min-width: 0;
  }
  .title {
    color: var(--text-primary);
    overflow-wrap: anywhere;
  }
  a.title {
    color: var(--accent-blue);
  }
  .uri {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    overflow-wrap: anywhere;
  }
  .description {
    overflow-wrap: anywhere;
  }
  .meta {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }
  .placeholder {
    align-items: center;
    flex-wrap: wrap;
  }
  .resource summary {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    cursor: pointer;
    overflow-wrap: anywhere;
  }
  @media (pointer: coarse) {
    .resource summary {
      min-height: var(--mobile-chrome-hit-target);
    }
  }
</style>
