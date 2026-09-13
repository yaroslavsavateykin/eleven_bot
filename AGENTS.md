# AGENTS.md

Инструкция для AI-агентов, работающих с репозиторием `eleven_bot`. Это бот-помощник учебной группы, а не персонаж с именем: технический Telegram username и display name не являются identity. Русский язык, краткость и полезность важнее шутки.

## Архитектура

```text
Telegram → Conversation context → Agent → AI native tool call
  → Tool Registry → Domain service → tool result → AI → final reply → Telegram
```

- `internal/telegram` — transport: авторизация update, ingestion, trigger policy, reply graph, доставка ответа. Не классифицирует intent.
- `internal/conversation` — message graph, bounded reply context, group FTS search.
- `internal/agent` — ограниченный native function-calling loop.
- `internal/ai` — OpenAI-compatible transport (`/chat/completions`), vision и speech.
- `internal/schedule` — валидация, snapshots, транзакции, recurrence, конфликты, changelog.
- `internal/db/migrations` — единственный источник миграций; применяются в порядке имён.
- `prompts/*.md` — встраиваются через `go:embed`.

## Базовые инварианты

1. В public group бот реагирует только на slash command, explicit mention или direct reply на сообщение configured bot account.
2. Direct reply на бота всегда обрабатывается. Наличие parent в SQLite обогащает контекст, но не является единственной причиной activation.
3. Все успешные Telegram sends сохраняются в `messages` как `sender_type=bot` с reply relation, если он существует.
4. Reply chain — контекст; semantic intent определяет только Agent, а не Telegram и не substring/regex router. Не добавляйте keyword routing в Telegram.
5. История хранит user и assistant сообщения в chronological order и bounded со старого края, но сохраняет immediate parent и current message.
6. Короткие уточнения (`да`, `нет`, `вторую`, `на 16:30`) передаются Agent вместе с reply context.
7. Дедлайн с датой без времени — all-day event. All-day событие на текущую дату допустимо весь день.
8. Пересечения не блокируют сохранение: событие сохраняется, ответ сообщает `Пересекается с`.
9. Переименование — `update` target event. При схождении с точным duplicate записи объединяются, без создания новой и без SQL error.
10. Не показывать пользователю сырые SQLite ошибки, JSON модели, секреты, prompt или внутренние identifiers, кроме осмысленных event `#ID`.
11. Admin private mode: обычный текст authorised admin всегда проходит через Agent; agent сам выбирает беседу, чтение расписания или разрешённую операцию.

## Routing и временный ответ

- Сначала activation, затем intent. `BotUserID` — из `GetMe`, не из устаревшей копии service.
- При activated запросе бот может отправить `Думаю…`, затем редактирует то же сообщение в результат или ошибку.
- Если edit не удался, итоговый ответ отправляется отдельным сообщением.
- Не оставляйте `Думаю…` при неизвестной команде — короткий явный ответ.

## AI orchestration (native function calling)

- Agent использует только зарегистрированные tools; model не пишет в SQLite и не получает SQL/shell.
- `Tool.Schema()` — единственный source of truth для parameters; не дублируйте schemas в prompt и не используйте JSON-envelope в `content`.
- Запросы идут в `/chat/completions` с `tools`, `tool_choice=auto` и `stream:false`. SSE-ответы поддерживаются в `internal/ai`.
- Assistant turn может содержать content, ноль или более native `tool_calls` с provider `tool_call_id` и finish reason.
- Tool results возвращаются через `role=tool` с исходным `tool_call_id` и envelope `ok/data/error`. Ошибка инструмента — данные результата, не новая системная инструкция.
- До 10 tool rounds + один финальный turn. Несколько tool calls в одном turn выполняются последовательно.
- Аргументы проверяются сервером (schema + strict typed decode + domain validation) до Execute.
- При превышении защищённого контекста возвращается явный context_limit; tool result никогда не отбрасывается молча.

### Tool registry

