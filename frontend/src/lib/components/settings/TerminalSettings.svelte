<script lang="ts">
  import { onDestroy } from "svelte";
  import {
    DEFAULT_TERMINAL_SETTINGS,
    getStores,
    SelectDropdown,
  } from "@middleman/ui";
  import XIcon from "@lucide/svelte/icons/x";
  import type { TerminalSettings as TerminalSettingsType } from "@middleman/ui/api/types";
  import { updateSettings } from "../../api/settings.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";

  interface FontData {
    family: string;
    fullName: string;
    postscriptName: string;
    style: string;
  }

  interface Props {
    terminal: TerminalSettingsType;
    onUpdate: (terminal: TerminalSettingsType) => void;
    compact?: boolean;
    livePreview?: boolean;
    onSavingChange?: (saving: boolean) => void;
  }

  const {
    terminal,
    onUpdate,
    compact = false,
    livePreview = false,
    onSavingChange,
  }: Props = $props();

  const { settings: settingsStore } = getStores();
  const embedded = isEmbedded();

  const commonMonospaceFonts = [
    "JetBrains Mono",
    "SF Mono",
    "Iosevka Term",
    "Fira Code",
    "Cascadia Code",
    "Source Code Pro",
    "Menlo",
    "Monaco",
    "Consolas",
    "Courier New",
  ];
  const rendererOptions = [
    { value: "xterm", label: "xterm.js" },
    { value: "ghostty-web", label: "ghostty-web" },
  ];

  let draftReady = $state(false);
  let saving = $state(false);
  let fontFamilyDraft = $state("");
  let fontSizeDraft = $state<number | null>(
    DEFAULT_TERMINAL_SETTINGS.font_size,
  );
  let scrollbackDraft = $state<number | null>(
    DEFAULT_TERMINAL_SETTINGS.scrollback,
  );
  let lineHeightDraft = $state<number | null>(
    DEFAULT_TERMINAL_SETTINGS.line_height,
  );
  let letterSpacingDraft = $state<number | null>(
    DEFAULT_TERMINAL_SETTINGS.letter_spacing,
  );
  let cursorBlinkDraft = $state(
    DEFAULT_TERMINAL_SETTINGS.cursor_blink,
  );
  let fontLigaturesDraft = $state(
    DEFAULT_TERMINAL_SETTINGS.font_ligatures,
  );
  let rendererDraft = $state<TerminalSettingsType["renderer"]>("xterm");
  let hideTmuxStatusDraft = $state(
    DEFAULT_TERMINAL_SETTINGS.hide_tmux_status,
  );
  let fontDialogOpen = $state(false);
  let localFonts = $state<FontData[] | null>(null);
  let fontLoadError = $state<string | null>(null);
  let loadingFonts = $state(false);
  let livePreviewBaseline = $state<TerminalSettingsType | null>(null);

  function normalizeFontFamily(value: string): string {
    return value.trim();
  }

  function quoteFontFamily(family: string): string {
    return `${quoteSingleFontFamily(family)}, monospace`;
  }

  function quoteSingleFontFamily(family: string): string {
    const escaped = family.replaceAll("\\", "\\\\").replaceAll('"', '\\"');
    return `"${escaped}"`;
  }

  function firstFontFamilySeparatorIndex(value: string): number {
    let quote: "'" | "\"" | null = null;
    let escaped = false;
    for (let index = 0; index < value.length; index += 1) {
      const char = value[index];
      if (escaped) {
        escaped = false;
        continue;
      }
      if (char === "\\") {
        escaped = true;
        continue;
      }
      if (quote) {
        if (char === quote) quote = null;
        continue;
      }
      if (char === "'" || char === "\"") {
        quote = char;
        continue;
      }
      if (char === ",") return index;
    }
    return -1;
  }

  function replacePreferredFontFamily(
    currentValue: string,
    family: string,
  ): string {
    const separatorIndex = firstFontFamilySeparatorIndex(currentValue);
    if (separatorIndex === -1) return quoteFontFamily(family);

    const fallbacks = currentValue.slice(separatorIndex + 1).trim();
    if (!fallbacks) return quoteFontFamily(family);
    return `${quoteSingleFontFamily(family)}, ${fallbacks}`;
  }

  function isLikelyMonospaceFont(font: FontData): boolean {
    const name = `${font.family} ${font.fullName} ${font.postscriptName}`;
    return /\b(mono|code|console|terminal|typewriter|courier|menlo|monaco|consolas|iosevka|hack)\b/i
      .test(name);
  }

  function pendingTerminalSettings(): TerminalSettingsType {
    return {
      font_family: normalizedFontFamilyDraft,
      font_size: fontSizeDraft ?? DEFAULT_TERMINAL_SETTINGS.font_size,
      scrollback: scrollbackDraft ?? DEFAULT_TERMINAL_SETTINGS.scrollback,
      line_height: lineHeightDraft ?? DEFAULT_TERMINAL_SETTINGS.line_height,
      letter_spacing:
        letterSpacingDraft ?? DEFAULT_TERMINAL_SETTINGS.letter_spacing,
      cursor_blink: cursorBlinkDraft,
      font_ligatures: fontLigaturesDraft,
      renderer: rendererDraft,
      hide_tmux_status: hideTmuxStatusDraft,
    };
  }

  const currentTerminal = $derived(terminal);
  const normalizedFontFamilyDraft = $derived(
    normalizeFontFamily(fontFamilyDraft),
  );
  const pendingTerminal = $derived.by(pendingTerminalSettings);
  const isDirty = $derived(
    pendingTerminal.font_family !== currentTerminal.font_family ||
      pendingTerminal.font_size !== currentTerminal.font_size ||
      pendingTerminal.scrollback !== currentTerminal.scrollback ||
      pendingTerminal.line_height !== currentTerminal.line_height ||
      pendingTerminal.letter_spacing !== currentTerminal.letter_spacing ||
      pendingTerminal.cursor_blink !== currentTerminal.cursor_blink ||
      pendingTerminal.font_ligatures !== currentTerminal.font_ligatures ||
      pendingTerminal.renderer !== currentTerminal.renderer ||
      pendingTerminal.hide_tmux_status !== currentTerminal.hide_tmux_status
  );
  const isDefaultDraft = $derived(
    pendingTerminal.font_family === DEFAULT_TERMINAL_SETTINGS.font_family &&
      pendingTerminal.font_size === DEFAULT_TERMINAL_SETTINGS.font_size &&
      pendingTerminal.scrollback === DEFAULT_TERMINAL_SETTINGS.scrollback &&
      pendingTerminal.line_height === DEFAULT_TERMINAL_SETTINGS.line_height &&
      pendingTerminal.letter_spacing ===
        DEFAULT_TERMINAL_SETTINGS.letter_spacing &&
      pendingTerminal.cursor_blink ===
        DEFAULT_TERMINAL_SETTINGS.cursor_blink &&
      pendingTerminal.font_ligatures ===
        DEFAULT_TERMINAL_SETTINGS.font_ligatures &&
      pendingTerminal.renderer === DEFAULT_TERMINAL_SETTINGS.renderer &&
      pendingTerminal.hide_tmux_status ===
        DEFAULT_TERMINAL_SETTINGS.hide_tmux_status
  );
  const xtermOnlyControlsEnabled = $derived(rendererDraft === "xterm");
  const canSave = $derived(!saving && isDirty);
  const localMonospaceFonts = $derived.by(() => {
    if (!localFonts) return [];
    const fonts: FontData[] = [];
    for (const font of localFonts) {
      if (!isLikelyMonospaceFont(font)) continue;
      if (fonts.some((existing) => existing.family === font.family)) continue;
      fonts.push(font);
    }
    return fonts.sort((left, right) =>
      left.family.localeCompare(right.family),
    );
  });
  const supportsLocalFontPicker = $derived(
    typeof window !== "undefined" &&
      typeof (window as Window & {
        queryLocalFonts?: () => Promise<FontData[]>;
      }).queryLocalFonts === "function",
  );

  function syncDraftFromTerminal(value: TerminalSettingsType): void {
    fontFamilyDraft = value.font_family;
    fontSizeDraft = value.font_size;
    scrollbackDraft = value.scrollback;
    lineHeightDraft = value.line_height;
    letterSpacingDraft = value.letter_spacing;
    cursorBlinkDraft = value.cursor_blink;
    fontLigaturesDraft = value.font_ligatures;
    rendererDraft = value.renderer;
    hideTmuxStatusDraft = value.hide_tmux_status;
  }

  $effect(() => {
    if (draftReady) return;
    syncDraftFromTerminal(terminal);
    if (livePreview) {
      livePreviewBaseline = currentTerminal;
    }
    draftReady = true;
  });

  $effect(() => {
    if (!draftReady) return;
    if (!livePreview) return;
    settingsStore.setTerminalSettings(pendingTerminal);
  });

  onDestroy(() => {
    if (!livePreview) return;
    if (saving) return;
    settingsStore.setTerminalSettings(livePreviewBaseline ?? currentTerminal);
  });

  async function loadLocalFonts(): Promise<void> {
    if (!supportsLocalFontPicker) {
      fontLoadError = "Local font access is not available in this browser.";
      return;
    }
    loadingFonts = true;
    fontLoadError = null;
    try {
      const queryLocalFonts = (window as unknown as Window & {
        queryLocalFonts: () => Promise<FontData[]>;
      }).queryLocalFonts;
      localFonts = await queryLocalFonts();
    } catch (err) {
      fontLoadError = err instanceof Error ? err.message : String(err);
    } finally {
      loadingFonts = false;
    }
  }

  function openFontDialog(): void {
    fontDialogOpen = true;
    if (localFonts === null) {
      void loadLocalFonts();
    }
  }

  function selectFontFamily(family: string): void {
    fontFamilyDraft = replacePreferredFontFamily(fontFamilyDraft, family);
    fontDialogOpen = false;
  }

  async function save(): Promise<void> {
    if (embedded) return;
    if (!isDirty) return;

    saving = true;
    onSavingChange?.(true);
    try {
      const settings = await updateSettings({ terminal: pendingTerminal });
      const updated = settings.terminal;
      syncDraftFromTerminal(updated);
      if (livePreview) {
        livePreviewBaseline = updated;
      }
      onUpdate(updated);
      settingsStore.setTerminalSettings(updated);
    } catch (err) {
      syncDraftFromTerminal(currentTerminal);
      if (livePreview) {
        settingsStore.setTerminalSettings(currentTerminal);
      }
      console.warn("Failed to save terminal settings:", err);
    } finally {
      saving = false;
      onSavingChange?.(false);
    }
  }

  function reset(): void {
    syncDraftFromTerminal(DEFAULT_TERMINAL_SETTINGS);
  }

