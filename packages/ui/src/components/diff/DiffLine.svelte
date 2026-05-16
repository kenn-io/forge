<script lang="ts">
  import type { DualToken } from "../../utils/highlight.js";

  interface Props {
    type: "context" | "add" | "delete";
    content: string;
    oldNum?: number;
    newNum?: number;
    noNewline?: boolean;
    tokens: DualToken[];
    reviewEnabled?: boolean;
    oldSelected?: boolean;
    newSelected?: boolean;
    onselectside?: ((side: "left" | "right", event: MouseEvent) => void) | undefined;
  }

  const {
    type,
    oldNum,
    newNum,
    noNewline,
    tokens,
    reviewEnabled = false,
    oldSelected = false,
    newSelected = false,
    onselectside,
  }: Props = $props();

  const marker = $derived(type === "add" ? "+" : type === "delete" ? "-" : " ");
  const canSelectOld = $derived(reviewEnabled && oldNum != null);
  const canSelectNew = $derived(reviewEnabled && newNum != null);
</script>

<div
  class="diff-line"
  class:diff-line--add={type === "add"}
  class:diff-line--del={type === "delete"}
>
  <span
    class="gutter"
    class:gutter--add={type === "add"}
    class:gutter--del={type === "delete"}
    class:gutter--selectable={canSelectOld}
    class:gutter--selected={oldSelected}
  >
    {#if canSelectOld}
      <button
        class="line-comment-btn"
        title="Comment on old line"
        aria-label="Comment on old line {oldNum}"
        onclick={(event) => onselectside?.("left", event)}
      >
        {oldNum}
      </button>
    {:else}
      {oldNum ?? ""}
    {/if}
  </span>
  <span
    class="gutter gutter-new"
    class:gutter--add={type === "add"}
    class:gutter--del={type === "delete"}
    class:gutter--selectable={canSelectNew}
    class:gutter--selected={newSelected}
  >
    {#if canSelectNew}
      <button
        class="line-comment-btn"
        title="Comment on new line"
        aria-label="Comment on new line {newNum}"
        onclick={(event) => onselectside?.("right", event)}
      >
        {newNum}
      </button>
    {:else}
      {newNum ?? ""}
    {/if}
  </span>
  <span
    class="marker"
    class:marker--add={type === "add"}
    class:marker--del={type === "delete"}
  >{marker}</span>
  <pre class="code">{#each tokens as span, index (index)}<span style:--dc={span.darkColor} style:--lc={span.lightColor}>{span.content}</span>{/each}{#if noNewline}<span class="no-newline"> (no newline at end of file)</span>{/if}</pre>
</div>

<style>
  .diff-line {
    display: flex;
    align-items: stretch;
    line-height: 20px;
    font-size: var(--font-size-sm);
    background: var(--diff-bg);
  }

  .diff-line--add {
    background: var(--diff-add-bg);
  }

  .diff-line--del {
    background: var(--diff-del-bg);
  }

  .gutter {
    width: var(--diff-line-number-gutter-width, 50px);
    flex-shrink: 0;
    text-align: right;
    padding: 0 8px 0 1ch;
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--diff-line-num);
    user-select: none;
    line-height: 20px;
    background: var(--diff-bg);
  }

  .gutter--add {
    background: var(--diff-add-gutter);
  }

  .gutter--del {
    background: var(--diff-del-gutter);
  }

  .gutter--selectable {
    padding: 0;
  }

  .gutter--selected {
    box-shadow: inset 0 0 0 2px var(--accent-blue);
  }

  .line-comment-btn {
    width: 100%;
    height: 20px;
    padding: 0 8px 0 0;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    font-size: var(--font-size-xs);
    text-align: right;
    cursor: pointer;
  }

  .line-comment-btn:hover {
    background: color-mix(in srgb, var(--accent-blue) 16%, transparent);
    color: var(--text-primary);
  }

  .marker {
    width: 16px;
    flex-shrink: 0;
    text-align: center;
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    color: var(--diff-text);
    user-select: none;
    line-height: 20px;
  }

  .marker--add {
    color: var(--diff-add-text);
  }

  .marker--del {
    color: var(--diff-del-text);
  }

  .code {
    flex: 1 0 auto;
    margin: 0;
    padding: 0 8px 0 4px;
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    line-height: 20px;
    color: var(--diff-text);
    white-space: pre;
    background: transparent;
    border: none;
  }

  :global(.diff-area--word-wrap) .code {
    flex-basis: 0;
    min-width: 0;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  /* Token colors via CSS custom properties — theme switch is pure CSS,
     no JS re-renders needed. Each span carries --dc (dark) and --lc (light). */
  .code span:not(.no-newline) {
    color: var(--lc, inherit);
  }

  :global(html.dark) .code span:not(.no-newline) {
    color: var(--dc, inherit);
  }

  .no-newline {
    color: var(--diff-line-num);
    font-style: italic;
    font-size: var(--font-size-xs);
  }
</style>
