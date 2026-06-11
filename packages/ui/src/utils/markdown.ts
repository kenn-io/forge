import { Marked } from "marked";
import type { RendererObject, TokenizerAndRendererExtension, Tokens } from "marked";
import DOMPurify from "dompurify";
import { canonicalProvider } from "../api/provider-routes.js";
import { itemReferenceAnchorAttributes } from "./item-reference.js";
import type { ItemReferenceType } from "./item-reference.js";

interface RepoContext {
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
}

type ItemRefToken = Tokens.Generic & {
  type: "itemRef";
  raw: string;
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
  number: number;
  itemType?: ItemReferenceType | undefined;
  text: string;
};

function assertItemRefToken(token: Tokens.Generic): asserts token is ItemRefToken {
  if (
    token.type !== "itemRef" ||
    typeof token.raw !== "string" ||
    typeof token.provider !== "string" ||
    (token.platformHost !== undefined && typeof token.platformHost !== "string") ||
    typeof token.owner !== "string" ||
    typeof token.name !== "string" ||
    typeof token.repoPath !== "string" ||
    typeof token.number !== "number" ||
    (token.itemType !== undefined && token.itemType !== "pr" && token.itemType !== "issue") ||
    typeof token.text !== "string"
  ) {
    throw new Error("Unexpected itemRef token shape");
  }
}

function renderItemRefToken(token: Tokens.Generic): string {
  assertItemRefToken(token);
  return `<a ${itemReferenceAnchorAttributes(token)}>${token.text}</a>`;
}

