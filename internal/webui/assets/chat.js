// One conversation: the thread and the composer.
//
// A reply keeps streaming when the person leaves. Its state lives in
// app.live, and whichever view shows that conversation paints it.
//
// A chat with an agent sends only each new message: the agent keeps the
// chat's history itself. Its reply is a task in Keera Agents, which the page
// reads until it is done. The task's id is saved with the reply, so a reply
// still being worked on is picked up again when the chat is opened.

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
  agentMark,
} from "./ui.js";

// Providers scale larger images down anyway, and each image is resent with
// every later message.
const maxImageSide = 2048;

const cutOff = "The reply was cut off before it finished. Try again.";

// How often an agent's task is read while it works.
const followEvery = 1500;
// The longest wait between reads while they fail.
const followMax = 60_000;

/** chatView shows conversation id, or a new chat with agent, if given. */
export function chatView(app, id, agent = null) {
  const view = {
    id,
    el: null,
    render,
    paint,
    retitle,
    focus: () => input.focus(),
    // started is true once the chat has messages.
    started: () => !!conv?.messages.length,
    destroy,
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
        atEnd = true;
        scroll.scrollTo({ top: scroll.scrollHeight, behavior: "smooth" });
      },
    },
    icon(icons.down),
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
  const stuckNote = h("div", { class: "dock-stuck", hidden: true });
  const dock = h("div", { class: "dock" }, h("div", { class: "column" }, stuckNote, composer, note));

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

  // atEnd means the reader is at the bottom. Someone who scrolled up is not
  // pulled back down by new tokens.
  let atEnd = true;
  scroll.addEventListener("scroll", () => {
    atEnd = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 48;
    toBottom.hidden = atEnd;
  });
  function stick() {
    if (atEnd) scroll.scrollTop = scroll.scrollHeight;
    toBottom.hidden = atEnd || !conv || !conv.messages.length;
  }
  // The thread also resizes without a redraw, such as when a phone keyboard
  // opens or an image loads. This keeps the end in view. The dock floats
  // over the thread, so the thread leaves room for its height.
  const resized = new ResizeObserver(() => {
    el.style.setProperty("--dock-height", `${dock.offsetHeight}px`);
    stick();
  });
  resized.observe(scroll);
  resized.observe(thread);
  resized.observe(dock);

  /* -------------------------------------------------------- rendering */

  function live() {
    return conv?.id && app.live.get(conv.id);
  }

  function render() {
    const empty = !conv || !conv.messages.length;
    el.classList.toggle("is-empty", empty && !!conv);
    el.classList.toggle("no-access", !app.choice);
    // Without a model to send to, the composer could only refuse. A started
    // chat says why in its place.
    const stuck = !app.choice && !empty;
    dock.hidden = !app.choice && !stuck;
    composer.hidden = !app.choice;
    note.hidden = !app.choice;
    stuckNote.hidden = !stuck;
    if (stuck) replace(stuckNote, stuckNotice(app, conv));
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
        app.choice?.agent
          ? agentMark(app.choice.agent.icon, "hero-mark hero-agent")
          : h("img", {
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
    const org = app.me.user.org_name;
    const gateway = `${org ? `${org}'s` : "your"} Keera Gateway`;
    const agent = app.choice.agent;
    replace(
      note,
      agent
        ? `Agents can be wrong, so check what matters. ${agent.name} works in Keera Agents with its own tools, under the rules of ${gateway}.`
        : `Replies can be wrong, so check what matters. Messages go through ${gateway}, and its rules apply.`,
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
            class: a.url || a.pages ? "thumb" : "thumb thumb-file",
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
        // An agent keeps its own history, which an edit cannot change.
        !busy && !conv.agent
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
      { class: "msg" },
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
    } else if ((m.streaming || waiting(m)) && !m.content) {
      body.append(
        h("div", { class: "typing", "aria-label": "Waiting for the reply" }, h("span"), h("span"), h("span")),
      );
    } else if (m.content) {
      const md = markdown(m.content);
      if (m.streaming) md.classList.add("is-streaming");
      body.append(md);
    }

    return h(
      "div",
      { class: "msg" },
      body,
      m.error ? errorEl(m, canRetry) : null,
      m.trouble ? h("div", { class: "msg-note" }, `Still waiting. ${m.trouble}`) : null,
      m.stopped ? h("div", { class: "msg-note" }, "Stopped.") : null,
      m.finish === "length" ? h("div", { class: "msg-note" }, "The reply reached its length limit.") : null,
      m.agent ? agentTail(m, i, last) : modelTail(m, i, last, canRetry),
    );
  }

  /** modelTail is a model reply's actions and its thought process. */
  function modelTail(m, i, last, canRetry) {
    if (m.streaming) return null;
    return [
      actionsEl(
        last,
        copyButton(m),
        m.reasoning ? foldButton(m, i, "showReasoning", icons.thought, "the thought process") : null,
        canRetry && !m.error ? iconButton(icons.retry, "Answer again", () => send({ regenerate: true })) : null,
        h("span", { class: "msg-model" }, modelName(app, m.model)),
      ),
      m.reasoning && m.showReasoning
        ? fold("Thought process", h("div", { class: "reasoning-body" }, m.reasoning))
        : null,
    ];
  }

  /** agentTail is an agent reply's actions and its steps. The task opens in
   *  Keera Agents with everything it did. */
  function agentTail(m, i, last) {
    if (m.streaming || waiting(m)) return null;
    const steps = m.steps?.length ? m.steps : null;
    return [
      actionsEl(
        last,
        copyButton(m),
        steps ? foldButton(m, i, "showSteps", icons.steps, "the steps") : null,
        app.me.agents_url && m.task
          ? h(
              "a",
              {
                class: "icon-btn",
                href: `${app.me.agents_url}/tasks/${encodeURIComponent(m.task)}`,
                target: "_blank",
                rel: "noopener",
                title: "Open in Keera Agents",
                "aria-label": "Open in Keera Agents",
              },
              icon(icons.external),
            )
          : null,
        h(
          "span",
          { class: "msg-model msg-agent" },
          agentMark(agentOf(app, m.agent)?.icon, "avatar-sm"),
          agentName(app, m.agent),
        ),
      ),
      steps && m.showSteps
        ? fold(
            "Steps",
            h(
              "ol",
              { class: "reasoning-body agent-steps" },
              steps.map((step) => h("li", {}, step)),
            ),
          )
        : null,
    ];
  }

  function actionsEl(last, ...buttons) {
    return h("div", { class: "msg-actions" + (last ? " is-last" : "") }, ...buttons);
  }

  /** foldButton shows or hides one of a reply's folds, such as m.showSteps. */
  function foldButton(m, i, key, glyph, what) {
    return iconButton(
      glyph,
      `${m[key] ? "Hide" : "Show"} ${what}`,
      (e) => {
        m[key] = !m[key];
        const node = replyEl(m, i);
        e.currentTarget.closest(".msg").replaceWith(node);
        node.querySelector(".reasoning")?.scrollIntoView({ block: "nearest", behavior: "smooth" });
      },
      { "aria-expanded": m[key] ? "true" : "false" },
    );
  }

  /** fold goes below the actions, so it opens where the person clicked. */
  function fold(head, body) {
    return h("div", { class: "reasoning" }, h("div", { class: "reasoning-head" }, head), body);
  }

  function errorEl(m, canRetry) {
    return h(
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
    );
  }

  /* ------------------------------------------------------- attachments */

  // attached holds images as { name, url }, PDFs as { name, pdf, text,
  // pages } and text files as { name, text }. A PDF has pages only if the
  // chosen model or agent read images when it was attached. A sent message
  // keeps PDF pages among its images, plus { name, at, pages } in pdfs,
  // where at is the first page's index. A PDF sent as text is one of its
  // files.
  async function attach(files) {
    if (!app.choice) return;
    const model = reader(app.choice);
    for (const file of files) {
      const room = maxImages - attached.flatMap(imagesOf).length;
      const image = imageTypes.includes(file.type);
      // PDFs count as files too, since each keeps its text.
      if (!image && attached.filter((a) => a.text != null).length >= maxFiles) {
        toast(`A message can carry at most ${maxFiles} files.`, "bad");
        continue;
      }
      if (image) {
        if (!model.vision) {
          toast(`${model.name} reads text only, so it cannot see ${file.name}.`, "bad");
          continue;
        }
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
        // A model that reads images gets the pages, which keep tables,
        // figures and scans. Any other gets the text.
        if (model.vision && !room) {
          toast(`A message can carry at most ${maxImages} images, and each page of a PDF is one.`, "bad");
          continue;
        }
        try {
          const { text, pages, read, total } = await readPDF(file, model.vision ? room : 0);
          if (!model.vision && !text.trim()) {
            toast(
              `${file.name} has no text, perhaps because it is scanned. It needs a model that reads images.`,
              "bad",
            );
            continue;
          }
          attached.push({ name: file.name, pdf: true, text, pages: model.vision ? pages : null });
          const fit = model.vision ? pages.length : read;
          if (total > fit) {
            const first = fit === 1 ? "first page" : `first ${fit} pages`;
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
    // The model or agent may have changed since the files were attached.
    const model = reader(app.choice);
    if (!model.vision && attached.some((a) => a.url)) {
      toast(`${model.name} reads text only. Remove the images to send.`, "bad");
      return;
    }
    const blank = attached.find((a) => a.pdf && !(model.vision && a.pages) && !a.text.trim());
    if (blank) {
      toast(`${blank.name} has no text, perhaps because it is scanned. Attach it again to send its pages.`, "bad");
      return;
    }
    const images = [];
    const pdfs = [];
    const files = [];
    for (const a of attached) {
      if (a.pdf && model.vision && a.pages) {
        pdfs.push({ name: a.name, at: images.length, pages: a.pages.length });
        images.push(...a.pages);
      } else if (a.url) {
        images.push(a.url);
      } else {
        files.push({ name: a.name, text: a.text });
      }
    }
    // A chat started with only a document is named after it.
    const about = attached.find((a) => !a.url)?.name;
    const sent = attached;
    input.value = "";
    autosize(input);
    attached = [];
    paintTray();
    send({ content: text, images, pdfs, files, about, sent });
  }

  async function send({
    content = "",
    images = [],
    pdfs = [],
    files = [],
    about = "",
    // sent is what was in the tray, put back if the message is refused.
    sent = [],
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
    const vision = !!reader(choice).vision;
    const turns = target.messages.map((m) => turn(m, vision));
    const reply = choice.agent
      ? { role: "assistant", agent: choice.agent.id, content: "", steps: [], streaming: true }
      : {
          role: "assistant",
          model: choice.model.alias,
          content: "",
          reasoning: "",
          streaming: true,
          showReasoning: false,
        };
    target.messages.push(reply);
    // A new chat gets its id now, so its reply lives in app.live like any
    // other. It is saved, and its URL set, only once the server accepts it.
    const fresh = !target.id;
    if (fresh) target.id = newID();
    const state = liveState(target, reply);
    app.live.set(target.id, state);
    atEnd = true;
    render();
    const paintOwner = painter(target);

    // Until the server accepts the message, nothing is saved and the
    // conversation can be rolled back.
    let started = false;
    let changed = false;
    try {
      await exclusive(
        target.id,
        async () => {
          // Another tab may have changed this chat. Sending from a stale copy
          // would lose that.
          if (!fresh) {
            const stored = await app.chats.find(target.id);
            if (!stored || stored.updated_at !== target.updated_at) {
              changed = true;
              throw new ApiError(409, "changed", "This chat changed in another tab, so it was reloaded. Send again.");
            }
          }
          if (choice.agent) {
            // The agent keeps the history, so it gets only the new message,
            // the last turn, in a chat whose id it knows before the chat is
            // saved.
            const task = await api.tellAgent(choice.agent.id, target.id, turns.at(-1));
            started = true;
            reply.task = task.id;
            await begin(target, state, { fresh, content, images, about, choice });
            await follow(state, paintOwner, choice.agent.name);
            return;
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
          await begin(target, state, { fresh, content, images, about, choice });
          await readReply(stream, reply, paintOwner);
        },
        () => {
          throw new ApiError(409, "busy", "A reply is still being written in this chat, perhaps in another tab.");
        },
      );
    } catch (err) {
      if (started) {
        endedBy(reply, err);
      } else if (err instanceof ApiError || err?.name === "AbortError") {
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
          attached = sent;
          paintTray();
          queueMicrotask(() => autosize(input));
        }
        if (err instanceof ApiError && err.code !== "signed_out") toast(err.message, "bad");
      } else {
        reply.error = err?.message || "The reply ended unexpectedly.";
      }
    } finally {
      await settle(target, state, started);
      // A new chat that was never accepted was never saved either.
      if (fresh && !started) target.id = null;
      const v = app.view;
      if (changed && v === view) load();
      else if (v && (v === view || v.id === target.id)) v.render();
      if (v === view) input.focus();
    }
  }

  /** begin saves the conversation once the server accepts the message, so
   *  it is not lost if the reply fails. A new chat gets its title and URL
   *  here. */
  async function begin(target, state, { fresh, content, images, about, choice }) {
    // Deleted while the server took the message. An agent's message cannot
    // be stopped on its way, so its task is cancelled once following stops.
    if (state.deleted) return;
    const now = new Date().toISOString();
    if (fresh) {
      target.title = titleFrom(content, images.length, about);
      target.created_at = now;
      if (app.view === view) {
        view.id = target.id;
        history.replaceState(null, "", "/c/" + target.id);
      }
    }
    target.agent = choice.agent?.id;
    if (!choice.agent) {
      target.model = choice.model.alias;
      target.key_id = choice.key.id;
    }
    target.updated_at = now;
    try {
      await app.chats.save({
        ...target,
        // An agent's reply is kept with its task at once, so it can be
        // picked up again after a reload.
        messages: choice.agent ? target.messages : target.messages.filter((m) => m !== state.reply),
      });
    } catch {
      // The final save retries, and the reply shows any error.
    }
    if (!state.deleted) app.saw(summary(target));
  }

  /** resume follows an agent's reply that was still being worked on when
   *  the page closed. Another tab may follow it already. */
  function resume(target, reply) {
    const state = liveState(target, reply);
    const paintOwner = painter(target);
    exclusive(
      target.id,
      async () => {
        if (app.live.has(target.id)) return;
        // Steps are not saved while the agent works, so read them all again.
        Object.assign(reply, { streaming: true, steps: [] });
        app.live.set(target.id, state);
        paintOwner();
        try {
          await follow(state, paintOwner, agentName(app, reply.agent));
        } catch (err) {
          // Nothing awaits this, so a rethrown error would be lost.
          endedBy(reply, err);
        } finally {
          await settle(target, state);
          const v = app.view;
          if (v && v.id === target.id) v.render();
        }
      },
      () => {},
    );
  }

  /** liveState is a reply being written, as app.live holds it. */
  function liveState(target, reply) {
    return { conv: target, reply, abort: new AbortController() };
  }

  /** painter repaints whichever view shows the reply, if any. */
  function painter(target) {
    return () => {
      const v = app.view;
      if (v && v.id === target.id) v.paint();
    };
  }

  /** settle ends a reply: it saves it, unless save is false, and frees the
   *  chat for the next message. */
  async function settle(target, state, save = true) {
    state.reply.streaming = false;
    // Only for while the reply is followed.
    delete state.reply.trouble;
    // Do not bring back a chat deleted while its reply arrived.
    const keep = save && !state.deleted;
    if (keep) await saveReply(target, state.reply);
    // Only after saving, so the next message cannot start from before it.
    app.live.delete(target.id);
    if (keep) app.saw(summary(target));
  }

  /** saveReply keeps a finished reply. If storage fails, the reply says so. */
  async function saveReply(target, reply) {
    target.updated_at = new Date().toISOString();
    reply.created_at ||= target.updated_at;
    try {
      await app.chats.save(target);
    } catch (err) {
      reply.error = `The reply could not be kept. Copy anything you need from it now. ${err.message}`;
      reply.error_detail = undefined;
    }
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
    app.chooseFor(conv);
    atEnd = true;
    render();
    const last = conv.messages.at(-1);
    if (!l && last && waiting(last)) resume(conv, last);
  }

  function destroy() {
    gone = true;
    resized.disconnect();
    // A live reply keeps streaming.
  }

  // Before the first paint, which shows the composer only with a model.
  if (!id) agent ? app.chooseAgent(agent) : app.choose(null, null);
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

/** waiting reports whether an agent's reply is still being worked on: it
 *  has a task but no outcome yet. */
function waiting(m) {
  return !!m.task && !m.content && !m.error && !m.stopped;
}

/** follow reads an agent's task until it is done, and fills in the reply.
 *  It gives up only when Keera Agents no longer has the task. Any other
 *  failure may pass, such as Keera Agents out of reach or refusing the
 *  sign-in for a while, and the agent works on meanwhile. So it keeps trying
 *  and shows why it is still waiting. Only stopping throws. */
async function follow(state, paint, name) {
  const { reply } = state;
  const signal = state.abort.signal;
  let after = 0;
  let failures = 0;
  for (;;) {
    let t;
    try {
      t = await api.agentTask(reply.task, after, signal);
    } catch (err) {
      if (err?.name === "AbortError") throw err;
      // The page shows sign-in. The reply stays waiting, so it is picked up
      // again after signing in, and the agent's work is not lost.
      if (err instanceof ApiError && err.code === "signed_out") return;
      // Keera Agents' own answer: the task is gone, or not this person's.
      if (err instanceof ApiError && err.code === "not_found") {
        reply.error = "This reply is no longer in Keera Agents.";
        reply.error_detail = err.message;
        return;
      }
      reply.trouble = err?.message || "The reply could not be read.";
      paint();
      failures++;
      await pause(Math.min(followEvery * 2 ** (failures + 1), followMax), signal);
      continue;
    }
    if (reply.trouble) {
      delete reply.trouble;
      paint();
    }
    failures = 0;
    after = t.after;
    if (t.steps.length) {
      reply.steps = [...(reply.steps || []), ...t.steps];
      paint();
    }
    if (t.done) {
      reply.content = t.result || "";
      if (t.state === "succeeded") reply.content ||= "Done.";
      else if (t.state === "cancelled") reply.stopped = true;
      else {
        reply.error = `${name} could not finish this.`;
        reply.error_detail = t.error;
      }
      return;
    }
    await pause(followEvery, signal);
  }
}

/** readReply fills in a model's reply as it streams. */
async function readReply(stream, reply, paint) {
  let raw = "";
  let thought = "";
  let done = false;
  for await (const { event, data } of stream) {
    if (event === "delta") {
      raw += data.content || "";
      thought += data.reasoning || "";
      const split = splitThink(raw);
      reply.content = split.content;
      reply.reasoning = thought + split.reasoning;
      paint();
    } else if (event === "done") {
      Object.assign(reply, data, { streaming: false });
      done = true;
    }
  }
  // The server always ends with "done", even on refusal. Without it, the
  // stream was cut, such as by a proxy.
  if (!done) reply.error = cutOff;
}

/** endedBy marks a reply the server accepted but that did not finish:
 *  stopped by the person, or cut off. */
function endedBy(reply, err) {
  if (err?.name === "AbortError") {
    reply.stopped = true;
    if (reply.task) stopTask(reply);
  } else {
    // Browser errors like "network error" only help admins.
    reply.error = cutOff;
    reply.error_detail = err?.message;
  }
}

/** stopTask asks Keera Agents to stop an agent's task. The agent may have
 *  finished meanwhile, so a failure is ignored. */
function stopTask(reply) {
  api.cancelAgentTask(reply.task).catch(() => {});
}

/** pause waits ms, or rejects as soon as signal aborts. */
function pause(ms, signal) {
  return new Promise((resolve, reject) => {
    const stop = () => {
      clearTimeout(timer);
      reject(new DOMException("Stopped", "AbortError"));
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", stop);
      resolve();
    }, ms);
    if (signal.aborted) stop();
    else signal.addEventListener("abort", stop, { once: true });
  });
}

/** reader is what reads a message: the chosen model, or the agent's model,
 *  which Keera Agents says reads images or not. */
function reader(choice) {
  return choice.agent ? { name: choice.agent.name, vision: !!choice.agent.vision } : choice.model;
}

/** turn is a message for the server. Failed replies are marked so the
 *  server leaves them out. A model that does not read images refuses them,
 *  so it is told they were left out instead. */
function turn(m, vision) {
  const t = { role: m.role, content: m.content || "" };
  if (m.images?.length && vision) t.images = m.images;
  else if (m.images?.length) {
    const note = `[${count(m.images.length, "image")} left out here: this model reads text only]`;
    t.content = t.content ? `${note}\n\n${t.content}` : note;
  }
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

/** heroLine names the model or agent first. A description alone reads as if
 *  it were about chat itself. */
function heroLine(app) {
  const a = app.choice.agent;
  if (a) {
    return [
      "You're chatting with ",
      h("strong", {}, a.name),
      ": ",
      sentence(a.description || "An agent with its own tools and workspace"),
    ];
  }
  const m = app.choice.model;
  const about = m.description || (m.router ? "It picks the right model for each message" : "How can I help?");
  return ["You're chatting with ", h("strong", {}, m.name), ": ", sentence(about)];
}

function modelName(app, alias) {
  const m = app.me.models.find((x) => x.alias === alias);
  return m?.name || alias || "";
}

function agentOf(app, id) {
  return app.me.agents.find((a) => a.id === id);
}

/** agentName names an agent, also one no longer offered. */
function agentName(app, id) {
  return agentOf(app, id)?.name || "Agent";
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

/** stuckNotice explains why a started chat cannot go on. */
function stuckNotice(app, conv) {
  if (conv.agent) {
    return h(
      "div",
      { class: "notice" },
      h("strong", {}, "This chat's agent is not available"),
      h(
        "p",
        {},
        "Keera Agents may be out of reach, or the agent is no longer shared with you. Reload the page to try again.",
      ),
    );
  }
  if (app.me.agents.length) {
    return h(
      "div",
      { class: "notice" },
      h("strong", {}, "No chat model is available"),
      h("p", {}, "This chat is with a model, and none is open to you now. Start a new chat to work with an agent."),
    );
  }
  return accessNotice(app);
}

/** splitThink is splitThink in turns.go, for a reply still streaming. */
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

/** readPDF reads a PDF's text, and renders its first pages as images, as
 *  many as room. Models here read images or text, not PDFs. read counts the
 *  pages whose text fits in maxFileBytes. pdf.js is large, so it loads only
 *  when needed. */
async function readPDF(file, room) {
  const pdfjs = await import("./vendor/pdfjs/pdf.min.mjs");
  pdfjs.GlobalWorkerOptions.workerSrc = "/assets/vendor/pdfjs/pdf.worker.min.mjs";
  const task = pdfjs.getDocument({ data: await file.arrayBuffer() });
  try {
    const doc = await task.promise;
    const { text, read } = await pdfText(doc);
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
    return { text, pages, read, total: doc.numPages };
  } finally {
    task.destroy();
  }
}

/** pdfText joins a PDF's pages' text, each under its page number, while it
 *  fits in maxFileBytes. A scan has none. */
async function pdfText(doc) {
  const encoder = new TextEncoder();
  let text = "";
  let any = false;
  let read = 0;
  for (let n = 1; n <= doc.numPages; n++) {
    const page = await doc.getPage(n);
    const content = await page.getTextContent();
    page.cleanup();
    const body = content.items
      .map((item) => (item.str || "") + (item.hasEOL ? "\n" : ""))
      .join("")
      .trim();
    const next = `${text}${text ? "\n\n" : ""}[Page ${n}]\n${body}`;
    if (encoder.encode(next).length > maxFileBytes) break;
    text = next;
    any ||= !!body;
    read = n;
  }
  return { text: any ? text : "", read };
}

/** attachments regroups a message's images: PDFs as { name, pdf, text,
 *  pages }, other images as { name, url }. A PDF sent as pages has no text. */
function attachments(images = [], pdfs = []) {
  const out = [];
  for (let n = 0; n < images.length;) {
    const pdf = pdfs.find((p) => p.at === n && p.pages > 0);
    const pages = pdf ? images.slice(n, n + pdf.pages) : null;
    out.push(pdf ? { name: pdf.name, pdf: true, text: "", pages } : { name: `image ${n + 1}`, url: images[n] });
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