</script>

<div
  class:compact
  class="terminal-settings"
>
  <label class="font-field" for="terminal-font-family">
    <span class="setting-label">Monospace font family</span>
    <div class="font-row">
      <input
        id="terminal-font-family"
        class="font-input"
        type="text"
        bind:value={fontFamilyDraft}
        placeholder='"JetBrains Mono", "SF Mono", Menlo, Consolas, monospace'
        disabled={saving}
      />
      <button
        class="choose-btn"
        type="button"
        disabled={saving}
        onclick={openFontDialog}
      >
        Choose
      </button>
    </div>
  </label>

  <div class="control-grid">
    <label class="control-field" for="terminal-font-size">
      <span class="setting-label">Font size</span>
      <input
        id="terminal-font-size"
        class="number-input"
        type="number"
        min="8"
        max="32"
        step="1"
        bind:value={fontSizeDraft}
        disabled={saving}
      />
    </label>

    <label class="control-field" for="terminal-line-height">
      <span class="setting-label">Line height</span>
      <input
        id="terminal-line-height"
        class="number-input"
        type="number"
        min="0.8"
        max="2"
        step="0.05"
        bind:value={lineHeightDraft}
        disabled={saving || !xtermOnlyControlsEnabled}
      />
    </label>

    <label class="control-field" for="terminal-scrollback">
      <span class="setting-label">Scrollback</span>
      <input
        id="terminal-scrollback"
        class="number-input"
        type="number"
        min="100"
        max="100000"
        step="100"
        bind:value={scrollbackDraft}
        disabled={saving}
      />
    </label>

    <label class="control-field" for="terminal-letter-spacing">
      <span class="setting-label">Letter spacing</span>
      <input
        id="terminal-letter-spacing"
        class="number-input"
        type="number"
        min="-2"
        max="8"
        step="1"
        bind:value={letterSpacingDraft}
        disabled={saving || !xtermOnlyControlsEnabled}
      />
    </label>

    <div class="renderer-field">
      <span class="setting-label">Terminal renderer</span>
      <SelectDropdown
        class="renderer-dropdown"
        value={rendererDraft}
        options={rendererOptions}
        onchange={(value) => {
          rendererDraft = value as TerminalSettingsType["renderer"];
        }}
        title="Terminal renderer"
        disabled={saving}
      />
    </div>
  </div>

  <label class="toggle-field">
    <input
      type="checkbox"
      bind:checked={cursorBlinkDraft}
      disabled={saving}
    />
    <span>Cursor blink</span>
  </label>

  <label class="toggle-field">
    <input
      type="checkbox"
      bind:checked={fontLigaturesDraft}
      disabled={saving || !xtermOnlyControlsEnabled}
    />
    <span>Font ligatures</span>
  </label>

  <label class="toggle-field">
    <input
      type="checkbox"
      bind:checked={hideTmuxStatusDraft}
      disabled={saving}
    />
    <span>Hide tmux status line in new sessions</span>
  </label>

  <div class="setting-actions">
    <p class="setting-help">
      {#if xtermOnlyControlsEnabled}
        Leave the font blank to use the app default monospace stack.
      {:else}
        ghostty-web does not expose line height, letter spacing, or ligature controls.
      {/if}
    </p>
    <div class="button-row">
      <button
        class="save-btn"
        type="button"
        disabled={!canSave}
        onclick={() => void save()}
      >
        {saving ? "Saving..." : "Save"}
      </button>
      <button
        class="reset-btn"
        type="button"
        disabled={saving || isDefaultDraft}
        onclick={reset}
      >
        Reset
      </button>
    </div>
  </div>
</div>

{#if fontDialogOpen}
  <div
    class="font-dialog-backdrop"
    role="presentation"
    onclick={(event) => {
      if (event.currentTarget === event.target) fontDialogOpen = false;
    }}
  >
    <div
      class="font-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="terminal-font-dialog-title"
    >
      <div class="font-dialog-header">
        <h2 id="terminal-font-dialog-title">Choose monospace font</h2>
        <button
          class="close-btn"
          type="button"
          aria-label="Close font picker"
          onclick={() => {
            fontDialogOpen = false;
          }}
        >
          <XIcon size="14" strokeWidth="2" aria-hidden="true" />
        </button>
      </div>

      <div class="font-section">
        <div class="font-section-title">Common fonts</div>
        <div class="font-list">
          {#each commonMonospaceFonts as family (family)}
            <button
              class="font-option"
              type="button"
              style:font-family={quoteFontFamily(family)}
              onclick={() => selectFontFamily(family)}
            >
              <span>{family}</span>
              <code>abc 123</code>
            </button>
          {/each}
        </div>
      </div>

      <div class="font-section">
        <div class="font-section-title">Local monospace fonts</div>
        {#if loadingFonts}
          <p class="font-state">Loading local fonts...</p>
        {:else if fontLoadError}
          <p class="font-state error">{fontLoadError}</p>
          {#if supportsLocalFontPicker}
            <button
              class="retry-fonts-btn"
              type="button"
              onclick={() => void loadLocalFonts()}
            >
              Try again
            </button>
          {/if}
        {:else if localMonospaceFonts.length > 0}
          <div class="font-list local">
            {#each localMonospaceFonts as font (font.family)}
              <button
                class="font-option"
                type="button"
                style:font-family={quoteFontFamily(font.family)}
                onclick={() => selectFontFamily(font.family)}
              >
                <span>{font.family}</span>
                <code>{font.style || "Regular"}</code>
              </button>
            {/each}
          </div>
        {:else}
          <p class="font-state">
            No local monospace fonts were found. You can still type a font
            family manually.
          </p>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .terminal-settings {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .terminal-settings.compact {
    width: 340px;
  }

  .font-field,
  .renderer-field,
  .control-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .setting-label {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    font-weight: 600;
  }

  .font-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 6px;
  }

  .control-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
  }

  .font-input,
  .number-input {
    width: 100%;
    height: 28px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    padding: 0 8px;
  }

  .font-input {
    font-family: var(--font-mono);
  }

  .renderer-field :global(.renderer-dropdown) {
    width: 100%;
    min-width: 0;
  }

  .toggle-field {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .setting-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .button-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .setting-help {
    margin: 0;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    line-height: 1.4;
  }

  .save-btn,
  .reset-btn,
  .choose-btn,
  .retry-fonts-btn {
    height: 28px;
    padding: 0 10px;
    font-size: var(--font-size-sm);
    font-weight: 600;
    border-radius: var(--radius-sm);
    transition: background 0.12s, color 0.12s, opacity 0.12s,
      border-color 0.12s;
  }

  .save-btn {
    color: white;
    background: var(--accent-blue);
  }

  .save-btn:hover:not(:disabled) {
    opacity: 0.9;
  }

  .save-btn:disabled,
  .reset-btn:disabled,
  .choose-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .reset-btn,
  .choose-btn,
  .retry-fonts-btn {
    color: var(--text-secondary);
    border: 1px solid var(--border-muted);
    background: var(--bg-surface);
  }

  .reset-btn:hover:not(:disabled),
  .choose-btn:hover:not(:disabled),
  .retry-fonts-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .font-dialog-backdrop {
    position: fixed;
    inset: 0;
    z-index: 70;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 20px;
    background: color-mix(in srgb, black 48%, transparent);
  }

  .font-dialog {
    width: min(560px, 100%);
    max-height: min(680px, calc(100vh - 40px));
    overflow: hidden;
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 16px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: var(--shadow-lg);
  }

  .font-dialog-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .font-dialog-header h2 {
    margin: 0;
    font-size: var(--font-size-lg);
    font-weight: 700;
  }

  .close-btn {
    width: 26px;
    height: 26px;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
  }

  .close-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .font-section {
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .font-section-title {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .font-list {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
    min-height: 0;
  }

  .font-list.local {
    overflow-y: auto;
    max-height: 240px;
    padding-right: 2px;
  }

  .font-option {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    height: 34px;
    padding: 0 10px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    text-align: left;
  }

  .font-option:hover,
  .font-option:focus-visible {
    border-color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 9%, var(--bg-primary));
    outline: none;
  }

  .font-option span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .font-option code {
    flex-shrink: 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .font-state {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    line-height: 1.5;
  }

  .font-state.error {
    color: var(--accent-red);
  }

  @media (max-width: 620px) {
    .terminal-settings.compact {
      width: min(340px, calc(100vw - 32px));
    }

    .control-grid,
    .font-list {
      grid-template-columns: 1fr;
    }
  }
</style>
