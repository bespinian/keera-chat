// A small Markdown renderer for replies: paragraphs, headings, lists, quotes,
// tables, fenced code and the usual inline marks.
//
// It builds DOM nodes and never parses a string as HTML, so nothing a model
// writes can become markup or script. Links open only for http, https and
// mailto.
//
// It is forgiving on purpose: it runs on every frame while a reply streams
// in, so half a table or an unclosed code fence must still draw sensibly.

import { h, icon, icons, copyText } from "./ui.js";

/** render turns Markdown into an element. */
export function render(text) {
  const root = h("div", { class: "md" });
  root.append(
    ...blocks(
      String(text || "")
        .replace(/\r\n?/g, "\n")
        .split("\n"),
    ),
  );
  return root;
}

/* ---------------------------------------------------------------- blocks */

const FENCE = /^ {0,3}(`{3,}|~{3,})\s*([^\s`]*)[^`]*$/;
const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})\s*$/;
const HEADING = /^ {0,3}(#{1,6})(?:\s+(.*?))?\s*#*\s*$/;
const RULE = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const QUOTE = /^ {0,3}> ?(.*)$/;
const LIST = /^( *)([-*+]|\d{1,9}[.)])(?:[ \t]+(.*))?$/;
const TABLE_SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;

function blocks(lines) {
  const out = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }

    let m;
    if ((m = line.match(FENCE))) {
      const fence = m[1];
      const code = [];
      i++;
      while (i < lines.length) {
        // A fence closes with a run of the same mark, at least as long.
        const close = lines[i].match(FENCE_CLOSE);
        if (close && close[1][0] === fence[0] && close[1].length >= fence.length) {
          i++;
          break;
        }
        code.push(lines[i]);
        i++;
      }
      out.push(codeBlock(code.join("\n"), m[2]));
      continue;
    }
    if ((m = line.match(HEADING))) {
      out.push(h("h" + Math.min(m[1].length + 1, 6), { class: "md-h" }, inline(m[2] || "")));
      i++;
      continue;
    }
    // Before lists, so "- - -" is a rule and not a list of dashes.
    if (RULE.test(line)) {
      out.push(h("hr"));
      i++;
      continue;
    }
    if (QUOTE.test(line)) {
      const inner = [];
      while (i < lines.length && lines[i].trim() && (m = lines[i].match(QUOTE))) {
        inner.push(m[1]);
        i++;
      }
      out.push(h("blockquote", {}, blocks(inner)));
      continue;
    }
    if (line.includes("|") && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1])) {
      const rows = [line, lines[i + 1]];
      i += 2;
      while (i < lines.length && lines[i].includes("|") && lines[i].trim()) {
        rows.push(lines[i]);
        i++;
      }
      out.push(table(rows));
      continue;
    }
    if (LIST.test(line)) {
      const [list, next] = parseList(lines, i);
      out.push(list);
      i = next;
      continue;
    }

    // A paragraph runs until a blank line or the start of another block.
    const para = [line];
    i++;
    while (i < lines.length && lines[i].trim() && !startsBlock(lines[i])) {
      para.push(lines[i]);
      i++;
    }
    out.push(h("p", {}, inline(para.map((l) => l.trim()).join("\n"))));
  }
  return out;
}

function startsBlock(line) {
  return FENCE.test(line) || HEADING.test(line) || QUOTE.test(line) || RULE.test(line) || LIST.test(line);
}

function indentOf(line) {
  return line.match(/^ */)[0].length;
}

/** parseList reads one list starting at lines[i], with any lists nested in
 *  it, and returns it with the index of the first line after it. */
function parseList(lines, i) {
  const first = lines[i].match(LIST);
  const indent = first[1].length;
  const ordered = /\d/.test(first[2]);
  const list = h(ordered ? "ol" : "ul", {});
  const start = parseInt(first[2], 10);
  if (ordered && start !== 1) list.setAttribute("start", start);
  let loose = false;

  while (i < lines.length) {
    const m = lines[i].match(LIST);
    if (!m || m[1].length !== indent || /\d/.test(m[2]) !== ordered) break;
    const contentIndent = indent + m[2].length + 1;
    const body = [m[3] || ""];
    i++;
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) {
        // A blank line belongs to the item when what follows is indented
        // under it; otherwise the item, and maybe the list, ends here.
        let j = i;
        while (j < lines.length && !lines[j].trim()) j++;
        if (j < lines.length && indentOf(lines[j]) > indent) {
          body.push("");
          i++;
          loose = true;
          continue;
        }
        if (j < lines.length) {
          const sib = lines[j].match(LIST);
          if (sib && sib[1].length === indent && /\d/.test(sib[2]) === ordered) {
            loose = true;
            i = j;
          }
        }
        break;
      }
      const lm = line.match(LIST);
      if (lm && lm[1].length <= indent) break;
      if (indentOf(line) > indent) {
        body.push(line.slice(Math.min(indentOf(line), contentIndent)));
        i++;
        continue;
      }
      // A line that is not indented but starts no block carries on the
      // item's paragraph, as models often write.
      if (!startsBlock(line)) {
        body.push(line);
        i++;
        continue;
      }
      break;
    }
    const parts = blocks(body);
    // A tight list keeps its items as plain lines, not paragraphs.
    if (!loose && parts[0]?.tagName === "P") {
      parts.splice(0, 1, ...parts[0].childNodes);
    }
    list.append(h("li", {}, parts));
  }
  return [list, i];
}

function cells(row) {
  let s = row.trim();
  if (s.startsWith("|")) s = s.slice(1);
  if (s.endsWith("|") && !s.endsWith("\\|")) s = s.slice(0, -1);
  // Split on pipes that are not escaped.
  return s.split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, "|"));
}

function table(rows) {
  const head = cells(rows[0]);
  const align = cells(rows[1]).map((c) =>
    c.startsWith(":") && c.endsWith(":") ? "center" : c.endsWith(":") ? "right" : "",
  );
  const cell = (tag, text, j) => h(tag, { style: align[j] ? { textAlign: align[j] } : null }, inline(text));
  return h(
    "div",
    { class: "md-table" },
    h(
      "table",
      {},
      h(
        "thead",
        {},
        h(
          "tr",
          {},
          head.map((c, j) => cell("th", c, j)),
        ),
      ),
      h(
        "tbody",
        {},
        rows.slice(2).map((r) => {
          const cs = cells(r);
          return h(
            "tr",
            {},
            head.map((_, j) => cell("td", cs[j] || "", j)),
          );
        }),
      ),
    ),
  );
}

function codeBlock(code, lang) {
  const copyBtn = h(
    "button",
    {
      type: "button",
      class: "codeblock-copy",
      onClick: async () => {
        if (await copyText(code, "Code copied")) {
          copyBtn.replaceChildren(icon(icons.check), "Copied");
          setTimeout(() => copyBtn.replaceChildren(icon(icons.copy), "Copy"), 1600);
        }
      },
    },
    icon(icons.copy),
    "Copy",
  );
  return h(
    "div",
    { class: "codeblock" },
    h("div", { class: "codeblock-head" }, h("span", {}, lang || "code"), copyBtn),
    h("pre", {}, h("code", {}, code)),
  );
}

/* ---------------------------------------------------------------- inline */

const ESCAPABLE = /[\\`*_{}[\]()#+\-.!~|>]/;
const LINK =
  /^!?\[((?:[^\][]|\[[^\][]*\])*)\]\(\s*<?([^\s<>()]+(?:\([^\s()]*\))?[^\s<>()]*)>?(?:\s+["'][^"']*["'])?\s*\)/;
const AUTOLINK = /^https?:\/\/[^\s<>"]+/;

/** inline renders the marks inside one block, line breaks kept. */
function inline(text) {
  const out = [];
  let buf = "";
  const flush = () => {
    if (!buf) return;
    buf.split("\n").forEach((l, k) => {
      if (k) out.push(h("br"));
      if (l) out.push(document.createTextNode(l));
    });
    buf = "";
  };

  let i = 0;
  while (i < text.length) {
    const c = text[i];
    const rest = text.slice(i);

    if (c === "\\" && ESCAPABLE.test(text[i + 1] || "")) {
      buf += text[i + 1];
      i += 2;
      continue;
    }

    if (c === "`") {
      const run = rest.match(/^`+/)[0];
      const end = text.indexOf(run, i + run.length);
      if (end > 0) {
        flush();
        let code = text.slice(i + run.length, end);
        if (/^ .*[^ ] $/.test(code)) code = code.slice(1, -1);
        out.push(h("code", {}, code));
        i = end + run.length;
        continue;
      }
      buf += run;
      i += run.length;
      continue;
    }

    if (c === "[" || (c === "!" && text[i + 1] === "[")) {
      const m = rest.match(LINK);
      if (m) {
        flush();
        out.push(link(m[2], m[1]));
        i += m[0].length;
        continue;
      }
    }

    if (c === "h" && (i === 0 || !/[\w/]/.test(text[i - 1]))) {
      const m = rest.match(AUTOLINK);
      if (m) {
        // Trailing punctuation ends the sentence, not the address.
        let url = m[0].replace(/[.,:;!?'"]+$/, "");
        if (url.endsWith(")") && !url.includes("(")) url = url.slice(0, -1);
        flush();
        out.push(link(url, null));
        i += url.length;
        continue;
      }
    }

    if (c === "*" || c === "_" || c === "~") {
      const m = emphasis(text, i);
      if (m) {
        flush();
        out.push(h(m.tag, {}, inline(m.inner)));
        i = m.end;
        continue;
      }
    }

    buf += c;
    i++;
  }
  flush();
  return out;
}

/** emphasis finds a mark that opens at i and closes later on, as
 *  { tag, inner, end }, or null. Underscores only count at word edges, so
 *  snake_case names stay as they are. */
function emphasis(text, i) {
  const c = text[i];
  const double = text[i + 1] === c;
  if (c === "~" && !double) return null;
  const marker = double ? c + c : c;
  const from = i + marker.length;
  // An opening mark is followed by something other than a space.
  if (!text[from] || /\s/.test(text[from])) return null;
  if (c === "_" && i > 0 && /\w/.test(text[i - 1])) return null;

  let j = from;
  while ((j = text.indexOf(marker, j)) >= 0) {
    const inner = text.slice(from, j);
    const after = text[j + marker.length] || "";
    const okClose =
      inner &&
      !/\s$/.test(inner) &&
      !(c === "_" && /\w/.test(after)) &&
      // A single * must not be half of a closing **.
      (double || after !== c || text[j + 2] === c);
    if (okClose && !(inner.includes("`") && (inner.split("`").length - 1) % 2)) {
      const tag = c === "~" ? "del" : double ? "strong" : "em";
      return { tag, inner, end: j + marker.length };
    }
    j += marker.length;
  }
  return null;
}

function link(href, label) {
  const safe = /^(https?:|mailto:)/i.test(href);
  if (!safe) return document.createTextNode(label || href);
  return h("a", { href, target: "_blank", rel: "noopener noreferrer" }, label === null ? href : inline(label));
}
