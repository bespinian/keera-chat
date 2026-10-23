// Keera Chat: the page. It routes on the URL itself: / is a new chat and
// /c/<id> a conversation.

import { api, ApiError, whenSignedOut } from "./api.js";
import { chatView } from "./chat.js";
import { chats } from "./store.js";
import {
  h,
  icon,
  icons,
  iconButton,
  replace,
  toast,
  menu,
  closeMenu,
  modal,
  confirmDialog,
  theme,
  setTheme,
  initials,
  money,
  day,
  remembered,
  remember,
  oneLine,
  sentence,
  idpMark,
} from "./ui.js";

setTheme(theme());
fitScreen();

const root = document.getElementById("root");

/** app is the state every part of the page shares. */
const app = {
  me: null,
  // The person's conversations in this browser. See store.js.
  chats: null,
  conversations: [],
  settings: { instructions: "" },
  // choice is the model and key the next message goes with.
  choice: null,
  view: null,
  // Replies still streaming, by conversation id. See chat.js.
  live: new Map(),
  choose,
  saw,
  go,
};

/* ------------------------------------------------------------------ boot */

async function boot() {
  whenSignedOut(() => signIn());
  try {
    app.me = await api.me();
    app.chats = chats(app.me.user.id + "|" + app.me.user.org_id);
    [app.conversations, app.settings] = await Promise.all([app.chats.list(), app.chats.settings()]);
  } catch (err) {
    // whenSignedOut has already shown the sign-in screen. Showing it again
    // would lose the sign-in error, which the first one took from the URL.
    if (err instanceof ApiError && err.code === "signed_out") return;
    if (err instanceof ApiError && err.status === 401) return signIn();
    return fatal(err);
  }
  shell();
  route();
  window.addEventListener("popstate", route);
  // A reply is kept once it is complete, so leaving while one arrives loses
  // it. The browser asks the person first.
  window.addEventListener("beforeunload", (e) => {
    if (app.live.size) {
      e.preventDefault();
      e.returnValue = "";
    }
  });
}

// A phone's keyboard covers the bottom of the page, and the composer with it.
// Chrome and Firefox shrink the page instead (interactive-widget in
// index.html), but Safari does not, so the shell takes the height of what is
// still in view.
function fitScreen() {
  const vv = window.visualViewport;
  if (!vv) return;
  const fit = () => {
    // Times the scale, so zooming in does not shrink the page as well.
    document.documentElement.style.setProperty("--screen-height", `${vv.height * vv.scale}px`);
    // Safari scrolls the page up to show the field, which leaves the top of
    // the shell out of view once it is short enough to fit.
    if (vv.scale === 1) window.scrollTo(0, 0);
  };
  vv.addEventListener("resize", fit);
  fit();
}

function fatal(err) {
  card(
    h("div", { class: "banner banner-bad" }, err.message || String(err)),
    h("button", { class: "btn", onClick: () => location.reload() }, icon(icons.retry), "Try again"),
  );
}

/** card fills the page with one card under the brand, for signing in or a
 *  start that failed. */
function card(...children) {
  root.className = "";
  replace(
    root,
    h(
      "div",
      { class: "signin" },
      h(
        "div",
        { class: "signin-card" },
        h(
          "div",
          { class: "signin-brand" },
          h("img", { src: "/assets/favicon.svg", alt: "", width: 28, height: 28, class: "brand-mark" }),
          "Keera Chat",
        ),
        ...children,
      ),
    ),
  );
}

/* --------------------------------------------------------------- sign in */

