<script lang="ts">
  import { onDestroy } from "svelte";
  import type { MessageDetailData } from "../../api/messages/types";
  import type { MessageLinkInput } from "../../messages/messageLinks";
  import type { IssueRef, KataAPI } from "../../messages/types";
  import IssuePickerDialog from "../shared/IssuePickerDialog.svelte";
  import { linkify } from "./linkify";

  interface Props {
    detail: MessageDetailData | null;
    loading: boolean;
    error: string | null;
    permalinkOf: (id: number) => string;
    remoteImageURL: (id: number, token: string, index: string) => string;
    kata?: Pick<KataAPI, "search"> | undefined;
    onLinkMessage?: ((
      issueUid: string,
      input: {
        message_id: number;
        conversation_id?: number;
        subject: string;
        from: string;
        sent_at: string;
      },
    ) => Promise<{ qualified_id: string }>) | undefined;
    reverseLinks?: IssueRef[] | undefined;
    onOpenIssue?: ((uid: string) => void) | undefined;
    // When true, this MessageDetail is rendered inside MessageThread's stack:
    // the subject heading is suppressed (MessageThread's header row carries
    // the subject), and the layout drops the nested scroll markers so
    // MessageThread owns the pane scroll.
    compact?: boolean;
    // v2a: image opt-in + view-mode (wired in T9; forwarded here in T8).
    imagesLoaded?: boolean | undefined;
    onLoadImages?: ((id: number, token: string) => void) | undefined;
    viewMode?: "html" | "text" | undefined;
    onViewModeChange?: ((id: number, mode: "html" | "text") => void) | undefined;
    remoteImageCount?: number | undefined;
    remoteImageToken?: string | undefined;
    htmlSanitizationFailed?: boolean | undefined;
  }

  let {
    detail,
    loading,
    error,
    permalinkOf,
    remoteImageURL,
    kata,
    onLinkMessage,
    reverseLinks,
    onOpenIssue,
    compact = false,
    // v2a props - consumed in T9/T10.
    imagesLoaded = false,
    onLoadImages,
    viewMode = "html",
    onViewModeChange,
    remoteImageCount = 0,
    remoteImageToken = "",
    htmlSanitizationFailed = false,
  }: Props = $props();

  let copied = $state(false);
  let copyTimer: ReturnType<typeof setTimeout> | null = null;
  let pickerOpen = $state(false);
  let saving = $state(false);
  let saveError = $state<string | null>(null);
  let linkedToast = $state<string | null>(null);
  let toastTimer: ReturnType<typeof setTimeout> | null = null;
  let destroyed = false;

  // Clear the pending copy-permalink timer when the component is destroyed
  // so stale callbacks cannot fire against unmounted state.
  onDestroy(() => {
    destroyed = true;
    if (copyTimer !== null) clearTimeout(copyTimer);
    if (toastTimer !== null) clearTimeout(toastTimer);
  });

  function copyPermalink(): void {
    if (!detail) return;
    const link = permalinkOf(detail.id);
    void navigator.clipboard.writeText(link).then(() => {
      copied = true;
      if (copyTimer !== null) clearTimeout(copyTimer);
      copyTimer = setTimeout(() => {
        copied = false;
        copyTimer = null;
      }, 1500);
    });
  }

  async function handlePickIssue(picked: {
    id: number;
    uid: string;
    qualified_id: string;
    title: string;
  }): Promise<void> {
    if (!onLinkMessage || !detail) return;
    pickerOpen = false;
    saving = true;
    saveError = null;
    linkedToast = null;
    if (toastTimer !== null) {
      clearTimeout(toastTimer);
      toastTimer = null;
    }

    const input: MessageLinkInput = {
      message_id: detail.id,
      conversation_id: detail.conversation_id,
      subject: detail.subject,
      from: detail.from,
      sent_at: detail.sent_at,
    };

    try {
      const { qualified_id } = await onLinkMessage(picked.uid, input);
      if (destroyed) return;
      linkedToast = `Linked to ${qualified_id}.`;
      toastTimer = setTimeout(() => {
        linkedToast = null;
        toastTimer = null;
      }, 3000);
    } catch (err) {
      if (destroyed) return;
      saveError = err instanceof Error ? err.message : "Link failed.";
    } finally {
      if (!destroyed) saving = false;
    }
  }

  function truncate(s: string, n: number): string {
    return s.length > n ? s.slice(0, n - 1) + "..." : s;
  }

  function formatDate(iso: string): string {
    const ts = Date.parse(iso);
    if (Number.isNaN(ts)) return iso;
    return new Intl.DateTimeFormat(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    }).format(new Date(ts));
  }

  function formatSize(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }

  const bodySegments = $derived(detail ? linkify(detail.body) : []);

  const showToggle = $derived(
    detail !== null &&
      typeof detail.body_html === "string" &&
      detail.body_html !== "" &&
      !htmlSanitizationFailed,
  );

  const showImageBanner = $derived(
    detail !== null &&
      viewMode === "html" &&
      !htmlSanitizationFailed &&
      remoteImageCount > 0 &&
      !imagesLoaded,
  );

  function selectViewMode(mode: "html" | "text"): void {
    if (detail === null || onViewModeChange === undefined) return;
    onViewModeChange(detail.id, mode);
  }

  function loadImages(): void {
    if (detail === null || onLoadImages === undefined) return;
    onLoadImages(detail.id, remoteImageToken);
  }

  // Mode decision: text view, sanitization-failed, or empty body_html
  // all route to the plain-text path; the iframe is reserved for the
  // HTML path with a non-empty sanitized body.
  const renderAsHTML = $derived(
    detail !== null &&
      viewMode === "html" &&
      !htmlSanitizationFailed &&
      typeof detail.body_html === "string" &&
      detail.body_html !== "",
  );

  function swapImageSources(html: string, messageId: number, token: string): string {
    const doc = new DOMParser().parseFromString(html, "text/html");
    for (const img of doc.querySelectorAll<HTMLImageElement>("img[data-remote-image-idx]")) {
      const idx = img.getAttribute("data-remote-image-idx") ?? "";
      img.setAttribute(
        "src",
        remoteImageURL(messageId, token, idx),
      );
      img.removeAttribute("data-remote-image-idx");
    }
    return doc.body.innerHTML;
  }

  const srcdocHTML = $derived.by(() => {
    if (!renderAsHTML || detail === null) return "";
    const body = imagesLoaded && remoteImageToken !== ""
      ? swapImageSources(detail.body_html ?? "", detail.id, remoteImageToken)
      : (detail.body_html ?? "");
    // location.origin is a strict subset of CSP-safe characters, so it
    // can be string-interpolated into the meta tag without escaping.
    const origin = typeof globalThis.location !== "undefined" ? globalThis.location.origin : "";
    const csp = [
      "default-src 'none'",
      `img-src ${origin} data:`,
      "script-src 'none'",
      "object-src 'none'",
      "base-uri 'none'",
      "form-action 'none'",
      "connect-src 'none'",
      "frame-src 'none'",
      "style-src 'unsafe-inline'",
    ].join("; ");
    return `<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${csp}">
<style>
  html,body{--font-size-md:0.875em;margin:0;padding:0 16px;font:var(--font-size-md)/1.5 -apple-system,Segoe UI,sans-serif;color:#222;background:#fff;word-wrap:break-word}
  img{max-width:100%;height:auto}
  a{color:#0a66c2}
</style>
</head><body>${body}</body></html>`;
  });
