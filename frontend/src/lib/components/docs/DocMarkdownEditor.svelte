<script lang="ts">
  import { onDestroy } from "svelte";
  import {
    EditorView,
    keymap,
    lineNumbers,
    highlightActiveLine,
    drawSelection,
  } from "@codemirror/view";
  import { EditorState, type Extension } from "@codemirror/state";
  import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
  import { markdown } from "@codemirror/lang-markdown";
  import {
    autocompletion,
    closeBrackets,
    closeBracketsKeymap,
    completionKeymap,
    type CompletionSource,
  } from "@codemirror/autocomplete";

  interface Props {
    initialValue: string;
    onSave?: (value: string) => void;
    onCancel?: () => void;
    onChange?: (value: string, dirty: boolean) => void;
    extraExtensions?: Extension[];
    completionSources?: CompletionSource[];
    placeholder?: string;
  }

  let {
    initialValue,
    onSave,
    onCancel,
    onChange,
    extraExtensions = [],
    completionSources = [],
    placeholder = "",
  }: Props = $props();

  let host: HTMLDivElement | null = $state(null);
  let view: EditorView | null = null;
  let lastInitial = "";

  $effect(() => {
    if (!host) return;
    lastInitial = initialValue;
    const baseExtensions: Extension[] = [
      lineNumbers(),
      history(),
      closeBrackets(),
      autocompletion({
        closeOnBlur: false,
        ...(completionSources.length > 0 ? { override: completionSources } : {}),
      }),
      highlightActiveLine(),
      drawSelection(),
      markdown(),
      EditorView.lineWrapping,
      keymap.of([
        ...closeBracketsKeymap,
        ...defaultKeymap,
        ...historyKeymap,
        ...completionKeymap,
        indentWithTab,
        {
          key: "Mod-s",
          preventDefault: true,
          run: () => {
            triggerSave();
            return true;
          },
        },
        {
          key: "Escape",
          preventDefault: true,
          run: () => {
            onCancel?.();
            return true;
          },
        },
      ]),
      EditorView.updateListener.of((update) => {
        if (!update.docChanged) return;
        const value = update.state.doc.toString();
        onChange?.(value, value !== lastInitial);
      }),
      EditorView.theme(
        {
          "&": { height: "100%", fontSize: "var(--font-size-md)" },
          ".cm-scroller": {
            fontFamily: "var(--font-mono)",
            lineHeight: "1.55",
          },
          ".cm-content": { padding: "12px 4px" },
          ".cm-gutters": {
            backgroundColor: "transparent",
            borderRight: "1px solid var(--border-hairline)",
            color: "var(--text-faint)",
          },
          ".cm-activeLine": { backgroundColor: "var(--bg-surface-hover)" },
          ".cm-activeLineGutter": { backgroundColor: "transparent" },
          ".cm-cursor, .cm-dropCursor": {
            borderLeftWidth: "1ch",
            borderLeftColor: "var(--text-primary)",
          },
          // drawSelection paints selection backgrounds via these classes
          // instead of the native browser highlight. The base theme is
          // registered as { dark: false }, so CodeMirror's default picks
          // a light-mode background that disappears under app dark mode.
          // Tie the colors to app theme variables so the selection stays
          // legible in both themes; --accent-blue-soft already varies by
          // theme in app.css and the foreground sticks with --text-primary.
          ".cm-selectionBackground, ::selection": {
            backgroundColor: "var(--accent-blue-soft)",
          },
          "&.cm-focused .cm-selectionBackground": {
            backgroundColor: "var(--accent-blue-soft)",
          },
          ".cm-tooltip.cm-tooltip-autocomplete": {
            border: "1px solid var(--border-default)",
            background: "var(--bg-elevated)",
            color: "var(--text-primary)",
            borderRadius: "var(--radius-md)",
            boxShadow: "var(--shadow-md)",
          },
          ".cm-tooltip-autocomplete ul li[aria-selected]": {
            background: "var(--accent-blue-soft)",
            color: "var(--text-primary)",
          },
        },
        { dark: false },
      ),
    ];
    view = new EditorView({
      state: EditorState.create({
        doc: initialValue,
        extensions: [...baseExtensions, ...extraExtensions],
      }),
      parent: host,
    });
    if (placeholder && !initialValue) {
      // Visual hint via CSS only — no extension needed for v1.
      host.dataset.placeholder = placeholder;
    }
    return () => {
      view?.destroy();
      view = null;
    };
  });

  onDestroy(() => {
    view?.destroy();
    view = null;
  });

  function triggerSave() {
    if (!view) return;
    const value = view.state.doc.toString();
    // Don't optimistically mark the buffer clean here — the save is
    // async and may fail. The parent unmounts/re-initializes the
    // editor on success, which is what flips `lastInitial`; on
    // failure the dirty indicator must keep showing.
    onSave?.(value);
  }

  export function focus(): void {
    view?.focus();
  }

  export function currentValue(): string {
    return view?.state.doc.toString() ?? initialValue;
  }
</script>

<div class="editor-host" bind:this={host}></div>

<style>
  .editor-host {
    height: 100%;
    overflow: hidden;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
  }

  .editor-host :global(.cm-editor) {
    height: 100%;
  }

  .editor-host :global(.cm-editor.cm-focused) {
    outline: none;
    border-color: var(--accent-blue);
  }
</style>
