# Eleven Bot

Telegram-бот для учебной группы: отвечает на вопросы, работает с расписанием и дедлайнами, умеет искать по истории сообщений группы и использует AI через OpenAI-compatible API.

Основной способ запуска — Docker Compose. Данные хранятся в SQLite в каталоге `data/` и не пропадают после перезапуска контейнера.

## Что понадобится

- Linux-сервер, например Ubuntu 22.04/24.04 или Debian;
- Telegram Bot Token от `@BotFather`;
- numeric Telegram ID администратора;
- доступ к OpenAI-compatible API;
- Docker и Docker Compose.

## 1. Установка Docker на сервер

Подключитесь к серверу по SSH и выполните:

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

Проверьте:

```bash
docker --version
docker compose version
```

## 2. Установка Eleven Bot

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

Минимально нужно заполнить:

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

# Веб-интерфейс
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

Если у вас есть домен и reverse proxy, укажите его в `BASE_URL`, например:

```env
BASE_URL=https://schedule.example.org
```

Секреты из `.env` не коммитьте в Git.

## 3. Первый запуск

Соберите и запустите контейнер:

```bash
docker compose up -d --build
```

Проверьте состояние:

```bash
docker compose ps
curl http://127.0.0.1:6767/healthz
```

Посмотреть логи:

```bash
docker compose logs -f app
```

Если `/healthz` отвечает успешно, приложение запущено.

## 4. Получение Telegram Group Chat ID

При первом запуске оставьте:

```env
TELEGRAM_DISCOVERY_MODE=true
TELEGRAM_GROUP_CHAT_ID=
```

При этом `ADMIN_TELEGRAM_USER_ID` уже должен быть указан.

После запуска отправьте в нужной Telegram-группе:

```text
/chatid
```

Бот вернёт ID вида:

```text
-1001234567890
```

Запишите его в `.env` и сразу выключите discovery mode:

```env
TELEGRAM_GROUP_CHAT_ID=-1001234567890
TELEGRAM_DISCOVERY_MODE=false
```

Примените изменения:

```bash
docker compose up -d --force-recreate
```

После этого бот будет работать только с настроенной группой.

## 5. Проверка бота

В Telegram можно проверить, например:

```text
/today
/week
/help
/ask что завтра по парам?
```

В группе бот отвечает на команды, прямое упоминание `@username_бота` и reply на сообщение самого бота.

В личных сообщениях работать с ботом может только пользователь из `ADMIN_TELEGRAM_USER_ID`.

## Управление сервером

### Логи

```bash
docker compose logs -f app
```

Последние 100 строк:

```bash
docker compose logs --tail=100 app
```

### Перезапуск

```bash
docker compose restart app
```

### Остановка

```bash
docker compose down
```

База при этом остаётся в `./data/`.

### Запуск

```bash
docker compose up -d
```

## Обновление

Из директории проекта:

```bash
git pull
docker compose up -d --build
docker compose ps
```

После обновления полезно проверить:

```bash
curl http://127.0.0.1:6767/healthz
docker compose logs --tail=100 app
```

## Резервная копия базы

SQLite находится здесь:

```text
./data/app.db
```

Для простой безопасной резервной копии:

```bash
docker compose stop app
cp data/app.db "$HOME/eleven_bot-$(date +%F-%H%M).db"
docker compose start app
```

## Доступ к веб-интерфейсу

По умолчанию приложение слушает порт `6767`.

Если вы хотите открывать его напрямую извне, разрешите порт в firewall:

```bash
sudo ufw allow 6767/tcp
```

Тогда интерфейс будет доступен по адресу:

```text
http://SERVER_IP:6767
```

Для постоянного публичного сервера лучше использовать домен, HTTPS и reverse proxy (Caddy/Nginx), а наружу не публиковать порт `6767` напрямую.

## Разработка без Docker

Нужен Go 1.27:

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

## Структура проекта

```text
cmd/app/          точка входа
internal/         основная логика приложения
prompts/          AI-промпты
docs/             архитектура и OpenAPI
data/             SQLite-база при локальном Docker-запуске
Dockerfile
docker-compose.yml
.env.example
```

Подробности внутренней архитектуры находятся в [docs/architecture.md](docs/architecture.md), а правила для AI-агентов — в [AI_AGENTS.md](AI_AGENTS.md).
