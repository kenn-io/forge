import DOMPurify, { type UponSanitizeElementHook, type UponSanitizeElementHookEvent } from "dompurify";
import { Marked, type MarkedExtension, type Tokens } from "marked";
import { providerItemRefExtension, type RepoContext } from "@middleman/ui/utils/markdown";
import {
  joinFolderPath,
  parseWikilink,
  resolveRelativeDocPath,
  resolveWikilink,
  type FolderIndex,
  type WikilinkResolution,
} from "./folderLinks";

/**
 * Renders a folder markdown document to sanitized HTML. Extends the
 * shared marked/DOMPurify stack used by issue bodies with folder-aware
 * link resolution: [[wikilinks]], relative .md links, and image src
 * paths are rewritten so the viewer can navigate without a full reload
 * and so embedded images point at the blob endpoint.
 */

export interface DocsMarkdownOptions {
  folderID: string;
  currentDocPath: string;
  index: FolderIndex;
  // Builds the in-app URL for navigating to another doc within the folder.
  buildDocURL: (folderID: string, relPath: string, anchor?: string) => string;
  // Resolves an image path (relative or absolute within the folder) to a
  // network URL the browser can fetch.
  buildBlobURL: (folderID: string, relPath: string) => string;
  // Repository previews disable this so contributor-controlled Markdown
  // cannot make the maintainer's browser fetch arbitrary network URLs.
  allowExternalImages?: boolean;
  // Repository previews provide this so #123-style references render like
  // issue/PR comments instead of Docs/Kata short-id links.
  repoContext?: RepoContext | undefined;
}

export interface FrontmatterSplit {
  frontmatter: string | null;
  body: string;
}

const FRONTMATTER_RE = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?/;

export function splitFrontmatter(source: string): FrontmatterSplit {
  const match = FRONTMATTER_RE.exec(source);
  if (!match) return { frontmatter: null, body: source };
  return {
    frontmatter: match[1] ?? "",
    body: source.slice(match[0].length),
  };
}

// Tags allowed inside the rendered HTML. Docs are richer than issue
// bodies — full headings, tables, images, and strikethrough — but
// scripts and event handlers are still blocked by DOMPurify defaults.
const ALLOWED_TAGS = [
  "a",
  "blockquote",
  "br",
  "code",
  "del",
  "details",
  "em",
  "h1",
  "h2",
  "h3",
  "h4",
  "h5",
  "h6",
  "hr",
  "img",
  "li",
  "ol",
  "p",
  "pre",
  "span",
  "strong",
  "summary",
  "table",
  "tbody",
  "td",
  "th",
  "thead",
  "tr",
  "ul",
];

const ALLOWED_ATTR = [
  "alt",
  "class",
  "data-anchor",
  "data-anchor-link",
  "data-doc-link",
  "data-doc-image-token",
  "data-doc-path",
  "data-external-url",
  "data-kata-link",
  "data-kata-mention",
  "data-kata-project",
  "data-kata-short-id",
  "data-kata-uid",
  "data-folder",
  "data-item-type",
  "data-name",
  "data-number",
  "data-owner",
  "data-platform-host",
  "data-provider",
  "data-repo-path",
  "data-wikilink",
  "decoding",
  "href",
  "id",
  "loading",
  "open",
  "rel",
  "src",
  "target",
  "title",
];

export function renderDocsMarkdown(source: string, options: DocsMarkdownOptions): string {
  const { body } = splitFrontmatter(source);
  const imageToken = newImageToken();
  const md = new Marked();
  md.use({ gfm: true, breaks: false, async: false } satisfies MarkedExtension);
  md.use({
    extensions: [
      wikilinkExtension(options, imageToken),
      ...(options.repoContext ? [providerItemRefExtension(options.repoContext)] : []),
      ...(options.repoContext ? [] : [kataLinkExtension()]),
      mentionExtension(),
    ],
  });
  md.use({ renderer: docsRenderer(options, imageToken) });
  const rawHtml = md.parse(body) as string;
  // DOMPurify hook that scrubs src/href values on every element regardless
  // of whether they came from a marked renderer or raw HTML in the source.
  // Without it, raw `<img src="data:image/svg+xml,...">` in markdown would
  // bypass our renderer-side isUnsafeUri filter and be preserved by
  // DOMPurify's default data-URI image allowance.
  DOMPurify.addHook("uponSanitizeElement", dropUntrustedImages(imageToken));
  DOMPurify.addHook("uponSanitizeAttribute", scrubUnsafeURIAttrs);
  try {
    const sanitized = DOMPurify.sanitize(rawHtml, {
      ALLOWED_TAGS,
      ALLOWED_ATTR,
    });
    return stripImageTokenAttrs(sanitized);
  } finally {
    DOMPurify.removeHook("uponSanitizeElement");
    DOMPurify.removeHook("uponSanitizeAttribute");
  }
}

