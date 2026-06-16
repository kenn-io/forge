import DOMPurify from "dompurify";

type DiffOp<T> =
  | { kind: "equal"; oldItem: T; newItem: T }
  | { kind: "delete"; oldItem: T }
  | { kind: "insert"; newItem: T };

function parseHTMLFragment(html: string): HTMLTemplateElement {
  const template = document.createElement("template");
  template.innerHTML = html;
  return template;
}

function nodesEqual(left: Node, right: Node): boolean {
  if (left.nodeType !== right.nodeType) return false;
  if (left.nodeType === Node.TEXT_NODE) return left.textContent === right.textContent;
  return left instanceof Element && right instanceof Element && left.outerHTML === right.outerHTML;
}

function nodesCompatible(left: Node, right: Node): boolean {
  if (left.nodeType === Node.TEXT_NODE && right.nodeType === Node.TEXT_NODE) return true;
  if (!(left instanceof Element) || !(right instanceof Element)) return false;
  return left.tagName === right.tagName && compatibleAttributes(left, right);
}

function compatibleAttributes(left: Element, right: Element): boolean {
  const leftAttrs = attributesMap(left);
  const rightAttrs = attributesMap(right);
  if (leftAttrs.size !== rightAttrs.size) return false;
  for (const [name, value] of leftAttrs) {
    if (rightAttrs.get(name) !== value) return false;
  }
  return true;
}

function attributesMap(element: Element): Map<string, string> {
  const attrs = new Map<string, string>();
  for (const attr of Array.from(element.attributes)) {
    attrs.set(attr.name, attr.value);
  }
  return attrs;
}

function diffSequence<T>(
  oldItems: readonly T[],
  newItems: readonly T[],
  equal: (left: T, right: T) => boolean,
): DiffOp<T>[] {
  if (oldItems.length * newItems.length > 20_000) {
    return [
      ...oldItems.map((oldItem): DiffOp<T> => ({ kind: "delete", oldItem })),
      ...newItems.map((newItem): DiffOp<T> => ({ kind: "insert", newItem })),
    ];
  }

  const rows = oldItems.length + 1;
  const cols = newItems.length + 1;
  const lengths = Array.from({ length: rows }, () => Array<number>(cols).fill(0));
  for (let i = oldItems.length - 1; i >= 0; i--) {
    for (let j = newItems.length - 1; j >= 0; j--) {
      lengths[i]![j] = equal(oldItems[i]!, newItems[j]!)
        ? lengths[i + 1]![j + 1]! + 1
        : Math.max(lengths[i + 1]![j]!, lengths[i]![j + 1]!);
    }
  }

  const ops: DiffOp<T>[] = [];
  let i = 0;
  let j = 0;
  while (i < oldItems.length && j < newItems.length) {
    if (equal(oldItems[i]!, newItems[j]!)) {
      ops.push({ kind: "equal", oldItem: oldItems[i]!, newItem: newItems[j]! });
      i++;
      j++;
    } else if (lengths[i + 1]![j]! >= lengths[i]![j + 1]!) {
      ops.push({ kind: "delete", oldItem: oldItems[i]! });
      i++;
    } else {
      ops.push({ kind: "insert", newItem: newItems[j]! });
      j++;
    }
  }
  while (i < oldItems.length) {
    ops.push({ kind: "delete", oldItem: oldItems[i]! });
    i++;
  }
  while (j < newItems.length) {
    ops.push({ kind: "insert", newItem: newItems[j]! });
    j++;
  }
  return ops;
}

function diffChildNodes(oldParent: ParentNode, newParent: ParentNode): Node[] {
  const oldChildren = Array.from(oldParent.childNodes);
  const newChildren = Array.from(newParent.childNodes);
  const ops = pairCompatibleNodes(diffSequence(oldChildren, newChildren, nodesEqual));
  const output: Node[] = [];
  for (const op of ops) {
    if (op.kind === "equal") {
      output.push(op.oldItem.cloneNode(true));
    } else if (op.kind === "replace") {
      output.push(...diffNode(op.oldItem, op.newItem));
    } else if (op.kind === "delete") {
      output.push(wrapChangedNode("del", op.oldItem));
    } else {
      output.push(wrapChangedNode("ins", op.newItem));
    }
  }
  return output;
}

type PairedNodeOp = DiffOp<Node> | { kind: "replace"; oldItem: Node; newItem: Node };
type DeleteNodeOp = Extract<DiffOp<Node>, { kind: "delete" }>;
type InsertNodeOp = Extract<DiffOp<Node>, { kind: "insert" }>;