function itemRefExtension(repo?: RepoContext): TokenizerAndRendererExtension {
  const supportsBangMR = canonicalProvider(repo?.provider ?? "") === "gitlab";
  return {
    name: "itemRef",
    level: "inline",
    start(src: string): number | undefined {
      const marker = supportsBangMR ? "[#!]" : "#";
      const crossIdx = src.search(new RegExp(`[\\w.-]+/[\\w./-]+${marker}\\d`));
      // Bare: look for # preceded by start or non-word
      const bareIdx = src.search(/(^|[^\w])#\d/);
      const mrBareIdx = supportsBangMR ? src.search(/(^|[^\w])!\d/) : -1;
      const adjusted = bareIdx >= 0 && src[bareIdx] !== "#" ? bareIdx + 1 : bareIdx;
      const adjustedMR = mrBareIdx >= 0 && src[mrBareIdx] !== "!" ? mrBareIdx + 1 : mrBareIdx;
      return [crossIdx, adjusted, adjustedMR].filter((idx) => idx >= 0).sort((a, b) => a - b)[0];
    },
    tokenizer(this: { lexer: { state: { inLink: boolean } } }, src: string): ItemRefToken | undefined {
      if (this.lexer.state.inLink || !repo) return undefined;

      const crossMatch = src.match(/^([\w.-]+(?:\/[\w.-]+)+)([#!])(\d+)(?!\w)/);
      if (crossMatch) {
        const repoPath = crossMatch[1]!;
        const marker = crossMatch[2]!;
        if (marker === "!" && !supportsBangMR) return undefined;
        const parts = repoPath.split("/");
        const name = parts.pop()!;
        const owner = parts.join("/");
        return {
          type: "itemRef",
          raw: crossMatch[0],
          provider: repo.provider,
          platformHost: repo.platformHost,
          owner,
          name,
          repoPath,
          number: parseInt(crossMatch[3]!, 10),
          itemType: marker === "!" ? "pr" : supportsBangMR ? "issue" : undefined,
          text: crossMatch[0],
        };
      }

      if (supportsBangMR) {
        const mrBareMatch = src.match(/^!(\d+)(?!\w)/);
        if (mrBareMatch) {
          return {
            type: "itemRef",
            raw: mrBareMatch[0],
            provider: repo.provider,
            platformHost: repo.platformHost,
            owner: repo.owner,
            name: repo.name,
            repoPath: repo.repoPath,
            number: parseInt(mrBareMatch[1]!, 10),
            itemType: "pr",
            text: mrBareMatch[0],
          };
        }
      }

      const bareMatch = src.match(/^#(\d+)(?!\w)/);
      if (bareMatch) {
        return {
          type: "itemRef",
          raw: bareMatch[0],
          provider: repo.provider,
          platformHost: repo.platformHost,
          owner: repo.owner,
          name: repo.name,
          repoPath: repo.repoPath,
          number: parseInt(bareMatch[1]!, 10),
          itemType: supportsBangMR ? "issue" : undefined,
          text: bareMatch[0],
        };
      }
      return undefined;
    },
    renderer(token): string {
      return renderItemRefToken(token);
    },
  };
}

export interface RenderMarkdownOpts {
  // When true, GFM task-list checkboxes render as enabled <input> elements
  // tagged with data-task-index="N" (zero-based, in document order). The
  // caller is responsible for intercepting clicks and persisting state —
  // unhandled clicks toggle visually but do not save.
  interactiveTasks?: boolean;
}

// Per-render state for the custom checkbox renderer. Marked is single-
// threaded synchronous, so a module-level variable is safe.
//
// `itemStack` is a stack of pending listitem invocation scopes. When a
// listitem fires, it pushes a fresh frame; the checkbox renderer (for
// THIS item's `[ ]`) writes its allocated index to the top frame; the
// listitem reads the same frame back on its way out and pops. Nested
// task children push their own frames on top, so a parent's frame is
// preserved while inner items emit their own checkboxes.
type ListItemFrame = { checkboxIndex: number };
let renderState: {
  taskIndex: number;
  interactiveTasks: boolean;
  itemStack: ListItemFrame[];
  // Counts blockquote nesting depth so listitem can detect when it
  // sits inside `> ...`. The source-side task helpers don't see
  // blockquoted task lines (TASK_LINE matches column-0 bullets),
  // so the renderer must skip interactivity inside blockquotes —
  // otherwise data-task-index values would drift from the source
  // and clicks would mutate the wrong line.
  blockquoteDepth: number;
} = {
  taskIndex: 0,
  interactiveTasks: false,
  itemStack: [],
  blockquoteDepth: 0,
};

const htmlCache = new Map<string, string>();
const markedCache = new Map<string, Marked>();

// Six-dot drag handle SVG used to grab a task-list item. Inlined so
// the rendered markdown is self-contained and no extra fetch is needed.
const DRAG_HANDLE_SVG =
  `<svg viewBox="0 0 12 16" width="12" height="16" aria-hidden="true">` +
  `<circle cx="3" cy="3" r="1.2"/>` +
  `<circle cx="9" cy="3" r="1.2"/>` +
  `<circle cx="3" cy="8" r="1.2"/>` +
  `<circle cx="9" cy="8" r="1.2"/>` +
  `<circle cx="3" cy="13" r="1.2"/>` +
  `<circle cx="9" cy="13" r="1.2"/>` +
  `</svg>`;

function isMermaidFence(lang: string | undefined): boolean {
  return (lang ?? "").trim().split(/\s+/, 1)[0]?.toLowerCase() === "mermaid";
}

function escapeHtml(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

const taskListRenderer: RendererObject = {
  blockquote(token): string {
    renderState.blockquoteDepth++;
    const inner = this.parser.parse(token.tokens);
    renderState.blockquoteDepth--;
    return `<blockquote>\n${inner}</blockquote>\n`;
  },
  code(token: Tokens.Code): string | false {
    if (!isMermaidFence(token.lang)) return false;
    return `<pre class="mermaid">${escapeHtml(token.text)}</pre>`;
  },
  // The checkbox renderer is called during the recursive parse
  // of a listitem's inner tokens. It allocates the next task
  // index and writes it onto the top frame of itemStack so the
  // enclosing listitem can pick up THIS item's index — even if
  // nested children push and pop frames of their own first.
  // Inside a blockquote, the source-side helpers can't see the
  // task line (TASK_LINE doesn't match `> -` prefixes), so
  // emit the default disabled checkbox to keep indices aligned.
  checkbox({ checked }): string {
    const inBlockquote = renderState.blockquoteDepth > 0;
    const interactive = renderState.interactiveTasks && !inBlockquote;
    const checkedAttr = checked ? ' checked=""' : "";
    if (interactive) {
      const index = renderState.taskIndex++;
      const stack = renderState.itemStack;
      if (stack.length > 0) {
        stack[stack.length - 1]!.checkboxIndex = index;
      }
      return `<input${checkedAttr} type="checkbox" data-task-index="${index}">`;
    }
    return `<input${checkedAttr} disabled="" type="checkbox">`;
  },
  listitem(token): string {
    const frame: ListItemFrame = { checkboxIndex: -1 };
    renderState.itemStack.push(frame);
    const inner = this.parser.parse(token.tokens);
    renderState.itemStack.pop();
    if (!token.task) return `<li>${inner}</li>\n`;
    const interactive = renderState.interactiveTasks && renderState.blockquoteDepth === 0;
    if (!interactive) {
      return `<li class="task-list-item">${inner}</li>\n`;
    }
    const index = frame.checkboxIndex;
    const handle =
      `<span class="task-drag-handle" ` +
      `data-task-index="${index}" ` +
      `draggable="true" ` +
      `role="button" ` +
      `tabindex="-1" ` +
      `aria-label="Drag to reorder">` +
      DRAG_HANDLE_SVG +
      `</span>`;
    return (
      `<li class="task-list-item task-list-item--interactive" ` +
      `data-task-index="${index}">` +
      `${handle}${inner}</li>\n`
    );
  },
};

function getMarked(repo?: RepoContext): Marked {
  const key = repo ? `${repo.provider}/${repo.platformHost ?? ""}/${repo.repoPath}` : "";
  let instance = markedCache.get(key);
  if (!instance) {
    instance = new Marked({ breaks: true, gfm: true });
    instance.use({ extensions: [itemRefExtension(repo)] });
    instance.use({
      renderer: taskListRenderer,
    });
    markedCache.set(key, instance);
  }
  return instance;
}

export function renderMarkdown(raw: string, repo?: RepoContext, opts: RenderMarkdownOpts = {}): string {
  if (!raw) return "";
  const interactiveTasks = !!opts.interactiveTasks;
  const repoKey = repo ? `${repo.provider}/${repo.platformHost ?? ""}/${repo.repoPath}` : "";
  const key = `${repoKey}\0${interactiveTasks ? 1 : 0}\0${raw}`;
  const cached = htmlCache.get(key);
  if (cached !== undefined) return cached;

  renderState = {
    taskIndex: 0,
    interactiveTasks,
    itemStack: [],
    blockquoteDepth: 0,
  };
  const html = DOMPurify.sanitize(getMarked(repo).parse(raw) as string, {
    ADD_ATTR: [
      "target",
      "data-provider",
      "data-platform-host",
      "data-owner",
      "data-name",
      "data-repo-path",
      "data-number",
      "data-item-type",
      "data-external-url",
      "data-task-index",
      "draggable",
    ],
  });
  if (htmlCache.size > 500) htmlCache.clear();
  htmlCache.set(key, html);
  return html;
}
