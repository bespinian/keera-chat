// The chat API. An answer that is not OK carries the gateway's error envelope,
// {"error": {"code", "message"}}, whether chat or the gateway refused. Only
// something in between, such as a proxy, answers without it.

import { sentence } from "./ui.js";

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

let onSignedOut = () => {};

/** whenSignedOut sets fn to be called once a request finds the sign-in gone,
 *  so the page can go back to the sign-in screen. */
export function whenSignedOut(fn) {
  onSignedOut = fn;
}

async function request(method, path, body, { signal, raw } = {}) {
  const headers = { Accept: raw ? "text/event-stream" : "application/json" };
  // A header no plain form can send, which the server requires on every
  // change, so another site cannot make this browser act here.
  if (method !== "GET") headers["X-Keera-Chat"] = "1";
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let res;
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
      credentials: "same-origin",
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

/** plain says what went wrong when the answer has no words of its own, as
 *  when something between the browser and Keera Chat refused. */
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

  /** chat sends a conversation to be answered. It resolves once the server
   *  took it, or throws an ApiError when it was refused, and then yields the
   *  reply's events as they arrive: { event: "delta" | "done", data }. */
  async chat(payload, signal) {
    const res = await request("POST", "/api/chat", payload, { signal, raw: true });
    return events(res.body);
  },
};

/** events reads a server-sent event stream. */
async function* events(stream) {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true }).replace(/\r\n/g, "\n");
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
        // An event that is not JSON is skipped; the stream carries on.
      }
    }
  }
}
