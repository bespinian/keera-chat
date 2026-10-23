// Small DOM helpers, the same ones the gateway's panel is built with. Every
// node is made from text, so nothing a model or a person writes is ever
// parsed as markup.

export function h(tag, props, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "style" && typeof v === "object") Object.assign(el.style, v);
    else if (k.startsWith("on"))
      el.addEventListener(k.slice(2).toLowerCase(), v);
    else if (k === "dataset") Object.assign(el.dataset, v);
    else if (v === true) el.setAttribute(k, "");
    else el.setAttribute(k, v);
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children.flat(4)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

/** svg is h() for SVG elements, which need their namespace. */
export function svg(tag, props, ...children) {
  const el = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === null || v === undefined || v === false) continue;
    el.setAttribute(k, v);
  }
  append(el, children);
  return el;
}

/** idpMarks are the identity providers' logos, as the gateway's panel has
 *  them. They are keyed by the names an operator picks in the gateway's
 *  KEERA_OIDC_PROVIDERS, so the obvious spellings of each vendor (idpAliases)
 *  share the artwork. */
const idpMarks = {
  google: {
    viewBox: "0 0 24 24",
    path: "M12.24 10.285V14.4h6.806c-.275 1.765-2.056 5.174-6.806 5.174-4.095 0-7.439-3.389-7.439-7.574s3.344-7.574 7.439-7.574c2.33 0 3.891.989 4.785 1.849l3.254-3.138C18.189 1.186 15.479 0 12.24 0c-6.635 0-12 5.365-12 12s5.365 12 12 12c6.926 0 11.52-4.869 11.52-11.726 0-.788-.085-1.39-.189-1.989H12.24z",
  },
  microsoft: {
    viewBox: "0 0 23 23",
    path: "M0 0h11v11H0zM12 0h11v11H12zM0 12h11v11H0zM12 12h11v11H12z",
  },
  github: {
    viewBox: "0 0 24 24",
    path: "M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12",
  },
};

const idpAliases = {
  workspace: "google",
  gsuite: "google",
  entra: "microsoft",
  entraid: "microsoft",
  azure: "microsoft",
  azuread: "microsoft",
  m365: "microsoft",
};

/** idpMark is an identity provider's logo, or null for one with no mark. */
export function idpMark(name) {
  const key = String(name || "").toLowerCase();
  const mark = idpMarks[idpAliases[key] || key];
  if (!mark) return null;
  return svg(
    "svg",
    { viewBox: mark.viewBox, fill: "currentColor", "aria-hidden": "true" },
    svg("path", { d: mark.path }),
  );
}

export function replace(el, ...children) {
  el.replaceChildren();
  append(el, children);
  return el;
}

/* ----------------------------------------------------------------- icons */

export function icon(path) {
  return svg(
    "svg",
    {
      viewBox: "0 0 24 24",
      fill: "none",
      stroke: "currentColor",
      "stroke-width": "1.8",
      "stroke-linecap": "round",
      "stroke-linejoin": "round",
      "aria-hidden": "true",
    },
    svg("path", { d: path }),
  );
}