async function signIn() {
  document.title = "Sign in · Keera Chat";
  const params = new URLSearchParams(location.search);
  let error = sentence(params.get("sign_in_error") || "");
  if (params.has("sign_in_error")) history.replaceState(null, "", "/");

  let cfg = null;
  try {
    cfg = await api.authConfig();
  } catch (err) {
    error = error || err.message;
  }

  const ways = [];
  for (const p of cfg?.providers || []) {
    ways.push(
      h(
        "a",
        { class: "btn btn-signin", href: "/auth/login?provider=" + encodeURIComponent(p.name) },
        idpMark(p.name) || icon(icons.signout),
        `Continue with ${p.label || p.name}`,
      ),
    );
  }
  if (cfg?.passkeys) {
    ways.push(
      h(
        "a",
        { class: "btn btn-signin", href: "/auth/login?provider=passkey" },
        icon(icons.passkey),
        "Continue with a passkey",
      ),
    );
  }

  card(
    h("h1", { class: "signin-title" }, "Welcome"),
    h("p", { class: "signin-lead" }, "Sign in with your Keera account to start chatting."),
    error ? h("div", { class: "banner banner-bad", role: "alert" }, error) : null,
    ways.length
      ? h("div", { class: "signin-providers" }, ways)
      : cfg
        ? h(
            "div",
            { class: "banner banner-warn" },
            "This Keera Gateway has no way to sign in set up yet. Ask its administrator.",
          )
        : h("button", { class: "btn", onClick: () => signIn() }, icon(icons.retry), "Try again"),
    h(
      "p",
      { class: "signin-note" },
      "Your organisation decides which models you can use and what they may cost. ",
      cfg?.gateway_url
        ? h("a", { href: cfg.gateway_url, target: "_blank", rel: "noopener" }, "Keera Gateway")
        : "Keera Gateway",
      " keeps the rules.",
    ),
  );
}

/* ----------------------------------------------------------------- shell */

const els = {};

function shell() {
  root.className = "";
  const collapsed = remembered("sidebar") === "closed";

  els.search = h("input", {
    class: "search-input",
    type: "search",
    placeholder: "Search chats",
    "aria-label": "Search chats",
    onInput: paintList,
  });
  els.list = h("nav", { class: "conv-list", "aria-label": "Chats" });
  els.account = h("button", {
    class: "account",
    type: "button",
    "aria-haspopup": "menu",
    onClick: (e) => accountMenu(e.currentTarget),
  });
  els.picker = h("button", {
    class: "model-picker",
    type: "button",
    "aria-haspopup": "menu",
    onClick: (e) => modelMenu(e.currentTarget),
  });
  els.main = h("div", { class: "main-body" });

  const sidebar = h(
    "aside",
    { class: "sidebar" },
    h(
      "div",
      { class: "sidebar-head" },
      h(
        "a",
        { class: "brand", href: "/", onClick: (e) => go(e, "/") },
        h("img", { src: "/assets/favicon.svg", alt: "", width: 24, height: 24, class: "brand-mark" }),
        "Keera Chat",
      ),
      iconButton(icons.sidebar, "Close the sidebar", () => toggleSidebar(true), { class: "hide-narrow" }),
      iconButton(icons.close, "Close the menu", () => setNav(false), { class: "show-narrow" }),
    ),
    h(
      "a",
      { class: "new-chat", href: "/", onClick: (e) => go(e, "/") },
      icon(icons.newChat),
      "New chat",
      h("kbd", { class: "hide-narrow" }, shortcutLabel()),
    ),
    h("div", { class: "search" }, icon(icons.search), els.search),
    els.list,
    h("p", { class: "conv-hint" }, "For privacy, your chats are stored in this browser and never on the server."),
    h("div", { class: "sidebar-foot" }, els.account),
  );

  els.shell = h(
    "div",
    { class: "shell" + (collapsed ? " sidebar-collapsed" : "") },
    sidebar,
    h("div", { class: "nav-scrim", onClick: () => setNav(false) }),
    h(
      "main",
      { class: "main" },
      h(
        "header",
        { class: "topbar" },
        iconButton(icons.menu, "Open the menu", () => setNav(true), { class: "show-narrow" }),
        iconButton(icons.sidebar, "Open the sidebar", () => toggleSidebar(false), {
          class: "when-collapsed",
        }),
        iconButton(icons.newChat, "New chat", () => go(null, "/"), { class: "when-collapsed-or-narrow" }),
        els.picker,
        h("div", { class: "spacer" }),
      ),
      els.main,
    ),
  );
  replace(root, els.shell);
  paintAccount();
  paintList();

  document.addEventListener("keydown", (e) => {
    // Ctrl or Cmd + Shift + O starts a new chat, as in ChatGPT.
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === "o") {
      e.preventDefault();
      go(null, "/");
    }
  });
}

