<script lang="ts">
  import FileSearchIcon from "@lucide/svelte/icons/file-search";
  import { tick } from "svelte";
  import type { DiffFile } from "../../api/types.js";
  import { getStores } from "../../context.js";
  import { Card, IconButton, SearchInput, floatingPopoverStyle } from "@kenn-io/kit-ui";

  interface Props {
    disabled?: boolean;
  }

  const { disabled = false }: Props = $props();
  const { diff } = getStores();

  let open = $state(false);
  let query = $state("");
  let highlightIndex = $state(0);
  let inputEl = $state<HTMLInputElement>(undefined!);
  let pickerEl = $state<HTMLDivElement>();
  let triggerEl = $state<HTMLSpanElement>();
  let menuEl = $state<HTMLDivElement>();
  let menuStyle = $state("");

  const files = $derived(diff.getVisibleFileList()?.files ?? diff.getVisibleDiffFiles());
  const filteredFiles = $derived.by(() => {
    const normalizedQuery = query.trim().toLowerCase();
    if (!normalizedQuery) return files;
    return files.filter((file) => file.path.toLowerCase().includes(normalizedQuery));
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

    function handleDocumentClick(event: MouseEvent): void {
      const target = event.target;
      if (target instanceof Node && pickerEl?.contains(target)) return;
      if (target instanceof Node && menuEl?.contains(target)) return;
      close();
    }

    function handleDocumentKeydown(event: KeyboardEvent): void {
      if (event.key === "Escape") close(true);
    }

    document.addEventListener("mousedown", handleDocumentClick);
    document.addEventListener("keydown", handleDocumentKeydown);
    window.addEventListener("resize", positionMenu);
    window.addEventListener("scroll", positionMenu, true);
    return () => {
      document.removeEventListener("mousedown", handleDocumentClick);
      document.removeEventListener("keydown", handleDocumentKeydown);
      window.removeEventListener("resize", positionMenu);
      window.removeEventListener("scroll", positionMenu, true);
    };
  });

  function fileName(path: string): string {
    const index = path.lastIndexOf("/");
    return index >= 0 ? path.slice(index + 1) : path;
  }

  function directory(path: string): string {
    const index = path.lastIndexOf("/");
    return index >= 0 ? path.slice(0, index) : "";
  }

  async function toggle(): Promise<void> {
    if (disabled) return;
    if (open) {
      close();
      return;
    }
    open = true;
    query = "";
    highlightIndex = Math.max(files.findIndex((file) => file.path === activeFile), 0);
    await tick();
    positionMenu();
    inputEl?.focus();
    await scrollHighlightedOptionIntoView();
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

  function close(restoreFocus = false): void {
    open = false;
    query = "";
    highlightIndex = 0;
    if (restoreFocus) {
      void tick().then(() => {
        triggerEl?.querySelector<HTMLButtonElement>("button")?.focus();
      });
    }
  }

  function selectFile(file: DiffFile, restoreFocus = false): void {
    if (disabled) return;
    diff.requestScrollToFile(file.path);
    close(restoreFocus);
  }

  function handleInput(): void {
    highlightIndex = 0;
  }

  async function scrollHighlightedOptionIntoView(): Promise<void> {
    await tick();
    menuEl
      ?.querySelector<HTMLElement>(`#changed-file-option-${highlightIndex}`)
      ?.scrollIntoView?.({ block: "nearest" });
  }

  function moveHighlight(nextIndex: number): void {
    highlightIndex = nextIndex;
    void scrollHighlightedOptionIntoView();
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      moveHighlight(Math.min(highlightIndex + 1, filteredFiles.length - 1));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      moveHighlight(Math.max(highlightIndex - 1, 0));
    } else if (event.key === "Enter") {
      event.preventDefault();
      const selected = filteredFiles[highlightIndex];
      if (selected) selectFile(selected, true);
    }
  }
</script>

<div class="file-jump" bind:this={pickerEl}>
  <span class="file-jump-trigger-anchor" bind:this={triggerEl}>
    <IconButton
      size="sm"
      tone="info"
      ariaLabel="Jump to file"
      title="Jump to file"
      ariaExpanded={open}
      ariaHaspopup="listbox"
      {...(open ? { ariaControls: "changed-files-listbox" } : {})}
      ariaPressed={open}
      disabled={disabled || files.length === 0}
      onclick={toggle}
    >
      <FileSearchIcon size={16} strokeWidth={1.9} aria-hidden="true" />
    </IconButton>
  </span>
  {#if open}
    <div class="file-jump-menu" bind:this={menuEl} style={menuStyle} role="dialog" aria-label="Jump to file">
      <Card level="default" padding="none" class="file-jump-menu-card">
        <div class="file-jump-search">
          <!-- kit-ui-check-ignore: command-style jump list needs active-file highlight and clear-before-close Escape, which form Typeahead does not expose -->
          <SearchInput role="combobox"
            bind:inputEl
            bind:value={query}
            size="sm"
            block
            ariaExpanded={open}
            ariaControls="changed-files-listbox"
            {...(filteredFiles.length > 0
              ? { ariaActivedescendant: `changed-file-option-${highlightIndex}` }
              : {})}
            ariaAutocomplete="list"
            ariaLabel="Jump to file"
            placeholder="Jump to file"
            oninput={handleInput}
            onkeydown={handleKeydown}
          />
        </div>
        <!-- kit-ui-check-ignore: same command-style picker exception as the combobox above; kit Typeahead cannot preserve its active-file keyboard contract -->
        <div id="changed-files-listbox" class="file-jump-list" role="listbox" aria-label="Jump to file">
          {#each filteredFiles as file, index (file.path)}
            {@const dir = directory(file.path)}
            <button
              id={`changed-file-option-${index}`}
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
      </Card>
    </div>
  {/if}
</div>

<style>
  .file-jump {
    position: relative;
    z-index: 10;
    flex-shrink: 0;
  }

  .file-jump-trigger-anchor {
    display: inline-flex;
  }

  .file-jump-menu {
    position: fixed;
    z-index: var(--z-popover);
  }

  :global(.file-jump-menu-card) {
    max-height: min(520px, 70vh);
    overflow: hidden;
    box-shadow: var(--shadow-md);
  }

  .file-jump-search {
    margin: 4px;
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
