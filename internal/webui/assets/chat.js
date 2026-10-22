// One conversation: the thread and the composer.
//
// A reply keeps streaming when the person goes to another conversation. Its
// state lives in app.live, keyed by conversation, and whichever view shows
// that conversation paints it, so coming back shows the reply still arriving.

import { api, ApiError } from "./api.js";
import { render as markdown } from "./markdown.js";
import { exclusive, newID, summary } from "./store.js";
import {
  h,
  icon,
  icons,
  iconButton,
  imageButton,
  replace,
  toast,
  copyText,
  sentence,
  oneLine,
} from "./ui.js";

// The formats every API that reads images accepts. The server checks the same
// list, and maxImages, in internal/chat/turns.go, and store.js checks
// imported files against both.
const imageTypes = "image/png,image/jpeg,image/gif,image/webp";
// Providers scale larger images down to about this anyway, and every image is
// sent again with each later message.
const maxImageSide = 2048;
const maxImages = 10;

export function chatView(app, id) {
  const view = {
    id,
    el: null,
    destroy,
    paint: null,
    focus: () => input.focus(),
  };
  let conv = id ? null : { id: null, title: "", messages: [] };
  let attached = [];
  let editing = -1;
  let failed = "";

  /* --------------------------------------------------------------- DOM */

  const thread = h("div", { class: "thread", role: "log", "aria-live": "off" });
  const scroll = h("div", { class: "chat-scroll" }, thread);
  const toBottom = h(
    "button",
    {
      class: "to-bottom",
      type: "button",
      title: "Scroll to the end",
      "aria-label": "Scroll to the end",
      hidden: true,
      onClick: () => {
        pinned = true;
        scroll.scrollTo({ top: scroll.scrollHeight, behavior: "smooth" });
      },
    },
    icon(icons.arrowDown),
  );

  const input = h("textarea", {
    class: "composer-input",
    rows: "1",
    placeholder: "How can I help?",
    "aria-label": "Message",
    onInput: () => {
      autosize(input);
      paintSend();
    },
    onKeyDown: (e) => {
      // Enter sends and Shift+Enter breaks the line, as in every chat.
      if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
        e.preventDefault();
        submit();
      }
      if (e.key === "Escape" && live()) live().abort.abort();
    },
    onPaste: (e) => {
      const files = [...(e.clipboardData?.files || [])].filter((f) =>
        f.type.startsWith("image/"),
      );
      if (files.length) {
        e.preventDefault();
        attach(files);
      }
    },
  });

  const picker = h("input", {
    type: "file",
    accept: imageTypes,
    multiple: true,
    hidden: true,
    onChange: () => {
      const files = [...picker.files];
      // Cleared, so choosing the same file again still fires a change.
      picker.value = "";
      attach(files);
    },
  });
  const tray = h("div", { class: "composer-tray", hidden: true });
  const attachBtn = iconButton(
    icons.plus,
    "Attach images",
    () => picker.click(),
    {
      class: "composer-attach",
    },
  );
  const sendBtn = h(
    "button",
    {
      class: "composer-send",
      type: "submit",
      title: "Send",
      "aria-label": "Send",
    },
    icon(icons.send),
  );
  const stopBtn = h(
    "button",
    {
      class: "composer-send composer-stop",
      type: "button",
      title: "Stop",
      "aria-label": "Stop the reply",
      hidden: true,
      onClick: () => live() && live().abort.abort(),
    },
    icon(icons.stop),
  );
  const composer = h(
    "form",
    {
      class: "composer",
      onSubmit: (e) => {
        e.preventDefault();
        submit();
      },
      onClick: (e) => {
        // The whole box is the field, as in other chats.
        if (e.target === composer) input.focus();
      },
    },
    tray,
    input,
    h(
      "div",
      { class: "composer-row" },
      attachBtn,
      h("div", { class: "spacer" }),
      sendBtn,
      stopBtn,
    ),
    picker,
  );
  const note = h("div", { class: "composer-note" });
  const hero = h("div", { class: "hero" });
  const dock = h(
    "div",
    { class: "dock" },
    h("div", { class: "column" }, composer, note),
  );

  const el = h(
    "div",
    {
      class: "chat",
      onDragover: (e) => {
        if ([...(e.dataTransfer?.items || [])].some((i) => i.kind === "file")) {
          e.preventDefault();
          el.classList.add("dropping");
        }
      },
      onDragleave: (e) => {
        if (!el.contains(e.relatedTarget)) el.classList.remove("dropping");
      },
      onDrop: (e) => {
        el.classList.remove("dropping");
        const files = [...(e.dataTransfer?.files || [])];
        if (files.length) {
          e.preventDefault();
          attach(files);
        }
      },
    },
    hero,
    scroll,
    toBottom,
    dock,
  );
  view.el = el;

  /* --------------------------------------------------------- scrolling */

  // pinned tracks whether the reader is at the bottom, so someone who
  // scrolled up to reread is not pulled back down by the next token.
  let pinned = true;
  scroll.addEventListener("scroll", () => {
    pinned = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 48;
    toBottom.hidden = pinned;
  });
  function stick() {
    if (pinned) scroll.scrollTop = scroll.scrollHeight;
    toBottom.hidden = pinned || !conv || !conv.messages.length;
  }

  /* -------------------------------------------------------- rendering */

  function live() {
    return conv && conv.id ? app.live.get(conv.id) : view.pending;
  }

  function render() {
    const empty = !conv || !conv.messages.length;
    el.classList.toggle("is-empty", empty && !!conv);
    el.classList.toggle("is-loading", !conv);
    // Without a model to send to, the composer could only refuse.
    dock.hidden = !app.choice;
    paintHero();
    paintNote();
    paintSend();
    document.title =
      conv && conv.title ? `${conv.title} · Keera Chat` : "Keera Chat";
    if (!conv) {
      replace(
        thread,
        h("div", { class: "thread-loading" }, h("div", { class: "spinner" })),
      );
      return;
    }
    replace(
      thread,
      conv.messages.map((m, i) => messageEl(m, i)),
    );
    stick();
  }

  // While a reply streams, only its own node is redrawn, once per frame.
  let queued = false;
  view.paint = () => {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      const l = live();
      if (!l || !conv) return render();
      const i = conv.messages.indexOf(l.reply);
      const node = thread.children[i];
      if (node) node.replaceWith(messageEl(l.reply, i));
      else render();
      stick();
    });
  };

  function paintHero() {
    if (!conv || conv.messages.length) {
      hero.replaceChildren();
      return;
    }
    const noAccess = !app.choice;
    replace(
      hero,
      h(
        "div",
        { class: "hero-inner column" },
        h("img", {
          class: "hero-mark",
          src: "/assets/keera.png",
          alt: "",
          width: 80,
          height: 80,
        }),
        h("h1", { class: "hero-title" }, greeting(app.me.user)),
        noAccess
          ? accessNotice(app)
          : h("p", { class: "hero-sub" }, heroLine(app)),
      ),
    );
  }

  function paintNote() {
    if (!app.choice) return;
    replace(
      note,
      "Replies can be wrong, so check what matters. Messages go through ",
      app.me.user.org_name ? `${app.me.user.org_name}'s ` : "your ",
      "Keera Gateway, and its rules apply.",
    );
  }

  function paintSend() {
    const busy = !!live();
    sendBtn.hidden = busy;
    stopBtn.hidden = !busy;
    sendBtn.disabled = !app.choice || (!input.value.trim() && !attached.length);
    attachBtn.disabled = !app.choice;
    input.disabled = !app.choice;
  }

  function paintTray() {
    tray.hidden = attached.length === 0;
    replace(
      tray,
      attached.map((a) =>
        h(
          "div",
          { class: "thumb" },
          imageButton(a.url, a.name),
          h(
            "button",
            {
              class: "thumb-remove",
              type: "button",
              title: "Remove",
              "aria-label": `Remove ${a.name}`,
              onClick: () => {
                attached = attached.filter((x) => x !== a);
                paintTray();
                paintSend();
                input.focus();
              },
            },
            icon(icons.close),
          ),
        ),
      ),
    );
  }

  /* --------------------------------------------------------- messages */

  function messageEl(m, i) {
    return m.role === "user" ? userEl(m, i) : replyEl(m, i);
  }

  function userEl(m, i) {
    if (editing === i) return editEl(m, i);
    const busy = !!live();
    return h(
      "div",
      { class: "msg msg-user" },
      h(
        "div",
        { class: "bubble" },
        m.images?.length
          ? h(
              "div",
              { class: "bubble-images" },
              m.images.map((url) => imageButton(url)),
            )
          : null,
        m.content ? h("div", { class: "bubble-text" }, m.content) : null,
      ),
      h(
        "div",
        { class: "msg-actions" },
        m.content
          ? iconButton(icons.copy, "Copy", () => copyText(m.content))
          : null,
        !busy
          ? iconButton(icons.pencil, "Edit", () => {
              editing = i;
              render();
            })
          : null,
      ),
    );
  }

  /** editEl rewrites one of the person's messages. Sending it drops every
   *  message after it, as in other chats. */
  function editEl(m, i) {
    const box = h("textarea", {
      class: "edit-input",
      "aria-label": "Edit your message",
    });
    box.value = m.content;
    const cancel = () => {
      editing = -1;
      render();
    };
    const save = () => {
      const text = box.value.trim();
      if (!text && !m.images?.length) return;
      editing = -1;
      send({ content: text, images: m.images || [], edit: i });
    };
    box.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
        e.preventDefault();
        save();
      }
      if (e.key === "Escape") cancel();
    });
    box.addEventListener("input", () => autosize(box));
    queueMicrotask(() => {
      autosize(box);
      box.focus();
      box.setSelectionRange(box.value.length, box.value.length);
    });
    return h(
      "div",
      { class: "msg msg-user msg-editing" },
      h(
        "div",
        { class: "edit-box" },
        box,
        h(
          "div",
          { class: "edit-row" },
          h(
            "span",
            { class: "edit-hint" },
            "Sending replaces the replies after this message.",
          ),
          h("div", { class: "spacer" }),
          h(
            "button",
            { class: "btn btn-sm", type: "button", onClick: cancel },
            "Cancel",
          ),
          h(
            "button",
            { class: "btn btn-sm btn-primary", type: "button", onClick: save },
            "Send",
          ),
        ),
      ),
    );
  }

  function replyEl(m, i) {
    const last = i === conv.messages.length - 1;
    const body = h("div", { class: "msg-body" });
    if (m.streaming && !m.content && m.reasoning) {
      // Without a sign of it, a model that only thinks looks stuck.
      body.append(h("div", { class: "thinking shimmer" }, "Thinking…"));
    } else if (m.streaming && !m.content) {
      body.append(
        h(
          "div",
          { class: "typing", "aria-label": "Waiting for the reply" },
          h("span"),
          h("span"),
          h("span"),
        ),
      );
    } else if (m.content) {
      const md = markdown(m.content);
      if (m.streaming) md.classList.add("is-streaming");
      body.append(md);
    }

    const actions =
      !m.streaming &&
      h(
        "div",
        { class: "msg-actions" + (last ? " is-last" : "") },
        m.content
          ? iconButton(icons.copy, "Copy", () => copyText(m.content))
          : null,
        m.reasoning
          ? iconButton(
              icons.thought,
              m.showReasoning
                ? "Hide the thought process"
                : "Show the thought process",
              (e) => {
                m.showReasoning = !m.showReasoning;
                const node = replyEl(m, i);
                e.currentTarget.closest(".msg").replaceWith(node);
                node
                  .querySelector(".reasoning")
                  ?.scrollIntoView({ block: "nearest", behavior: "smooth" });
              },
              {
                attrs: { "aria-expanded": m.showReasoning ? "true" : "false" },
              },
            )
          : null,
        last && !live() && app.choice && !m.error
          ? iconButton(icons.retry, "Answer again", () =>
              send({ regenerate: true }),
            )
          : null,
        h("span", { class: "msg-model" }, modelName(app, m.model)),
      );

    return h(
      "div",
      { class: "msg msg-reply" },
      body,
      m.error
        ? h(
            "div",
            { class: "reply-error", role: "alert" },
            h(
              "div",
              {},
              sentence(m.error),
              // What the model's server said, which only helps whoever runs
              // Keera, so it stays folded away.
              m.error_detail
                ? h(
                    "details",
                    { class: "error-detail" },
                    h("summary", {}, "Details"),
                    h("div", {}, m.error_detail),
                  )
                : null,
            ),
            last && !live() && app.choice
              ? h(
                  "button",
                  {
                    class: "btn btn-sm",
                    type: "button",
                    onClick: () => send({ regenerate: true }),
                  },
                  icon(icons.retry),
                  "Try again",
                )
              : null,
          )
        : null,
      m.stopped ? h("div", { class: "msg-note" }, "Stopped.") : null,
      m.finish === "length"
        ? h("div", { class: "msg-note" }, "The reply reached its length limit.")
        : null,
      actions,
      // Opened from the actions, so it shows where the person clicked.
      actions && m.reasoning && m.showReasoning
        ? h(
            "div",
            { class: "reasoning" },
            h("div", { class: "reasoning-head" }, "Thought process"),
            h("div", { class: "reasoning-body" }, m.reasoning),
          )
        : null,
    );
  }

  /* ------------------------------------------------------------ images */

  async function attach(files) {
    if (!app.choice) return;
    for (const file of files) {
      if (attached.length >= maxImages) {
        toast(`A message can carry at most ${maxImages} images.`, "bad");
        break;
      }
      if (!imageTypes.split(",").includes(file.type)) {
        toast(`${file.name} is not a PNG, JPEG, GIF or WebP image.`, "bad");
        continue;
      }
      try {
        attached.push({ name: file.name, url: await readImage(file) });
      } catch {
        toast(`${file.name} could not be read as an image.`, "bad");
      }
    }
    paintTray();
    paintSend();
    input.focus();
  }

  /* ----------------------------------------------------------- sending */

  function submit() {
    const text = input.value.trim();
    if ((!text && !attached.length) || live() || !app.choice) return;
    const images = attached.map((a) => a.url);
    input.value = "";
    autosize(input);
    attached = [];
    paintTray();
    send({ content: text, images });
  }

  async function send({
    content = "",
    images = [],
    edit = null,
    regenerate = false,
  }) {
    const choice = app.choice;
    if (!choice || live()) return;
    const target = conv;
    const before = target.messages.slice();
    const lastUser = target.messages.findLastIndex((m) => m.role === "user");
    if (regenerate && lastUser < 0) return;

    // Shown at once, and kept once the server takes it.
    let ask = null;
    if (regenerate) {
      target.messages = target.messages.slice(0, lastUser + 1);
    } else {
      ask = {
        role: "user",
        content,
        images,
        created_at: new Date().toISOString(),
      };
      if (edit !== null) target.messages = target.messages.slice(0, edit);
      target.messages.push(ask);
    }
    // What the model answers: the conversation up to the person's message.
    const turns = target.messages.map(turn);
    const reply = {
      role: "assistant",
      model: choice.model.alias,
      content: "",
      reasoning: "",
      streaming: true,
    };
    target.messages.push(reply);
    const state = {
      conv: target,
      reply,
      abort: new AbortController(),
      raw: "",
      thought: "",
    };
    if (target.id) app.live.set(target.id, state);
    else view.pending = state;
    pinned = true;
    render();

    const paintOwner = () => {
      // Whichever view shows this conversation now, if any.
      const v = app.view;
      if (v && v.paint && (v.id === target.id || v.pending === state))
        v.paint();
    };

    // started is set once the server took the message. Until then, nothing
    // is kept and the conversation can go back to how it was.
    let started = false;
    let changed = false;
    try {
      await exclusive(
        target.id,
        async () => {
          // Another tab may have written to this conversation since it was
          // opened here. Sending from the old copy would lose that.
          if (target.id) {
            const stored = await app.chats.find(target.id);
            if (!stored || stored.updated_at !== target.updated_at) {
              changed = true;
              throw new ApiError(
                409,
                "changed",
                "This chat changed in another tab, so it was reloaded. Send again.",
              );
            }
          }
          const stream = await api.chat(
            {
              model: choice.model.alias,
              model_name: choice.model.name || choice.model.alias,
              key_id: choice.key.id,
              instructions: app.settings.instructions,
              messages: turns,
            },
            state.abort.signal,
          );
          started = true;
          await begin(target, state, { content, images, choice });

          for await (const { event, data } of stream) {
            if (event === "delta") {
              state.raw += data.content || "";
              state.thought += data.reasoning || "";
              const split = splitThink(state.raw);
              reply.content = split.content;
              reply.reasoning = state.thought + split.reasoning;
              paintOwner();
            } else if (event === "done") {
              const open = reply.showReasoning;
              Object.assign(reply, data, {
                streaming: false,
                showReasoning: open,
              });
            }
          }
        },
        () => {
          throw new ApiError(
            409,
            "busy",
            "A reply is still being written in this chat, perhaps in another tab.",
          );
        },
      );
    } catch (err) {
      if (started && err && err.name === "AbortError") {
        reply.stopped = true;
      } else if (
        err instanceof ApiError ||
        (err && err.name === "AbortError")
      ) {
        // Refused or stopped before the server took it, so the conversation
        // is put back as it was, and a new message goes back into the
        // composer.
        target.messages = before;
        if (ask && edit === null && app.view === view && !input.value) {
          input.value = content;
          attached = images.map((url, n) => ({ name: `image ${n + 1}`, url }));
          paintTray();
          queueMicrotask(() => autosize(input));
        }
        if (err instanceof ApiError && err.code !== "signed_out")
          toast(err.message, "bad");
      } else {
        reply.error = (err && err.message) || "The reply ended unexpectedly.";
      }
    } finally {
      reply.streaming = false;
      // A conversation deleted while its reply arrived stays deleted.
      const keep = started && !state.deleted;
      if (keep) {
        reply.created_at ||= new Date().toISOString();
        target.updated_at = new Date().toISOString();
        try {
          await app.chats.save(target);
        } catch (err) {
          reply.error = `The reply could not be kept. Copy anything you need from it now. ${err.message}`;
          reply.error_detail = undefined;
        }
      }
      // Only once it is kept, so a next message cannot start from before it.
      if (target.id) app.live.delete(target.id);
      if (view.pending === state) view.pending = null;
      if (keep) app.saw(summary(target));
      const v = app.view;
      if (changed && v === view) load();
      else if (v && (v === view || v.id === target.id)) v.render();
      if (v === view) input.focus();
    }
  }

  /** begin keeps a conversation once the server took its new message, so
   *  the message is not lost whatever happens to the reply. A new
   *  conversation gets its id and its place in the sidebar here. */
  async function begin(target, state, { content, images, choice }) {
    const now = new Date().toISOString();
    if (!target.id) {
      target.id = newID();
      target.title = titleFrom(content, images.length);
      target.created_at = now;
      app.live.set(target.id, state);
      if (view.pending === state) view.pending = null;
      // The address follows the new conversation, unless the person already
      // went somewhere else.
      if (app.view === view) {
        view.id = target.id;
        history.replaceState(null, "", "/c/" + target.id);
      }
    }
    target.model = choice.model.alias;
    target.key_id = choice.key.id;
    target.updated_at = now;
    try {
      await app.chats.save({
        ...target,
        messages: target.messages.filter((m) => m !== state.reply),
      });
    } catch {
      // The reply is kept with it at the end, or says why it could not be.
    }
    if (!state.deleted) app.saw(summary(target));
  }
  view.render = render;

  /** retitle takes a title given in the sidebar, so the next save keeps it. */
  view.retitle = (title) => {
    if (!conv) return;
    conv.title = title;
    document.title = `${title} · Keera Chat`;
  };

  /* ------------------------------------------------------------- setup */

  async function load() {
    const l = app.live.get(view.id);
    if (l) {
      conv = l.conv;
    } else {
      try {
        conv = await app.chats.find(view.id);
        if (!conv)
          failed =
            "This chat is not in this browser. It was deleted, or kept in another browser.";
      } catch (err) {
        failed = err.message;
      }
      // The page was closed while the reply arrived, so it was not kept.
      if (conv && conv.messages.at(-1)?.role === "user") {
        conv.messages.push({
          role: "assistant",
          model: conv.model,
          content: "",
          error:
            "No reply was kept for this message. The page may have closed while it arrived.",
        });
      }
    }
    if (view.gone) return;
    if (failed) {
      replace(
        thread,
        h(
          "div",
          { class: "thread-failed" },
          h("p", {}, failed),
          h(
            "a",
            { class: "btn", href: "/", onClick: (e) => app.go(e, "/") },
            "Start a new chat",
          ),
        ),
      );
      el.classList.remove("is-loading");
      dock.hidden = true;
      return;
    }
    app.choose(conv.model, conv.key_id);
    render();
    pinned = true;
    stick();
  }

  function destroy() {
    view.gone = true;
    // The reply it was showing keeps streaming without it.
  }

  // Settled before the first paint, which shows the composer only when there
  // is a model to send to.
  if (!id) app.choose(null, null);
  render();
  if (id) load();
  return view;
}