function shortcutLabel() {
  return /Mac|iPhone|iPad/.test(navigator.platform) ? "⇧⌘O" : "Ctrl+Shift+O";
}

function toggleSidebar(close) {
  els.shell.classList.toggle("sidebar-collapsed", close);
  remember("sidebar", close ? "closed" : "");
}

/** setNav opens or closes the sidebar as a drawer, on a narrow screen. */
function setNav(open) {
  els.shell.classList.toggle("nav-open", open);
  if (open) els.search.focus();
}

/* ---------------------------------------------------------------- routing */

function go(e, path) {
  if (e) {
    // A click with a modifier opens a tab, as links do.
    if (e.ctrlKey || e.metaKey || e.shiftKey) return;
    e.preventDefault();
  }
  closeMenu();
  if (location.pathname !== path) history.pushState(null, "", path);
  route();
}

function route() {
  const m = location.pathname.match(/^\/c\/(c[0-9a-f]{24})$/);
  const id = m ? m[1] : null;
  if (!m && location.pathname !== "/") history.replaceState(null, "", "/");
  setNav(false);
  // Already there: a new chat that just got its id, or the same link again.
  if (app.view && id && app.view.id === id) return;
  if (app.view) app.view.destroy();
  app.view = chatView(app, id);
  replace(els.main, app.view.el);
  paintList();
  // Not on a phone, where focusing the field pops up the keyboard.
  if (matchMedia("(min-width: 769px)").matches) app.view.focus();
}

/* ------------------------------------------------------------------ model */

/** choose settles the model and key the next message goes with: the ones
 *  asked for if the person may still use them, else the last ones they
 *  picked, else the first. */
function choose(alias, keyID) {
  const models = app.me.models;
  if (!models.length) {
    app.choice = null;
    paintPicker();
    return;
  }
  const model =
    models.find((m) => m.alias === alias) ||
    models.find((m) => m.alias === remembered("model")) ||
    models[0];
  const keys = keysFor(model.alias);
  const key =
    keys.find((k) => k.id === keyID) ||
    keys.find((k) => k.id === remembered("key:" + model.alias)) ||
    keys[0];
  app.choice = { model, key };
  paintPicker();
}

function keysFor(alias) {
  return app.me.keys.filter((k) => k.models.includes(alias));
}

function paintPicker() {
  const c = app.choice;
  els.picker.hidden = !c;
  if (!c) return;
  replace(
    els.picker,
    h("span", { class: "model-picker-name" }, c.model.name || c.model.alias),
    icon(icons.chevronDown),
  );
}

function modelMenu(anchor) {
  const c = app.choice;
  if (!c) return;
  const items = app.me.models.map((m) => ({
    label: m.name || m.alias,
    hint: m.router
      ? m.description || "Picks a model for each message"
      : m.description || (m.name && m.name !== m.alias ? m.alias : ""),
    icon: m.router ? icons.router : icons.sparkle,
    checked: m.alias === c.model.alias,
    onClick: () => {
      remember("model", m.alias);
      choose(m.alias, remembered("key:" + m.alias));
      app.view?.render();
    },
  }));
  // Most people hold one key and never see this. Someone with several picks
  // which one pays, since each has its own budget and rules.
  const keys = keysFor(c.model.alias);
  if (keys.length > 1) {
    items.push({ separator: true }, { heading: "Send with key" });
    for (const k of keys) {
      items.push({
        label: k.name,
        hint: k.project || "",
        icon: icons.key,
        checked: k.id === c.key.id,
        onClick: () => {
          remember("key:" + c.model.alias, k.id);
          choose(c.model.alias, k.id);
        },
      });
    }
  }
  menu(anchor, items, { wide: true });
}

