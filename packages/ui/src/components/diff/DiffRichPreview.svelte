<script lang="ts">
  import type { DiffFile, FilePreview } from "../../api/types.js";
  import { getStores } from "../../context.js";
  import type { DiffViewMode } from "../../stores/diff.svelte.js";
  import { renderMarkdownDiff, renderMarkdownSplitDiff } from "../../utils/markdown-diff.js";
  import { renderMarkdown } from "../../utils/markdown.js";

  interface Props {
    file: DiffFile;
    provider: string;
    platformHost?: string | undefined;
    owner: string;
    name: string;
    repoPath: string;
    number: number;
    active: boolean;
    viewMode?: DiffViewMode;
  }

  const { file, provider, platformHost, owner, name, repoPath, number, active, viewMode = "unified" }: Props = $props();
  const { diff: diffStore } = getStores();

  interface MarkdownComparison {
    diffHtml?: string;
    oldHtml: string;
    newHtml: string;
  }

  let loading = $state(false);
  let error = $state<string | null>(null);
  let preview = $state<FilePreview | null>(null);
  let requestVersion = 0;

  const isMarkdownFile = $derived(isMarkdownPath(file.path));
  const markdownComparison = $derived.by(() =>
    active && isMarkdownFile ? buildMarkdownComparison(file, viewMode) : null,
  );
  const text = $derived(preview ? decodeText(preview.content) : "");
  const dataURL = $derived(preview ? `data:${preview.media_type};base64,${preview.content}` : "");
  const kind = $derived(previewKind(file.path, preview?.media_type ?? ""));
  const displayText = $derived(formatText(file.path, text));

  $effect(() => {
    const sourceFile = file;
    if (!active || isMarkdownFile) return;
    const version = ++requestVersion;
    loading = true;
    error = null;
    preview = null;
    void diffStore.loadFilePreview(owner, name, number, sourceFile.path)
      .then((result) => {
        if (version !== requestVersion) return;
        preview = result;
      })
      .catch((err: unknown) => {
        if (version !== requestVersion) return;
        error = err instanceof Error ? err.message : String(err);
      })
      .finally(() => {
        if (version === requestVersion) loading = false;
      });
  });

  function decodeText(content: string): string {
    const binary = atob(content);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) {
      bytes[i] = binary.charCodeAt(i);
    }
    return new TextDecoder("utf-8", { fatal: false }).decode(bytes);
  }

  function isMarkdownPath(path: string): boolean {
    return [".md", ".markdown", ".mdown", ".mkd"].includes(extension(path));
  }

  function extension(path: string): string {
    const idx = path.lastIndexOf(".");
    return idx >= 0 ? path.slice(idx).toLowerCase() : "";
  }

  function previewKind(
    path: string,
    mediaType: string,
  ): "markdown" | "image" | "pdf" | "text" | "unsupported" {
    const ext = extension(path);
    if (mediaType.startsWith("image/")) return "image";
    if (mediaType === "application/pdf") return "pdf";
    if (
      mediaType.includes("markdown") ||
      [".md", ".markdown", ".mdown", ".mkd"].includes(ext)
    ) return "markdown";
    if (
      mediaType.startsWith("text/") ||
      mediaType.includes("json") ||
      mediaType.includes("yaml") ||
      mediaType.includes("toml") ||
      [".css", ".csv", ".html", ".js", ".jsx", ".ts", ".tsx", ".xml"].includes(ext)
    ) return "text";
    return "unsupported";
  }

  function formatText(path: string, value: string): string {
    if (extension(path) !== ".json") return value;
    try {
      return `${JSON.stringify(JSON.parse(value), null, 2)}\n`;
    } catch {
      return value;
    }
  }

  function buildMarkdownComparison(source: DiffFile, mode: DiffViewMode): MarkdownComparison {
    const oldLines: string[] = [];
    const newLines: string[] = [];
    for (const hunk of source.hunks) {
      if (oldLines.length > 0 || newLines.length > 0) {
        oldLines.push("", "---", "");
        newLines.push("", "---", "");
      }
      for (const line of hunk.lines) {
        if (line.type !== "add") oldLines.push(line.content);
        if (line.type !== "delete") newLines.push(line.content);
      }
    }
    const repo = { provider, platformHost, owner, name, repoPath };
    const oldHtml = renderMarkdown(`${oldLines.join("\n")}\n`, repo);
    const newHtml = renderMarkdown(`${newLines.join("\n")}\n`, repo);
    const splitHtml = mode === "split" ? renderMarkdownSplitDiff(oldHtml, newHtml) : null;
    return {
      ...(mode === "split" ? {} : { diffHtml: renderMarkdownDiff(oldHtml, newHtml) }),
      oldHtml: splitHtml?.beforeHtml ?? oldHtml,
      newHtml: splitHtml?.afterHtml ?? newHtml,
    };
  }
</script>

