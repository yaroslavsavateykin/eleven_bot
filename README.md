# Eleven Bot

Telegram-бот для учебной группы: отвечает на учебные вопросы, работает с расписанием и дедлайнами, ищет по истории сообщений и использует AI через OpenAI-compatible API.

На сервере приложение запускается из готового Docker-образа:

```text
ghcr.io/yaroslavsavateykin/eleven_bot:latest
```

Собирать проект на сервере не нужно. Docker Compose только скачивает опубликованный образ и запускает его.

## Что понадобится

- Linux-сервер, например Ubuntu 22.04/24.04 или Debian;
- Telegram Bot Token от `@BotFather`;
- numeric Telegram ID администратора;
- доступ к OpenAI-compatible API;
- Docker и Docker Compose.

## 1. Установка Docker

Подключитесь к серверу по SSH:

```bash
sudo apt update
sudo apt install -y git curl
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
```

После этого перелогиньтесь по SSH или выполните:

```bash
newgrp docker
```

Проверка:

```bash
docker --version
docker compose version
```

## 2. Установка Eleven Bot

Клонируйте репозиторий. На сервере нужны в основном `docker-compose.yml`, `.env` и каталог с базой.

```bash
git clone https://github.com/yaroslavsavateykin/eleven_bot.git
cd eleven_bot

cp .env.example .env

mkdir -p data
sudo chown -R 1000:1000 data
```

Откройте конфигурацию:

```bash
nano .env
```

В начале файла уже указан готовый Docker-образ:

```env
ELEVEN_BOT_IMAGE=ghcr.io/yaroslavsavateykin/eleven_bot:latest
```

Обычно оставляйте `:latest`. Если нужен фиксированный релиз, можно закрепить конкретную версию:

```env
ELEVEN_BOT_IMAGE=ghcr.io/yaroslavsavateykin/eleven_bot:v0.1.0
```

Основные настройки:

```env
# Telegram
TELEGRAM_BOT_TOKEN=...
TELEGRAM_GROUP_CHAT_ID=
TELEGRAM_DISCOVERY_MODE=true
ADMIN_TELEGRAM_USER_ID=123456789

# Группа
GROUP_NAME=411 группа
GROUP_TIMEZONE=Europe/Moscow

# База
DATABASE_PATH=/data/app.db

# Веб
HTTP_ADDR=:6767
BASE_URL=http://SERVER_IP:6767
ADMIN_PASSWORD=очень-длинный-пароль
EXTERNAL_API_TOKEN=ещё-один-длинный-случайный-токен

# AI
AI_BASE_URL=https://your-ai-endpoint.example/v1
AI_API_KEY=...
AI_TEXT_MODEL=...
AI_VISION_MODEL=...
AI_STT_MODEL=

# Обычно менять не нужно
AI_TOOL_MODE=native
AI_AGENT_MODEL=
AI_STRICT_TOOLS=false
AI_DISABLE_PARALLEL_TOOLS=false
AI_CONTEXT_BYTES=131072
RAW_MESSAGE_RETENTION_HOURS=48
GITHUB_REPOSITORY=yaroslavsavateykin/eleven_bot
```

Секреты из `.env` не добавляйте в Git.

## 3. Первый запуск

Сначала скачайте готовый образ:

```bash
docker compose pull
```

Затем запустите:

```bash
docker compose up -d
```

Проверка:

```bash
docker compose ps
curl http://127.0.0.1:6767/healthz
```

Логи:

```bash
docker compose logs -f app
```

На сервере ничего не компилируется: `docker compose pull` получает уже собранный образ из GHCR.

## 4. Получение Telegram Group Chat ID

Для первого запуска оставьте:

```env
TELEGRAM_DISCOVERY_MODE=true
TELEGRAM_GROUP_CHAT_ID=
```

При этом `ADMIN_TELEGRAM_USER_ID` уже должен быть заполнен.