/* ------------------------------------------------------------------- list */

/** saw records that a conversation was created or used, so the sidebar
 *  lists it first. */
function saw(summary) {
  const i = app.conversations.findIndex((c) => c.id === summary.id);
  const prev = i >= 0 ? app.conversations.splice(i, 1)[0] : {};
  app.conversations.unshift({ ...prev, ...summary });
  paintList();
}

function paintList() {
  const q = els.search.value.trim().toLowerCase();
  const shown = app.conversations.filter((c) => !q || (c.title || "").toLowerCase().includes(q));
  if (!shown.length) {
    replace(
      els.list,
      h(
        "div",
        { class: "conv-empty" },
        q ? "No chat has that in its title." : "Your chats will show here.",
      ),
    );
    return;
  }
  const groups = new Map();
  for (const c of shown) {
    const g = bucket(c.updated_at);
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g).push(c);
  }
  const current = app.view?.id;
  replace(
    els.list,
    [...groups].map(([label, convs]) =>
      h(
        "div",
        { class: "conv-group" },
        h("div", { class: "conv-group-label" }, label),
        convs.map((c) => convItem(c, c.id === current)),
      ),
    ),
  );
}

function convItem(c, current) {
  const streaming = app.live.has(c.id);
  const item = h(
    "div",
    { class: "conv-item" + (current ? " is-current" : "") },
    h(
      "a",
      {
        class: "conv-link",
        href: "/c/" + c.id,
        "aria-current": current ? "page" : null,
        title: c.title,
        onClick: (e) => go(e, "/c/" + c.id),
      },
      h("span", { class: "conv-title" }, c.title || "New chat"),
      streaming ? h("span", { class: "conv-live", title: "A reply is arriving" }) : null,
    ),
    iconButton(icons.more, "More", (e) => convMenu(e.currentTarget, c, item), { class: "conv-more" }),
  );
  return item;
}

function convMenu(anchor, c, item) {
  menu(anchor, [
    { label: "Rename", icon: icons.pencil, onClick: () => renameInline(c, item) },
    {
      label: "Delete",
      icon: icons.trash,
      danger: true,
      onClick: async () => {
        const ok = await confirmDialog(
          "Delete this chat?",
          `“${c.title}” will be deleted for good. This cannot be undone.`,
          "Delete",
        );
        if (!ok) return;
        drop(app.live.get(c.id));
        try {
          await app.chats.remove(c.id);
        } catch (err) {
          return toast(err.message, "bad");
        }
        app.conversations = app.conversations.filter((x) => x.id !== c.id);
        if (app.view?.id === c.id) go(null, "/");
        else paintList();
      },
    },
  ]);
}

/** drop stops a reply whose conversation is being deleted, so it does not
 *  save the conversation again when it ends. */
function drop(live) {
  if (!live) return;
  live.deleted = true;
  live.abort.abort();
}

function renameInline(c, item) {
  const field = h("input", { class: "conv-rename", value: c.title, "aria-label": "Chat title" });
  let done = false;
  const finish = async (save) => {
    if (done) return;
    done = true;
    const title = oneLine(field.value, 200);
    if (save && title && title !== c.title) {
      try {
        await app.chats.rename(c.id, title);
        c.title = title;
        // The open chat and a reply still arriving each hold the conversation
        // too, and save it whole, so they must not keep the old title.
        const l = app.live.get(c.id);
        if (l) l.conv.title = title;
        if (app.view?.id === c.id) app.view.retitle(title);
      } catch (err) {
        toast(err.message, "bad");
      }
    }
    paintList();
  };
  field.addEventListener("keydown", (e) => {
    if (e.key === "Enter") finish(true);
    if (e.key === "Escape") finish(false);
  });
  field.addEventListener("blur", () => finish(true));
  replace(item, field);
  field.focus();
  field.select();
}

