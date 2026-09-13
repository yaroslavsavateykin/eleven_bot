# Бот-помощник учебной группы

Небольшой агент учебной группы в Telegram. Он отвечает на учебные вопросы сам, а для расписания и истории группы вызывает безопасные tools поверх SQLite.

Бот не использует имя как часть личности. Технические display name и username Telegram-аккаунта не влияют на ответы.

## Возможности

- Расписание, разовые события, пары, дедлайны и повторяющиеся занятия.
- Единый диалог для учебных вопросов, расписания, изменений и поиска по истории группы.
- Поиск по сохранённым сообщениям группы для вопросов о домашних заданиях, отчётах и объявлениях.
- Дедлайн с датой без времени сохраняется как событие на весь день.
- Пересечения не блокируют сохранение: событие добавляется, а бот сообщает о конфликте в ответе.
- `/today`, `/week`, `/ask`, `/roast`, `/all`, `/help`.
- Reply-цепочки: reply на **любое сообщение бота** является достаточным trigger для ответа в группе.
- Временный ответ `Думаю…` редактируется в итог или в ошибку.
- Веб-интерфейс расписания, `/healthz`, `/admin` и API.
- SQLite, миграции и message graph переживают перезапуск контейнера.

## Как бот реагирует в группе

В настроенной группе бот обрабатывает только:

1. Команды, адресованные этому боту: `/ask`, `/event`, `/today` и т. д.
2. Явное упоминание `@username_бота`.
3. Прямой reply пользователя на сообщение бота.

Обычные несвязанные сообщения группы игнорируются. Reply на сообщение другого человека или другого бота также игнорируется.

Reply-цепочка является контекстом, а не вечной блокировкой режима. Короткие ответы вроде `да`, `вторую` или `на 16:30` продолжают уточнение события. Новый явный запрос вроде `добавь завтра пятой парой физхимию` начинает новую операцию, даже если отправлен reply на старую ветку.

## Команды

```text
/today
/week
/ask что завтра по парам?
/event завтра в 13:00 коллоквиум по органике на 90 минут
/event перенеси #12 на завтра в 15:00
/event отмени семинар по ВМС
/roast @username
/all кто идёт за кофе?
/help
```

Примеры обычного продолжения:

```text
Пользователь: /today
Бот: Сегодня событий нет.
Пользователь reply: а завтра?
Бот: [ответ по расписанию]

Пользователь: /event перенеси органику
Бот: Какую пару перенести?
Пользователь reply: вторую
Бот: На какое время?
Пользователь reply: на 16:30
Бот: [изменение сохранено]
```

## Дедлайны и пересечения

Дедлайн без времени занимает весь день:

```text
/event добавь 10 сентября дедлайн подачи заявления
```

Дедлайн с временем является обычным timed event:

```text
/event добавь сегодня в 15:00 дедлайн подачи заявления
```

Если время пересекается с другой парой, бот всё равно сохраняет новое событие и пишет, с чем оно пересекается. Расписание может быть противоречивым, реальность иногда тоже.

## Получение ID группы

Telegram не даёт надёжно узнать numeric ID группы из её названия или username. Получайте его только через временный discovery mode и `/chatid`.

1. В `.env` временно установите:

```env
TELEGRAM_DISCOVERY_MODE=true
TELEGRAM_GROUP_CHAT_ID=
ADMIN_TELEGRAM_USER_ID=ВАШ_NUMERIC_TELEGRAM_ID
```

2. Перезапустите сервис.
3. Отправьте `/chatid` в нужной группе **с аккаунта администратора**.
4. Бот пришлёт numeric Chat ID вида `-100...`.
5. Сохраните его в `.env` и обязательно выключите discovery mode:

```env
TELEGRAM_GROUP_CHAT_ID=-1001234567890
TELEGRAM_DISCOVERY_MODE=false
```

6. Перезапустите сервис ещё раз.

После настройки `/chatid` не является рабочей пользовательской командой. Это намеренно: служебный режим не должен остаться включённым навсегда.

## Конфигурация

Создайте `.env` рядом с `compose.yml` или локальным `docker-compose.yml`. Секреты никогда не добавляйте в Git.

