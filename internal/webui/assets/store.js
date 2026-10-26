// Conversations, kept in this browser's IndexedDB. The server keeps none.
//
// A browser may be shared, so everything belongs to an owner: one person in
// one organisation. Small summaries let the sidebar skip loading images.

const name = "keera-chat";

let opening = null;

function database() {
  opening ||= new Promise((resolve, reject) => {
    const req = indexedDB.open(name, 1);
    req.onupgradeneeded = () => {
      const db = req.result;
      db.createObjectStore("conversations", { keyPath: "id" });
      db.createObjectStore("summaries", { keyPath: "id" }).createIndex("owner", "owner");
      db.createObjectStore("settings", { keyPath: "owner" });
    };
    req.onsuccess = () => {
      const db = req.result;
      // Let a newer page in another tab upgrade the database.
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

/** run runs fn in one transaction and resolves with its request's result
 *  once the transaction completes. */
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

/** keep writes a conversation and its summary together, so they match. */
function keep(t, c) {
  t.objectStore("conversations").put(c);
  t.objectStore("summaries").put({ ...summary(c), owner: c.owner });
}

function forget(t, id) {
  t.objectStore("conversations").delete(id);
  t.objectStore("summaries").delete(id);
}

const newestFirst = (a, b) => (a.updated_at < b.updated_at ? 1 : -1);

/** chats is one owner's conversations and settings. */
export function chats(owner) {
  return {
    /** list returns summaries, newest first. */
    async list() {
      const all = await run(["summaries"], "readonly", (t) => t.objectStore("summaries").index("owner").getAll(owner));
      return all.sort(newestFirst);
    },

    /** find returns a conversation, or null if this owner has none. */
    async find(id) {
      const c = await run(["conversations"], "readonly", (t) => t.objectStore("conversations").get(id));
      return c && c.owner === owner ? c : null;
    },

    async save(conv) {
      const kept = {
        id: conv.id,
        owner,
        title: conv.title,
        model: conv.model,
        key_id: conv.key_id,
        created_at: conv.created_at,
        updated_at: conv.updated_at,
        // Drop view-only fields.
        messages: conv.messages.map(({ streaming, showReasoning, ...m }) => m),
      };
      await run(["conversations", "summaries"], "readwrite", (t) => keep(t, kept));
      keepForGood();
    },

    /** rename keeps updated_at, so the chat keeps its place in the list. */
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

    /** removeAll deletes all of the owner's chats but keeps settings. */
    removeAll() {
      return run(["conversations", "summaries"], "readwrite", (t) => {
        const req = t.objectStore("summaries").index("owner").getAllKeys(owner);
        req.onsuccess = () => {
          for (const id of req.result) forget(t, id);
        };
      });
    },

    /** exportAll returns the owner's chats and instructions as a file. The
     *  owner is left out, so another organisation can import it. */
    async exportAll() {
      const all = [];
      // Use the owner index so other people's chats are never read.
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
      const conversations = all.sort(newestFirst).map(({ owner, ...c }) => c);
      return {
        format: exportFormat,
        version: 1,
        exported_at: new Date().toISOString(),
        instructions: settings?.instructions || "",
        conversations,
      };
    },

    /** importAll adds the chats of an exported file's text. A chat is
     *  replaced only by a newer copy, so importing twice adds nothing. Ids in
     *  skip are left alone. Instructions are taken only if the owner has
     *  none. */
    async importAll(text, skip = new Set()) {
      const { conversations, instructions } = parseExport(text);
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
              // Another owner in this browser has the same chat.
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

/** parseExport validates an exported file's text and keeps only known
 *  fields. The file is untrusted. */
function parseExport(text) {
  let file;
  try {
    file = JSON.parse(text);
  } catch {
    // Not JSON. Refused below.
  }
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
// Must match internal/chat/turns.go, so imported chats can be sent.
export const imageTypes = ["image/png", "image/jpeg", "image/gif", "image/webp"];
export const maxImages = 10;
export const maxFiles = 10;
export const maxFileBytes = 512 << 10;
const imagePattern = new RegExp(`^data:(${imageTypes.join("|")});base64,[A-Za-z0-9+/]+=*$`);

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
  if (m.pdfs != null) {
    // Each PDF is a run of the message's images. Runs do not overlap.
    let end = 0;
    const ok = (p) => {
      const fits = p && typeof p.name === "string" && Number.isSafeInteger(p.at) && Number.isSafeInteger(p.pages);
      if (!fits || p.at < end || p.pages < 1 || p.at + p.pages > (kept.images?.length || 0)) return false;
      end = p.at + p.pages;
      return true;
    };
    if (!Array.isArray(m.pdfs) || !m.pdfs.every(ok)) {
      throw new Error("a message has a PDF whose pages are not among its images.");
    }
    kept.pdfs = m.pdfs.map(({ name, at, pages }) => ({ name, at, pages }));
  }
  if (m.files != null) {
    const ok = (f) => f && typeof f.name === "string" && typeof f.text === "string";
    if (!Array.isArray(m.files) || !m.files.every(ok)) {
      throw new Error("a message has a file that cannot be read.");
    }
    if (m.files.length > maxFiles) {
      throw new Error(`a message has more than ${maxFiles} files.`);
    }
    if (m.files.some((f) => new TextEncoder().encode(f.text).length > maxFileBytes)) {
      throw new Error(`a message has a file larger than ${maxFileBytes >> 10} KB.`);
    }
    kept.files = m.files.map(({ name, text }) => ({ name, text }));
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

/** time normalizes v to ISO, so times compare as strings. "" if invalid. */
function time(v) {
  const d = typeof v === "string" ? new Date(v) : null;
  return d && !isNaN(d) ? d.toISOString() : "";
}

/** summary is what the sidebar shows of a conversation. */
export function summary(c) {
  return { id: c.id, title: c.title, model: c.model, updated_at: c.updated_at };
}

/** newID is random, so ids reveal nothing about who or when. */
export function newID() {
  const b = crypto.getRandomValues(new Uint8Array(12));
  return "c" + [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}

/** exclusive runs fn unless another tab is replying in the same chat, then
 *  calls busy. Without Web Locks (plain http, not localhost), fn just runs. */
export function exclusive(id, fn, busy) {
  if (!id || !navigator.locks) return fn();
  return navigator.locks.request("keera.chat.reply." + id, { ifAvailable: true }, (lock) => (lock ? fn() : busy()));
}

let persisting = false;

/** keepForGood asks for persistent storage. Some browsers prompt, so it
 *  waits until a chat is saved. */
function keepForGood() {
  if (persisting || !navigator.storage?.persist) return;
  persisting = true;
  navigator.storage
    .persisted()
    .then((done) => done || navigator.storage.persist())
    .catch(() => {});
}