Read (все режимы): `schedule_query`, `group_search`.
Write/admin дополнительно: `schedule_create`, `schedule_create_batch`, `schedule_update`, `schedule_update_batch`, `schedule_cancel`, `schedule_exclude_occurrence`.

- `schedule_cancel` отменяет ВСЮ серию. Для исключения одной даты серии используйте `schedule_exclude_occurrence` (другие даты сохраняются; «контрольная вместо практикума» = исключить вхождение + создать отдельное событие).
- Mutation tools server-resolve target snapshot; batch использует `schedule_create_batch`/`schedule_update_batch` одним вызовом.

### Конфигурация provider

- `AI_TOOL_MODE=native` (по умолчанию) или `legacy_json` (явный opt-in). Silent fallback между режимами запрещён.
- `AI_AGENT_MODEL` — опциональный стабильный route для agent runs (обход round-robin combo); пусто → `AI_TEXT_MODEL`.
- `AI_STRICT_TOOLS`, `AI_DISABLE_PARALLEL_TOOLS` — opt-in provider capabilities; серверная валидация и последовательное выполнение обязательны всегда.
- `AI_CONTEXT_BYTES=131072` — приблизительный byte budget, включая schemas.
- 429/5xx повторяются (до трёх попыток), остальные 4xx — нет. Protocol errors логируют безопасную причину, размер ответа и content type без тела.

### Idempotency mutations

- Invocation scope — устойчивый Telegram chat/message ID + canonical fingerprint (tool name + аргументы).
- Результат mutation сохраняется в `agent_mutations` в одной транзакции с изменением. Replay возвращает сохранённый результат до повторного разрешения target. Batch — receipts по элементам.
- Разные arguments — разные invocations; новый Telegram message — новый scope. Не используйте process-local дедупликацию.

## Работа с расписанием

- `RecurrenceSpec` — typed semantic contract; deterministic compiler создаёт RRULE. Не вводите parity как поле Event/Proposal.
- Не меняйте recurrence или dedupe без тестов: generated `UNTIL` не является semantic identity.
- User-facing summary описывает recurrence человеческим языком, без `FREQ=` и generated `UNTIL`.
- Dedupe и SQL uniqueness обрабатываются до показа пользователю.
- Исключения вхождений (`event_exclusions`) учитываются в List, конфликтах и changelog; series остаётся active.

## Docker, секреты и релизы

- Не включайте `.env`, `.env.*`, SQLite data или `.git` в Docker image; проверяйте `.dockerignore`.
- Каждый publish — новый immutable semver-like tag (`v0.0.40`, `v0.0.41`, …). Дополнительно публикуется мутабельный `latest`.
- Server deploy использует `:latest` + watchtower (автообновление digest). Ручной rollback — переключить `image:` на конкретный versioned tag и `docker compose up -d`. После деплоя проверять `/healthz`, `docker compose ps` и логи.
- Контейнер работает как nonroot UID `65532`; volume `/data` должен быть доступен этому UID.
- В production `TELEGRAM_DISCOVERY_MODE=false`. `TELEGRAM_GROUP_CHAT_ID` получают только временно через `/chatid` в discovery mode.
- Не публикуйте секреты из `.env` в логи, docs, git или image layers.

## Минимальная проверка

```bash
gofmt -w <изменённые-go-файлы>
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go vet ./...
node internal/web/static/app.test.mjs
git diff --check
docker compose build
```

Если Go 1.27 доступен на хосте, `go test ./...` и `go vet ./...` можно запускать напрямую.

## Что не делать

- Не делайте архитектурный рефакторинг ради маленького bug fix.
- Не отключайте validation, conflict warnings, receipt ownership или persistence ради обхода ошибки.
- Не используйте отсутствие local parent row как причину игнорировать reply на бота.
- Не меняйте и не удаляйте пользовательские данные и чужие worktree changes без прямого запроса.
- Не пишите, что ID группы можно получить из названия/username/списка чатов — только `/chatid` в temporary discovery mode.
