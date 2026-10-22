# Keera Chat for agents

How Keera Chat is laid out, how a request flows through it, and the
conventions the code follows. What it does and how to run it is in
[README.md](README.md).

## Commands

```sh
make check                                      # test + vet + fmt-check, what CI runs besides lint and build
make test                                       # go test -race -cover ./...
go test -race ./internal/chat -run TestReply    # one test
make fmt                                        # gofmt -w cmd internal
make lint                                       # golangci-lint
make dev                                        # live reload against the gateway's make dev
make build                                      # build/keera-chat
```

`make lint` needs golangci-lint on PATH. CI pins v2.14, so a newer one may report
what CI does not. `go.mod` asks for Go 1.27, and the code uses its newer
standard library, such as `errors.AsType`.

## Layout

- `cmd/keera-chat` is `main`: `version` or `--version` prints the build,
  anything else runs the server.
- `internal/server` wires the rest together: it reads the config, builds the
  logger, the gateway client and the cookie box, and runs the listener until
  SIGINT or SIGTERM.
- `internal/config` reads the `KEERA_CHAT_*` settings from the environment and
  refuses bad ones at start-up. `load` takes a `getenv`, so tests need no
  process environment.
- `internal/chat` is the HTTP side. `server.go` has the routes, the middleware,
  the who-is cache and the error answers. `auth.go` is the sign-in and the
  sealed cookies, `api.go` is `/api/me` and switching organisation,
  `reply.go` streams a reply, and `turns.go` turns a conversation into the
  messages a model gets.
- `internal/gateway` is the client for the few control API routes chat calls,
  and words the gateway's errors for a person.
- `internal/gatewaytest` is a stand-in gateway for the tests, with one person,
  a few keys and scripted models.
- `internal/secret` seals the cookies with AES-GCM.
- `internal/webui` embeds `assets/`, the page, and serves it gzipped with
  ETags.
- `internal/version` says which build a binary is.

## How a request flows

- `chat.Server.Handler` (`internal/chat/server.go`) wraps every route in
  `logged`, `sameOrigin` and `securityHeaders`. Any path that is not `/api/`,
  `/auth/` or a file gets `index.html`, and the page routes on the URL (`/` is
  a new chat, `/c/<id>` a conversation). Unknown `/api/` and `/auth/` paths are
  JSON 404s.
- Every change (non-GET) must carry `X-Keera-Chat: 1`, which `api.js` sends.
  A new `fetch` that skips `api.js` is refused as cross-site.
- `/api/*` handlers go through `signedIn`, which unseals the session cookie
  and resolves the token with `/control/v1/me`, cached for a minute
  (`whoCache`). A 401 from the gateway clears the cookie.
- Errors always use the gateway's envelope, `{"error": {"code", "message"}}`.
  `gatewayFailed` passes the gateway's refusals on as they are. Anything else
  is logged and answered vaguely.
- `POST /api/chat` (`reply.go`) takes the whole conversation, builds the
  OpenAI-style messages in `turns.go` (`wire` drops failed turns, `splitThink`
  takes out inline thinking), and streams server-sent events: `delta` for each
  piece, then `done` with the whole reply. A gateway refusal, such as a spent
  budget, arrives as a `done` whose reply has an error, and the page keeps it in
  the conversation. Stopping the reply in the page ends the request context,
  and with it the request to the gateway.

## The page

- `app.js` is the shell, the sidebar and the menus. `chat.js` is one
  conversation. `api.js` is fetch and the error envelope. `store.js` is
  IndexedDB. `markdown.js` renders replies with `h()`. `ui.js` has the DOM
  helpers.
- A reply keeps streaming when the person opens another conversation. Its
  state lives in `app.live`, keyed by conversation, and whichever view shows
  that conversation paints it.
- `store.js` keeps everything per owner (person and organisation), so people
  who share a browser do not see each other's chats. It keeps summaries apart
  from whole conversations, so the sidebar does not read every image.
- Image limits (types, count) live in three places that must agree:
  `chat.js`, `internal/chat/turns.go`, and the import check in `store.js`.
- The assets are embedded with `go:embed`, so any change to the page needs a
  Go rebuild. `make dev` does this through air.

## Conventions

- **No dependencies.** The Go code uses only the standard library, and the
  page is plain ES modules and CSS with no build step, so `go build` is the
  whole build. Ask before adding a module or an npm package.
- **Chat keeps nothing.** Conversations live in the browser. Nothing that
  holds a token, a conversation or a message is logged: a request is logged
  with its method, path, status and duration, and never its query or body.
- **Markup is built, never parsed.** The page makes every node with `h()` and
  `replace()` from `ui.js`, and never sets `innerHTML`, so nothing a model or a
  person writes can become markup. The CSP allows scripts from this origin
  only.
- **Words for people.** Messages the server sends are lower-case clauses
  without a full stop, joined with semicolons, and say what to do next. The
  page's `sentence()` adds the capital and the full stop. A technical error,
  such as a model server's, gets a plain sentence and keeps its own text as
  the detail.
- **Comments say why**, in plain, whole sentences. Doc comments start with
  the name they describe. The code says "the person", not "the user", and
  writes British English: organisation, licence.
- **Settings** are `KEERA_CHAT_*`, checked in `internal/config`, and listed in
  the README's settings table. A new one goes in all three.
- **Tests** drive chat end to end through the `harness` in
  `internal/chat/chat_test.go`, against `gatewaytest`. Table tests are maps
  keyed by a short name for the case. A test that checks a behaviour of the
  real gateway needs `gatewaytest` to check it too.
