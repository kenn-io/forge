<script lang="ts">
  import { Checkbox } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import Modal from "../shared/Modal.svelte";
  import { onDestroy, untrack } from "svelte";
  import { DEFAULT_TERMINAL_SETTINGS } from "../../api/types.js";
  import { getStores } from "../../context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import type { TerminalSettings as TerminalSettingsType } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { settingsErrorMessage } from "../../stores/settings-workflow.js";
  import {
    queryLocalFonts,
    supportsLocalFonts,
    type LocalFontData as FontData,
  } from "../../browser/local-fonts.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  import {
    previewTerminalSettings,
    restoreTerminalSettingsPreview,
    saveTerminalSettings,
    terminalSettingsChanges,
  } from "../../stores/terminal-settings-persistence.js";

  interface Props {
    terminal: TerminalSettingsType;
    onUpdate: (terminal: TerminalSettingsType) => void;
    compact?: boolean;
    livePreview?: boolean;
    onSavingChange?: (saving: boolean) => void;
    onFontDialogOpenChange?: (open: boolean) => void;
  }

  const {
    terminal,
    onUpdate,
    compact = false,
    livePreview = false,
    onSavingChange,
    onFontDialogOpenChange,
  }: Props = $props();

  const { settings: settingsStore } = getStores();
  const embedded = isEmbedded();
  const runtime = getAppRuntime();
  let localFontExecution: AppExecution<void, unknown> | null = null;

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
  let hideTmuxStatusDraft = $state(
    DEFAULT_TERMINAL_SETTINGS.hide_tmux_status,
  );
  let retainedSessionsDraft = $state<number | null>(
    DEFAULT_TERMINAL_SETTINGS.retained_sessions,
  );
  let fontDialogOpen = $state(false);
  let localFonts = $state<FontData[] | null>(null);
  let fontLoadError = $state<string | null>(null);
  let loadingFonts = $state(false);
  let livePreviewBaseline = $state<TerminalSettingsType | null>(null);

  $effect(() => {
    onFontDialogOpenChange?.(fontDialogOpen);
  });

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
      hide_tmux_status: hideTmuxStatusDraft,
      retained_sessions:
        retainedSessionsDraft ?? DEFAULT_TERMINAL_SETTINGS.retained_sessions,
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
      pendingTerminal.hide_tmux_status !== currentTerminal.hide_tmux_status ||
      pendingTerminal.retained_sessions !== currentTerminal.retained_sessions
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
      pendingTerminal.hide_tmux_status ===
        DEFAULT_TERMINAL_SETTINGS.hide_tmux_status &&
      pendingTerminal.retained_sessions ===
        DEFAULT_TERMINAL_SETTINGS.retained_sessions
  );
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
    supportsLocalFonts(),
  );

  function syncDraftFromTerminal(value: TerminalSettingsType): void {
    fontFamilyDraft = value.font_family;
    fontSizeDraft = value.font_size;
    scrollbackDraft = value.scrollback;
    lineHeightDraft = value.line_height;
    letterSpacingDraft = value.letter_spacing;
    cursorBlinkDraft = value.cursor_blink;
    fontLigaturesDraft = value.font_ligatures;
    hideTmuxStatusDraft = value.hide_tmux_status;
    retainedSessionsDraft = value.retained_sessions;
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
    const baseline = livePreviewBaseline ?? currentTerminal;
    const preview = pendingTerminal;
    untrack(() => {
      previewTerminalSettings(settingsStore, baseline, preview);
    });
  });

  onDestroy(() => {
    localFontExecution?.interrupt();
    if (!livePreview) return;
    if (saving) return;
    restoreTerminalSettingsPreview(settingsStore);
  });

  function loadLocalFonts(): void {
    if (!supportsLocalFontPicker) {
      fontLoadError = "Local font access is not available in this browser.";
      return;
    }
    loadingFonts = true;
    fontLoadError = null;
    localFontExecution?.interrupt();
    localFontExecution = runtime.runCommand(
      Effect.tryPromise({
        try: (signal) => {
          signal.throwIfAborted();
          return queryLocalFonts();
        },
        catch: (cause) => cause,
      }).pipe(
        Effect.tap((fonts) => Effect.sync(() => {
          localFonts = fonts;
        })),
        Effect.ensuring(Effect.sync(() => {
          loadingFonts = false;
        })),
        Effect.asVoid,
      ),
      {
        operation: "load local terminal fonts",
        safeContext: {},
        onFailure: (error) => {
          fontLoadError = error instanceof Error ? error.message : String(error);
        },
      },
    );
  }

  function openFontDialog(): void {
    fontDialogOpen = true;
    if (localFonts === null) {
      loadLocalFonts();
    }
  }

  function selectFontFamily(family: string): void {
    fontFamilyDraft = replacePreferredFontFamily(fontFamilyDraft, family);
    fontDialogOpen = false;
  }

  function closeFontDialogForEscape(event: KeyboardEvent): void {
    if (!fontDialogOpen || event.key !== "Escape" || event.defaultPrevented) return;
    event.preventDefault();
    fontDialogOpen = false;
  }

  function save(): void {
    if (embedded) return;
    if (!isDirty) return;

    saving = true;
    onSavingChange?.(true);
    const program = saveTerminalSettings({
        baseline: currentTerminal,
        changes: terminalSettingsChanges(currentTerminal, pendingTerminal),
        store: settingsStore,
      }).pipe(
        Effect.tap((updated) =>
          Effect.sync(() => {
            syncDraftFromTerminal(updated);
            if (livePreview) {
              livePreviewBaseline = updated;
            }
            onUpdate(updated);
          }),
        ),
        Effect.ensuring(
          Effect.sync(() => {
            saving = false;
            onSavingChange?.(false);
          }),
        ),
      );
    runtime.runCommand(program, {
      operation: "save terminal settings",
      safeContext: {},
      onFailure: (failure) => {
        syncDraftFromTerminal(currentTerminal);
        showFlash(settingsErrorMessage(failure), { tone: "danger" });
      },
    });
  }

  function reset(): void {
    syncDraftFromTerminal(DEFAULT_TERMINAL_SETTINGS);
  }

