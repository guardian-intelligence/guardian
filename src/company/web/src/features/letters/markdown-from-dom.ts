// The letter editor's write half: the edited page (contenteditable over the
// rendered letter) back to the markdown the store holds. It understands the
// vocabulary the letters render — paragraphs, h2/h3, quotes, lists, emphasis,
// strong, links, line breaks — and treats every other element (the ink
// spans, anything a browser invents while editing) as transparent, so only
// the words and their structure survive. Rendering the result through
// `marked` reproduces the page it was read from.

const BLOCK_TAGS = new Set([
  "P",
  "DIV",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "BLOCKQUOTE",
  "UL",
  "OL",
  "HR",
  "PRE",
]);

export function markdownFromDom(root: Node): string {
  return `${blocksOf(root).join("\n\n")}\n`;
}

function blocksOf(parent: Node): string[] {
  const blocks: string[] = [];
  // Inline content sitting directly in a block container (a browser leaves
  // this behind after deleting every paragraph) is gathered into one.
  let loose: Node[] = [];
  const flushLoose = () => {
    const text = inlineOfNodes(loose).trim();
    if (text) blocks.push(escapeBlockStart(text));
    loose = [];
  };
  for (const node of Array.from(parent.childNodes)) {
    if (node instanceof HTMLElement && BLOCK_TAGS.has(node.tagName)) {
      flushLoose();
      const block = blockOf(node);
      if (block) blocks.push(block);
    } else {
      loose.push(node);
    }
  }
  flushLoose();
  return blocks;
}

function blockOf(el: HTMLElement): string {
  switch (el.tagName) {
    case "H1":
    case "H2":
      return prefixed("## ", inlineOf(el).trim());
    case "H3":
    case "H4":
    case "H5":
    case "H6":
      return prefixed("### ", inlineOf(el).trim());
    case "BLOCKQUOTE":
      return blocksOf(el)
        .join("\n\n")
        .split("\n")
        .map((line) => (line ? `> ${line}` : ">"))
        .join("\n");
    case "UL":
    case "OL":
      return Array.from(el.children)
        .filter((item) => item.tagName === "LI")
        .map((item, i) => {
          const marker = el.tagName === "OL" ? `${i + 1}. ` : "- ";
          return `${marker}${blocksOf(item).join(" ")}`;
        })
        .join("\n");
    case "HR":
      return "---";
    case "PRE":
      return `\`\`\`\n${el.textContent ?? ""}\n\`\`\``;
    case "DIV":
      // A browser wraps a new line in a div when it doesn't know the
      // paragraph separator; a div of blocks is just its blocks.
      return Array.from(el.children).some((child) => BLOCK_TAGS.has(child.tagName))
        ? blocksOf(el).join("\n\n")
        : escapeBlockStart(inlineOf(el).trim());
    default:
      return escapeBlockStart(inlineOf(el).trim());
  }
}

function prefixed(prefix: string, text: string): string {
  return text ? `${prefix}${text}` : "";
}

function inlineOf(el: Node): string {
  return inlineOfNodes(Array.from(el.childNodes));
}

function inlineOfNodes(nodes: readonly Node[]): string {
  return nodes.map(inlineNode).join("").replace(/ {2,}/g, " ");
}

function inlineNode(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) {
    return escapeInline((node.textContent ?? "").replace(/ /g, " ").replace(/\s+/g, " "));
  }
  if (!(node instanceof HTMLElement)) return "";
  switch (node.tagName) {
    case "EM":
    case "I":
      return wrap("_", inlineOf(node));
    case "STRONG":
    case "B":
      return wrap("**", inlineOf(node));
    case "CODE":
      return `\`${node.textContent ?? ""}\``;
    case "A": {
      const href = node.getAttribute("href") ?? "";
      const text = inlineOf(node);
      return href ? `[${text}](${href})` : text;
    }
    case "BR":
      return "\\\n";
    default:
      return inlineOf(node);
  }
}

// Emphasis markers must hug their words: "_ word _" is not emphasis, so
// edge spaces move outside the markers.
function wrap(marker: string, inner: string): string {
  const match = /^(\s*)(.*?)(\s*)$/s.exec(inner);
  const [, lead = "", core = "", trail = ""] = match ?? [];
  return core ? `${lead}${marker}${core}${marker}${trail}` : inner;
}

function escapeInline(text: string): string {
  return text.replace(/[\\`*_[\]<~]/g, (c) => `\\${c}`);
}

// Text that would read as a heading, quote, list, or rule at the start of a
// paragraph stays a paragraph.
function escapeBlockStart(text: string): string {
  return text
    .replace(/^(#|>|[-+*](?=\s))/, (m) => `\\${m}`)
    .replace(/^(\d+)([.)])(?=\s)/, "$1\\$2");
}