/** bucket names the group a conversation is listed under. */
function bucket(iso) {
  const then = new Date(iso);
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const days = Math.floor((today - new Date(then).setHours(0, 0, 0, 0)) / 864e5);
  if (days <= 0) return "Today";
  if (days === 1) return "Yesterday";
  if (days < 7) return "Previous 7 days";
  if (days < 30) return "Previous 30 days";
  return then.toLocaleDateString(undefined, { month: "long", year: "numeric" });
}

/* ---------------------------------------------------------------- account */

function paintAccount() {
  const u = app.me.user;
  replace(
    els.account,
    h("span", { class: "avatar", "aria-hidden": "true" }, initials(u.name, u.email)),
    h(
      "span",
      { class: "account-id" },
      h("span", { class: "account-name" }, u.name || u.email),
      h("span", { class: "account-org" }, u.org_name || u.email),
    ),
    icon(icons.more),
  );
}

function accountMenu(anchor) {
  const u = app.me.user;
  const items = [
    h(
      "div",
      { class: "menu-who" },
      h("strong", {}, u.name || u.email),
      u.name ? h("span", {}, u.email) : null,
    ),
  ];
  const b = app.choice?.key.budget;
  if (b) items.push(budgetEl(b));
  items.push({ separator: true });

  if (u.orgs.length > 1) {
    items.push({ heading: "Organisation" });
    for (const o of u.orgs) {
      items.push({
        label: o.name,
        icon: icons.orgs,
        checked: o.id === u.org_id,
        onClick: () => switchOrg(o),
      });
    }
    items.push({ separator: true });
  }

  const themes = { system: "Follows your system", light: "Light", dark: "Dark" };
  items.push(
    { label: "Custom instructions", icon: icons.sliders, onClick: instructions },
    {
      label: "Theme",
      icon: icons.theme,
      hint: themes[theme()],
      onClick: () => {
        const next = { system: "light", light: "dark", dark: "system" }[theme()];
        setTheme(next);
        toast(`Theme: ${themes[next].toLowerCase()}`);
      },
    },
    {
      label: "My access in Keera Gateway",
      icon: icons.external,
      href: app.me.gateway_url + "/access",
    },
    { separator: true },
    { label: "Export chats", icon: icons.download, onClick: exportChats },
    { label: "Import chats", icon: icons.upload, onClick: importChats },
    { label: "Delete all chats", icon: icons.trash, danger: true, onClick: deleteAll },
    { label: "Sign out", icon: icons.signout, onClick: signOut },
  );
  menu(anchor, items, { up: true, wide: true });
}

/** budgetEl shows how much of the tightest budget above the current key is
 *  spent, so a refusal for a spent budget does not come as a surprise. */
function budgetEl(b) {
  const share = Math.min(1, b.spend_micros / b.budget_micros);
  const period = { day: "today", week: "this week", month: "this month" }[b.period] || "";
  // Whose budget it is, so the person knows whom to ask for more.
  const owner = { org: "Organisation", project: "Project", key: "Key" }[b.scope] || "";
  return h(
    "div",
    { class: "menu-budget" },
    h(
      "div",
      { class: "menu-budget-row" },
      h("span", {}, `Budget ${period}`.trim()),
      h("span", { class: "menu-budget-num" }, `${money(b.spend_micros)} of ${money(b.budget_micros)}`),
    ),
    h(
      "div",
      { class: "meter" + (share >= 0.9 ? " is-high" : "") },
      h("div", { class: "meter-fill", style: { width: (share * 100).toFixed(1) + "%" } }),
    ),
    h(
      "div",
      { class: "menu-budget-reset" },
      [b.name ? `${owner} ${b.name}` : owner, b.resets_at ? `starts again on ${day(b.resets_at)}` : ""]
        .filter(Boolean)
        .join(" · "),
    ),
  );
}

