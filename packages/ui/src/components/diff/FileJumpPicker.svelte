<script lang="ts">
  import FileSearchIcon from "@lucide/svelte/icons/file-search";
  import SearchIcon from "@lucide/svelte/icons/search";
  import { tick } from "svelte";
  import type { DiffFile } from "../../api/types.js";
  import { getStores } from "../../context.js";
  import { floatingPopoverStyle } from "../shared/floatingPosition.js";

  interface Props {
    disabled?: boolean;
  }

  const { disabled = false }: Props = $props();
  const { diff } = getStores();

  let open = $state(false);
  let query = $state("");
  let highlightIndex = $state(0);
  let inputEl = $state<HTMLInputElement>();
  let pickerEl = $state<HTMLDivElement>();
  let triggerEl = $state<HTMLButtonElement>();
  let menuEl = $state<HTMLDivElement>();
  let menuStyle = $state("");

  const files = $derived(diff.getVisibleFileList()?.files ?? diff.getVisibleDiffFiles());
  const filteredFiles = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return files;
    return files.filter((file) => file.path.toLowerCase().includes(q));
  });
  const activeFile = $derived(diff.getActiveFile());

  $effect(() => {
    if (highlightIndex > filteredFiles.length - 1) {
      highlightIndex = Math.max(filteredFiles.length - 1, 0);
    }
  });

  $effect(() => {
    if (disabled) close();
  });

  $effect(() => {
    if (!open) return;

    function updatePosition(): void {
      positionMenu();
    }

    function handleDocumentClick(event: MouseEvent): void {
      const target = event.target;
      if (target instanceof Node && pickerEl?.contains(target)) return;
      if (target instanceof Node && menuEl?.contains(target)) return;
      close();
    }

    function handleDocumentKeydown(event: KeyboardEvent): void {
      if (event.key === "Escape") close();
    }

    document.addEventListener("mousedown", handleDocumentClick);
    document.addEventListener("keydown", handleDocumentKeydown);
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    return () => {
      document.removeEventListener("mousedown", handleDocumentClick);
      document.removeEventListener("keydown", handleDocumentKeydown);
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
  });

  function fileName(path: string): string {
    const idx = path.lastIndexOf("/");
    return idx >= 0 ? path.slice(idx + 1) : path;
  }

  function directory(path: string): string {
    const idx = path.lastIndexOf("/");
    return idx >= 0 ? path.slice(0, idx) : "";
  }

  async function toggle(): Promise<void> {
    if (disabled) return;
    if (open) {
      close();
      return;
    }
    open = true;
    query = "";
    highlightIndex = Math.max(
      files.findIndex((file) => file.path === activeFile),
      0,
    );
    await tick();
    positionMenu();
    inputEl?.focus();
  }

  function positionMenu(): void {
    if (!triggerEl) return;
    const measuredSize = menuEl
      ? { popoverWidth: menuEl.offsetWidth, popoverHeight: menuEl.offsetHeight }
      : {};

    menuStyle = floatingPopoverStyle({
      trigger: triggerEl.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      ...measuredSize,
      align: "end",
      edgeGap: 8,
      triggerGap: 6,
      maxWidth: 420,
      constrainWidth: true,
    });
  }

  function close(): void {
    open = false;
    query = "";
    highlightIndex = 0;
  }

  function selectFile(file: DiffFile): void {
    if (disabled) return;
    diff.requestScrollToFile(file.path);
    close();
  }

  function handleInput(): void {
    highlightIndex = 0;
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      highlightIndex = Math.min(highlightIndex + 1, filteredFiles.length - 1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      highlightIndex = Math.max(highlightIndex - 1, 0);
    } else if (event.key === "Enter") {
      event.preventDefault();
      const selected = filteredFiles[highlightIndex];
      if (selected) selectFile(selected);
    }
  }
</script>

<div class="file-jump" bind:this={pickerEl}>
  <button
    bind:this={triggerEl}
    class="file-jump-trigger"
    class:file-jump-trigger--active={open}
    type="button"
    aria-label="Jump to file"
    aria-expanded={open}
    title="Jump to file"
    disabled={disabled || files.length === 0}
    onclick={toggle}
  >
    <FileSearchIcon size={16} strokeWidth={1.9} aria-hidden="true" />
  </button>
  {#if open}
    <div class="file-jump-menu" bind:this={menuEl} style={menuStyle}>
      <div class="file-jump-search">
        <SearchIcon size={14} strokeWidth={1.8} aria-hidden="true" />
        <input
          bind:this={inputEl}
          type="text"
          role="searchbox"
          aria-label="Jump to file"
          placeholder="Jump to file"
          autocomplete="off"
          bind:value={query}
          oninput={handleInput}
          onkeydown={handleKeydown}
        />
      </div>
      <div class="file-jump-list" role="listbox" aria-label="Changed files">
        {#each filteredFiles as file, index (file.path)}
          {@const dir = directory(file.path)}
          <button
            class="file-jump-option"
            class:file-jump-option--active={file.path === activeFile}
            class:file-jump-option--highlighted={index === highlightIndex}
            type="button"
            role="option"
            aria-selected={file.path === activeFile}
            disabled={disabled}
            onmouseenter={() => {
              highlightIndex = index;
            }}
            onclick={() => selectFile(file)}
          >
            <span class="file-jump-name">{fileName(file.path)}</span>
            {#if dir}
              <span class="file-jump-dir">{dir}</span>
            {/if}
          </button>
        {:else}
          <div class="file-jump-empty">No matching files</div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .file-jump {
    position: relative;
    z-index: 1200;
    flex-shrink: 0;
  }

  .file-jump-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 24px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    background: var(--bg-surface);
  }

  .file-jump-trigger:hover:not(:disabled),
  .file-jump-trigger--active {
    color: var(--accent-blue);
    border-color: var(--accent-blue);
  }

  .file-jump-trigger:disabled {
    cursor: default;
    opacity: 0.45;
  }

  .file-jump-menu {
    position: fixed;
    z-index: 2000;
    max-height: min(520px, 70vh);
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    box-shadow: var(--shadow-md);
    padding: 2px;
  }

  .file-jump-search {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    margin: 2px;
    padding: 0 8px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-muted);
  }

  .file-jump-search input {
    width: 100%;
    min-width: 0;
    border: 0;
    outline: none;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-xs);
  }

  .file-jump-search input::placeholder {
    color: var(--text-muted);
  }

  .file-jump-list {
    max-height: min(460px, calc(70vh - 48px));
    overflow-y: auto;
    padding: 2px;
  }

  .file-jump-option {
    display: flex;
    align-items: baseline;
    gap: 8px;
    width: 100%;
    min-height: 24px;
    padding: 4px 8px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    text-align: left;
  }

  .file-jump-option--highlighted {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .file-jump-option--active {
    color: var(--accent-blue);
  }

  .file-jump-name {
    min-width: max-content;
    font-weight: 500;
  }

  .file-jump-dir {
    min-width: 0;
    overflow: hidden;
    color: var(--text-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .file-jump-empty {
    padding: 14px 10px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
</style>
