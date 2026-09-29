<script lang="ts">
  import ArrowRightLeft from "@lucide/svelte/icons/arrow-right-left";
  import Brain from "@lucide/svelte/icons/brain";
  import FileText from "@lucide/svelte/icons/file-text";
  import Globe from "@lucide/svelte/icons/globe";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Search from "@lucide/svelte/icons/search";
  import Settings from "@lucide/svelte/icons/settings-2";
  import Terminal from "@lucide/svelte/icons/terminal";
  import Trash from "@lucide/svelte/icons/trash-2";
  import Wrench from "@lucide/svelte/icons/wrench";
  import ChatContentBlock from "./ChatContentBlock.svelte";
  import { lineDiff, stripAnsi } from "./chat-content.js";
  import type { ChatMessage } from "./chat-types.js";

  let { message, id }: { message: ChatMessage; id: string } = $props();

  const kinds: Record<string, { icon: typeof Wrench; label: string }> = {
    read: { icon: FileText, label: "Read" },
    edit: { icon: Pencil, label: "Edit" },
    delete: { icon: Trash, label: "Delete" },
    move: { icon: ArrowRightLeft, label: "Move" },
    search: { icon: Search, label: "Search" },
    execute: { icon: Terminal, label: "Execute" },
    think: { icon: Brain, label: "Think" },
    fetch: { icon: Globe, label: "Fetch" },
    switch_mode: { icon: Settings, label: "Switch mode" },
  };
  const kind = $derived(kinds[message.kind ?? ""] ?? { icon: Wrench, label: "Tool" });
</script>

<div class="details" {id}>
  <div class="kind"><kind.icon size={12} aria-hidden="true" />{kind.label}</div>
  {#if message.locations?.length}
    <ul class="locations" aria-label="Locations">
      {#each message.locations as location, index (index)}
        <li><code>{location.path}{location.line != null ? `:${location.line}` : ""}</code></li>
      {/each}
    </ul>
  {/if}
  {#each message.toolContent ?? [] as item, index (index)}
    {#if item.type === "diff"}
      <figure class="diff">
        <figcaption><code>{item.path ?? "file"}</code>{item.oldText == null ? " (new file)" : ""}</figcaption>
        <ol aria-label={`Changes to ${item.path ?? "file"}`}>
          {#each lineDiff(item.oldText, item.newText) as line, lineIndex (lineIndex)}
            {#if line.kind === "gap"}
              <li class="gap" aria-label="Unchanged lines hidden">…</li>
            {:else}
              <li class={line.kind}><span class="sign" aria-hidden="true">{line.kind === "added" ? "+" : line.kind === "removed" ? "-" : " "}</span><span class="kit-sr-only">{line.kind === "added" ? "Added: " : line.kind === "removed" ? "Removed: " : ""}</span>{line.text}</li>
            {/if}
          {/each}
        </ol>
      </figure>
    {:else if item.type === "terminal"}
      {#snippet exit(code: number | null | undefined)}
        {#if code != null}
          <span class={["exit", code === 0 ? "exit--ok" : "exit--failed"]}>exit {code}</span>
        {/if}
      {/snippet}
      {#if item.output}
        <figure class="terminal-output">
          <figcaption>
            <Terminal size={12} aria-hidden="true" />Terminal {item.terminalId ?? ""}{@render exit(item.exitCode)}
          </figcaption>
          <pre aria-label={item.terminalId ? `Terminal ${item.terminalId} output` : "Terminal output"}>{stripAnsi(item.output)}</pre>
        </figure>
      {:else}
        <span class="terminal"
          ><Terminal size={12} aria-hidden="true" />Terminal {item.terminalId ?? ""}{@render exit(item.exitCode)}</span
        >
      {/if}
    {:else if item.content?.type === "text"}
      <pre class="output">{item.content.text ?? ""}</pre>
    {:else if item.content}
      <ChatContentBlock content={item.content} />
    {/if}
  {/each}
  {#if message.rawInput}
    <details class="raw"><summary>Raw input</summary><pre>{message.rawInput}</pre></details>
  {/if}
  {#if message.rawOutput}
    <details class="raw"><summary>Raw output</summary><pre>{message.rawOutput}</pre></details>
  {/if}
</div>

<style>
  .details {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    min-width: 0;
    padding: 0 var(--space-5) var(--space-3) calc(14px + var(--space-4) + var(--space-5));
  }
  .kind,
  .terminal {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text-secondary);
  }
  .terminal {
    align-self: flex-start;
    padding: 0 var(--space-3);
    border: 1px solid var(--border-default);
    border-radius: 999px;
  }
  .locations {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  code,
  pre {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    overflow-wrap: anywhere;
  }
  pre {
    max-height: 16rem;
    overflow: auto;
    margin: 0;
    white-space: pre-wrap;
    color: var(--text-primary);
  }
  .diff {
    margin: 0;
    min-width: 0;
  }
  .diff figcaption {
    margin-bottom: var(--space-2);
    color: var(--text-secondary);
  }
  .diff ol {
    max-height: 20rem;
    overflow: auto;
    margin: 0;
    padding: var(--space-2) 0;
    list-style: none;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    line-height: 1.5;
  }
  .diff li {
    display: block;
    padding: 0 var(--space-4);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    color: var(--text-primary);
  }
  .diff .sign {
    display: inline-block;
    width: 1.5ch;
    color: var(--text-muted);
    user-select: none;
  }
  .diff .added {
    background: color-mix(in srgb, var(--accent-green) 12%, transparent);
  }
  .diff .removed {
    background: color-mix(in srgb, var(--accent-red) 12%, transparent);
  }
  .diff .gap {
    color: var(--text-muted);
  }
  .terminal-output {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    margin: 0;
    min-width: 0;
  }
  .terminal-output figcaption {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text-secondary);
  }
  .terminal-output pre {
    padding: var(--space-3) var(--space-4);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    white-space: pre;
    overflow-wrap: normal;
  }
  .exit {
    margin-left: var(--space-2);
    padding: 0 var(--space-2);
    border: 1px solid currentColor;
    border-radius: var(--radius-sm);
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
  }
  .exit--ok {
    color: var(--accent-green);
  }
  .exit--failed {
    color: var(--accent-red);
  }
  .raw summary {
    color: var(--text-secondary);
    cursor: pointer;
  }
  .raw pre {
    margin-top: var(--space-2);
  }
</style>