/* --------------------------------------------------------------- helpers */

/** turn is a message as the server reads it. A reply that failed is marked,
 *  so the server leaves it out of what goes to the model. */
function turn(m) {
  const t = { role: m.role, content: m.content || "" };
  if (m.images && m.images.length) t.images = m.images;
  if (m.error) t.failed = true;
  return t;
}

/** titleFrom names a new conversation after its first message. Asking the
 *  model for a title would spend the person's budget on every new chat. */
function titleFrom(text, images) {
  const t = oneLine(text, 60);
  if (t) return t;
  if (images === 1) return "Image";
  if (images > 1) return "Images";
  return "New chat";
}

function greeting(user) {
  const hour = new Date().getHours();
  const part =
    hour < 5
      ? "Good evening"
      : hour < 12
        ? "Good morning"
        : hour < 18
          ? "Good afternoon"
          : "Good evening";
  const first = (user.name || "").trim().split(/\s+/)[0];
  return first ? `${part}, ${first}` : part;
}

// A description alone reads as if it were about chat itself, so the line
// names the model it describes.
function heroLine(app) {
  const m = app.choice.model;
  const about =
    m.description ||
    (m.router
      ? "It picks the right model for each message"
      : "How can I help?");
  return [
    "You're chatting with ",
    h("strong", {}, m.name || m.alias),
    ": ",
    sentence(about),
  ];
}

