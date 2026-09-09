# Учебный бот группы

Telegram assistant for a study group: schedule storage, safe event changes, schedule questions, and reply-based conversations in Russian.

## Features

- Persistent SQLite schedule with recurrence, conflict warnings, provenance, and changelog.
- `/event` creates, updates, and cancels events through strict structured AI output and Go-side validation.
- User messages and successfully persisted bot replies form a persistent graph, so conversations can continue after restart.
- A small bounded AI agent handles replies through whitelisted tools only.
- Dashboard, authenticated API, compact Docker deployment, and embedded migrations/prompts.

## Architecture

`Telegram -> Conversation -> Agent -> Tools -> Schedule -> SQLite`

Telegram is only transport. Conversation owns message ingestion and reply context. AI decides user intent; Go validates and executes allowed work. Details and diagram: [`docs/architecture.md`](docs/architecture.md).

## Quick Start

1. Clone the repository.
2. Run `cp .env.example .env`.
3. Create a bot with BotFather and set `TELEGRAM_BOT_TOKEN`.
4. Set `TELEGRAM_GROUP_CHAT_ID` to the group ID and `ADMIN_TELEGRAM_USER_ID` for permitted private admin access.
5. Configure `AI_BASE_URL`, `AI_API_KEY`, and `AI_TEXT_MODEL` for an OpenAI-compatible provider.
6. Set strong `EXTERNAL_API_TOKEN` and `ADMIN_PASSWORD` values.
7. Run `docker compose up -d --build`.

The dashboard is at `/`, health check at `/healthz`, and admin page at `/admin` (HTTP Basic user `admin`). `/data` is persistent in Docker Compose.

## Configuration

| Variable | Purpose |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token; empty disables Telegram |
| `TELEGRAM_GROUP_CHAT_ID` | Authorized group chat ID |
| `ADMIN_TELEGRAM_USER_ID` | Admin allowed to use the bot in private chat |
| `TELEGRAM_DISCOVERY_MODE` | Enables admin-only `/chatid` discovery |
| `GROUP_NAME`, `GROUP_TIMEZONE` | Display name and IANA schedule timezone |
| `DATABASE_PATH` | SQLite path, `/data/app.db` in Docker |
| `AI_BASE_URL`, `AI_API_KEY`, `AI_TEXT_MODEL` | OpenAI-compatible AI connection |
| `RAW_MESSAGE_RETENTION_HOURS` | Raw graph retention; default 48 hours |
| `GITHUB_REPOSITORY` | GitHub `owner/repository` to monitor for new tags |
| `BASE_URL`, `HTTP_ADDR` | Dashboard URL and listen address |
| `EXTERNAL_API_TOKEN`, `ADMIN_PASSWORD` | API bearer token and admin password |

## Telegram Usage

```text
/event завтра в 13:00 коллоквиум по органике на 90 минут
/event перенеси #12 на завтра в 15:00
/today
/week
/ask что у нас сегодня?
/all кто идёт за кофе?
/roast @username
/help
```

Reply to a bot message to continue its conversation. For example, after `/event перенеси органику`, reply `Вторую`, then reply to the next question with `Завтра в 16:30`. The complete reply chain is recovered from SQLite. The bot also accepts an explicit `@bot_username` mention. Ordinary group messages are ignored.

## Admin Private Mode

Only the immutable Telegram ID in `ADMIN_TELEGRAM_USER_ID` can use the bot in a private chat. The administrator can ask ordinary questions and write schedule changes in natural language, for example `Добавь завтра на первой паре квантовую` or `Дедлайн документов до пятницы`. These changes use the same group schedule and are visible on the dashboard immediately, but are not posted to the group automatically.

`/sync preview` shows the pending digest privately. `/sync` sends it to the configured group and only then removes its pending announcement records. A failed Telegram send leaves the changes pending for retry.

The bot checks tags for `GITHUB_REPOSITORY` hourly. The first check records the current tag without notification; later new tags are sent once to the administrator in a private message with the release link.

## AI Safety

AI has no SQLite, shell, or unrestricted internal-service access. Conversational `/ask`, replies, and mentions use the bounded agent and its registered tools. `/roast` is a deliberately separate command with a narrow, embedded safety prompt and opt-in member check. Event mutations stay on the established path: structured proposal, strict Go validation, candidate/snapshot checks, then transactional `schedule.Apply`.

Prompts live in `prompts/*.md` and are embedded into the binary with `go:embed`.

## Development

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
node internal/web/static/app.test.mjs
docker compose build
```

The repository pins Go 1.27. If the host launcher has not installed that toolchain, use the same released image used by CI: `docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test ./...`.

## Project Structure

- `cmd/app`: composition root and lifecycle.
- `internal/conversation`: message graph and reply-chain context.
- `internal/agent`: bounded tool orchestration.
- `internal/ai`: AI transport and strict event parsing.
- `internal/schedule`: domain validation and transactional event mutations.
- `internal/telegram`: Telegram-specific transport and rendering.
- `internal/db/migrations`: the sole embedded migration source.
- `prompts`: versioned embedded prompts.

## Limitations

- The agent currently exposes schedule read tools; event mutations use the compatible `/event` structured operation flow.
- Message kind and media-group ID are recorded, but media content is not interpreted.
- Retention can intentionally shorten very old reply context; surviving messages remain structurally valid.
- Reminder delivery and per-occurrence recurrence exceptions are not implemented.
