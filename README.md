# 411 группа

Go 1.27, SQLite, Telegram long polling and a glass calendar dashboard.

## Run

Configure `.env` using `.env.example`: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_GROUP_CHAT_ID`, `ADMIN_TELEGRAM_USER_ID`, AI connection settings, `EXTERNAL_API_TOKEN`, and a strong `ADMIN_PASSWORD`. Run `docker compose up -d --build`; health is `/healthz`, dashboard `/`, HTTP Basic admin page `/admin` (username `admin`). Use HTTPS when exposed publicly. No token means Telegram is disabled.

Only the configured group accepts member interactions. Private messages are accepted only from the positive numeric `ADMIN_TELEGRAM_USER_ID`; unset means private access is disabled. Usernames never grant authority. The configured identity is recorded as admin when observed, including first contact in private. Private operations still manage the configured group's events; provenance retains the actual private chat/message IDs. Discovery `/chatid` is available only to the configured admin with `TELEGRAM_DISCOVERY_MODE=true`.

Command menus are installed only for the configured group and admin private chat. Default, all-private and all-group menus are cleared. Arbitrary historical per-chat or language-specific menus cannot be enumerated by Telegram; authorization still rejects those chats.

## Event Commands

```text
/event завтра в 13:00 коллоквиум по органике на 90 минут
/event каждый вторник в 10:00 семинар, 4 раза, до 11:35
/event перенеси #12 на завтра в 15:00
/event замени #12 на консультацию, длительность 30 минут
/event удали #12
/today
/week
/ask что у нас сегодня?
/roast @username
/all кто идёт за кофе?
```

`/event` accepts its request in the command, or when replying to the message that contains it. Successful changes are compact standalone messages; only clarifications and errors reply to the command message. If clarification is needed, reply normally to the bot's active question within five minutes, for example `второй` or the event title; unrelated messages and replies to older bot messages are ignored. Starting another `/event` replaces the active clarification. Persisted state survives restart; `/add` is not a command.

The model parses create/update/cancel, understands убери/удали/отмени/замени/перенеси, and must choose IDs from active group candidates. A valid operation applies immediately. One message may create several independently unambiguous events; ambiguity produces one concise Russian question with readable candidates. Explicit IDs still restrict model authority. Cancellation is soft and every mutation appends a changelog and source record atomically; update replaces tags and recomputes dedupe identity.

Creates with an unambiguous date and time save immediately; they never need a location or duration. Omitted duration defaults to lesson 95, test/quiz 60, exam 120, and other events 60 minutes. Successful replies show only the action, title, natural date and time range, plus concise overlap warnings; one multi-event request produces one message. Explicit ends/durations must agree; limits are 1..1440 minutes. Edit later via a new `/event` targeting the event ID. Inspect replies: model interpretation is not a deterministic natural-language guarantee.

Overlapping events are both persisted; end equal to another start is not an intersection. Create/update replies and API responses return structured overlap warnings with the intersected event and occurrence time. New recurring proposals must be daily or slower and finite within one year, at most 366 counted occurrences. Existing unbounded daily-or-slower series are checked across the proposed series' full horizon. Unsupported/dense legacy recurrence blocks the operation safely. Legacy events without an end occupy 95 minutes for overlap checks. Recurring updates/cancellations affect the entire series, not a single occurrence. Missing dates/times, low confidence, invalid JSON/fields/timezones, excessive prompts and invalid recurrence request correction without writing.

The structured event path uses JSON response mode, strict decoding, and local validation; the AI endpoint must support OpenAI-compatible `response_format: json_object`. No paid AI calls are required for tests.

## API And Limits

Public reads: `/api/public/v1/schedule?from=<RFC3339>&to=<RFC3339>` (max 366 days), `/api/public/v1/status`. Bearer-authenticated routes include `/api/events`, `/api/events/{id}`, `/api/changes`, `/api/users`. `POST /api/events` permits overlaps and includes `warnings` with each intersected event/occurrence. API event creation retains its existing direct-import behavior, separate from Telegram event operations. `/api/ingest` is not implemented; reminder delivery, photo/audio ingestion and per-occurrence recurrence exceptions are not implemented.

SQLite migrations are embedded under `internal/db/migrations` and preserve existing data. Telegram receipt keys remain persisted to prevent replay after raw-message retention cleanup. Historical proposal and intent rows are retained but unused.

## Checks

`go test ./...`, `go vet ./...`, `node internal/web/static/app.test.mjs`. Chromium checks: `PLAYWRIGHT_MODULE=/path/to/playwright-core/index.mjs CHROMIUM_PATH=/path/to/chrome node internal/web/static/browser.test.mjs` against localhost:8080. Go checks can run in `golang:1.27` with the project mounted at `/src`.
# eleven_bot