export const icons = {
  // A pen on a page, so it is not mistaken for editing a message.
  newChat: "M12 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7M18.4 2.6a2.1 2.1 0 0 1 3 3L12 15l-4 1 1-4z",
  send: "M12 19V5M5 12l7-7 7 7",
  stop: "M7 7h10v10H7z",
  plus: "M12 5v14M5 12h14",
  close: "M18 6L6 18M6 6l12 12",
  copy: "M20 9h-9a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2v-9a2 2 0 0 0-2-2zM5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1",
  check: "M20 6L9 17l-5-5",
  pencil: "M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z",
  trash:
    "M3 6h18M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6",
  retry: "M3 12a9 9 0 1 0 3-6.7L3 8M3 3v5h5",
  more: "M5 12h.01M12 12h.01M19 12h.01",
  menu: "M3 6h18M3 12h18M3 18h18",
  sidebar: "M3 4h18v16H3zM9 4v16",
  search: "M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16zM21 21l-4.3-4.3",
  chevronDown: "M6 9l6 6 6-6",
  arrowDown: "M12 5v14M19 12l-7 7-7-7",
  signout: "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9",
  theme: "M12 3a9 9 0 1 0 9 9 7 7 0 0 1-9-9z",
  sliders:
    "M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6",
  download: "M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3",
  upload: "M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12",
  external: "M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6M15 3h6v6M10 14L21 3",
  orgs: "M3 21h18M5 21V7l7-4 7 4v14M9 9h.01M9 13h.01M9 17h.01M15 9h.01M15 13h.01M15 17h.01",
  passkey:
    "M2.6 17.4A2 2 0 0 0 2 18.8V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.2a2 2 0 0 0 1.4-.6l.8-.8a6.5 6.5 0 1 0-4-4zM16.5 7.5h.01",
  key: "M21 2l-2 2m-7.6 7.6a5 5 0 1 1-7.1 7.1 5 5 0 0 1 7.1-7.1zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3",
  router: "M4 6h5l4 6h7M20 12l-3-3m3 3l-3 3M9 6l4 12h7",
  thought:
    "M9 18h6M10 22h4M12 2a7 7 0 0 0-4 12.7c.6.5 1 1.3 1 2.3h6c0-1 .4-1.8 1-2.3A7 7 0 0 0 12 2z",
  sparkle:
    "M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9zM19 16l.8 2.2L22 19l-2.2.8L19 22l-.8-2.2L16 19l2.2-.8z",
};

/** iconButton is a square, quiet button that says what it does to a screen
 *  reader and on hover. */
export function iconButton(path, label, onClick, extra = {}) {
  return h(
    "button",
    {
      type: "button",
      class: "icon-btn " + (extra.class || ""),
      title: label,
      "aria-label": label,
      onClick,
      ...(extra.attrs || {}),
    },
    icon(path),
  );
}

/* ---------------------------------------------------------------- toasts */

let toastHost = null;

export function toast(message, tone = "") {
  if (!toastHost) {
    // A live region, so screen readers announce toasts.
    toastHost = h("div", {
      class: "toasts",
      role: "status",
      "aria-live": "polite",
    });
    document.body.append(toastHost);
  }
  const el = h("div", { class: "toast " + tone }, message);
  toastHost.append(el);
  setTimeout(() => el.remove(), tone === "bad" ? 7000 : 3000);
}

export async function copyText(text, what = "Copied") {
  try {
    await navigator.clipboard.writeText(text);
    toast(what, "good");
    return true;
  } catch {
    // Clipboard access needs a secure context, which a plain-http setup on
    // another machine is not.
    toast("This browser did not allow copying here. Select the text instead.", "bad");
    return false;
  }
}

/* ----------------------------------------------------------------- modal */

/** showOverlay puts node over the page and returns the function that takes it
 *  away again, which Escape calls too. Closing gives focus back to where it
 *  was and passes its value on to onClose. */
function showOverlay(node, onClose = () => {}) {
  const previous = document.activeElement;
  const onKey = (e) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      close(null);
    }
  };
  const close = (value) => {
    node.remove();
    document.removeEventListener("keydown", onKey, true);
    if (previous && previous.focus) previous.focus();
    onClose(value);
  };
  document.body.append(node);
  document.addEventListener("keydown", onKey, true);
  return close;
}

/** modal shows a dialog and resolves with what onSubmit returns, or null when
 *  it is dismissed. onSubmit may throw to keep the dialog open. */
