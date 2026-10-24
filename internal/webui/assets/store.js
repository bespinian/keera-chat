// Conversations, kept in this browser's IndexedDB. Keera Chat's server keeps
// none: each message brings its conversation with it.
//
// A browser may be shared, so everything belongs to an owner: one person in
// one organisation. A conversation is kept whole, and a small summary of it
// beside, so the sidebar does not read every image to show the titles.

const name = "keera-chat";

let opening = null;

function database() {
  opening ||= new Promise((resolve, reject) => {
    let req;
    try {
      req = indexedDB.open(name, 1);
    } catch (err) {
      reject(err);
      return;
    }
    req.onupgradeneeded = () => {
      const db = req.result;
      db.createObjectStore("conversations", { keyPath: "id" });
      db.createObjectStore("summaries", { keyPath: "id" }).createIndex("owner", "owner");
      db.createObjectStore("settings", { keyPath: "owner" });
    };
    req.onsuccess = () => {
      const db = req.result;
      // A newer page in another tab needs this one to let go before it can
      // change the database.
      db.onversionchange = () => db.close();
      resolve(db);
    };
    req.onerror = () => reject(req.error);
  }).catch((err) => {
    opening = null;
    throw new Error("This browser does not let Keera Chat keep chats here, so it cannot start. " + reason(err));
  });
  return opening;
}

/** run does the work fn starts in one transaction, and resolves with the
 *  result of the request fn returns, once all of it is written. */
async function run(stores, mode, fn) {
  const db = await database();
  return new Promise((resolve, reject) => {
    const t = db.transaction(stores, mode);
    const req = fn(t);
    t.oncomplete = () => resolve(req?.result);
    t.onerror = t.onabort = () => reject(new Error(reason(t.error)));
  });
}

function reason(err) {
  if (err?.name === "QuotaExceededError") {
    return "This browser has no room left for chats. Delete some you no longer need.";
  }
  return "The browser said: " + (err?.message || "no reason given") + ".";
}

/** keep writes a conversation and its summary in t, so the sidebar never lists
 *  a chat other than as it is kept. */
function keep(t, c) {
  t.objectStore("conversations").put(c);
  t.objectStore("summaries").put({ ...summary(c), owner: c.owner });
}

function forget(t, id) {
  t.objectStore("conversations").delete(id);
  t.objectStore("summaries").delete(id);
}

const newestFirst = (a, b) => (a.updated_at < b.updated_at ? 1 : -1);

/** chats is the conversations and settings of one owner. */
export function chats(owner) {
  return {
    /** list is the summaries, the most recently used first. */
    async list() {
      const all = await run(["summaries"], "readonly", (t) => t.objectStore("summaries").index("owner").getAll(owner));
      return all.sort(newestFirst);
    },

    /** find is a conversation, or null when this owner has none by that id. */
    async find(id) {
      const c = await run(["conversations"], "readonly", (t) => t.objectStore("conversations").get(id));
      return c && c.owner === owner ? c : null;
    },

    /** save keeps a conversation and moves it to the top of the list. */
    async save(conv) {
      const kept = {
        id: conv.id,
        owner,
        title: conv.title,
        model: conv.model,
        key_id: conv.key_id,
        created_at: conv.created_at,
        updated_at: conv.updated_at,
        // Leave out what the page uses only while showing it.
        messages: conv.messages.map(({ streaming, showReasoning, ...m }) => m),
      };
      await run(["conversations", "summaries"], "readwrite", (t) => keep(t, kept));
      keepForGood();
    },

    /** rename changes a title. Renaming is not using: the conversation keeps
     *  its place in the list. */
    rename(id, title) {
      return run(["conversations", "summaries"], "readwrite", (t) => {
        const req = t.objectStore("conversations").get(id);
        req.onsuccess = () => {
          const c = req.result;
          if (!c || c.owner !== owner) return;
          c.title = title;
          keep(t, c);
        };
      });
    },

    remove(id) {
      return run(["conversations", "summaries"], "readwrite", (t) => forget(t, id));
    },

    /** removeAll deletes every conversation of the owner, and keeps their
     *  settings. */
    removeAll() {
      return run(["conversations", "summaries"], "readwrite", (t) => {
        const req = t.objectStore("summaries").index("owner").getAllKeys(owner);
        req.onsuccess = () => {
          for (const id of req.result) forget(t, id);
        };
      });
    },

    /** exportAll is every conversation of the owner and their instructions,
     *  as a file another browser can import. It leaves the owner out, so the
     *  file can be imported under another organisation too. */
    async exportAll() {
      const all = [];
      // Going through the summaries' owner index means other people's chats,
      // and their images, are never read.
      const settings = await run(["summaries", "conversations", "settings"], "readonly", (t) => {
        const ids = t.objectStore("summaries").index("owner").getAllKeys(owner);
        ids.onsuccess = () => {
          for (const id of ids.result) {
            const req = t.objectStore("conversations").get(id);
            req.onsuccess = () => req.result && all.push(req.result);
          }
        };
        return t.objectStore("settings").get(owner);
      });
      const conversations = all
        .filter((c) => c.owner === owner)
        .sort(newestFirst)
        .map(({ owner, ...c }) => c);
      return {
        format: exportFormat,
        version: 1,
        exported_at: new Date().toISOString(),
        instructions: settings?.instructions || "",
        conversations,
      };
    },

    /** importAll keeps the conversations of an exported file. A conversation
     *  already here is replaced only by a newer copy of it, so importing the
     *  same file twice adds nothing. Ids in skip are left alone. The
     *  instructions are taken only when the owner has none yet. */
    async importAll(file, skip = new Set()) {
      const { conversations, instructions } = parseExport(file);
      const result = { added: 0, updated: [] };
      await run(["conversations", "summaries", "settings"], "readwrite", (t) => {
        for (const c of conversations) {
          if (skip.has(c.id)) continue;
          const req = t.objectStore("conversations").get(c.id);
          req.onsuccess = () => {
            const here = req.result;
            if (here && here.owner === owner) {
              if (here.updated_at >= c.updated_at) return;
              result.updated.push(c.id);
            } else {
              // The same chat, imported by someone else who shares this browser.
              if (here) c.id = newID();
              result.added++;
            }
            keep(t, { ...c, owner });
          };
        }
        if (instructions) {
          const req = t.objectStore("settings").get(owner);
          req.onsuccess = () => {
            if (req.result?.instructions) return;
            t.objectStore("settings").put({ ...req.result, instructions, owner });
            result.instructions = true;
          };
        }
      });
      if (result.added || result.updated.length) keepForGood();
      return result;
    },

    async settings() {
      const s = await run(["settings"], "readonly", (t) => t.objectStore("settings").get(owner));
      return { instructions: s?.instructions || "" };
    },

    async saveSettings(settings) {
      await run(["settings"], "readwrite", (t) => t.objectStore("settings").put({ ...settings, owner }));
      return settings;
    },
  };
}