```env
# Telegram
TELEGRAM_BOT_TOKEN=...
TELEGRAM_GROUP_CHAT_ID=-1001234567890
TELEGRAM_DISCOVERY_MODE=false
ADMIN_TELEGRAM_USER_ID=123456789

# Группа и база
GROUP_NAME=411 группа
GROUP_TIMEZONE=Europe/Moscow
DATABASE_PATH=/data/app.db
RAW_MESSAGE_RETENTION_HOURS=48

# Веб-интерфейс. BASE_URL отправляется ботом в сообщениях.
HTTP_ADDR=:6767
BASE_URL=https://schedule.example.org

# Администрирование и API
ADMIN_PASSWORD=замените-на-длинный-пароль
EXTERNAL_API_TOKEN=длинный-случайный-токен

# OpenAI-совместимый AI endpoint
AI_BASE_URL=https://ai.example.org/v1
AI_API_KEY=...
AI_TEXT_MODEL=...
AI_VISION_MODEL=...
AI_STT_MODEL=

# Необязательно: проверка новых GitHub tags
GITHUB_REPOSITORY=yaroslavsavateykin/eleven_bot
```

Главные переменные:

| Переменная | Назначение |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | Токен, выданный BotFather. |
| `TELEGRAM_GROUP_CHAT_ID` | ID единственной разрешённой группы. |
| `ADMIN_TELEGRAM_USER_ID` | Numeric ID администратора для private chat. |
| `TELEGRAM_DISCOVERY_MODE` | Временно включает `/chatid`; после настройки должен быть `false`. |
| `BASE_URL` | Внешняя ссылка, которую бот отправляет пользователям. Не `localhost` на сервере. |
| `HTTP_ADDR` | Адрес, на котором слушает приложение. По умолчанию `:6767`. |
| `DATABASE_PATH` | Путь SQLite. В Docker используется `/data/app.db`. |
| `AI_BASE_URL`, `AI_API_KEY`, `AI_TEXT_MODEL` | Параметры OpenAI-compatible endpoint. |
| `AI_STT_MODEL` | Необязательная модель распознавания голосовых сообщений. Если пусто, бот попросит отправить текстом. |
| `AI_TOOL_MODE` | `native` (по умолчанию) — native function calling. `legacy_json` — JSON-envelope для endpoint без поддержки native tools. |
| `AI_CONTEXT_BYTES` | Приблизительный byte budget для всего запроса, включая schemas (по умолчанию 131072). |
| `AI_STRICT_TOOLS` | `true` — добавляет `strict: true` и раскрывает required-поля для OpenAI strict mode (по умолчанию `false`). |
| `AI_DISABLE_PARALLEL_TOOLS` | `true` — отключает `parallel_tool_calls` для endpoint, где это поддерживается (по умолчанию `false`). |

## Запуск через Docker Compose

Для локальной разработки:

```bash
cp .env.example .env
# заполните .env
docker compose up -d --build
curl http://127.0.0.1:6767/healthz
```

Веб-интерфейс будет на `http://localhost:6767`, если `BASE_URL` не переопределён.

Локальный compose хранит базу в `./data`. Контейнер запускается от непривилегированного пользователя; при ручном создании volume у него должны быть права на запись для UID `65532`.

## Запуск готового образа

Образы публикуются в GitHub Container Registry. Каждый release получает неизменяемый тег (`v0.0.31` и т. д.), а также мутабельный `latest`, который используется для обычного обновления сервера:

```text
ghcr.io/yaroslavsavateykin/eleven_bot:latest
```

На сервере compose закреплён `:latest`, и watchtower (`--label-enable`) автоматически подхватывает новый digest. Для ручного обновления или отката используйте конкретный тег:

```yaml
services:
  eleven-bot:
    image: ghcr.io/yaroslavsavateykin/eleven_bot:latest
    pull_policy: always
    env_file: .env
    volumes:
      - eleven_bot_data:/data
    ports:
      - "6767:6767"
    restart: unless-stopped
    init: true
    read_only: true
    security_opt:
      - no-new-privileges:true
    tmpfs:
      - /tmp:rw,noexec,nosuid,size=64m
    healthcheck:
      test: ["CMD", "/app", "-healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
    labels:
      com.centurylinklabs.watchtower.enable: "true"

volumes:
  eleven_bot_data:
```