function scrubUnsafeURIAttrs(_node: Element, data: { attrName: string; attrValue: string; keepAttr: boolean }): void {
  if (data.attrName !== "src" && data.attrName !== "href") return;
  if (isUnsafeUri(data.attrValue)) data.keepAttr = false;
}

function dropUntrustedImages(imageToken: string): UponSanitizeElementHook {
  return (node: Node, data: UponSanitizeElementHookEvent): void => {
    if (data.tagName !== "img") return;
    if (!(node instanceof Element)) return;
    if (node.getAttribute("data-doc-image-token") === imageToken) return;
    node.parentNode?.removeChild(node);
  };
}

function newImageToken(): string {
  const crypto = globalThis.crypto;
  if (crypto && "randomUUID" in crypto) return crypto.randomUUID();
  return Math.random().toString(36).slice(2);
}

function trustedImageAttr(imageToken: string): string {
  return `data-doc-image-token="${escapeAttr(imageToken)}"`;
}

function stripImageTokenAttrs(html: string): string {
  return html.replace(/\sdata-doc-image-token="[^"]*"/g, "");
}

interface WikilinkToken extends Tokens.Generic {
  type: "wikilink";
  raw: string;
  embed: boolean;
  inner: string;
}

function wikilinkExtension(options: DocsMarkdownOptions, imageToken: string) {
  return {
    name: "wikilink",
    level: "inline" as const,
    start(src: string) {
      const i = src.indexOf("[[");
      if (i === -1) return undefined;
      // Allow the optional ! prefix one char earlier.
      return i > 0 && src[i - 1] === "!" ? i - 1 : i;
    },
    tokenizer(src: string): WikilinkToken | undefined {
      const match = /^(!?)\[\[([^\]\n]+)\]\]/.exec(src);
      if (!match) return undefined;
      return {
        type: "wikilink",
        raw: match[0],
        embed: match[1] === "!",
        inner: match[2] ?? "",
      };
    },
    renderer(token: Tokens.Generic): string {
      const wl = token as WikilinkToken;
      const parsed = parseWikilink(wl.inner);
      if (wl.embed) {
        // Treat ![[image.png]] as an inline image embed against the folder.
        // Match the standard markdown image renderer's perf attributes so
        // long folder notes don't load all images at once.
        if (!isAssetRef(parsed.target)) {
          return escapeHtml(parsed.alias ?? parsed.target);
        }
        const src = options.buildBlobURL(options.folderID, parsed.target);
        const alt = escapeAttr(parsed.alias ?? parsed.target);
        return `<img ${trustedImageAttr(imageToken)} src="${escapeAttr(src)}" alt="${alt}" loading="lazy" decoding="async">`;
      }
      const resolution = resolveWikilink(parsed.target, options.index);
      const display = escapeHtml(parsed.alias ?? parsed.target);
      return wikilinkAnchor(resolution, parsed.anchor, display, options);
    },
  };
}

interface KataLinkToken extends Tokens.Generic {
  type: "kataLink";
  raw: string;
  project: string | null;
  shortId: string;
}

// Kata short-id regex: lowercase alphanumeric, 2-32 chars. Kata's
// generator emits lowercase base32-ish words like "capt", "rent",
// "budget", so the constraint matches the corpus without false-
// positively swallowing hashtags or anchor fragments.
const SHORT_ID_RE = /[a-z0-9][a-z0-9_-]{1,31}/;
// Project segment: starts with letter, may include : _ - .
// Captures Kata's "notes:inbox" style names without grabbing
// trailing path segments.
const PROJECT_RE = /[A-Za-z][\w:.-]*/;
const KATA_QUALIFIED_RE = new RegExp(`^(${PROJECT_RE.source})\\/#(${SHORT_ID_RE.source})(?![\\w-])`);
const KATA_SHORT_RE = new RegExp(`^#(${SHORT_ID_RE.source})(?![\\w-])`);