export function modal({ title, body, submit = "Save", danger = false, onSubmit }) {
  return new Promise((resolve) => {
    const error = h("div", { class: "banner banner-bad", hidden: true });
    const okBtn = h(
      "button",
      { class: "btn " + (danger ? "btn-danger-solid" : "btn-primary"), type: "submit" },
      submit,
    );
    const form = h(
      "form",
      {
        class: "modal",
        role: "dialog",
        "aria-modal": "true",
        "aria-label": title,
        onSubmit: async (e) => {
          e.preventDefault();
          okBtn.disabled = true;
          try {
            close(onSubmit ? await onSubmit() : true);
          } catch (err) {
            error.textContent = err.message || String(err);
            error.hidden = false;
            okBtn.disabled = false;
          }
        },
      },
      h("div", { class: "modal-head" }, h("h2", {}, title)),
      h("div", { class: "modal-body" }, error, body),
      h(
        "div",
        { class: "modal-foot" },
        h("button", { class: "btn", type: "button", onClick: () => close(null) }, "Cancel"),
        okBtn,
      ),
    );
    const overlay = h(
      "div",
      {
        class: "overlay",
        onMousedown: (e) => {
          if (e.target === overlay) close(null);
        },
      },
      form,
    );
    const close = showOverlay(overlay, resolve);
    const first = form.querySelector("textarea, input, select") || okBtn;
    first.focus();
  });
}

/** confirmDialog asks before something that cannot be undone. */
export function confirmDialog(title, text, submit) {
  return modal({ title, body: h("p", { class: "modal-text" }, text), submit, danger: true });
}

/** viewImage shows an image as large as the window allows. A click anywhere
 *  or Escape closes it. */
function viewImage(src, alt = "") {
  const closeBtn = iconButton(icons.close, "Close", () => close(), { class: "viewer-close" });
  const overlay = h(
    "div",
    {
      class: "overlay viewer",
      role: "dialog",
      "aria-modal": "true",
      "aria-label": alt || "Image",
      onClick: () => close(),
    },
    h("img", { src, alt }),
    closeBtn,
  );
  const close = showOverlay(overlay);
  closeBtn.focus();
}

/** imageButton is a preview that opens the image in full when clicked. */
export function imageButton(src, alt = "") {
  return h(
    "button",
    {
      class: "image-open",
      type: "button",
      title: "Enlarge",
      "aria-label": alt ? `Enlarge ${alt}` : "Enlarge image",
      onClick: () => viewImage(src, alt),
    },
    h("img", { src, alt }),
  );
}

/* ------------------------------------------------------------------ menu */

let openMenu = null;

/** menu opens a list of actions next to anchor. Each item is
 *  { label, icon, onClick, href, danger, checked, hint }, { separator: true },
 *  { heading }, or a node, which is shown as it is. An item with href opens
 *  it in a new tab. It closes on a click outside, on Escape, after an action,
 *  and on a second click on its anchor. */
export function menu(anchor, items, { up = false, wide = false } = {}) {
  if (openMenu && anchor.getAttribute("aria-expanded") === "true") {
    closeMenu();
    return null;
  }
  closeMenu();
  const list = h("div", {
    class: "menu" + (wide ? " menu-wide" : ""),
    role: "menu",
  });
  for (const item of items) {
    if (!item) continue;
    if (item instanceof Node) {
      list.append(item);
      continue;
    }
    if (item.separator) {
      list.append(h("div", { class: "menu-sep", role: "separator" }));
      continue;
    }
    if (item.heading) {
      list.append(h("div", { class: "menu-heading" }, item.heading));
      continue;
    }
    list.append(
      h(
        item.href ? "a" : "button",
        {
          class: "menu-item" + (item.danger ? " menu-danger" : ""),
          role: "menuitem",
          type: item.href ? null : "button",
          href: item.href || null,
          target: item.href ? "_blank" : null,
          rel: item.href ? "noopener" : null,
          "aria-checked": item.checked === undefined ? null : String(!!item.checked),
          onClick: (e) => {
            closeMenu();
            if (item.onClick) item.onClick(e);
          },
        },
        item.icon ? icon(item.icon) : h("span", { class: "menu-icon-space" }),
        h(
          "span",
          { class: "menu-label" },
          h("span", {}, item.label),
          item.hint ? h("span", { class: "menu-hint" }, item.hint) : null,
        ),
        item.checked ? h("span", { class: "menu-check" }, icon(icons.check)) : null,
      ),
    );
  }
  document.body.append(list);
  place(list, anchor, up);
  anchor.setAttribute("aria-expanded", "true");

  const onDown = (e) => {
    if (!list.contains(e.target) && !anchor.contains(e.target)) closeMenu();
  };
  const onKey = (e) => {
    if (e.key === "Escape") {
      closeMenu();
      anchor.focus();
    }
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const items = [...list.querySelectorAll(".menu-item")];
      const i = items.indexOf(document.activeElement);
      const next = e.key === "ArrowDown" ? i + 1 : i - 1;
      items[(next + items.length) % items.length]?.focus();
    }
  };
  const onResize = () => closeMenu();
  setTimeout(() => document.addEventListener("mousedown", onDown), 0);
  document.addEventListener("keydown", onKey);
  window.addEventListener("resize", onResize);
  openMenu = () => {
    list.remove();
    anchor.setAttribute("aria-expanded", "false");
    document.removeEventListener("mousedown", onDown);
    document.removeEventListener("keydown", onKey);
    window.removeEventListener("resize", onResize);
  };
  list.querySelector(".menu-item")?.focus({ preventScroll: true });
  return list;
}