async function switchOrg(o) {
  if (o.id === app.me.user.org_id) return;
  try {
    await api.switchOrg(o.id);
  } catch (err) {
    return toast(err.message, "bad");
  }
  // Keys, models and chats all belong to the organisation, so start over.
  location.href = "/";
}

async function instructions() {
  const field = h("textarea", {
    class: "input",
    rows: "8",
    maxlength: "8000",
    placeholder: "For example: I work in hospital administration in Bern. Answer in German, and keep it short.",
  });
  field.value = app.settings.instructions;
  const saved = await modal({
    title: "Custom instructions",
    body: h(
      "div",
      {},
      h(
        "p",
        { class: "modal-text" },
        "What should every reply take into account? This is sent ahead of each conversation. ",
        "Your organisation may add instructions of its own.",
      ),
      field,
    ),
    onSubmit: () => app.chats.saveSettings({ instructions: field.value.trim() }),
  });
  if (saved) {
    app.settings = saved;
    toast("Saved. New messages follow these instructions.", "good");
  }
}

/** exportChats saves the person's chats in this organisation to a file, so
 *  they can take them to another browser or computer. */
async function exportChats() {
  let file;
  try {
    file = await app.chats.exportAll();
  } catch (err) {
    return toast(err.message, "bad");
  }
  if (!file.conversations.length) return toast("You have no chats to export yet.");
  const url = URL.createObjectURL(new Blob([JSON.stringify(file)], { type: "application/json" }));
  const link = h("a", { href: url, download: `keera-chats-${file.exported_at.slice(0, 10)}.json` });
  document.body.append(link);
  link.click();
  link.remove();
  // The browser reads the file a little after the click.
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
  toast(`Exported ${count(file.conversations.length, "chat")}. Anyone with the file can read them.`, "good");
}

function importChats() {
  const picker = h("input", {
    type: "file",
    accept: ".json,application/json",
    onChange: () => picker.files[0] && importFile(picker.files[0]),
  });
  picker.click();
}

async function importFile(f) {
  let result;
  try {
    let data;
    try {
      data = JSON.parse(await f.text());
    } catch {
      throw new Error("This is not a file exported from Keera Chat.");
    }
    // A chat whose reply is still arriving is kept once the reply is in.
    result = await app.chats.importAll(data, new Set(app.live.keys()));
    [app.conversations, app.settings] = await Promise.all([app.chats.list(), app.chats.settings()]);
  } catch (err) {
    return toast(err.message, "bad");
  }
  paintList();
  // The chat on screen may have been replaced by a newer copy.
  if (app.view && result.updated.includes(app.view.id)) {
    app.view.destroy();
    app.view = null;
    route();
  }
  const done = [];
  if (result.added) done.push(`added ${count(result.added, "chat")}`);
  if (result.updated.length) done.push(`updated ${count(result.updated.length, "chat")}`);
  let message = done.length ? sentence(done.join(" and ")) : "These chats are all here already.";
  if (result.instructions) message += " Your custom instructions came with them.";
  toast(message, "good");
}

function count(n, noun) {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

async function deleteAll() {
  const ok = await confirmDialog(
    "Delete all chats?",
    `Every chat you have in ${app.me.user.org_name || "this organisation"} in this browser will be deleted for good. This cannot be undone.`,
    "Delete all",
  );
  if (!ok) return;
  for (const l of app.live.values()) drop(l);
  drop(app.view?.pending);
  try {
    await app.chats.removeAll();
  } catch (err) {
    return toast(err.message, "bad");
  }
  app.conversations = [];
  go(null, "/");
}

async function signOut() {
  try {
    await api.logout();
  } catch (err) {
    // The server clears the cookie, so a request that never reached it
    // leaves the person signed in.
    return toast(err.message, "bad");
  }
  location.href = "/";
}

boot();
