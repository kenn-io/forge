<script lang="ts">
  import { tick } from "svelte";

  import type { KataTaskAPI, KataTaskSummary } from "../../api/kata/taskTypes.js";

  interface Props {
    value: string;
    api: KataTaskAPI;
    placeholder?: string;
    rows?: number;
    disabled?: boolean;
    ariaLabel?: string;
    onkeydown?: (event: KeyboardEvent) => void;
  }

  let {
    value = $bindable(""),
    api,
    placeholder = "",
    rows = 3,
    disabled = false,
    ariaLabel = undefined,
    onkeydown = undefined,
  }: Props = $props();

  let textarea: HTMLTextAreaElement | null = $state(null);
  let open = $state(false);
  let query = $state("");
  let highlight = $state(0);
  let results: KataTaskSummary[] = $state.raw([]);
  let ambiguousShortIDs = $state.raw<ReadonlySet<string>>(new Set());
  let searching = $state(false);
  let queryStart = $state(-1);

  let searchVersion = 0;

  $effect(() => {
    if (!open) {
      results = [];
      ambiguousShortIDs = new Set();
      return;
    }
    const q = query;
    const version = ++searchVersion;
    results = [];
    searching = true;
    void api
      .search({
        scope: { kind: "all" },
        status: "open",
        owner: "",
        label: "",
        query: q,
      })
      .then((response) => {
        if (version !== searchVersion) return;
        const counts: Record<string, number> = {};
        for (const issue of response.issues) {
          counts[issue.short_id] = (counts[issue.short_id] ?? 0) + 1;
        }
        ambiguousShortIDs = new Set(Object.entries(counts).filter(([, count]) => count > 1).map(([shortID]) => shortID));
        results = response.issues.slice(0, 8);
        highlight = 0;
      })
      .catch(() => {
        if (version !== searchVersion) return;
        results = [];
        ambiguousShortIDs = new Set();
      })
      .finally(() => {
        if (version === searchVersion) searching = false;
      });
  });

  function findTriggerIndex(text: string, caret: number): number {
    for (let i = caret - 1; i >= 0; i--) {
      const char = text[i];
      if (char === "#") {
        if (i === 0) return i;
        const prev = text[i - 1];
        if (prev === " " || prev === "\n" || prev === "\t") return i;
        return -1;
      }
      if (char === " " || char === "\n" || char === "\t") return -1;
    }
    return -1;
  }

  function refreshReference(): void {
    if (!textarea) {
      open = false;
      return;
    }
    const caret = textarea.selectionStart;
    const text = textarea.value;
    const triggerIndex = findTriggerIndex(text, caret);
    if (triggerIndex === -1) {
      open = false;
      query = "";
      queryStart = -1;
      return;
    }
    queryStart = triggerIndex;
    query = text.slice(triggerIndex + 1, caret);
    open = true;
  }

  function handleKeyup(event: KeyboardEvent): void {
    if (event.key === "ArrowLeft" || event.key === "ArrowRight" || event.key === "Home" || event.key === "End") {
      refreshReference();
    }
  }

  function handleBlur(event: FocusEvent): void {
    const related = event.relatedTarget as HTMLElement | null;
    if (related && related.closest(".reference-menu")) return;
    open = false;
  }

  function referenceIDFor(issue: KataTaskSummary): string {
    return ambiguousShortIDs.has(issue.short_id) ? issue.qualified_id : issue.short_id;
  }

  async function selectResult(issue: KataTaskSummary): Promise<void> {
    if (!textarea || queryStart < 0) return;
    const text = textarea.value;
    const caret = textarea.selectionStart;
    const before = text.slice(0, queryStart);
    const after = text.slice(caret);
    const replacement = `#${referenceIDFor(issue)} `;
    value = before + replacement + after;
    open = false;
    query = "";
    await tick();
    if (!textarea) return;
    const nextCaret = before.length + replacement.length;
    textarea.focus();
    textarea.setSelectionRange(nextCaret, nextCaret);
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (open && results.length > 0) {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        highlight = (highlight + 1) % results.length;
        return;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        highlight = (highlight - 1 + results.length) % results.length;
        return;
      }
      if ((event.key === "Enter" && !event.metaKey && !event.ctrlKey) || event.key === "Tab") {
        event.preventDefault();
        const item = results[highlight];
        if (item) void selectResult(item);
        return;
      }
    }
    if (open && event.key === "Escape") {
      event.preventDefault();
      open = false;
      return;
    }
    onkeydown?.(event);
  }
</script>

<div class="reference-wrap">
  <textarea
    bind:this={textarea}
    bind:value
    {disabled}
    {rows}
    {placeholder}
    aria-label={ariaLabel}
    oninput={refreshReference}
    onkeydown={handleKeydown}
    onkeyup={handleKeyup}
    onclick={refreshReference}
    onblur={handleBlur}
  ></textarea>
  {#if open}
    <div class="reference-menu" role="listbox" aria-label="Insert task reference">
      {#if searching && results.length === 0}
        <div class="reference-empty">Searching...</div>
      {:else if results.length === 0}
        <div class="reference-empty">No matches for #{query}</div>
      {:else}
        {#each results as issue, index (issue.uid)}
          <button
            type="button"
            class={["reference-option", index === highlight && "active"]}
            role="option"
            aria-selected={index === highlight}
            onmousedown={(event) => {
              event.preventDefault();
              void selectResult(issue);
            }}
            onmouseenter={() => (highlight = index)}
          >
            <span class="reference-short">#{issue.short_id}</span>
            <span class="reference-title">{issue.title}</span>
            <span class="reference-project">{issue.project_name}</span>
          </button>
        {/each}
      {/if}
    </div>
  {/if}
</div>

<style>
  .reference-wrap {
    position: relative;
    width: 100%;
  }

  textarea {
    width: 100%;
    resize: vertical;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    line-height: 1.4;
    padding: 8px 10px;
  }

  textarea:focus {
    outline: none;
    border-color: var(--accent-blue);
  }

  textarea::placeholder {
    color: var(--text-muted);
  }

  .reference-menu {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    right: 0;
    z-index: 50;
    display: grid;
    gap: 1px;
    max-height: 240px;
    overflow: auto;
    padding: 4px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-surface);
    box-shadow: var(--shadow-md);
  }

  .reference-empty {
    padding: 8px 10px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .reference-option {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 8px;
    border: 0;
    border-radius: 4px;
    background: transparent;
    color: var(--text-primary);
    cursor: pointer;
    font: inherit;
    font-size: var(--font-size-sm);
    padding: 5px 8px;
    text-align: left;
  }

  .reference-option:hover,
  .reference-option.active {
    background: var(--accent-blue-soft);
  }

  .reference-short {
    color: var(--accent-blue);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .reference-title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .reference-project {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
</style>