function kataLinkExtension() {
  return {
    name: "kataLink",
    level: "inline" as const,
    start(src: string): number | undefined {
      // Earliest plausible start: either a `#` not preceded by a word
      // char, or a project segment followed by `/#`.
      let best: number | undefined;
      for (let i = 0; i < src.length; i++) {
        const ch = src[i];
        if (ch !== "#") continue;
        const prev = i > 0 ? src[i - 1] : "";
        // Skip `#` preceded by a word char (e.g. URL fragments,
        // `foo#bar`). Project-qualified forms are picked up below.
        if (prev && /[\w]/.test(prev)) continue;
        best = i;
        break;
      }
      const qm = /(?:^|[\s({[,;:>])([A-Za-z][\w:.-]*\/#[a-z0-9])/.exec(src);
      if (qm) {
        const at = qm.index + (qm.index === 0 && /[A-Za-z]/.test(src[0] ?? "") ? 0 : 1);
        if (best === undefined || at < best) best = at;
      }
      return best;
    },
    tokenizer(
      this: { lexer?: { state?: { inLink?: boolean; inRawBlock?: boolean } } },
      src: string,
    ): KataLinkToken | undefined {
      // Don't fire inside link text or raw code/HTML blocks — `[see #abc](url)`
      // and `<code>#abc</code>` should render verbatim, not as kata anchors.
      const state = this.lexer?.state;
      if (state?.inLink || state?.inRawBlock) return undefined;
      const qualified = KATA_QUALIFIED_RE.exec(src);
      if (qualified) {
        return {
          type: "kataLink",
          raw: qualified[0],
          project: qualified[1] ?? null,
          shortId: qualified[2] ?? "",
        };
      }
      const bare = KATA_SHORT_RE.exec(src);
      if (bare) {
        return {
          type: "kataLink",
          raw: bare[0],
          project: null,
          shortId: bare[1] ?? "",
        };
      }
      return undefined;
    },
    renderer(token: Tokens.Generic): string {
      const k = token as KataLinkToken;
      const display = k.project ? `${k.project}/#${k.shortId}` : `#${k.shortId}`;
      const projectAttr = k.project ? ` data-kata-project="${escapeAttr(k.project)}"` : "";
      return (
        `<a class="kata-link" href="#"` +
        ` data-kata-link="true"` +
        ` data-kata-short-id="${escapeAttr(k.shortId)}"` +
        projectAttr +
        ` title="Open Kata issue">${escapeHtml(display)}</a>`
      );
    },
  };
}

interface MentionToken extends Tokens.Generic {
  type: "mention";
  raw: string;
  handle: string;
}

// Mentions must end on a real handle character — letters, digits, or
// underscore. Allowing `.` or `-` at the end picks up sentence
// punctuation like `@wes.` as part of the handle. The lookahead only
// rejects further word characters so trailing `.`/`-`/`,` correctly
// stay outside the handle.
const MENTION_RE = /^@([A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9_])?)(?!\w)/;

function mentionExtension() {
  return {
    name: "mention",
    level: "inline" as const,
    start(src: string): number | undefined {
      for (let i = 0; i < src.length; i++) {
        if (src[i] !== "@") continue;
        const prev = i > 0 ? src[i - 1] : "";
        if (prev && /\w/.test(prev)) continue;
        return i;
      }
      return undefined;
    },
    tokenizer(src: string): MentionToken | undefined {
      const match = MENTION_RE.exec(src);
      if (!match) return undefined;
      return {
        type: "mention",
        raw: match[0],
        handle: match[1] ?? "",
      };
    },
    renderer(token: Tokens.Generic): string {
      const m = token as MentionToken;
      return (
        `<span class="kata-mention" data-kata-mention="${escapeAttr(m.handle)}">` + `@${escapeHtml(m.handle)}</span>`
      );
    },
  };
}

function wikilinkAnchor(
  resolution: WikilinkResolution,
  anchor: string | undefined,
  display: string,
  options: DocsMarkdownOptions,
): string {
  if (resolution.kind === "missing") {
    return `<span class="wikilink wikilink--missing" data-wikilink="missing" title="Note not found">${display}</span>`;
  }
  if (resolution.kind === "ambiguous") {
    const candidates = resolution.candidates.join("|");
    return (
      `<a class="wikilink wikilink--ambiguous" href="#"` +
      ` data-wikilink="ambiguous" data-doc-link="${escapeAttr(candidates)}"` +
      ` data-folder="${escapeAttr(options.folderID)}"` +
      (anchor ? ` data-anchor="${escapeAttr(anchor)}"` : "") +
      ` title="Multiple notes match — click to pick one">${display}</a>`
    );
  }
  const url = options.buildDocURL(options.folderID, resolution.path, anchor);
  return (
    `<a class="wikilink" href="${escapeAttr(url)}"` +
    ` data-wikilink="resolved" data-doc-link="${escapeAttr(resolution.path)}"` +
    ` data-folder="${escapeAttr(options.folderID)}"` +
    (anchor ? ` data-anchor="${escapeAttr(anchor)}"` : "") +
    `>${display}</a>`
  );
}

function docsRenderer(options: DocsMarkdownOptions, imageToken: string) {
  return {
    code(token: Tokens.Code): string | false {
      if (!isMermaidFence(token.lang)) return false;
      return `<pre class="mermaid">${escapeHtml(token.text)}</pre>`;
    },
    link(this: { parser: { parseInline: (tokens: Tokens.Generic[]) => string } }, token: Tokens.Link) {
      const inner = this.parser.parseInline(token.tokens ?? []);
      const href = token.href ?? "";
      const title = token.title ? ` title="${escapeAttr(token.title)}"` : "";
      // Empty href — fall back to plain text so the renderer still emits
      // valid HTML rather than a broken anchor.
      if (!href) return `<span>${inner}</span>`;
      if (href.startsWith("kata://issue/")) {
        // DOMPurify rejects unknown URI schemes on href, which would
        // drop the link and lose the UID. Park the UID in a data
        // attribute and ship a safe local href so the viewer's click
        // handler can extract it cleanly.
        const uid = href.slice("kata://issue/".length);
        return `<a href="#" data-kata-link="issue"` + ` data-kata-uid="${escapeAttr(uid)}"${title}>${inner}</a>`;
      }
      if (isUnsafeUri(href)) return `<span>${inner}</span>`;
      if (isExternal(href)) {
        const externalHref = cleanURIForScheme(href);
        return `<a href="${escapeAttr(externalHref)}" target="_blank" rel="noreferrer"${title}>${inner}</a>`;
      }
      if (href.startsWith("#")) {
        // In-page anchor — pass through. The viewer wires up scroll behavior.
        return `<a href="${escapeAttr(href)}" data-anchor-link="true"${title}>${inner}</a>`;
      }
      // Split off an optional fragment before checking for a .md target.
      // Without this, `other.md#heading` fails the `.md` suffix check in
      // resolveRelativeDocPath and falls through to the generic anchor.
      const [bare, anchor] = splitAnchor(href);
      const finalPath = resolveRelativeDocPath(options.currentDocPath, bare);
      if (finalPath) {
        const url = options.buildDocURL(options.folderID, finalPath, anchor);
        return (
          `<a class="doc-link" href="${escapeAttr(url)}"` +
          ` data-doc-link="${escapeAttr(finalPath)}"` +
          ` data-folder="${escapeAttr(options.folderID)}"` +
          (anchor ? ` data-anchor="${escapeAttr(anchor)}"` : "") +
          `${title}>${inner}</a>`
        );
      }
      // Asset link or unknown — route through the blob endpoint so users
      // can click through to images/files referenced inline.
      if (isAssetRef(href)) {
        const path = joinFolderPath(options.currentDocPath, href) ?? stripLeadingSlash(href);
        const url = options.buildBlobURL(options.folderID, path);
        return `<a href="${escapeAttr(url)}" target="_blank" rel="noreferrer"${title}>${inner}</a>`;
      }
      return `<a href="${escapeAttr(href)}"${title}>${inner}</a>`;
    },
    image(token: Tokens.Image) {
      const alt = escapeAttr(token.text ?? "");
      const title = token.title ? ` title="${escapeAttr(token.title)}"` : "";
      const href = token.href ?? "";
      // Block dangerous URI schemes at the source. DOMPurify also
      // sanitizes these, but rejecting them in the renderer means
      // the bad src never reaches the rendered HTML in the first
      // place — and removes the empty `<img>` shell DOMPurify would
      // leave behind.
      if (isUnsafeUri(href)) {
        return alt ? escapeHtml(token.text ?? "") : "";
      }
      const imgAttrs = `loading="lazy" decoding="async"`;
      if (isExternal(href)) {
        const externalHref = cleanURIForScheme(href);
        if (options.allowExternalImages === false) {
          const label = token.text ? escapeHtml(token.text) : escapeHtml(href);
          return `<a href="${escapeAttr(externalHref)}" target="_blank" rel="noreferrer"${title}>${label}</a>`;
        }
        return `<img ${trustedImageAttr(imageToken)} src="${escapeAttr(externalHref)}" alt="${alt}" ${imgAttrs}${title}>`;
      }
      if (!isAssetRef(href)) {
        return `<img ${trustedImageAttr(imageToken)} src="${escapeAttr(href)}" alt="${alt}" ${imgAttrs}${title}>`;
      }
      const path = joinFolderPath(options.currentDocPath, href) ?? stripLeadingSlash(href);
      const url = options.buildBlobURL(options.folderID, path);
      return `<img ${trustedImageAttr(imageToken)} src="${escapeAttr(url)}" alt="${alt}" ${imgAttrs}${title}>`;
    },
    heading(this: { parser: { parseInline: (tokens: Tokens.Generic[]) => string } }, token: Tokens.Heading) {
      const inner = this.parser.parseInline(token.tokens ?? []);
      const id = slugify(token.text ?? "");
      const level = Math.min(Math.max(token.depth, 1), 6);
      return `<h${level} id="${escapeAttr(id)}">${inner}</h${level}>`;
    },
  };
}

function isMermaidFence(lang: string | undefined): boolean {
  return (lang ?? "").trim().split(/\s+/, 1)[0]?.toLowerCase() === "mermaid";
}

function isExternal(href: string): boolean {
  return /^(https?:|mailto:)/i.test(normalizeURIForScheme(href));
}

// URI schemes we refuse to render. javascript:/vbscript: are always
// active-content vectors. data: is permitted only for a small set of
// safe image types — data:image/svg+xml can embed <script>, and
// data:text/html turns into a same-origin frame.
function isUnsafeUri(href: string): boolean {
  // Browsers strip tab/newline/CR anywhere in a URL and treat backslashes
  // as forward slashes, so normalize before classifying — otherwise
  // `/<tab>/evil.com` or `/\evil.com` smuggles a protocol-relative
  // navigation past the leading-slash checks below.
  const trimmed = normalizeURIForScheme(href);
  // Protocol-relative references (//host, \\host, and mixed-slash variants)
  // navigate same-tab to an arbitrary host with no explicit scheme, bypassing
  // the external-link handling that adds target/rel. A single leading slash is
  // a same-origin root path and stays allowed.
  if (/^[/\\]{2}/.test(trimmed)) return true;
  if (/^(javascript:|vbscript:)/i.test(trimmed)) return true;
  if (trimmed.startsWith("data:")) {
    return !/^data:image\/(png|jpeg|gif|webp)\b/.test(trimmed);
  }
  if (/^[a-z][a-z0-9+.-]*:/i.test(trimmed)) {
    return !/^(https?:|mailto:)/i.test(trimmed);
  }
  return false;
}

function normalizeURIForScheme(href: string): string {
  return cleanURIForScheme(href).toLowerCase();
}

function cleanURIForScheme(href: string): string {
  return href.replace(/[\t\n\r]/g, "").trim();
}

// Only treat hrefs that clearly point at folder-bundled assets as blobs.
// Bare external-looking paths are passed through unchanged so we don't
// accidentally hijack them.
function isAssetRef(href: string): boolean {
  if (isExternal(href)) return false;
  if (href.startsWith("#")) return false;
  return /\.(png|jpe?g|gif|webp)$/i.test(href);
}

function stripLeadingSlash(href: string): string {
  return href.startsWith("/") ? href.slice(1) : href;
}

function splitAnchor(href: string): [string, string | undefined] {
  const idx = href.indexOf("#");
  if (idx === -1) return [href, undefined];
  return [href.slice(0, idx), href.slice(idx + 1)];
}

// Heading id slug. Lowercases, replaces non-alphanumerics with hyphens,
// trims leading/trailing hyphens. Stable enough for in-page anchors and
// scroll-spy without depending on the unicode slugifier.
export function slugify(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function escapeHtml(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function escapeAttr(value: string): string {
  return escapeHtml(value).replace(/"/g, "&quot;");
}