Если package приватный, выполните `docker login ghcr.io` на сервере с GitHub PAT, имеющим `read:packages`.

`.dockerignore` исключает `.env`, `.env.*`, `data/` и `.git/` из Docker context. Это обязательное условие перед публикацией образа.

## Приватный чат администратора

Только пользователь с `ADMIN_TELEGRAM_USER_ID` может писать боту в личные сообщения. Обычный текст там проходит через AI-agent: он сам определяет, является ли это беседой, вопросом о расписании или изменением события. Локальных проверок по ключевым словам нет. Например:

```text
Добавь завтра первой парой квантовую химию.
Дедлайн заявления 10 сентября.
Перенеси семинар ВМС на 16:30.
```

Изменения применяются к общей базе сразу, но не публикуются в группу автоматически. Для публикации:

```text
/sync preview
/sync
```

## Архитектура

```text
Telegram -> Conversation context -> Agent -> AI native tool call
  -> Tool Registry -> Domain service -> tool result -> AI -> final reply -> Telegram
```

- `internal/conversation` хранит входящие и bot-сообщения, reply relation и bounded context.
- `internal/telegram` занимается авторизацией, Telegram metadata, ingestion, trigger policy и доставкой ответа. Он не понимает смысл текста.
- `internal/conversation` хранит message graph, строит bounded reply context и выполняет group FTS search.
- `internal/agent` запускает native tool-calling loop (10 rounds + финальный turn) с `schedule_query`, `group_search` в группе и дополнительно `schedule_create`, `schedule_create_batch`, `schedule_update`, `schedule_update_batch`, `schedule_cancel` в write/admin режимах.
- `internal/ai` является OpenAI-compatible transport для текста, vision и speech.
- `internal/schedule` нормализует typed recurrence, компилирует её в RRULE, проверяет целостность и применяет изменения транзакционно.
- `internal/db/migrations` содержит единственный источник миграций.
- `prompts` содержит встраиваемые промпты.

Подробнее: [`docs/architecture.md`](docs/architecture.md). Правила для AI-агентов: [`AI_AGENTS.md`](AI_AGENTS.md).

### Native AI orchestration

Endpoint `/chat/completions` должен поддерживать native function tools и `tool_choice=auto`.
`AI_TOOL_MODE=native` по умолчанию, `legacy_json` — явный opt-in для endpoint без native tools; silent fallback запрещён.
До 10 tool rounds + финальный turn, несколько tool_calls в одном turn выполняются последовательно (max 100/round).
Schemas берутся из `Tool.Schema()` (расширяется в `schema.go` для shared recurrence/limits), аргументы
проверяются strict JSON (DisallowUnknownFields, EOF, duplicate keys, RFC3339) + typed decoder.
Tool results — компактные `role=tool` с исходным provider ID и envelope `ok/data/error`.

`AI_CONTEXT_BYTES=131072` — byte budget включая schemas, не tokenizer tokens. Последние два входных
сообщения и все tool exchanges защищены от удаления; старая история удаляется первой.
При превышении защищённого контекста — явное сообщение о лимите, без silent truncation. Conversation graph
имеет собственный предварительный лимит 3000 символов.

Mutation replay scoped по Telegram chat/message + canonical arguments. Результат в `agent_mutations`
атомарно с mutation; batch хранит receipts по элементам. Новый Telegram message — новый scope.
`AI_STRICT_TOOLS`/`AI_DISABLE_PARALLEL_TOOLS` — opt-in provider capabilities вне agent loop.
HTTP 429/5xx retry (до 3), остальные 4xx — нет. `Complete` оставлен для `/roast`; legacy adapter
не смешивает tool data с user prompt (tool → assistant content).

## Проверки перед публикацией

```bash
gofmt -w cmd internal
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go vet ./...
node internal/web/static/app.test.mjs
docker compose build
```

Если на хосте установлен Go 1.27, Docker-команды можно заменить на обычные `go test ./...` и `go vet ./...`.