</script>

<svelte:window onkeydowncapture={closeFontDialogForEscape} />

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
        disabled={saving}
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
        disabled={saving}
      />
    </label>

    <label class="control-field" for="terminal-retained-sessions">
      <span class="setting-label">Retained terminal sessions</span>
      <input
        id="terminal-retained-sessions"
        class="number-input"
        type="number"
        min="0"
        max="20"
        step="1"
        bind:value={retainedSessionsDraft}
        disabled={saving}
      />
      <span class="field-help">
        Keep recently viewed terminal sessions ready for faster workspace
        switching. Higher values use more memory. Use 0 to disable retention.
      </span>
    </label>

  </div>

  <Checkbox
    class="toggle-field"
    bind:checked={cursorBlinkDraft}
    disabled={saving}
    label="Cursor blink"
  />

  <Checkbox
    class="toggle-field"
    bind:checked={fontLigaturesDraft}
    disabled={saving}
    label="Font ligatures"
  />

  <Checkbox
    class="toggle-field"
    bind:checked={hideTmuxStatusDraft}
    disabled={saving}
    label="Hide tmux status line in new sessions"
  />

  <div class="setting-actions">
    <p class="setting-help">
      Leave the font blank to use the app default monospace stack.
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

<Modal
  open={fontDialogOpen}
  title="Choose monospace font"
  width={560}
  showClose
  onClose={() => (fontDialogOpen = false)}
>
  <div class="font-dialog-content">
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
              onclick={loadLocalFonts}
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
</Modal>

<style>
  .terminal-settings {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .terminal-settings.compact {
    width: 340px;
  }

  .font-field,
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

  .field-help {
    color: var(--text-tertiary);
    font-size: var(--font-size-xs);
    line-height: 1.35;
  }

  .font-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 6px;
  }

  .control-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--space-4);
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

  :global(.toggle-field .kit-checkbox__label) {
    color: var(--text-secondary);
    font-weight: 600;
  }

  .setting-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  /* The help paragraph is the only flexible item here: without this the
     buttons shrink below their label width and "Save"/"Reset" wrap
     mid-word inside the narrow compact popover. */
  .button-row {
    display: flex;
    flex: 0 0 auto;
    margin-left: auto;
    align-items: center;
    gap: 8px;
  }

  .setting-help {
    flex: 1 1 180px;
    min-width: 0;
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
    white-space: nowrap;
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







  .font-dialog-content {
    display: flex;
    flex-direction: column;
    gap: 12px;
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

  @media (max-width: 640px) {
    .terminal-settings.compact {
      width: min(340px, calc(100vw - 32px));
    }

    .control-grid,
    .font-list {
      grid-template-columns: 1fr;
    }
  }
</style>