export function closeMenu() {
  if (openMenu) {
    const close = openMenu;
    openMenu = null;
    close();
  }
}

/** place puts a floating element beside its anchor, kept on screen. */
function place(el, anchor, up) {
  const a = anchor.getBoundingClientRect();
  const m = el.getBoundingClientRect();
  const gap = 6;
  let top = up ? a.top - m.height - gap : a.bottom + gap;
  if (!up && top + m.height > window.innerHeight - 8) top = a.top - m.height - gap;
  if (top < 8) top = 8;
  const left = Math.max(8, Math.min(a.left, window.innerWidth - m.width - 8));
  el.style.top = top + "px";
  el.style.left = left + "px";
}

/* ------------------------------------------------------------ formatting */

/** money renders integer micro-francs, as the gateway reports spend. */
export function money(micros) {
  const v = (micros || 0) / 1e6;
  const s = v === 0 ? "0.00" : Math.abs(v) >= 1 ? v.toFixed(2) : v.toPrecision(2);
  return `CHF ${s}`;
}

/** sentence makes a sentence of a lower-case message from the server or the
 *  gateway: a capital first and a full stop last. */
export function sentence(s) {
  if (!s) return s;
  s = s[0].toUpperCase() + s.slice(1);
  return /[.!?…]$/.test(s) ? s : s + ".";
}

/** oneLine collapses all whitespace to single spaces and cuts the result to
 *  at most max characters, at a word where it can. */
export function oneLine(s, max) {
  s = s.trim().split(/\s+/).join(" ");
  const chars = [...s];
  if (chars.length <= max) return s;
  let cut = chars.slice(0, max - 1).join("");
  const i = cut.lastIndexOf(" ");
  if (i > max / 2) cut = cut.slice(0, i);
  return cut.replace(/[\s\p{P}]+$/u, "") + "…";
}

export function day(iso) {
  return new Date(iso).toLocaleDateString(undefined, {
    day: "numeric",
    month: "long",
  });
}

/** initials of a name or an address, for the avatar. */
export function initials(name, email) {
  const words = (name || "").trim().split(/\s+/).filter(Boolean);
  if (words.length >= 2) return (words[0][0] + words[words.length - 1][0]).toUpperCase();
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (email || "?").slice(0, 2).toUpperCase();
}

/* ----------------------------------------------------------------- theme */

export function theme() {
  return remembered("theme") || "system";
}

export function setTheme(t) {
  if (t === "system") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", t);
  remember("theme", t === "system" ? "" : t);
}

/** remembered and remember keep small per-browser choices, such as the last
 *  model. Storage may be refused, so both fail quietly. */
export function remembered(key) {
  try {
    return localStorage.getItem("keera.chat." + key) || "";
  } catch {
    return "";
  }
}

export function remember(key, value) {
  try {
    if (value) localStorage.setItem("keera.chat." + key, value);
    else localStorage.removeItem("keera.chat." + key);
  } catch {
    // See remembered.
  }
}