function modelName(app, alias) {
  const m = app.me.models.find((x) => x.alias === alias);
  return (m && m.name) || alias || "";
}

/** accessNotice explains why there is nothing to chat with, and who can
 *  change that. */
function accessNotice(app) {
  const { me } = app;
  const panel = (path, label) =>
    h(
      "a",
      { href: me.gateway_url + path, target: "_blank", rel: "noopener" },
      label,
    );
  const org = me.user.org_name || "your organisation";
  let title, body;
  if (me.keys.length) {
    title = "Your key does not allow a chat model";
    body = [
      "Its rules allow no model that can chat. Ask an administrator of ",
      org,
      " to allow one.",
    ];
  } else if (me.pending_keys) {
    title = "Your key is waiting to be activated";
    body = [
      "An administrator issued you a key. Activate it under ",
      panel("/access", "My access"),
      " in Keera Gateway, then come back here.",
    ];
  } else if (me.user.role === "admin" || me.user.role === "operator") {
    title = "You have no key yet";
    body = [
      "Chat sends with one of your own API keys. As an administrator you can issue one in your name under ",
      panel("/keys", "API keys"),
      " in Keera Gateway.",
    ];
  } else {
    title = me.inactive_keys
      ? "Your key is no longer active"
      : "You have no access yet";
    body = [
      me.inactive_keys ? "It expired or was revoked. " : "",
      "Ask an administrator of ",
      org,
      " for an API key in your name. Once you have one, it works here at once.",
    ];
  }
  return h(
    "div",
    { class: "notice" },
    h("strong", {}, title),
    h("p", {}, body),
  );
}