function pairCompatibleNodes(ops: DiffOp<Node>[]): PairedNodeOp[] {
  const paired: PairedNodeOp[] = [];
  for (let i = 0; i < ops.length; i++) {
    const current = ops[i]!;
    if (current.kind !== "delete") {
      paired.push(current);
      continue;
    }

    const deleteRun: DeleteNodeOp[] = [];
    while (ops[i]?.kind === "delete") {
      deleteRun.push(ops[i] as DeleteNodeOp);
      i++;
    }

    const insertRun: InsertNodeOp[] = [];
    while (ops[i]?.kind === "insert") {
      insertRun.push(ops[i] as InsertNodeOp);
      i++;
    }
    i--;

    if (insertRun.length === 0) {
      paired.push(...deleteRun);
      continue;
    }

    paired.push(...pairChangedNodeRun(deleteRun, insertRun));
  }
  return paired;
}

function pairChangedNodeRun(deleteRun: DeleteNodeOp[], insertRun: InsertNodeOp[]): PairedNodeOp[] {
  const deleteItems = deleteRun.map((op): ChangedNodeRunItem => ({ node: op.oldItem }));
  const insertItems = insertRun.map((op): ChangedNodeRunItem => ({ node: op.newItem }));
  const aligned = diffSequence(deleteItems, insertItems, (left, right) => nodesCompatible(left.node, right.node));
  return aligned.map((op): PairedNodeOp => {
    if (op.kind === "equal") {
      return {
        kind: "replace",
        oldItem: op.oldItem.node,
        newItem: op.newItem.node,
      };
    }
    if (op.kind === "delete") return { kind: "delete", oldItem: op.oldItem.node };
    return { kind: "insert", newItem: op.newItem.node };
  });
}

type ChangedNodeRunItem = { node: Node };

function diffNode(oldNode: Node, newNode: Node): Node[] {
  if (nodesEqual(oldNode, newNode)) return [oldNode.cloneNode(true)];
  if (oldNode.nodeType === Node.TEXT_NODE && newNode.nodeType === Node.TEXT_NODE) {
    return diffText(oldNode.textContent ?? "", newNode.textContent ?? "");
  }
  if (oldNode instanceof Element && newNode instanceof Element) {
    if (oldNode.tagName === newNode.tagName && compatibleAttributes(oldNode, newNode)) {
      const clone = oldNode.cloneNode(false) as Element;
      clone.append(...diffChildNodes(oldNode, newNode));
      markChangedContainer(clone);
      return [clone];
    }
  }
  return [wrapChangedNode("del", oldNode), wrapChangedNode("ins", newNode)];
}

function markChangedContainer(element: Element): void {
  if (element.matches("li,tr")) element.classList.add("changed");
}

function wrapChangedNode(tagName: "del" | "ins", node: Node): Element {
  if (node instanceof Element && isStructuralChildElement(node)) {
    return wrapChangedStructuralElement(tagName, node);
  }

  const wrapper = document.createElement(tagName);
  if (node instanceof Element && isBlockElement(node)) {
    wrapper.classList.add("markdown-diff__block");
  }
  wrapper.append(node.cloneNode(true));
  return wrapper;
}

function wrapChangedStructuralElement(tagName: "del" | "ins", element: Element): Element {
  const clone = element.cloneNode(false) as Element;
  clone.classList.add("changed", "markdown-diff__structural");
  clone.setAttribute("data-diff-kind", tagName === "del" ? "delete" : "insert");

  if (element.tagName === "TR") {
    appendChangedTableRowChildren(clone, element, tagName);
  } else {
    clone.append(wrapChangedChildren(tagName, element));
  }
  return clone;
}

function appendChangedTableRowChildren(row: Element, source: Element, tagName: "del" | "ins"): void {
  for (const child of Array.from(source.childNodes)) {
    if (child instanceof Element && /^(TD|TH)$/.test(child.tagName)) {
      const cell = child.cloneNode(false) as Element;
      cell.append(wrapChangedChildren(tagName, child));
      row.append(cell);
    } else if (!(child.nodeType === Node.TEXT_NODE && /^\s*$/.test(child.textContent ?? ""))) {
      row.append(child.cloneNode(true));
    }
  }
}

function wrapChangedChildren(tagName: "del" | "ins", source: ParentNode): HTMLElement {
  const wrapper = document.createElement(tagName);
  wrapper.append(...Array.from(source.childNodes).map((child) => child.cloneNode(true)));
  return wrapper;
}

function isStructuralChildElement(node: Element): boolean {
  return /^(LI|TR|TD|TH)$/.test(node.tagName);
}

function isBlockElement(node: Element): boolean {
  return /^(ADDRESS|ARTICLE|ASIDE|BLOCKQUOTE|DIV|DL|FIELDSET|FIGCAPTION|FIGURE|FOOTER|FORM|H[1-6]|HEADER|HR|LI|MAIN|NAV|OL|P|PRE|SECTION|TABLE|UL)$/.test(
    node.tagName,
  );
}

