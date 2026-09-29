<script lang="ts">
  import Terminal from "@lucide/svelte/icons/terminal";
  import ChatContentBlock from "./ChatContentBlock.svelte";
  import { lineDiff, stripAnsi } from "./chat-content.js";
  import type { ChatMessage } from "./chat-types.js";

  let { message, id }: { message: ChatMessage; id: string } = $props();

  const content = $derived(message.toolContent ?? []);
  const diffPaths = $derived(new Set(content.flatMap((item) => (item.type === "diff" && item.path ? [item.path] : []))));
  // A location a diff already names adds nothing; its line number moves into the diff caption.
  const locations = $derived((message.locations ?? []).filter((location) => !diffPaths.has(location.path)));
  const diffLine = (path: string | null | undefined) => message.locations?.find((location) => location.path === path)?.line;

  // Agents report absolute paths; the last segments identify the file and the
  // full path stays available on hover.
  function shortPath(path: string | null | undefined): string {
    if (!path) return "file";
    const parts = path.split("/").filter(Boolean);
    return parts.length > 3 ? `…/${parts.slice(-3).join("/")}` : path;
  }
</script>

<div class="details" {id}>
  {#if locations.length}
    <ul class="locations" aria-label="Locations">
      {#each locations as location, index (index)}
        <li><code title={location.path}>{shortPath(location.path)}{location.line != null ? `:${location.line}` : ""}</code></li>
      {/each}
    </ul>
  {/if}
  {#each content as item, index (index)}
    {#if item.type === "diff"}
      {@const line = diffLine(item.path)}
      <figure class="diff">
        <figcaption><code title={item.path ?? undefined}>{shortPath(item.path)}{line != null ? `:${line}` : ""}</code>{item.oldText == null ? " · new file" : ""}</figcaption>
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
          <figcaption><Terminal size={12} aria-hidden="true" />Output{@render exit(item.exitCode)}</figcaption>
          <pre aria-label="Command output">{stripAnsi(item.output)}</pre>
        </figure>
      {:else if item.exitCode != null}
        <span class="terminal"><Terminal size={12} aria-hidden="true" />No output{@render exit(item.exitCode)}</span>
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
  .terminal {
    display: inline-flex;
    align-items: center;
    align-self: flex-start;
    gap: var(--space-2);
    color: var(--text-secondary);
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
