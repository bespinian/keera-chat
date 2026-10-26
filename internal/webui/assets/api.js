// The chat API. Errors use the gateway's envelope, {"error": {"code", "message"}}.
// Only something in between, such as a proxy, answers without it.

import { sentence } from "./ui.js";

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

let onSignedOut = () => {};

/** whenSignedOut sets fn to call when a request finds the sign-in gone. */
export function whenSignedOut(fn) {
  onSignedOut = fn;
}

async function request(method, path, body, { signal, raw } = {}) {
  const headers = { Accept: raw ? "text/event-stream" : "application/json" };
  // CSRF guard: a plain form cannot send this header.
  if (method !== "GET") headers["X-Keera-Chat"] = "1";
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let res;
  try {
    res = await fetch(path, {
      method,
      headers,
      body: JSON.stringify(body),
      signal,
    });
  } catch (err) {
    if (err.name === "AbortError") throw err;
    throw new ApiError(0, "offline", "Keera Chat did not answer. Check your connection and try again.");
  }
  if (!res.ok) {
    let code = "";
    let message = plain(res.status);
    try {
      const data = await res.json();
      code = data.error?.code || "";
      message = data.error?.message || message;
    } catch {
      // Not our envelope, such as a proxy's error page.
    }
    if (res.status === 401 && code === "signed_out") onSignedOut();
    throw new ApiError(res.status, code, sentence(message));
  }
  return raw ? res : res.json();
}

/** plain words an error that came without a message, such as from a proxy. */
function plain(status) {
  if (status === 413) return "This chat is too large to send. Start a new chat, or attach fewer or smaller images.";
  if (status === 429) return "There are too many messages right now. Wait a moment and try again.";
  if (status >= 500) return "Keera Chat is not working right now. Try again in a little while.";
  return "Something went wrong. Reload the page and try again.";
}

export const api = {
  authConfig: () => request("GET", "/auth/config"),
  logout: () => request("POST", "/auth/logout"),
  me: () => request("GET", "/api/me"),
  switchOrg: (orgID) => request("POST", "/api/org", { org_id: orgID }),

  /** chat sends a conversation. It throws an ApiError if refused, then
   *  yields the reply's events: { event: "delta" | "done", data }. */
  async chat(payload, signal) {
    const res = await request("POST", "/api/chat", payload, { signal, raw: true });
    return events(res.body);
  },
};

async function* events(stream) {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    // reply.go ends lines with \n only.
    buf += decoder.decode(value, { stream: true });
    let i;
    while ((i = buf.indexOf("\n\n")) >= 0) {
      const block = buf.slice(0, i);
      buf = buf.slice(i + 2);
      let event = "message",
        data = "";
      for (const line of block.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data += line.slice(5).trim();
      }
      if (!data) continue;
      try {
        yield { event, data: JSON.parse(data) };
      } catch {
        // Skip events that are not JSON.
      }
    }
  }
}
