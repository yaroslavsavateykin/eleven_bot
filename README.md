# Бот-помощник учебной группы

Telegram-бот для расписания учебной группы. Хранит расписание в SQLite, отвечает на вопросы, принимает изменения через `/event` и продолжает разговор по reply-цепочкам.

Бот не использует имя как часть личности. Технические display name и username Telegram-аккаунта не влияют на ответы.

## Возможности

- Расписание, разовые события, пары, дедлайны и повторяющиеся занятия.
- Изменение расписания через `/event` и естественный язык в приватном чате администратора.
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

# Учебная неделя, если используется чётность
ACADEMIC_REFERENCE_WEEK_START=2026-09-07
ACADEMIC_REFERENCE_WEEK_PARITY=odd

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

Образы публикуются в GitHub Container Registry с неизменяемыми тегами:

```text
ghcr.io/yaroslavsavateykin/eleven_bot:v0.0.3
```

Используйте конкретный тег в production compose, а не плавающий `latest`:

```yaml
services:
  eleven-bot:
    image: ghcr.io/yaroslavsavateykin/eleven_bot:v0.0.3
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
Telegram -> conversation graph -> router -> agent/tools or event parser -> schedule -> SQLite
```

- `internal/conversation` хранит входящие и bot-сообщения, reply relation и bounded context.
- `internal/telegram` определяет trigger, отправляет `Думаю…`, редактирует его в итог и сохраняет ответы.
- `internal/agent` запускает ограниченный tool-calling agent.
- `internal/ai` связывается с OpenAI-compatible endpoint и валидирует JSON операций.
- `internal/schedule` нормализует recurrence, проверяет целостность и применяет изменения транзакционно.
- `internal/db/migrations` содержит единственный источник миграций.
- `prompts` содержит встраиваемые промпты.

Подробнее: [`docs/architecture.md`](docs/architecture.md). Правила для AI-агентов: [`AI_AGENTS.md`](AI_AGENTS.md).

## Проверки перед публикацией

```bash
gofmt -w cmd internal
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go vet ./...
node internal/web/static/app.test.mjs
docker compose build
```

Если на хосте установлен Go 1.27, Docker-команды можно заменить на обычные `go test ./...` и `go vet ./...`.
