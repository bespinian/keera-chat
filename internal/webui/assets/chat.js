// One conversation: the thread and the composer.
//
// A reply keeps streaming when the person leaves. Its state lives in
// app.live, and whichever view shows that conversation paints it.

import { api, ApiError } from "./api.js";
import { render as markdown } from "./markdown.js";
import { exclusive, newID, summary, imageTypes, maxImages, maxFiles, maxFileBytes } from "./store.js";
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
  count,
  setTitle,
  svg,
} from "./ui.js";

// Providers scale larger images down anyway, and each image is resent with
// every later message.
const maxImageSide = 2048;

const cutOff = "The reply was cut off before it finished. Try again.";

export function chatView(app, id) {
  // pending is a reply to a new chat with no id yet.
  const view = {
    id,
    el: null,
    render,
    paint,
    retitle,
    focus: () => input.focus(),
    destroy,
    pending: null,
  };
  // gone means the view was left.
  let gone = false;
  let conv = id ? null : { id: null, title: "", messages: [] };
  let attached = [];
  let editing = -1;
  // An edit the server refused, shown again in its box.
  let draft = null;

  /* --------------------------------------------------------------- DOM */

  const thread = h("div", {
    class: "thread column",
    role: "log",
    "aria-live": "off",
  });
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
      if (sends(e)) {
        e.preventDefault();
        submit();
      }
      if (e.key === "Escape") live()?.abort.abort();
    },
    onPaste: (e) => {
      const files = [...(e.clipboardData?.files || [])];
      if (files.length) {
        e.preventDefault();
        attach(files);
      }
    },
  });

  const picker = h("input", {
    // No accept list: text files can have any name, so attach checks content.
    type: "file",
    multiple: true,
    hidden: true,
    onChange: () => {
      const files = [...picker.files];
      // Clear, so picking the same file again still fires change.
      picker.value = "";
      attach(files);
    },
  });
  const tray = h("div", { class: "composer-tray", hidden: true });
  const attachBtn = iconButton(icons.plus, "Attach files", () => picker.click(), { class: "composer-attach" });
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
      onClick: () => live()?.abort.abort(),
    },
    icon(icons.stop),
  );
  // Context meter. pathLength is 100, so the dash offset is the free share.
  const ring = (cls) =>
    svg("circle", {
      class: cls,
      cx: "10",
      cy: "10",
      r: "7.5",
      pathLength: "100",
    });
  const meterFill = ring("composer-meter-fill");
  const meter = h(
    "span",
    { class: "composer-meter", role: "img", hidden: true },
    svg("svg", { viewBox: "0 0 20 20", "aria-hidden": "true" }, ring("composer-meter-track"), meterFill),
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
        // Clicking anywhere in the box focuses the field.
        if (e.target === composer) input.focus();
      },
    },
    tray,
    input,
    h("div", { class: "composer-row" }, attachBtn, h("div", { class: "spacer" }), meter, sendBtn, stopBtn),
    picker,
  );
  const note = h("div", { class: "composer-note" });
  const hero = h("div", { class: "hero" });
  const dock = h("div", { class: "dock" }, h("div", { class: "column" }, composer, note));

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

  // pinned means the reader is at the bottom. Someone who scrolled up is not
  // pulled back down by new tokens.
  let pinned = true;
  scroll.addEventListener("scroll", () => {
    pinned = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 48;
    toBottom.hidden = pinned;
  });
  function stick() {
    if (pinned) scroll.scrollTop = scroll.scrollHeight;
    toBottom.hidden = pinned || !conv || !conv.messages.length;
  }
  // The thread also resizes without a redraw, such as when a phone keyboard
  // opens or an image loads. This keeps the end in view.
  const resized = new ResizeObserver(stick);
  resized.observe(scroll);
  resized.observe(thread);

  /* -------------------------------------------------------- rendering */

  function live() {
    return conv?.id ? app.live.get(conv.id) : view.pending;
  }

  function render() {
    const empty = !conv || !conv.messages.length;
    el.classList.toggle("is-empty", empty && !!conv);
    el.classList.toggle("no-access", !app.choice);
    // Without a model to send to, the composer could only refuse.
    dock.hidden = !app.choice;
    paintHero();
    paintNote();
    paintSend();
    paintMeter();
    setTitle(conv?.title);
    if (!conv) {
      replace(thread, h("div", { class: "thread-loading" }, h("div", { class: "spinner" })));
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
  function paint() {
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
    });
  }

  function paintHero() {
    if (!conv || conv.messages.length) {
      hero.replaceChildren();
      return;
    }
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
        app.choice ? h("p", { class: "hero-sub" }, heroLine(app)) : accessNotice(app),
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

  /** paintMeter shows how much of the model's context is used. It hides for
   *  a router (no context of its own), for a count by another model (models
   *  count differently), and after a failed reply (maybe refused as too long). */
  function paintMeter() {
    const model = app.choice?.model;
    const max = model?.context;
    const last = conv?.messages.findLast((m) => m.role === "assistant");
    const counted = conv?.messages.findLast((m) => m.tokens);
    meter.hidden = !max || !counted || !!last.error || counted.model !== model.alias;
    if (meter.hidden) return;
    const used = counted.tokens;
    const share = Math.min(used / max, 1);
    const name = model.name;
    let label;
    if (share >= 0.95) {
      label = `${name}'s context is full. Start a new chat, or choose a model that can read more.`;
    } else {
      const pct = Math.round(share * 100);
      label =
        `${pct < 1 ? "Less than 1%" : `About ${pct}%`} of ${name}'s context used ` +
        `(${used.toLocaleString()} of ${max.toLocaleString()} tokens).`;
    }
    meter.title = label;
    meter.setAttribute("aria-label", label);
    meter.classList.toggle("is-high", share >= 0.8);
    meterFill.setAttribute("stroke-dashoffset", String(100 - share * 100));
  }

  function paintTray() {
    tray.hidden = attached.length === 0;
    replace(
      tray,
      attached.map((a) =>
        h(
          "div",
          {
            class: a.text != null ? "thumb thumb-file" : "thumb",
            title: a.pages ? null : a.name,
          },
          a.pages ? pdfThumb(a.name, a.pages) : a.url ? imageButton(a.url, a.name) : fileChip(a.name),
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
        m.images?.length ? bubbleImages(m) : null,
        m.files?.length
          ? h(
              "div",
              { class: "bubble-files" },
              m.files.map((f) => fileChip(f.name)),
            )
          : null,
        m.content ? h("div", { class: "bubble-text" }, m.content) : null,
      ),
      h(
        "div",
        { class: "msg-actions" },
        copyButton(m),
        !busy
          ? iconButton(icons.pencil, "Edit", () => {
              editing = i;
              render();
            })
          : null,
      ),
    );
  }

  /** editEl edits a message. Sending drops every message after it. */
  function editEl(m, i) {
    const box = h("textarea", {
      class: "edit-input",
      "aria-label": "Edit your message",
    });
    box.value = draft ?? m.content;
    draft = null;
    const cancel = () => {
      editing = -1;
      render();
    };
    const save = () => {
      const text = box.value.trim();
      if (!text && !m.images?.length && !m.files?.length) return;
      editing = -1;
      send({ content: text, images: m.images, pdfs: m.pdfs, files: m.files, edit: i });
    };
    box.addEventListener("keydown", (e) => {
      if (sends(e)) {
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
          h("span", { class: "edit-hint" }, "Sending replaces the replies after this message."),
          h("div", { class: "spacer" }),
          h("button", { class: "btn btn-sm", type: "button", onClick: cancel }, "Cancel"),
          h("button", { class: "btn btn-sm btn-primary", type: "button", onClick: save }, "Send"),
        ),
      ),
    );
  }

  function replyEl(m, i) {
    const last = i === conv.messages.length - 1;
    const canRetry = last && !live() && app.choice;
    const body = h("div", { class: "msg-body" });
    if (m.streaming && !m.content && m.reasoning) {
      // Otherwise a model that is only thinking looks stuck.
      body.append(h("div", { class: "thinking shimmer" }, "Thinking…"));
    } else if (m.streaming && !m.content) {
      body.append(
        h("div", { class: "typing", "aria-label": "Waiting for the reply" }, h("span"), h("span"), h("span")),
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
        copyButton(m),
        m.reasoning
          ? iconButton(
              icons.thought,
              m.showReasoning ? "Hide the thought process" : "Show the thought process",
              (e) => {
                m.showReasoning = !m.showReasoning;
                const node = replyEl(m, i);
                e.currentTarget.closest(".msg").replaceWith(node);
                node.querySelector(".reasoning")?.scrollIntoView({ block: "nearest", behavior: "smooth" });
              },
              { "aria-expanded": m.showReasoning ? "true" : "false" },
            )
          : null,
        canRetry && !m.error ? iconButton(icons.retry, "Answer again", () => send({ regenerate: true })) : null,
        h("span", { class: "msg-model" }, modelName(app, m.model)),
      );

    return h(
      "div",
      { class: "msg" },
      body,
      m.error
        ? h(
            "div",
            { class: "reply-error", role: "alert" },
            h(
              "div",
              {},
              sentence(m.error),
              // The server's own words only help admins, so they stay folded.
              m.error_detail
                ? h("details", { class: "error-detail" }, h("summary", {}, "Details"), h("div", {}, m.error_detail))
                : null,
            ),
            canRetry
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
      m.finish === "length" ? h("div", { class: "msg-note" }, "The reply reached its length limit.") : null,
      actions,
      // Below the actions, so it opens where the person clicked.
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

  /* ------------------------------------------------------- attachments */

  // attached holds images as { name, url }, PDFs as { name, pages } and text
  // files as { name, text }. A sent message keeps PDF pages among its images,
  // plus { name, at, pages } in pdfs, where at is the first page's index.
  async function attach(files) {
    if (!app.choice) return;
    for (const file of files) {
      const room = maxImages - attached.flatMap(imagesOf).length;
      if (imageTypes.includes(file.type)) {
        if (!room) {
          toast(`A message can carry at most ${maxImages} images.`, "bad");
          continue;
        }
        try {
          attached.push({ name: file.name, url: await readImage(file) });
        } catch {
          toast(`${file.name} could not be read as an image.`, "bad");
        }
      } else if (file.type === "application/pdf" || /\.pdf$/i.test(file.name)) {
        if (!room) {
          toast(`A message can carry at most ${maxImages} images, and each page of a PDF is one.`, "bad");
          continue;
        }
        try {
          const { pages, total } = await readPDF(file, room);
          attached.push({ name: file.name, pages });
          if (total > pages.length) {
            const first = pages.length === 1 ? "first page" : `first ${pages.length} pages`;
            toast(`Only the ${first} of ${file.name} fit in this message.`, "bad");
          }
        } catch (err) {
          toast(
            err?.name === "PasswordException"
              ? `${file.name} is protected by a password. Remove it and attach the PDF again.`
              : `${file.name} could not be read as a PDF.`,
            "bad",
          );
        }
      } else {
        if (attached.filter((a) => a.text != null).length >= maxFiles) {
          toast(`A message can carry at most ${maxFiles} files.`, "bad");
          continue;
        }
        if (file.size > maxFileBytes) {
          toast(`${file.name} is larger than ${maxFileBytes >> 10} KB.`, "bad");
          continue;
        }
        try {
          attached.push({ name: file.name, text: await readText(file) });
        } catch {
          toast(`${file.name} is not an image, a PDF or a text file.`, "bad");
        }
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
    const images = [];
    const pdfs = [];
    for (const a of attached) {
      if (a.pages) pdfs.push({ name: a.name, at: images.length, pages: a.pages.length });
      images.push(...imagesOf(a));
    }
    const files = attached.filter((a) => a.text != null).map(({ name, text }) => ({ name, text }));
    // A chat started with only a document is named after it.
    const about = attached.find((a) => !a.url)?.name;
    input.value = "";
    autosize(input);
    attached = [];
    paintTray();
    send({ content: text, images, pdfs, files, about });
  }

  async function send({
    content = "",
    images = [],
    pdfs = [],
    files = [],
    about = "",
    edit = null,
    regenerate = false,
  }) {
    const choice = app.choice;
    if (!choice || live()) return;
    const target = conv;
    const before = target.messages.slice();
    const lastUser = target.messages.findLastIndex((m) => m.role === "user");
    if (regenerate && lastUser < 0) return;

    // Shown now, saved once the server accepts it.
    let ask = null;
    if (regenerate) {
      target.messages = target.messages.slice(0, lastUser + 1);
    } else {
      ask = {
        role: "user",
        content,
        images,
        pdfs,
        files,
        created_at: new Date().toISOString(),
      };
      if (edit !== null) target.messages = target.messages.slice(0, edit);
      target.messages.push(ask);
    }
    const turns = target.messages.map(turn);
    const reply = {
      role: "assistant",
      model: choice.model.alias,
      content: "",
      reasoning: "",
      streaming: true,
      showReasoning: false,
    };
    target.messages.push(reply);
    const state = {
      conv: target,
      reply,
      abort: new AbortController(),
    };
    if (target.id) app.live.set(target.id, state);
    else view.pending = state;
    pinned = true;
    render();

    const paintOwner = () => {
      const v = app.view;
      if (v && (v.id === target.id || v.pending === state)) v.paint();
    };

    // Until the server accepts the message, nothing is saved and the
    // conversation can be rolled back.
    let started = false;
    let finished = false;
    let changed = false;
    try {
      await exclusive(
        target.id,
        async () => {
          // Another tab may have changed this chat. Sending from a stale copy
          // would lose that.
          if (target.id) {
            const stored = await app.chats.find(target.id);
            if (!stored || stored.updated_at !== target.updated_at) {
              changed = true;
              throw new ApiError(409, "changed", "This chat changed in another tab, so it was reloaded. Send again.");
            }
          }
          const stream = await api.chat(
            {
              model: choice.model.alias,
              model_name: choice.model.name,
              key_id: choice.key.id,
              instructions: app.settings.instructions,
              messages: turns,
            },
            state.abort.signal,
          );
          started = true;
          await begin(target, state, { content, images, about, choice });

          let raw = "";
          let thought = "";
          for await (const { event, data } of stream) {
            if (event === "delta") {
              raw += data.content || "";
              thought += data.reasoning || "";
              const split = splitThink(raw);
              reply.content = split.content;
              reply.reasoning = thought + split.reasoning;
              paintOwner();
            } else if (event === "done") {
              Object.assign(reply, data, { streaming: false });
              finished = true;
            }
          }
          // The server always ends with "done", even on refusal. Without it,
          // the stream was cut, such as by a proxy.
          if (!finished) reply.error = cutOff;
        },
        () => {
          throw new ApiError(409, "busy", "A reply is still being written in this chat, perhaps in another tab.");
        },
      );
    } catch (err) {
      const aborted = err?.name === "AbortError";
      if (started && aborted) {
        reply.stopped = true;
      } else if (err instanceof ApiError || aborted) {
        // Refused or stopped before the server accepted it: roll back, and put
        // the message back in the composer or edit box. A chat changed in
        // another tab is reloaded, so its edit is dropped.
        target.messages = before;
        if (edit !== null && app.view === view && !changed) {
          editing = edit;
          draft = content;
        }
        if (ask && edit === null && app.view === view && !input.value) {
          input.value = content;
          attached = [...attachments(images, pdfs), ...files];
          paintTray();
          queueMicrotask(() => autosize(input));
        }
        if (err instanceof ApiError && err.code !== "signed_out") toast(err.message, "bad");
      } else if (started) {
        // Browser errors like "network error" only help admins.
        reply.error = cutOff;
        reply.error_detail = err?.message;
      } else {
        reply.error = err?.message || "The reply ended unexpectedly.";
      }
    } finally {
      reply.streaming = false;
      // Do not bring back a chat deleted while its reply arrived.
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
      // Only after saving, so the next message cannot start from before it.
      if (target.id) app.live.delete(target.id);
      if (view.pending === state) view.pending = null;
      if (keep) app.saw(summary(target));
      const v = app.view;
      if (changed && v === view) load();
      else if (v && (v === view || v.id === target.id)) v.render();
      if (v === view) input.focus();
    }
  }

  /** begin saves the conversation once the server accepts the message, so
   *  it is not lost if the reply fails. A new chat gets its id here. */
  async function begin(target, state, { content, images, about, choice }) {
    const now = new Date().toISOString();
    if (!target.id) {
      target.id = newID();
      target.title = titleFrom(content, images.length, about);
      target.created_at = now;
      app.live.set(target.id, state);
      if (view.pending === state) view.pending = null;
      // Update the URL, unless the person already left.
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
      // The final save retries, and the reply shows any error.
    }
    if (!state.deleted) app.saw(summary(target));
  }

  /** retitle applies a sidebar rename, so the next save keeps it. */
  function retitle(title) {
    if (!conv) return;
    conv.title = title;
    setTitle(title);
  }

  /* ------------------------------------------------------------- setup */

  async function load() {
    let failed = "";
    const l = app.live.get(view.id);
    if (l) {
      conv = l.conv;
    } else {
      try {
        conv = await app.chats.find(view.id);
        if (!conv) failed = "This chat is not in this browser. It was deleted, or kept in another browser.";
      } catch (err) {
        failed = err.message;
      }
      // The page closed while the reply arrived, so it was not saved.
      if (conv && conv.messages.at(-1)?.role === "user") {
        conv.messages.push({
          role: "assistant",
          model: conv.model,
          content: "",
          error: "No reply was kept for this message. The page may have closed while it arrived.",
        });
      }
    }
    if (gone) return;
    if (failed) {
      replace(
        thread,
        h(
          "div",
          { class: "thread-failed" },
          h("p", {}, failed),
          h("a", { class: "btn", href: "/", onClick: (e) => app.go(e, "/") }, "Start a new chat"),
        ),
      );
      dock.hidden = true;
      return;
    }
    app.choose(conv.model, conv.key_id);
    pinned = true;
    render();
  }

  function destroy() {
    gone = true;
    resized.disconnect();
    // A live reply keeps streaming.
  }

  // Before the first paint, which shows the composer only with a model.
  if (!id) app.choose(null, null);
  render();
  if (id) load();
  return view;
}

/* --------------------------------------------------------------- helpers */

/** sends: Enter sends, Shift+Enter adds a line. */
function sends(e) {
  return e.key === "Enter" && !e.shiftKey && !e.isComposing;
}

function copyButton(m) {
  return m.content ? iconButton(icons.copy, "Copy", () => copyText(m.content)) : null;
}

/** turn is a message for the server. Failed replies are marked so the
 *  server leaves them out. */
function turn(m) {
  const t = { role: m.role, content: m.content || "" };
  if (m.images?.length) t.images = m.images;
  if (m.files?.length) t.files = m.files;
  if (m.error) t.failed = true;
  return t;
}

/** titleFrom names a chat after its first message. Asking the model would
 *  spend the person's budget on every new chat. */
function titleFrom(text, images, about) {
  const t = oneLine(text, 60) || oneLine(about || "", 60);
  if (t) return t;
  if (images === 1) return "Image";
  if (images > 1) return "Images";
  return "New chat";
}

function greeting(user) {
  const hour = new Date().getHours();
  const part = hour < 5 || hour >= 18 ? "Good evening" : hour < 12 ? "Good morning" : "Good afternoon";
  const first = (user.name || "").trim().split(/\s+/)[0];
  return first ? `${part}, ${first}` : part;
}

/** heroLine names the model first. A description alone reads as if it were
 *  about chat itself. */
function heroLine(app) {
  const m = app.choice.model;
  const about = m.description || (m.router ? "It picks the right model for each message" : "How can I help?");
  return ["You're chatting with ", h("strong", {}, m.name), ": ", sentence(about)];
}

function modelName(app, alias) {
  const m = app.me.models.find((x) => x.alias === alias);
  return m?.name || alias || "";
}

/** accessNotice explains why no model is available, and who can fix it. */
function accessNotice(app) {
  const { me } = app;
  const panel = (path, label) => h("a", { href: me.gateway_url + path, target: "_blank", rel: "noopener" }, label);
  const org = me.user.org_name || "your organisation";
  let title, body;
  if (me.keys.length) {
    title = "Your key does not allow a chat model";
    body = ["Its rules allow no model that can chat. Ask an administrator of ", org, " to allow one."];
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
    title = me.inactive_keys ? "Your key is no longer active" : "You have no access yet";
    body = [
      me.inactive_keys ? "It expired or was revoked. " : "",
      "Ask an administrator of ",
      org,
      " for an API key in your name. Once you have one, it works here at once.",
    ];
  }
  return h("div", { class: "notice" }, h("strong", {}, title), h("p", {}, body));
}

/** splitThink pulls inline <think>…</think> out of a reply. Some chat
 *  templates open the tag in the prompt, so the reply only closes it. The
 *  server does the same for the finished reply. */
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
  // Also checks that the file is an image.
  const bitmap = await createImageBitmap(file);
  const scale = maxImageSide / Math.max(bitmap.width, bitmap.height);
  // Keep GIFs as they are. Redrawing drops the animation.
  if (scale >= 1 || file.type === "image/gif") {
    bitmap.close();
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(reader.result);
      reader.onerror = () => reject(reader.error);
      reader.readAsDataURL(file);
    });
  }
  const canvas = h("canvas", {
    width: Math.round(bitmap.width * scale),
    height: Math.round(bitmap.height * scale),
  });
  canvas.getContext("2d").drawImage(bitmap, 0, 0, canvas.width, canvas.height);
  bitmap.close();
  return canvas.toDataURL(file.type, 0.9);
}

/** readPDF renders a PDF's first pages as images, as many as fit. Models
 *  here read images but not PDFs, and images keep tables, figures and scans.
 *  pdf.js is large, so it loads only when needed. */
async function readPDF(file, room) {
  const pdfjs = await import("./vendor/pdfjs/pdf.min.mjs");
  pdfjs.GlobalWorkerOptions.workerSrc = "/assets/vendor/pdfjs/pdf.worker.min.mjs";
  const task = pdfjs.getDocument({ data: await file.arrayBuffer() });
  try {
    const doc = await task.promise;
    const pages = [];
    for (let n = 1; n <= Math.min(doc.numPages, room); n++) {
      const page = await doc.getPage(n);
      const size = page.getViewport({ scale: 1 });
      const viewport = page.getViewport({ scale: maxImageSide / Math.max(size.width, size.height) });
      const canvas = h("canvas", { width: Math.floor(viewport.width), height: Math.floor(viewport.height) });
      await page.render({ canvas, viewport }).promise;
      // JPEG, since PNG is several times larger and each image is resent with
      // every later message.
      pages.push(canvas.toDataURL("image/jpeg", 0.85));
      page.cleanup();
    }
    return { pages, total: doc.numPages };
  } finally {
    task.destroy();
  }
}

/** attachments regroups a message's images: PDFs as { name, pages }, other
 *  images as { name, url }. */
function attachments(images = [], pdfs = []) {
  const out = [];
  for (let n = 0; n < images.length;) {
    const pdf = pdfs.find((p) => p.at === n && p.pages > 0);
    const pages = pdf ? images.slice(n, n + pdf.pages) : null;
    out.push(pdf ? { name: pdf.name, pages } : { name: `image ${n + 1}`, url: images[n] });
    n += pages ? pages.length : 1;
  }
  return out;
}

/** bubbleImages shows each PDF as a stack. Other images share one viewer. */
function bubbleImages(m) {
  const parts = attachments(m.images, m.pdfs);
  const plain = parts.filter((a) => !a.pages).map((a) => a.url);
  let n = 0;
  return h(
    "div",
    { class: "bubble-images" },
    parts.map((a) => (a.pages ? pdfThumb(a.name, a.pages) : imageButton(a.url, "", { gallery: plain, at: n++ }))),
  );
}

/** pdfThumb shows a PDF's first page, stacked with a page count. */
function pdfThumb(name, pages) {
  const many = pages.length > 1;
  return h(
    "span",
    { class: "pdf-stack" + (many ? " is-stack" : ""), title: `${name}, ${count(pages.length, "page")}` },
    imageButton(pages[0], name, { gallery: pages }),
    many ? h("span", { class: "pdf-count", "aria-hidden": "true" }, `${pages.length} pages`) : null,
  );
}

/** imagesOf returns an attachment's images. A PDF sends its pages. */
function imagesOf(a) {
  return a.pages || (a.url ? [a.url] : []);
}

/** readText reads a UTF-8 text file and rejects anything else. */
async function readText(file) {
  const text = new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer());
  if (text.includes("\0")) throw new Error("binary");
  return text;
}

function fileChip(name) {
  return h("span", { class: "file-chip" }, icon(icons.file), h("span", { class: "file-chip-name" }, name));
}

function autosize(el) {
  el.style.height = "auto";
  el.style.height = Math.min(el.scrollHeight, 320) + "px";
}
