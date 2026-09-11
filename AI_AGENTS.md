# Инструкция для AI-агентов

Этот файл обязателен для агента, который меняет код в `eleven_bot`. Он дополняет `AGENT.md`: `AGENT.md` содержит короткие доменные инварианты, этот файл описывает рабочий процесс и критичные связи.

## Цель проекта

Это бот-помощник учебной группы, а не персонаж с именем. Технический Telegram username и display name не являются identity. Русский язык, краткость и полезность важнее шутки.

## Базовые инварианты

1. В public group бот реагирует только на slash command, explicit mention или direct reply на сообщение configured bot account.
2. Direct reply на бот всегда обрабатывается. Наличие parent в SQLite обогащает контекст, но не должно быть единственной причиной activation.
3. Все успешные Telegram sends должны сохраняться в `messages` как `sender_type=bot` с reply relation, если он существует.
4. Reply chain является контекстом, а не immutable mode lock. Явный новый intent в текущем сообщении может перебить старый flow.
5. История для agent хранит user и assistant сообщения в chronological order; она bounded со старого края, но сохраняет immediate parent и current user message.
6. Structured event clarification понимает короткие ответы через root/context: `да`, `нет`, `вторую`, `#1`, `на 16:30`.
7. Явная операция `добавь`, `перенеси`, `измени`, `переименуй`, `удали`, `отмени` не должна превращаться в generic conversation.
8. Дедлайн с датой без времени является all-day event. All-day событие на текущую дату допустимо весь день.
9. Временные пересечения не блокируют сохранение. Событие сохраняется, а итоговый ответ сообщает `Пересекается с`.
10. Переименование является `update` target event. При схождении с точным existing duplicate корректно объединить записи, а не создавать новую и не выводить SQL error.
11. Никогда не показывать пользователю сырые SQLite ошибки, JSON модели, секреты, prompt или внутренние identifiers, кроме осмысленных event `#ID`.
12. Не меняйте семантику admin private mode: обычный текст authorised admin всегда проходит через AI-agent и его response schema; agent сам выбирает беседу, чтение расписания или разрешённую event operation. Не добавляйте keyword routing.

## Routing и временный ответ

- Сначала решается activation, затем intent.
- `BotUserID` получается через `GetMe`; handler должен иметь доступ к актуальному значению, не к устаревшей копии service.
- При activated group/private запросе бот может отправить `Думаю…`, затем должен отредактировать то же сообщение в результат или ошибку.
- Если edit не удался, итоговый ответ всё равно должен быть отправлен отдельным сообщением.
- Не оставляйте `Думаю…` при неизвестной команде: она должна получить короткий явный ответ.

## Работа с расписанием

- Обычный `/event`: используйте `schedule.ApplyAll`, операция атомарна.
- Admin private import: используйте `ApplyImport`, независимые хорошие факты сохраняются, плохие возвращаются в `Skipped` с короткой причиной.
- Не меняйте recurrence или dedupe без тестов: generated `UNTIL` не является semantic identity.
- User-facing summary должна сообщать recurrence человеческим языком, без `FREQ=` и без generated `UNTIL`.
- Dedupe и SQL uniqueness должны обрабатываться до отображения пользователю.

## AI и лимиты

- AI не получает SQL, shell или произвольный доступ к внутренним сервисам.
- Agent использует только зарегистрированные tools.
- `schedule_search` может быть большим. Любое добавление tool result в prompt обязано соблюдать prompt budget. Не возвращайте `AI prompt too long` на обычный вопрос о расписании.
- Prompts находятся в `prompts/*.md`, встраиваются через `go:embed` и требуют тестов при изменении поведения.

## Docker, секреты и релизы

- Не включайте `.env`, `.env.*`, SQLite data или `.git` в Docker image. Проверьте `.dockerignore` перед publication.
- Каждый publish получает новый immutable semver-like tag: `v0.0.4`, `v0.0.5` и т. д. `latest` может быть дополнительным указателем, но production deploy должен использовать конкретный tag.
- Не публикуйте секреты из `.env` в логи, docs, git или image layers.
- Контейнер работает как nonroot UID `65532`; Docker volume `/data` должен быть доступен этому UID.
- Для server deploy сначала pull versioned tag, затем recreate container, после чего проверить `/healthz`, `docker compose ps` и последние логи.
- В production `TELEGRAM_DISCOVERY_MODE=false`. Numeric `TELEGRAM_GROUP_CHAT_ID` получают только временно через `/chatid` в discovery mode, затем сервис перезапускают.

## Минимальная проверка

После изменения Go-кода выполните:

```bash
gofmt -w <изменённые-go-файлы>
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go vet ./...
node internal/web/static/app.test.mjs
git diff --check
```

Перед Docker release также выполните `docker compose build`. Для deploy добавьте verification на целевом сервере.

## Что не делать

- Не делайте архитектурный рефакторинг ради маленького bug fix.
- Не отключайте validation, conflict warnings, receipt ownership или persistence ради обхода ошибки.
- Не используйте отсутствие local parent row как причину игнорировать reply на бота.
- Не меняйте или не удаляйте пользовательские данные и чужие worktree changes без прямого запроса.
- Не пишите в документации, что ID группы можно получить из названия, username или списка чатов. Для настройки используется `/chatid` в temporary discovery mode.
