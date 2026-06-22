<script lang="ts">
  import Modal from "./DocsModal.svelte";
  import { renderDocsMarkdown, type DocsMarkdownOptions } from "../../api/docs/markdown";

  export interface HeadingEntry {
    id: string;
    level: number;
    text: string;
  }

  export interface DocMarkdownState {
    headings: HeadingEntry[];
    activeId: string | null;
  }

  interface Props {
    source: string;
    options: DocsMarkdownOptions;
    onState?: (state: DocMarkdownState) => void;
    onSelectDoc?: (relPath: string, anchor?: string) => void;
    onSelectIssue?: (uid: string) => void;
    onSelectKataShortId?: (shortId: string, project?: string) => void;
    // Bind to scroll the doc to a specific heading. Set by parent when
    // route includes an anchor, or when the user clicks an outline entry.
    scrollToAnchor?: string | null;
    // Fired once a scroll for the current scrollToAnchor has been
    // attempted, so the parent can treat the anchor as one-shot and not
    // re-apply it to a later document that has the same heading id.
    onAnchorConsumed?: () => void;
  }

  let {
    source,
    options,
    onState,
    onSelectDoc,
    onSelectIssue,
    onSelectKataShortId,
    scrollToAnchor = null,
    onAnchorConsumed,
  }: Props = $props();

  let container: HTMLDivElement | null = $state(null);
  let observer: IntersectionObserver | null = null;
  let activeId: string | null = $state(null);
  let ambiguous: { candidates: string[]; anchor?: string } | null = $state(null);

  let html = $derived(renderDocsMarkdown(source, options));
  // Heading scan runs over the rendered HTML string, not the DOM, so the
  // outline is in sync with what's about to be rendered without waiting
  // for a paint or microtask. IntersectionObserver still attaches once
  // the container is live (see effect below).
  let headings: HeadingEntry[] = $derived(extractHeadings(html));

  $effect(() => {
    void html;
    if (!container) return;
    activeId = headings[0]?.id ?? null;
    onState?.({ headings, activeId });
    attachObserver(container);
  });

  $effect(() => {
    const id = scrollToAnchor;
    if (!container || !id) return;
    queueMicrotask(() => {
      scrollHeadingIntoView(id);
      onAnchorConsumed?.();
    });
  });

  $effect(() => {
    return () => {
      observer?.disconnect();
      observer = null;
    };
  });

  function extractHeadings(rawHtml: string): HeadingEntry[] {
    const result: HeadingEntry[] = [];
    const headingRe = /<h([1-6])\s+id="([^"]+)"[^>]*>([\s\S]*?)<\/h[1-6]>/g;
    let match: RegExpExecArray | null;
    while ((match = headingRe.exec(rawHtml)) !== null) {
      const level = Number.parseInt(match[1]!, 10);
      const id = decodeHtml(match[2]!);
      const text = decodeHtml(match[3]!.replace(/<[^>]+>/g, "")).trim();
      result.push({ id, level, text });
    }
    return result;
  }

  function decodeHtml(value: string): string {
    return value
      .replace(/&lt;/g, "<")
      .replace(/&gt;/g, ">")
      .replace(/&quot;/g, '"')
      .replace(/&#39;/g, "'")
      .replace(/&amp;/g, "&");
  }

  function attachObserver(root: HTMLElement) {
    observer?.disconnect();
    if (typeof IntersectionObserver === "undefined") return;
    // Trigger when a heading crosses the top quarter of the viewport so
    // the outline highlight tracks the section the reader is currently in
    // rather than the one being scrolled past.
    observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            activeId = (entry.target as HTMLElement).id;
            onState?.({ headings, activeId });
            return;
          }
        }
      },
      { rootMargin: "0px 0px -75% 0px", threshold: 0 },
    );
    for (const el of root.querySelectorAll<HTMLHeadingElement>("h1,h2,h3,h4,h5,h6")) {
      observer.observe(el);
    }
  }

  function scrollHeadingIntoView(id: string) {
    if (!container) return;
    const el = container.querySelector<HTMLElement>(`#${escapeCssIdent(id)}`);
    // Instant scroll — smooth scrolling stalls perceived response on
    // long documents and the heading is already visually targeted, so
    // there's nothing for the animation to communicate.
    el?.scrollIntoView({ behavior: "auto", block: "start" });
  }

  function escapeCssIdent(value: string): string {
    if (typeof CSS !== "undefined" && typeof CSS.escape === "function") {
      return CSS.escape(value);
    }
    return value.replace(/[^a-zA-Z0-9_-]/g, "\\$&");
  }

  function handleClick(event: MouseEvent) {
    const target = event.target;
    if (!(target instanceof Element)) return;
    const anchor = target.closest("a");
    if (!anchor) return;

    const wikilinkKind = anchor.getAttribute("data-wikilink");
    const docLink = anchor.getAttribute("data-doc-link");
    const linkAnchor = anchor.getAttribute("data-anchor") ?? undefined;
    const kataLink = anchor.getAttribute("data-kata-link");
    const anchorLink = anchor.getAttribute("data-anchor-link");
    const kataShortId = anchor.getAttribute("data-kata-short-id");
    const kataProject = anchor.getAttribute("data-kata-project") ?? undefined;

    if (wikilinkKind === "ambiguous" && docLink) {
      event.preventDefault();
      ambiguous = linkAnchor
        ? { candidates: docLink.split("|"), anchor: linkAnchor }
        : { candidates: docLink.split("|") };
      return;
    }
    if (docLink) {
      event.preventDefault();
      onSelectDoc?.(docLink, linkAnchor);
      return;
    }
    if (kataShortId) {
      event.preventDefault();
      onSelectKataShortId?.(kataShortId, kataProject);
      return;
    }
    if (kataLink === "issue") {
      event.preventDefault();
      // The renderer parks the UID in data-kata-uid so the scheme
      // doesn't have to pass DOMPurify's URI allowlist.
      const uid = anchor.getAttribute("data-kata-uid")
        ?? (anchor.getAttribute("href") ?? "").replace(/^kata:\/\/issue\//, "");
      if (uid) onSelectIssue?.(uid);
      return;
    }
    if (anchorLink) {
      event.preventDefault();
      const id = (anchor.getAttribute("href") ?? "").slice(1);
      if (id) scrollHeadingIntoView(id);
      return;
    }
  }

  function chooseAmbiguous(path: string) {
    const anchor = ambiguous?.anchor;
    ambiguous = null;
    onSelectDoc?.(path, anchor);
  }

  function closeAmbiguous() {
    ambiguous = null;
  }
</script>

<!--
  Click delegation is the only practical way to intercept anchors inside
  the {@html ...} payload — those links don't exist in the Svelte tree
  for direct listeners. Anchors inside still get keyboard activation
  via the browser default for <a> elements.
-->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div class="doc-markdown" bind:this={container} onclick={handleClick} role="document">
  {@html html}
</div>

<Modal open={ambiguous !== null} title="Pick a note" onClose={closeAmbiguous} width={420}>
  <p class="picker-hint">Multiple notes share this name. Choose one:</p>
  <ul class="picker-list">
    {#each ambiguous?.candidates ?? [] as candidate (candidate)}
      <li>
        <button class="picker-row" type="button" onclick={() => chooseAmbiguous(candidate)}>
          {candidate}
        </button>
      </li>
    {/each}
  </ul>
</Modal>

<style>
  .doc-markdown {
    color: var(--text-primary);
    /* 16px matches IssueDetail body so editorial prose reads at the
       same size across Tasks and Docs. Held as a literal (not --fs-md,
       which is chrome-scale 14px) for the same reason called out in
       app.css. */
    font-size: var(--font-size-lg);
    line-height: 1.65;
  }

  .doc-markdown :global(h1),
  .doc-markdown :global(h2),
  .doc-markdown :global(h3),
  .doc-markdown :global(h4),
  .doc-markdown :global(h5),
  .doc-markdown :global(h6) {
    line-height: 1.25;
    letter-spacing: -0.01em;
    color: var(--text-primary);
    scroll-margin-top: 16px;
  }

  .doc-markdown :global(h1) { font-size: var(--font-size-4xl); margin: 0 0 12px; font-weight: 700; }
  .doc-markdown :global(h2) { font-size: var(--font-size-3xl); margin: 22px 0 10px; font-weight: 600; }
  .doc-markdown :global(h3) { font-size: var(--font-size-doc-h3); margin: 18px 0 8px; font-weight: 600; }
  .doc-markdown :global(h4) { font-size: var(--font-size-doc-h4); margin: 14px 0 6px; font-weight: 600; }
  .doc-markdown :global(h5),
  .doc-markdown :global(h6) {
    font-size: var(--font-size-doc-body);
    margin: 12px 0 4px;
    font-weight: 600;
    color: var(--text-secondary);
  }

  .doc-markdown :global(p) {
    margin: 0 0 12px;
  }

  .doc-markdown :global(ul),
  .doc-markdown :global(ol) {
    margin: 0 0 12px;
    padding-left: 22px;
  }

  .doc-markdown :global(li + li) {
    margin-top: 4px;
  }

  .doc-markdown :global(a) {
    color: var(--accent-blue);
    text-decoration: none;
    border-bottom: 1px solid color-mix(in srgb, var(--accent-blue) 25%, transparent);
    transition: border-color 0.15s, color 0.15s;
  }

  .doc-markdown :global(a:hover) {
    border-bottom-color: var(--accent-blue);
  }

  .doc-markdown :global(.wikilink) {
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    padding: 0 4px;
    border-radius: var(--radius-sm);
    border-bottom: none;
  }

  .doc-markdown :global(.wikilink:hover) {
    background: color-mix(in srgb, var(--accent-blue) 18%, transparent);
  }

  .doc-markdown :global(.wikilink--ambiguous) {
    border-bottom: 1px dashed var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 6%, transparent);
  }

  .doc-markdown :global(.wikilink--missing) {
    color: var(--text-muted);
    background: var(--bg-inset);
    padding: 0 4px;
    border-radius: var(--radius-sm);
    text-decoration: line-through;
    cursor: not-allowed;
  }

  .doc-markdown :global(.kata-link) {
    color: var(--accent-blue);
    text-decoration: none;
    padding: 0 4px;
    border-radius: var(--radius-sm);
    background: var(--accent-blue-soft);
    font-family: var(--font-mono);
    font-size: var(--font-size-prose-small);
    cursor: pointer;
  }

  .doc-markdown :global(.kata-link:hover) {
    background: var(--accent-blue-line);
  }

  .doc-markdown :global(.kata-mention) {
    color: var(--accent-purple);
    background: var(--accent-blue-soft);
    padding: 0 4px;
    border-radius: var(--radius-sm);
    font-family: var(--font-mono);
    font-size: var(--font-size-prose-small);
  }

  .doc-markdown :global(code) {
    padding: 1px 5px;
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    font-family: var(--font-mono);
    font-size: var(--font-size-prose-small);
  }

  .doc-markdown :global(pre) {
    margin: 0 0 14px;
    padding: 12px 14px;
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    overflow-x: auto;
    font-family: var(--font-mono);
    font-size: var(--font-size-prose-small);
    line-height: 1.5;
  }

  .doc-markdown :global(pre code) {
    background: transparent;
    padding: 0;
    font-size: inherit;
  }

  .doc-markdown :global(blockquote) {
    margin: 0 0 12px;
    padding: 4px 0 4px 14px;
    border-left: 3px solid var(--border-default);
    color: var(--text-secondary);
  }

  .doc-markdown :global(details) {
    margin: 0 0 12px;
  }

  .doc-markdown :global(summary) {
    cursor: pointer;
    font-weight: 600;
  }

  .doc-markdown :global(details[open] > summary) {
    margin-bottom: 8px;
  }

  .doc-markdown :global(img) {
    max-width: 100%;
    height: auto;
    border-radius: var(--radius-md);
    margin: 4px 0 16px;
  }

  .doc-markdown :global(table) {
    border-collapse: collapse;
    margin: 0 0 14px;
    font-size: var(--font-size-sm);
  }

  .doc-markdown :global(th),
  .doc-markdown :global(td) {
    padding: 6px 10px;
    border: 1px solid var(--border-muted);
    text-align: left;
  }

  .doc-markdown :global(th) {
    background: var(--bg-inset);
    font-weight: 600;
  }

  .doc-markdown :global(hr) {
    border: none;
    border-top: 1px solid var(--border-muted);
    margin: 22px 0;
  }

  .doc-markdown :global(del) {
    color: var(--text-muted);
  }

  .picker-hint {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    margin-bottom: 10px;
  }

  .picker-list {
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .picker-row {
    width: 100%;
    text-align: left;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-family: var(--font-mono);
    border: 1px solid var(--border-default);
    transition: background 0.1s, border-color 0.1s;
  }

  .picker-row:hover {
    background: var(--accent-blue-soft);
    border-color: var(--accent-blue);
    color: var(--accent-blue);
  }
</style>
