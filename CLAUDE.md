# atm-messaging-api

Internal multi-tenant Go microservice. Other systems (SaaS, ERPs) call it to send messages to their customers through different channels. Not public: only consumed by our own systems, authenticated with an API key.

Module: `github.com/AtmosferaSoltec/atm-messaging-api`.

First channel: unofficial WhatsApp (the business owner scans a QR like WhatsApp Web; the service sends text, images and PDFs from that number). Later: email (Resend, Brevo) and SMS. Only WhatsApp is implemented for now.

## Stack

- Go (current), Gin for HTTP
- whatsmeow (`go.mau.fi/whatsmeow`) for WhatsApp. Check its current API in the official repo before writing code; it changes between versions.
- PostgreSQL everywhere, in the shared `atmosfera-postgres` container: own database `messaging`, own role `messaging_app`, own schema `messaging` (provisioned by `atmosfera-postgres/initdb/05-messaging.sh`). `pgx/v5` + `goose` migrations (`db/migrations/`). Repositories are interfaces, so the domain does not depend on the engine.
- Redis optional later; start with an in-memory queue + persistence in the DB

## Domain model

- **Tenant**: one of our systems (e.g. La Vendimia). Has an API key (stored hashed), a webhook secret and plan/limits.
- **Account**: a business inside a tenant (e.g. one company of La Vendimia), identified by `external_ref` (its ID in the tenant's system).
- **Session**: a channel connection of an account (e.g. its WhatsApp number). Belongs to tenant + account.
  **An account has at most ONE session per channel.** Unique on `(tenant_id, account_id, channel)`. A tenant with 10 accounts has 10 WhatsApp sessions; an account never has several WhatsApp numbers.
  States: `pending_qr`, `connected`, `disconnected`, `banned`.
- **Message**: `tenant_id`, `session_id`, `channel`, `to`, `type` (text|image|document), `body`, `attachments`, `priority` (transactional|promotional), `idempotency_key`, `status` (queued|sending|sent|delivered|read|failed), `error`, timestamps.

Callers do not pick a session: they send `account_ref` + `channel`, and the service resolves the account's single session for that channel.

## Architecture

Business modules live in `internal/features/[module]/` (tenant, account, session, message, webhook, ...). Channel providers are infrastructure adapters in `internal/channel/`.

- `internal/channel/`: `Channel` interface (Send, Status, ...), provider-agnostic. Future providers (email, SMS) implement it.
- `internal/channel/whatsapp/`: whatsmeow implementation and a `SessionManager` (one client per session, reconnects on startup, emits events: QR, connected, disconnected, logged out, receipts). Nothing outside this package may import whatsmeow.
- `internal/queue/`: `Runner`, one worker goroutine per session. The queue itself is the `messages` table (survives restarts); `internal/limits/` holds the pure sending policy (pacing, daily caps, warm-up).
- `internal/features/tenant/`: API key auth, limits, quotas.
- `internal/http/`: Gin handlers and middlewares (auth, request id, logging, recover).
- Outgoing webhooks signed with HMAC-SHA256 (`X-Signature` header), with retries and backoff.

Other layout: `cmd/api/` (wiring and subcommands), `cmd/devqr/` (dev helper that draws the QR stream in the terminal), `internal/config/`, `internal/platform/` (database, httpserver, apierror), `db/migrations/`.

Note: auth middleware lives in `internal/features/tenant/`, and each feature registers its own routes; there is no separate `internal/http`.

## Business rules

- Everything is filtered by the `tenant_id` derived from the API key, never from a request parameter.
- Idempotent sending by `(tenant_id, idempotency_key)`.
- Per-session configurable limits: high for transactional; low for promotional, with random delay between messages (e.g. 8-20 s), daily cap and progressive warm-up for new numbers. These values are starting points, not official WhatsApp figures.
- Promotional messages support opt-out (per-session unsubscribe list; never send to unsubscribed recipients).
- A failed or slow session must not affect the others.
- A session is connected in only one instance at a time (single instance for now).

## Security

- Never log API keys, tokens or session credential contents.
- API keys: shown once on creation, only the hash is stored.
- Session files/DB hold customers' WhatsApp credentials: never commit them (`.gitignore`) and keep restricted permissions.
- Admin panel uses authentication separate from API keys.
- Validate attachment size and type.

## Commands

- `go run ./cmd/api migrate up|down|status` apply migrations (needs `DATABASE_URL`, see `.env.example`)
- `go run ./cmd/api tenant create -name NAME` create a tenant; prints the API key and webhook secret once
- `go run ./cmd/api serve` start the server (default command)
- `ATM_API_KEY=... go run ./cmd/devqr -session <id>` draw the QR of a pending session
- `go test -race ./...`

## Endpoints v1 (MVP)

- `POST   /v1/sessions` create session (body: `account_ref`, `channel`; 409 if the account already has one for that channel)
- Auth: `Authorization: Bearer <api key>` on everything under `/v1`. `GET /health` is public.
- `POST /v1/messages` body: `account_ref`, `channel`, `to`, `type`, `body`, optional `priority`, `idempotency_key`, `attachment` `{filename, mime_type, content_base64}` (images jpeg/png/webp, documents pdf, max 16 MiB decoded)
- `GET    /v1/sessions?account_ref=` list
- `GET    /v1/sessions/{id}` status
- `GET    /v1/sessions/{id}/qr/stream` QR over SSE
- `DELETE /v1/sessions/{id}` close session (logout)
- `POST   /v1/messages` send (text/image/document), responds 202 with `message_id`
- `GET    /v1/messages/{id}` message status
- `POST|GET /v1/sessions/{id}/opt-outs`, `DELETE /v1/sessions/{id}/opt-outs/{to}` manage the promotional unsubscribe list (promotional messages to them get 422)
- Tenant webhook settings: `GET|PUT|DELETE /v1/webhook`, `POST /v1/webhook/test`, `GET /v1/webhook/events`
- Admin API (`/admin/v1`, `Authorization: Bearer adm_...` from `POST /auth/login {username, password}`): `POST /auth/logout`, `GET /auth/me`; `GET|POST /tenants`, `GET|PATCH /tenants/{id}`, `POST /tenants/{id}/rotate-key`, `PUT|DELETE /tenants/{id}/webhook`, `GET /tenants/{id}/webhook/events`; `GET|POST /sessions` (POST takes `tenant_id`, `account_ref`, `channel`), `GET|DELETE /sessions/{id}`, `POST /sessions/{id}/connect-link {label?}` (returns the shareable URL once; replaces the previous link), `POST /sessions/{id}/disconnect` (unlink the number, keep the session, ready for a new QR); `GET /messages` (never returns bodies); `GET /metrics[?tenant_id=]`. Login is throttled (5 failures / 15 min per IP and per user).
- Public connect page API (no auth; the token in the URL is the credential): `GET /public/connect/{token}` (label, channel, status, expiry; no internal ids) and `GET /public/connect/{token}/qr/stream` (SSE: `qr`, `connected`, `closed`). Links are 256-bit random, stored hashed, expire (`CONNECT_LINK_TTL`, default 24h), belong to one session and can do nothing else. Opening the stream of an unlinked session starts a new pairing.
- Webhooks: `session.connected`, `session.disconnected`, `message.sent`, `message.delivered`, `message.read`, `message.failed`

## Webhook contract (for the tenants' systems)

`POST` to the tenant's URL with JSON `{id, type, created_at, data}` and headers `X-Webhook-Id`, `X-Webhook-Event`, `X-Webhook-Timestamp` (unix seconds) and `X-Signature: sha256=<hex>` = HMAC-SHA256 of `"<timestamp>.<raw body>"` with the tenant's webhook secret (shown on tenant creation and by `GET /v1/webhook`). Reject requests with an old timestamp. Any 2xx is success; otherwise it is retried (30s, 2m, 8m, ... up to 6 attempts). `session.disconnected` is only sent after a drop lasts 60s, or at once on logout/ban (`data.reason`). Production needs https URLs that do not resolve to private addresses.

## Phases (keep the order)

1. ~~Spike~~ (done): QR in terminal, persisted session, text + PDF sent, survives restart without rescanning.
2. Multi-session SessionManager + Gin API (create session, QR over SSE, send) with per-tenant API key. (implemented; sends directly, no queue yet)
3. ~~Per-session queue with rate limiting and retries~~ (implemented, untested with a real phone): the queue is the `messages` table; `message.Dispatcher` runs one `queue.Runner` worker per session; sending policy in `internal/limits` (env `LIMITS_*`, `QUEUE_*`, see `.env.example`); opt-outs in `internal/features/optout`.
4. ~~Signed webhooks and message statuses~~ (implemented, untested with a real phone): `internal/features/webhook` is an outbox (table `webhook_events`) with one worker per tenant, HMAC-SHA256 signing, exponential retries and an SSRF-safe HTTP client; delivered/read receipts update messages.
5. ~~Admin endpoints for the React panel~~ (implemented): `/admin/v1` with a single operator whose `ADMIN_USER`/`ADMIN_PASSWORD` live in `.env` (no user table, no JWT); login returns an opaque in-memory token (`adm_...`), separate from tenant API keys. Customers never get an account: the operator sends them a public connect link (`/public/connect/{token}`, see below).

**Before each phase, propose the plan in a few lines and wait for approval.**

## Development rules

1. **Language**: all code, comments, identifiers, types, DB tables/columns, routes, JSON fields, package and file names in English. Only user-facing message text returned to the client may be in Spanish.
2. **Modular architecture**: business logic lives in `internal/features/[module]/`. Features do not import each other's internals; share through `internal/shared/` or interfaces.
3. **Layering**: handler -> service -> repository. Handlers parse/validate/respond; services hold business rules; repositories only touch the database.
4. **Strict typing**: typed request/response structs (no `map[string]any`); small interfaces declared where consumed.
5. **Style**: `gofmt`, short lowercase package names, errors wrapped with context (`%w`), `context.Context` on all I/O, no global mutable state.
6. **Tests** for limits logic and auth.
7. **Config** via environment variables, documented in `.env.example`.

## Out of scope for now

Email, SMS, official WhatsApp API, multi-instance, billing. Keep the `Channel` interface ready but do not implement them.