function diffText(oldText: string, newText: string): Node[] {
  const oldTokens = tokenizeText(oldText);
  const newTokens = tokenizeText(newText);
  const ops = coalesceTextOps(diffSequence(oldTokens, newTokens, (left, right) => left === right));
  const output: Node[] = [];
  for (const op of ops) {
    if (op.kind === "equal") {
      output.push(document.createTextNode(op.text));
    } else {
      const text = op.tokens.join("");
      if (/^\s*$/.test(text)) {
        output.push(document.createTextNode(text));
        continue;
      }
      const wrapper = document.createElement(op.kind);
      wrapper.textContent = text;
      output.push(wrapper);
    }
  }
  return output;
}

type TextOp = { kind: "equal"; text: string } | { kind: "del" | "ins"; tokens: string[] };

function coalesceTextOps(ops: DiffOp<string>[]): TextOp[] {
  const output: TextOp[] = [];
  for (const op of ops) {
    if (op.kind === "equal") {
      const previous = output[output.length - 1];
      if (previous?.kind === "equal") previous.text += op.oldItem;
      else output.push({ kind: "equal", text: op.oldItem });
    } else {
      const kind = op.kind === "delete" ? "del" : "ins";
      const token = op.kind === "delete" ? op.oldItem : op.newItem;
      const previous = output[output.length - 1];
      if (previous?.kind === kind) previous.tokens.push(token);
      else output.push({ kind, tokens: [token] });
    }
  }
  return output;
}

function tokenizeText(text: string): string[] {
  return text.match(/\s+|[^\s]+/g) ?? [];
}

function diffHTMLFragment(beforeHtml: string, afterHtml: string): HTMLElement {
  const before = parseHTMLFragment(beforeHtml);
  const after = parseHTMLFragment(afterHtml);
  const host = document.createElement("div");
  host.append(...diffChildNodes(before.content, after.content));
  return host;
}

function sanitizeMarkdownDiff(html: string): string {
  return DOMPurify.sanitize(html, {
    ADD_ATTR: ["class", "aria-hidden", "data-diff-kind"],
  });
}

function placeholderFor(node: Element): Element {
  const placeholder = node.cloneNode(true) as Element;
  placeholder.classList.add("markdown-diff__placeholder");
  placeholder.setAttribute("aria-hidden", "true");
  return placeholder;
}

function projectDiffForSide(node: Node, side: "before" | "after"): Node | null {
  if (node instanceof Element) {
    const structuralKind = changedStructuralKind(node);
    if ((structuralKind === "delete" && side === "after") || (structuralKind === "insert" && side === "before")) {
      return placeholderFor(node);
    }

    const tagName = node.tagName.toLowerCase();
    if (tagName === "del" && side === "after") {
      return node.classList.contains("markdown-diff__block") ? placeholderFor(node) : null;
    }
    if (tagName === "ins" && side === "before") {
      return node.classList.contains("markdown-diff__block") ? placeholderFor(node) : null;
    }
    const clone = node.cloneNode(false) as Element;
    for (const child of Array.from(node.childNodes)) {
      const projected = projectDiffForSide(child, side);
      if (projected) clone.append(projected);
    }
    return clone;
  }
  return node.cloneNode(true);
}

function changedStructuralKind(node: Element): "delete" | "insert" | null {
  if (!node.classList.contains("markdown-diff__structural")) return null;
  const kind = node.getAttribute("data-diff-kind");
  return kind === "delete" || kind === "insert" ? kind : null;
}

function projectDiffHTML(host: ParentNode, side: "before" | "after"): string {
  const projection = document.createElement("div");
  for (const child of Array.from(host.childNodes)) {
    const projected = projectDiffForSide(child, side);
    if (projected) projection.append(projected);
  }
  return sanitizeMarkdownDiff(projection.innerHTML);
}

export function renderMarkdownDiff(beforeHtml: string, afterHtml: string): string {
  if (beforeHtml === afterHtml) return beforeHtml;
  return sanitizeMarkdownDiff(diffHTMLFragment(beforeHtml, afterHtml).innerHTML);
}

export function renderMarkdownSplitDiff(
  beforeHtml: string,
  afterHtml: string,
): { beforeHtml: string; afterHtml: string } {
  if (beforeHtml === afterHtml) return { beforeHtml, afterHtml };
  const host = diffHTMLFragment(beforeHtml, afterHtml);
  return {
    beforeHtml: projectDiffHTML(host, "before"),
    afterHtml: projectDiffHTML(host, "after"),
  };
}