const exportFormat = "keera-chat";

/** parseExport checks an exported file and keeps only what the page itself
 *  would have written. A file can come from anywhere, so an image must be an
 *  image, and an id one the page can route to. */
function parseExport(file) {
  if (!file || file.format !== exportFormat || !Array.isArray(file.conversations)) {
    throw new Error("This is not a file exported from Keera Chat.");
  }
  if (file.version !== 1) {
    throw new Error("This file comes from a newer Keera Chat, which this one cannot read yet.");
  }
  const conversations = file.conversations.map((c, i) => {
    try {
      return conversation(c);
    } catch (err) {
      throw new Error(`Chat ${i + 1} in this file cannot be read: ${err.message}`);
    }
  });
  return { conversations, instructions: text(file.instructions).slice(0, 8000) };
}

const idPattern = /^c[0-9a-f]{24}$/;
// The image types and count chat.js attaches and internal/chat/turns.go
// accepts, so an imported chat can be sent on.
const imagePattern = /^data:image\/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/]+=*$/;
const maxImages = 10;

function conversation(c) {
  if (!c || typeof c !== "object") throw new Error("it is not a chat.");
  if (!idPattern.test(c.id)) throw new Error("its id is not one Keera Chat makes.");
  if (!Array.isArray(c.messages)) throw new Error("it has no messages.");
  const updated = time(c.updated_at) || new Date().toISOString();
  return {
    id: c.id,
    title: text(c.title) || "Imported chat",
    model: text(c.model),
    key_id: text(c.key_id),
    created_at: time(c.created_at) || updated,
    updated_at: updated,
    messages: c.messages.map(message),
  };
}

function message(m) {
  if (!m || (m.role !== "user" && m.role !== "assistant")) {
    throw new Error("a message is neither from a person nor a model.");
  }
  const kept = { role: m.role, content: text(m.content) };
  if (m.images != null) {
    if (!Array.isArray(m.images) || !m.images.every((u) => typeof u === "string" && imagePattern.test(u))) {
      throw new Error("a message has an image that is not a PNG, JPEG, GIF or WebP.");
    }
    if (m.images.length > maxImages) {
      throw new Error(`a message has more than ${maxImages} images.`);
    }
    kept.images = m.images;
  }
  for (const k of ["model", "reasoning", "error", "error_detail", "finish"]) {
    if (m[k]) kept[k] = text(m[k]);
  }
  if (m.stopped === true) kept.stopped = true;
  if (Number.isSafeInteger(m.tokens) && m.tokens > 0) kept.tokens = m.tokens;
  if (time(m.created_at)) kept.created_at = time(m.created_at);
  return kept;
}

function text(v) {
  return typeof v === "string" ? v : "";
}

/** time is an ISO time in the form the page writes, so times compare as
 *  strings, or "" when v is none. */
function time(v) {
  const d = typeof v === "string" ? new Date(v) : null;
  return d && !isNaN(d) ? d.toISOString() : "";
}

/** summary is what the sidebar lists of a conversation. */
export function summary(c) {
  return { id: c.id, title: c.title, model: c.model, updated_at: c.updated_at };
}

/** newID names a new conversation. It is random, so ids say nothing about
 *  who chats or when. */
export function newID() {
  const b = crypto.getRandomValues(new Uint8Array(12));
  return "c" + [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}

/** exclusive runs fn while no other tab of this browser runs it for the same
 *  conversation, so two tabs cannot both write a reply into it and lose one.
 *  Where the browser has no locks, as on plain http away from this machine,
 *  fn just runs. */
export function exclusive(id, fn, busy) {
  if (!id || !navigator.locks) return fn();
  return navigator.locks.request("keera.chat.reply." + id, { ifAvailable: true }, (lock) => (lock ? fn() : busy()));
}

let persisting = false;

/** keepForGood asks the browser not to clear the chats when it runs short of
 *  space. Some browsers ask the person first, so it is asked once a chat is
 *  kept, not when the page opens. */
function keepForGood() {
  if (persisting || !navigator.storage?.persist) return;
  persisting = true;
  navigator.storage
    .persisted()
    .then((done) => done || navigator.storage.persist())
    .catch(() => {});
}