</script>

<article class="messages-detail" class:compact>
  {#if loading}
    <div class="detail-scroll" aria-label="Loading message">
      <div class="skeleton-header" aria-hidden="true">
        <span class="skel skel-from"></span>
        <span class="skel skel-subject"></span>
        <span class="skel skel-date"></span>
      </div>
      <div class="skeleton-body" aria-hidden="true">
        {#each { length: 6 } as _, i (i)}
          <span class="skel skel-line"></span>
        {/each}
      </div>
    </div>
  {:else if error}
    <div class="detail-message detail-error" role="alert">
      <p class="error-text">{error}</p>
      <p class="retry-hint">Try selecting the message again to retry.</p>
    </div>
  {:else if !detail}
    <div class="detail-message detail-empty">
      <p>Select a message to read it.</p>
    </div>
  {:else}
    {@const d = detail}
    <div class="detail-scroll">
      <header class="msg-header">
        {#if showToggle}
          <div class="view-mode-toggle" role="group" aria-label="Message view">
            <button
              type="button"
              class="view-mode-btn"
              class:active={viewMode === "html"}
              aria-pressed={viewMode === "html"}
              onclick={() => selectViewMode("html")}
            >HTML</button>
            <button
              type="button"
              class="view-mode-btn"
              class:active={viewMode === "text"}
              aria-pressed={viewMode === "text"}
              onclick={() => selectViewMode("text")}
            >Text</button>
          </div>
        {/if}
        <dl class="header-fields">
          <div class="field-row">
            <dt>From</dt>
            <dd>{d.from}</dd>
          </div>
          <div class="field-row">
            <dt>To</dt>
            <dd>{d.to.join(", ")}</dd>
          </div>
          {#if d.cc.length > 0}
            <div class="field-row">
              <dt>Cc</dt>
              <dd>{d.cc.join(", ")}</dd>
            </div>
          {/if}
          {#if d.bcc.length > 0}
            <div class="field-row">
              <dt>Bcc</dt>
              <dd>{d.bcc.join(", ")}</dd>
            </div>
          {/if}
          <div class="field-row">
            <dt>Date</dt>
            <dd>{formatDate(d.sent_at)}</dd>
          </div>
        </dl>
        {#if !compact}
          <h1 class="msg-subject">{d.subject}</h1>
        {/if}
        {#if d.labels.length > 0}
          <div class="label-chips" aria-label="Labels">
            {#each d.labels as label (label)}
              <span class="label-chip">{label}</span>
            {/each}
          </div>
        {/if}
      </header>

      {#if htmlSanitizationFailed}
        <p class="sanitize-failed" role="alert">
          Couldn't render HTML formatting. Showing text below.
        </p>
      {/if}
      {#if showImageBanner}
        <p class="image-banner" role="status">
          This message has {remoteImageCount} remote image{remoteImageCount === 1 ? "" : "s"}.
          <button type="button" class="load-images-btn" onclick={loadImages}>Load images</button>
        </p>
      {/if}

      <section class="msg-body-section" aria-label="Message body">
        {#if renderAsHTML}
          <iframe
            class="html-iframe"
            class:compact-iframe={compact}
            sandbox="allow-popups allow-popups-to-escape-sandbox"
            srcdoc={srcdocHTML}
            title="Message body"
          ></iframe>
        {:else}
          <!-- Index-based key: linkify can emit repeated segments (the same URL
               twice, identical text fragments) and value+kind would collide,
               breaking Svelte's keyed reconciliation. Order is stable per body,
               so the index is the correct identity here. -->
          <pre class="msg-body">{#each bodySegments as seg, i (i)}{#if seg.kind === "url"}<a
                href={seg.href}
                target="_blank"
                rel="noopener noreferrer"
              >{seg.value}</a>{:else}{seg.value}{/if}{/each}</pre>
        {/if}
      </section>

      {#if d.attachments.length > 0}
        <section class="msg-attachments-section" aria-label="Attachments">
          <h2 class="section-heading">Attachments</h2>
          <table class="attachments-table">
            <thead>
              <tr>
                <th>Filename</th>
                <th>Size</th>
                <th>Type</th>
              </tr>
            </thead>
            <tbody>
              <!-- Index-based key: messages attachments aren't guaranteed unique by
                   filename (two `image.png` inline attachments in one message
                   is a real wire shape). The list is static for the lifetime
                   of a given detail render, so index is stable. -->
              {#each d.attachments as att, i (i)}
                <tr>
                  <td class="att-filename">{att.filename}</td>
                  <td class="att-size">{formatSize(att.size_bytes)}</td>
                  <td class="att-mime">{att.mime_type}</td>
                </tr>
              {/each}
            </tbody>
          </table>
          <p class="download-hint">Download from the message source.</p>
        </section>
      {/if}

      <footer class="msg-actions">
        <button
          class="permalink-btn"
          type="button"
          aria-label={copied ? "Copied" : "Copy permalink"}
          onclick={copyPermalink}
        >
          {copied ? "Copied" : "Copy permalink"}
        </button>
        {#if onLinkMessage && kata}
          <button
            type="button"
            class="link-issue-btn"
            onclick={() => (pickerOpen = true)}
            disabled={saving}
          >
            {saving ? "Linking..." : "Link to task"}
          </button>
        {/if}
      </footer>
      {#if linkedToast}
        <p class="link-toast" role="status">{linkedToast}</p>
      {/if}
      {#if saveError}
        <p class="link-error" role="alert">{saveError}</p>
      {/if}
      {#if reverseLinks && reverseLinks.length > 0 && onOpenIssue}
        <section class="reverse-links" aria-label="Linked tasks">
          <h2 class="section-heading">Linked to</h2>
          <ul class="pill-list">
            {#each reverseLinks as ref (ref.uid)}
              <li>
                <button
                  type="button"
                  class="reverse-pill"
                  onclick={() => onOpenIssue?.(ref.uid)}
                  title={`${ref.qualified_id} - ${ref.title}`}
                >
                  <span class="pill-id">{ref.qualified_id}</span>
                  <span class="pill-title">{truncate(ref.title, 60)}</span>
                </button>
              </li>
            {/each}
          </ul>
        </section>
      {/if}
    </div>
  {/if}
  {#if kata && onLinkMessage}
    <IssuePickerDialog
      open={pickerOpen}
      {kata}
      onClose={() => (pickerOpen = false)}
      onPick={handlePickIssue}
    />
  {/if}
</article>

<style>
  .messages-detail {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    background: var(--bg-surface);
  }

  /* ---- scroll container ---- */

  .detail-scroll {
    flex: 1;
    overflow-y: auto;
    padding: 20px 24px 40px;
    min-height: 0;
  }

  /* In compact mode, MessageThread owns the pane scroll. The card flows to
     its content height so the stack can scroll as one unit. */
  .messages-detail.compact {
    height: auto;
    overflow: visible;
  }

  .messages-detail.compact .detail-scroll {
    flex: initial;
    overflow-y: visible;
    /* Tighter bottom padding than the non-compact base: MessageThread owns
       the outer scroll, so the in-card bottom gap can collapse. */
    padding: 16px 20px 24px;
  }

  /* ---- centered states: empty, error ---- */

  .detail-message {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 40px 24px;
    text-align: center;
  }

  .detail-empty p {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .error-text {
    color: var(--accent-red);
    font-size: var(--font-size-sm);
    font-weight: 500;
  }

  .retry-hint {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  /* ---- message header ---- */

  .msg-header {
    margin-bottom: 20px;
    padding-bottom: 16px;
    border-bottom: 1px solid var(--border-muted);
  }

  .header-fields {
    display: grid;
    gap: 3px 0;
    margin-bottom: 12px;
  }

  .field-row {
    display: flex;
    gap: 8px;
    font-size: var(--font-size-xs);
    line-height: 1.5;
    min-width: 0;
  }

  .field-row dt {
    flex-shrink: 0;
    width: 36px;
    color: var(--text-faint);
    font-weight: 500;
    text-align: right;
  }

  .field-row dd {
    flex: 1;
    min-width: 0;
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .msg-subject {
    font-size: var(--font-size-xl);
    font-weight: 600;
    letter-spacing: -0.014em;
    color: var(--text-primary);
    line-height: 1.25;
    margin-bottom: 10px;
    text-wrap: balance;
  }

  /* ---- label chips ---- */

  .label-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
  }

  .label-chip {
    display: inline-flex;
    align-items: center;
    height: 18px;
    padding: 0 7px;
    border-radius: 9px;
    font-size: var(--font-size-2xs);
    font-weight: 500;
    background: var(--bg-inset, #f0f0f0);
    color: var(--text-secondary);
    border: 1px solid var(--border-muted);
  }

  /* ---- body ---- */

  .msg-body-section {
    margin-bottom: 24px;
  }

  .msg-body {
    margin: 0;
    padding: 0;
    font-family: var(--font-mono, monospace);
    font-size: var(--font-size-sm);
    line-height: 1.65;
    color: var(--text-primary);
    white-space: pre-wrap;
    word-break: break-word;
    overflow-wrap: break-word;
  }

  .msg-body a {
    color: var(--accent-blue);
    text-decoration: none;
  }

  .msg-body a:hover {
    text-decoration: underline;
  }

  /* ---- attachments ---- */

  .msg-attachments-section {
    margin-bottom: 24px;
    padding: 16px;
    background: var(--bg-inset, #f9f9f9);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm, 4px);
  }

  .section-heading {
    font-size: var(--font-size-2xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-muted);
    margin-bottom: 10px;
  }

  .attachments-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--font-size-xs);
  }

  .attachments-table th {
    text-align: left;
    font-weight: 500;
    color: var(--text-faint);
    padding: 0 8px 6px 0;
    border-bottom: 1px solid var(--border-muted);
  }

  .attachments-table td {
    padding: 5px 8px 5px 0;
    vertical-align: middle;
    color: var(--text-secondary);
  }

  .att-filename {
    color: var(--text-primary);
    font-weight: 500;
    font-family: var(--font-mono, monospace);
  }

  .att-size {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  .att-mime {
    color: var(--text-faint);
    font-size: var(--font-size-2xs);
  }

  .download-hint {
    margin-top: 8px;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    font-style: italic;
  }

  /* ---- actions ---- */

  .msg-actions {
    display: flex;
    gap: 8px;
    padding-top: 16px;
    border-top: 1px solid var(--border-muted);
  }

  .permalink-btn {
    height: 26px;
    padding: 0 12px;
    border-radius: var(--radius-sm, 4px);
    font-size: var(--font-size-xs);
    font-weight: 500;
    color: var(--text-secondary);
    background: var(--bg-inset, #f0f0f0);
    border: 1px solid var(--border-default);
    cursor: pointer;
    transition: background 0.1s, color 0.1s;
  }

  .permalink-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .link-issue-btn {
    height: 26px;
    padding: 0 12px;
    border-radius: var(--radius-sm, 4px);
    font-size: var(--font-size-xs);
    font-weight: 500;
    color: var(--text-secondary);
    background: var(--bg-inset, #f0f0f0);
    border: 1px solid var(--border-default);
    cursor: pointer;
    transition: background 0.1s, color 0.1s;
  }

  .link-issue-btn:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .link-issue-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .link-toast {
    margin: 8px 0 0;
    padding: 6px 10px;
    border-radius: var(--radius-sm, 4px);
    background: color-mix(in srgb, var(--accent-green) 12%, transparent);
    color: var(--accent-green);
    font-size: var(--font-size-xs);
  }

  .link-error {
    margin: 8px 0 0;
    padding: 6px 10px;
    border-radius: var(--radius-sm, 4px);
    background: var(--accent-red-soft, rgba(193, 74, 60, 0.12));
    color: var(--accent-red, #c14a3c);
    font-size: var(--font-size-xs);
  }

  /* ---- reverse-link pills ---- */

  .reverse-links {
    margin-top: 20px;
    padding-top: 16px;
    border-top: 1px solid var(--border-muted);
  }

  .pill-list {
    display: flex;
    flex-direction: column;
    gap: 5px;
    list-style: none;
    padding: 0;
    margin: 0;
  }

  .reverse-pill {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 5px 10px;
    border-radius: var(--radius-sm, 4px);
    background: var(--bg-inset, #f0f0f0);
    border: 1px solid transparent;
    font-size: var(--font-size-xs);
    text-align: left;
    cursor: pointer;
    overflow: hidden;
    transition: background 0.1s, border-color 0.1s;
  }

  .reverse-pill:hover {
    background: var(--bg-surface-hover);
    border-color: var(--border-default);
  }

  .pill-id {
    flex-shrink: 0;
    font-family: var(--font-mono, monospace);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    color: var(--accent-blue);
    white-space: nowrap;
  }

  .pill-title {
    flex: 1;
    min-width: 0;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* ---- skeleton loading ---- */

  .skeleton-header,
  .skeleton-body {
    padding: 20px 24px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .skel {
    display: block;
    height: 12px;
    border-radius: 3px;
    background: var(--bg-inset, #e5e7eb);
    animation: shimmer 1.4s ease-in-out infinite;
  }

  .skel-from { width: 40%; }
  .skel-subject { width: 70%; height: 20px; margin-top: 4px; }
  .skel-date { width: 28%; }
  .skel-line { width: calc(55% + 10% * var(--i, 0)); }

  @keyframes shimmer {
    0%   { opacity: 0.5; }
    50%  { opacity: 1; }
    100% { opacity: 0.5; }
  }

  /* ---- HTML iframe ---- */

  .html-iframe {
    display: block;
    width: 100%;
    height: 100%;
    min-height: 200px;
    border: 0;
    background: var(--bg-surface);
  }

  .html-iframe.compact-iframe {
    height: 60vh;
    max-height: 640px;
    min-height: 200px;
  }

  /* Non-compact HTML mode: iframe fills the available body region. The
     ancestor chain (.detail-scroll -> .msg-body-section -> .html-iframe)
     must all be flex columns with min-height:0 so the iframe's flex:1
     resolves against the pane height rather than collapsing to its
     intrinsic min-height. */
  .messages-detail .detail-scroll:has(.html-iframe:not(.compact-iframe)) {
    display: flex;
    flex-direction: column;
    min-height: 0;
  }

  .messages-detail .detail-scroll:has(.html-iframe:not(.compact-iframe)) .msg-body-section {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }

  .messages-detail .detail-scroll:has(.html-iframe:not(.compact-iframe)) .html-iframe {
    flex: 1;
  }

  /* ---- view-mode toggle ---- */

  .view-mode-toggle {
    display: inline-flex;
    margin-bottom: 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm, 4px);
    background: var(--bg-inset);
  }

  .view-mode-btn {
    padding: 3px 10px;
    border: 0;
    background: transparent;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    font-weight: 500;
    cursor: pointer;
  }

  .view-mode-btn.active {
    background: var(--bg-surface);
    color: var(--text-primary);
  }

  /* ---- sanitization-failed banner ---- */

  .sanitize-failed {
    margin: 0 0 12px;
    padding: 8px 12px;
    border-radius: var(--radius-sm, 4px);
    background: var(--accent-red-soft, rgba(193, 74, 60, 0.12));
    color: var(--accent-red, #c14a3c);
    font-size: var(--font-size-xs);
  }

  /* ---- remote-image banner ---- */

  .image-banner {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0 0 12px;
    padding: 8px 12px;
    border-radius: var(--radius-sm, 4px);
    background: var(--bg-inset, #f0f0f0);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .load-images-btn {
    padding: 2px 10px;
    border-radius: var(--radius-sm, 4px);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-2xs);
    font-weight: 500;
    cursor: pointer;
  }

  .load-images-btn:hover {
    background: var(--bg-surface-hover);
  }
</style>