/** splitThink takes the thinking out of a reply that writes it inline as
 *  <think>…</think>. Some chat templates open the tag in the prompt, so the
 *  reply only closes it. The server does the same for the finished reply. */
function splitThink(text) {
  const open = text.match(/^\s*<think>/);
  const end = text.indexOf("</think>");
  if (!open && end < 0) return { content: text, reasoning: "" };
  const from = open ? open[0].length : 0;
  if (end < 0) return { content: "", reasoning: text.slice(from).trim() };
  return {
    content: text.slice(end + "</think>".length).trimStart(),
    reasoning: text.slice(from, end).trim(),
  };
}

async function readImage(file) {
  const url = await new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });
  // Also checks that the file is an image at all.
  const bitmap = await createImageBitmap(file);
  const scale = maxImageSide / Math.max(bitmap.width, bitmap.height);
  // A GIF is kept as it is, because redrawing it would drop its animation.
  if (scale >= 1 || file.type === "image/gif") {
    bitmap.close();
    return url;
  }
  const canvas = h("canvas", {
    width: Math.round(bitmap.width * scale),
    height: Math.round(bitmap.height * scale),
  });
  canvas.getContext("2d").drawImage(bitmap, 0, 0, canvas.width, canvas.height);
  bitmap.close();
  return canvas.toDataURL(file.type, 0.9);
}

function autosize(el) {
  el.style.height = "auto";
  el.style.height = Math.min(el.scrollHeight, 320) + "px";
}
