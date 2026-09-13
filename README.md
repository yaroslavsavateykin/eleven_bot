# Eleven Bot

Telegram-бот для учебной группы: отвечает на учебные вопросы, работает с расписанием и дедлайнами, ищет по истории сообщений и использует AI через OpenAI-compatible API.

Production-серверу **не нужен репозиторий и не нужен Go**. На сервере достаточно Docker Compose, файла `.env` и каталога с SQLite. Готовый образ публикуется в GHCR:

```text
ghcr.io/yaroslavsavateykin/eleven_bot:latest
```

## 1. Установить Docker

Для Ubuntu/Debian:

```bash
sudo apt update
sudo apt install -y curl
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
```

После этого перелогиньтесь по SSH или выполните:

```bash
newgrp docker
```

Проверьте:

```bash
docker --version
docker compose version
```

## 2. Создать директорию приложения

```bash
sudo mkdir -p /opt/eleven-bot/data
sudo chown -R "$USER":"$USER" /opt/eleven-bot
cd /opt/eleven-bot
```

Никакого `git clone` на сервере не требуется.

## 3. Создать docker-compose.yml

Создайте файл:

```bash
nano docker-compose.yml
```

И вставьте:

```yaml
services:
  app:
    image: ${ELEVEN_BOT_IMAGE:-ghcr.io/yaroslavsavateykin/eleven_bot:latest}
    pull_policy: always
    user: "1000:1000"

    env_file:
      - .env

    volumes:
      - ./data:/data

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
      retries: 5
      start_period: 40s
```

По умолчанию Compose использует последний опубликованный образ `:latest`.

Чтобы закрепить конкретный релиз, достаточно позже указать в `.env`, например:

```env
ELEVEN_BOT_IMAGE=ghcr.io/yaroslavsavateykin/eleven_bot:v0.1.0
```

## 4. Создать .env

Создайте:

```bash
nano .env
```

Пример:

```env
# Docker image
ELEVEN_BOT_IMAGE=ghcr.io/yaroslavsavateykin/eleven_bot:latest

# Telegram
TELEGRAM_BOT_TOKEN=
TELEGRAM_GROUP_CHAT_ID=
TELEGRAM_DISCOVERY_MODE=true
ADMIN_TELEGRAM_USER_ID=

# Группа
GROUP_NAME=411 группа
GROUP_TIMEZONE=Europe/Moscow

# Необязательная опорная учебная неделя
ACADEMIC_REFERENCE_WEEK_START=2026-09-07
ACADEMIC_REFERENCE_WEEK_PARITY=odd

# SQLite
DATABASE_PATH=/data/app.db

# Web
HTTP_ADDR=:6767
BASE_URL=http://SERVER_IP:6767
ADMIN_PASSWORD=change-this-to-a-long-random-password
EXTERNAL_API_TOKEN=change-me

# AI
AI_BASE_URL=
AI_API_KEY=
AI_TEXT_MODEL=
AI_VISION_MODEL=
AI_STT_MODEL=

# Agent
AI_TOOL_MODE=native
AI_AGENT_MODEL=
AI_STRICT_TOOLS=false
AI_DISABLE_PARALLEL_TOOLS=false
AI_CONTEXT_BYTES=131072

# Остальное
RAW_MESSAGE_RETENTION_HOURS=48
GITHUB_REPOSITORY=yaroslavsavateykin/eleven_bot
```

Минимально заполните:

- `TELEGRAM_BOT_TOKEN`;
- `ADMIN_TELEGRAM_USER_ID`;
- `AI_BASE_URL`;
- `AI_API_KEY`;
- `AI_TEXT_MODEL`;
- `ADMIN_PASSWORD`;
- `EXTERNAL_API_TOKEN`;
- `BASE_URL`.

## 5. Первый запуск

Скачайте готовый образ:

```bash
docker compose pull
```

Запустите:

```bash
docker compose up -d
```

Проверьте:

```bash
docker compose ps
curl http://127.0.0.1:6767/healthz
```

Логи:

```bash
docker compose logs -f app
```

На сервере ничего не компилируется.

## 6. Получить TELEGRAM_GROUP_CHAT_ID

Для первого запуска оставьте:

```env
TELEGRAM_DISCOVERY_MODE=true
TELEGRAM_GROUP_CHAT_ID=
```

В нужной Telegram-группе отправьте от аккаунта администратора:

```text
/chatid
```

Бот вернёт ID вида:

```text
-1001234567890
```

Запишите его в `.env`:

```env
TELEGRAM_GROUP_CHAT_ID=-1001234567890
TELEGRAM_DISCOVERY_MODE=false
```

Примените изменения:

```bash
docker compose up -d --force-recreate
```

Discovery mode после этого должен оставаться выключенным.

## Обновление

Если используется `:latest`:

```bash
cd /opt/eleven-bot
docker compose pull
docker compose up -d
```

Проверка:

```bash
docker compose ps
curl http://127.0.0.1:6767/healthz
docker compose logs --tail=100 app
```

Никакого `git pull` и никакого `docker compose build` на сервере не требуется.

## Откат

В `.env` замените:

```env
ELEVEN_BOT_IMAGE=ghcr.io/yaroslavsavateykin/eleven_bot:latest
```

на нужную версию:

```env
ELEVEN_BOT_IMAGE=ghcr.io/yaroslavsavateykin/eleven_bot:v0.1.0
```

Затем:

```bash
docker compose pull
docker compose up -d
```

## Управление

Логи:

```bash
docker compose logs -f app
```

Перезапуск:

```bash
docker compose restart app
```

Остановка:

```bash
docker compose down
```

Запуск:

```bash
docker compose up -d
```

Благодаря `restart: unless-stopped` контейнер автоматически поднимется после перезагрузки сервера вместе с Docker.

## Резервная копия SQLite

База находится здесь:

```text
/opt/eleven-bot/data/app.db
```

Простой вариант:

```bash
cd /opt/eleven-bot
docker compose stop app
cp data/app.db "$HOME/eleven_bot-$(date +%F-%H%M).db"
docker compose start app
```

## Веб-интерфейс

Приложение слушает порт `6767`.

Для временного прямого доступа:

```bash
sudo ufw allow 6767/tcp
```

После этого:

```text
http://SERVER_IP:6767
```

Для постоянного публичного сервера лучше поставить Caddy/Nginx с HTTPS и не публиковать `6767` напрямую в интернет.

## Выпуск нового релиза

В GitHub:

```text
Actions → Release → Run workflow
```

Введите версию, например:

```text
v0.1.0
```

Release Action:

1. запускает Go-тесты и `go vet`;
2. запускает тесты web-части;
3. собирает Docker image в GitHub Actions;
4. публикует image в GHCR;
5. обновляет `:latest`;
6. публикует version tags;
7. создаёт GitHub Release с release notes.

После этого на сервере достаточно:

```bash
cd /opt/eleven-bot
docker compose pull
docker compose up -d
```

Обычный CI ничего не публикует — публикация production image выполняется только через отдельный Release workflow.

## Для разработки

Исходный код нужен только на машине разработчика или в CI:

```bash
git clone https://github.com/yaroslavsavateykin/eleven_bot.git
cd eleven_bot

go test ./...
go vet ./...
go run ./cmd/app
```

Внутренняя архитектура описана в [docs/architecture.md](docs/architecture.md), правила для AI-агентов — в [AGENTS.md](AGENTS.md).
