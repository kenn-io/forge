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

function pairCompatibleNodes(ops: DiffOp<Node>[]): PairedNodeOp[] {
  const paired: PairedNodeOp[] = [];
  for (let i = 0; i < ops.length; i++) {
    const current = ops[i]!;
    const next = ops[i + 1];
    if (current.kind === "delete" && next?.kind === "insert" && nodesCompatible(current.oldItem, next.newItem)) {
      paired.push({ kind: "replace", oldItem: current.oldItem, newItem: next.newItem });
      i++;
    } else {
      paired.push(current);
    }
  }
  return paired;
}

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

function wrapChangedNode(tagName: "del" | "ins", node: Node): HTMLElement {
  const wrapper = document.createElement(tagName);
  if (node instanceof Element && isBlockElement(node)) {
    wrapper.classList.add("markdown-diff__block");
  }
  wrapper.append(node.cloneNode(true));
  return wrapper;
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

export function renderMarkdownDiff(beforeHtml: string, afterHtml: string): string {
  if (beforeHtml === afterHtml) return beforeHtml;
  const before = parseHTMLFragment(beforeHtml);
  const after = parseHTMLFragment(afterHtml);
  const host = document.createElement("div");
  host.append(...diffChildNodes(before.content, after.content));
  return DOMPurify.sanitize(host.innerHTML, {
    ADD_ATTR: ["class"],
  });
}