После запуска отправьте в нужной Telegram-группе:

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

Пересоздайте контейнер:

```bash
docker compose up -d --force-recreate
```

Discovery mode после настройки лучше всегда держать выключенным.

## 5. Проверка Telegram-бота

Например:

```text
/today
/week
/help
/ask что завтра по парам?
```

В группе бот реагирует на команды, прямое упоминание и reply на сообщение самого бота.

В личных сообщениях доступ разрешён только пользователю из `ADMIN_TELEGRAM_USER_ID`.

## Обновление сервера

Если используется `:latest`, обновление выглядит так:

```bash
cd eleven_bot
git pull
docker compose pull
docker compose up -d
```

Проверка после обновления:

```bash
docker compose ps
curl http://127.0.0.1:6767/healthz
docker compose logs --tail=100 app
```

Никакого `docker compose build` на сервере не требуется.

## Откат на конкретный релиз

Откройте `.env`:

```bash
nano .env
```

И вместо `:latest` укажите нужный release tag:

```env
ELEVEN_BOT_IMAGE=ghcr.io/yaroslavsavateykin/eleven_bot:v0.1.0
```

Затем:

```bash
docker compose pull
docker compose up -d
```

Чтобы вернуться на последнюю версию:

```env
ELEVEN_BOT_IMAGE=ghcr.io/yaroslavsavateykin/eleven_bot:latest
```

## Управление контейнером

Логи:

```bash
docker compose logs -f app
```

Последние 100 строк:

```bash
docker compose logs --tail=100 app
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

Контейнер настроен с `restart: unless-stopped`, поэтому после перезагрузки сервера он запускается автоматически вместе с Docker.

## Резервная копия базы

SQLite хранится в:

```text
./data/app.db
```

Простой вариант резервной копии:

```bash
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

Для постоянного публичного сервера лучше использовать домен, HTTPS и reverse proxy, например Caddy или Nginx, а порт `6767` наружу не публиковать.

## Как выпустить новый релиз

В репозитории есть отдельный GitHub Action:

```text
Actions → Release → Run workflow
```

Он запускается вручную и принимает номер версии, например:

```text
v0.1.0
```

Release workflow:

1. проверяет формат версии;
2. запускает `go test ./...`;
3. запускает `go vet ./...`;
4. запускает тесты веб-части;
5. собирает Docker-образ в GitHub Actions;
6. публикует его в GHCR;
7. создаёт теги образа `v0.1.0`, `0.1.0`, `0.1`, `0`, `latest` и `sha-...`;
8. создаёт GitHub Release с автоматически сгенерированными release notes.

Для публикации образа workflow использует repository secret:

```text
GHCR_TOKEN
```

После успешного release на сервере достаточно:

```bash
docker compose pull
docker compose up -d
```

## CI и Release

Обычный CI находится в:

```text
.github/workflows/ci.yml
```

Он запускает тесты на push и pull request, но ничего не публикует.

Ручная публикация релиза находится в:

```text
.github/workflows/release.yml
```

Таким образом обычный push в `main` не создаёт новый Docker-образ автоматически.

## Разработка локально

Для разработки без Docker нужен Go 1.27:

```bash
go test ./...
go vet ./...
go run ./cmd/app
```

Также доступны:

```bash
make test
make vet
make run
```

При необходимости локально собрать Docker-образ можно обычным `docker build`, но для production-сервера это не требуется.

## Структура проекта

```text
cmd/app/                   точка входа
internal/                  основная логика
prompts/                   AI-промпты
docs/                      архитектура и OpenAPI
data/                      SQLite
.github/workflows/ci.yml   обычные проверки
.github/workflows/release.yml
Dockerfile                 сборка production image
docker-compose.yml         запуск готового image
.env.example
```

Подробности архитектуры находятся в [docs/architecture.md](docs/architecture.md), правила для AI-агентов — в [AI_AGENTS.md](AI_AGENTS.md).