<div class="preview-shell">
  {#if isMarkdownFile}
    {#if markdownComparison}
      {#if viewMode === "split"}
        <div class="diff-rich-preview markdown-rich-diff markdown-rich-diff--split">
          <div
            class="markdown-rich-diff__pane markdown-rich-diff__block--delete"
            aria-label="Before markdown preview"
          >
            <div class="markdown-rich-diff__label">Before</div>
            <div class="markdown-body">
              {@html markdownComparison.oldHtml}
            </div>
          </div>
          <div
            class="markdown-rich-diff__pane markdown-rich-diff__block--add"
            aria-label="After markdown preview"
          >
            <div class="markdown-rich-diff__label">After</div>
            <div class="markdown-body">
              {@html markdownComparison.newHtml}
            </div>
          </div>
        </div>
      {:else}
        <div class="diff-rich-preview markdown-rich-diff markdown-rich-diff--unified markdown-body">
          {@html markdownComparison.diffHtml ?? markdownComparison.newHtml}
        </div>
      {/if}
    {:else}
      <div class="preview-state">Loading preview</div>
    {/if}
  {:else if loading}
    <div class="preview-state">Loading preview</div>
  {:else if error}
    <div class="preview-state preview-state--error">{error}</div>
  {:else if preview}
    {#if kind === "markdown"}
      <div class="diff-rich-preview markdown-body">
        {@html renderMarkdown(text, { provider, platformHost, owner, name, repoPath })}
      </div>
    {:else if kind === "image"}
      <div class="diff-image-preview">
        <img src={dataURL} alt={file.path} />
      </div>
    {:else if kind === "pdf"}
      <object
        class="diff-object-preview"
        data={dataURL}
        type={preview.media_type}
        aria-label={`${file.path} preview`}
      >
        <a href={dataURL}>Open PDF preview</a>
      </object>
    {:else if kind === "text"}
      <pre class="diff-text-preview">{displayText}</pre>
    {:else}
      <div class="preview-state">No rich preview for {preview.media_type}</div>
    {/if}
  {/if}
</div>

<style>
  .preview-shell {
    min-height: 140px;
    background: var(--bg-surface);
  }

  .preview-state {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 140px;
    padding: 20px;
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  .preview-state--error {
    color: var(--accent-red);
  }

  .diff-rich-preview {
    max-width: 920px;
    padding: 24px 32px 36px;
    color: var(--text-primary);
  }

  .markdown-rich-diff--split {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 12px;
    max-width: 1180px;
  }

  .markdown-rich-diff__pane {
    min-width: 0;
    padding: 12px 14px 18px;
    border: 1px solid var(--diff-border);
    border-radius: 6px;
  }

  .markdown-rich-diff__label {
    margin-bottom: 10px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: uppercase;
  }

  .markdown-rich-diff__block--add {
    background: color-mix(in srgb, var(--diff-add-bg) 76%, transparent);
    border-color: color-mix(in srgb, var(--diff-add-text) 42%, var(--diff-border));
  }

  .markdown-rich-diff__block--delete {
    background: color-mix(in srgb, var(--diff-del-bg) 78%, transparent);
    border-color: color-mix(in srgb, var(--diff-del-text) 42%, var(--diff-border));
  }

  .markdown-rich-diff__block--add :global(*) {
    color: var(--text-primary);
  }

  .markdown-rich-diff__block--delete :global(*) {
    color: var(--text-primary);
  }

  .markdown-rich-diff--split :global(.markdown-diff__placeholder) {
    visibility: hidden;
    pointer-events: none;
  }

  .markdown-rich-diff__block--delete :global(p),
  .markdown-rich-diff__block--delete :global(li),
  .markdown-rich-diff__block--delete :global(h1),
  .markdown-rich-diff__block--delete :global(h2),
  .markdown-rich-diff__block--delete :global(h3),
  .markdown-rich-diff__block--delete :global(h4),
  .markdown-rich-diff__block--delete :global(h5),
  .markdown-rich-diff__block--delete :global(h6) {
    text-decoration: line-through;
    text-decoration-color: color-mix(in srgb, var(--diff-del-text) 70%, transparent);
  }

  .markdown-rich-diff--unified {
    max-width: 920px;
  }

  .markdown-rich-diff--unified :global(ins),
  .markdown-rich-diff--unified :global(del) {
    padding: 0 0.16em;
    border-radius: 3px;
    text-decoration-thickness: 1px;
    text-underline-offset: 0.12em;
  }

  .markdown-rich-diff--unified :global(ins) {
    color: var(--text-primary);
    background: color-mix(in srgb, var(--diff-add-bg) 80%, transparent);
    text-decoration-color: color-mix(in srgb, var(--diff-add-text) 65%, transparent);
  }

  .markdown-rich-diff--unified :global(del) {
    color: var(--text-primary);
    background: color-mix(in srgb, var(--diff-del-bg) 82%, transparent);
    text-decoration-color: color-mix(in srgb, var(--diff-del-text) 70%, transparent);
  }

  .markdown-rich-diff--unified :global(.markdown-diff__block) {
    display: block;
    margin: 0.55rem 0;
    padding: 0.45rem 0.6rem;
    border: 1px solid transparent;
  }

  .markdown-rich-diff--unified :global(ins.markdown-diff__block) {
    border-color: color-mix(in srgb, var(--diff-add-text) 32%, transparent);
  }

  .markdown-rich-diff--unified :global(del.markdown-diff__block) {
    border-color: color-mix(in srgb, var(--diff-del-text) 36%, transparent);
  }

  @media (max-width: 760px) {
    .markdown-rich-diff--split {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .diff-image-preview {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 240px;
    padding: 24px;
    background:
      linear-gradient(45deg, var(--bg-inset) 25%, transparent 25%),
      linear-gradient(-45deg, var(--bg-inset) 25%, transparent 25%),
      linear-gradient(45deg, transparent 75%, var(--bg-inset) 75%),
      linear-gradient(-45deg, transparent 75%, var(--bg-inset) 75%);
    background-color: var(--bg-surface);
    background-position: 0 0, 0 10px, 10px -10px, -10px 0;
    background-size: 20px 20px;
  }

  .diff-image-preview img {
    max-width: min(100%, 960px);
    max-height: 70vh;
    object-fit: contain;
    border: 1px solid var(--border-muted);
    background: var(--bg-surface);
  }

  .diff-object-preview {
    width: 100%;
    height: min(72vh, 900px);
    border: 0;
    background: var(--bg-surface);
  }

  .diff-text-preview {
    margin: 0;
    padding: 18px 22px 28px;
    color: var(--diff-text);
    background: var(--diff-bg);
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    line-height: 1.55;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
</style>
