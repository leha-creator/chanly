# Maxim — бэкенд биржи рекламы в каналах MAX

> Рекламодатель выбирает каналы и платит, блогер публикует пост, платформа маркирует рекламу, контролирует размещение и рассчитывается.

В репозитории только бэкенд: Go, PostgreSQL, S3-совместимое хранилище. Один исполняемый файл с режимами `api`, `worker`, `migrate`. Кабинеты на React живут отдельно. Что строим и почему — [проектная документация](docs/README.md), этапы — [роадмап](.ai-factory/ROADMAP.md), устройство кода — [ARCHITECTURE.md](.ai-factory/ARCHITECTURE.md).

Готово:
- **Каркас и аккаунты.** Вход по коду из письма, сессии, организации и реквизиты, документ о режиме налогообложения и оферты (S-01, S-10).
- **Планировщик и интеграционный слой.** Задания с повторами, расписанием и правилами после простоя; учёт входящих событий и исходящих запросов; адаптер MAX с общим ограничителем; оповещения дежурным.
- **Подключение каналов.** Приём Webhook MAX с проверкой подлинности, подключение канала кодом из кабинета, проверка прав бота, реакция на их отзыв и удаление бота (S-12, S-25).
- **Каталог.** Цены по форматам с комиссией, интервалы недоступности, модерация каналов и проверка номера РКН администратором, суточные метрики и флаги аномалий, каталог рекламодателя с фильтрами, сортировками и курсором (S-02, S-13, S-16, S-32).
- **Медиатека и пост.** Загрузка изображений с проверкой по содержимому, превью и копии с плашкой «Реклама. название, ИНН», кампании, черновик поста в форматировании MAX с распознаванием ссылок, предпросмотр с проверками, версии поста для следующих этапов (S-04, S-05).

- **Заказ, автопостинг, маркировка, деньги, выплаты, приёмка и споры, уведомления, API администратора и отчётность ОРД** — этапы [роадмапа](.ai-factory/ROADMAP.md) до пилота.

Все операции [спецификации](api/openapi.yaml) реализованы. Тестовые контуры внешних систем (MAX, Яндекс ОРД, Рокет Ворк) в разработке заменены подделками.

## Быстрый старт

Нужен Docker.

```bash
docker compose -f compose.dev.yml up --build
```

| Адрес | Что |
|---|---|
| <http://localhost:8080/docs> | Swagger UI API (не в продакшене) |
| <http://localhost:8025> | Mailpit: письма с кодами входа и оповещения дежурным (`oncall@maxim.local`) |
| <http://localhost:9001> | Консоль MinIO (`minio` / `minio-secret`). Корзины: `maxim-private` и `maxim-public` (анонимное чтение без перечисления) |
| <http://localhost:9090/metrics>, `:9091/metrics` | Метрики `api` и `worker` |

Если порт занят, его меняет переменная `DEV_*_PORT`, например `DEV_API_PORT=58080 DEV_MAILPIT_PORT=58025`. Полный список — в [compose.dev.yml](compose.dev.yml).

Чтобы кабинеты сразу были не пустыми, наполните базу тестовыми данными — раздел «Тестовые данные в разработке».

Вход: `POST /api/v1/auth/code` с почтой, код из письма в Mailpit, затем `POST /api/v1/auth/login`. Меняющие запросы повторяют cookie `csrf_token` в заголовке `X-CSRF-Token` (Swagger UI делает это сам).

## Команды

| Команда | Что делает |
|---|---|
| `maxim api` | HTTP API кабинетов (`HTTP_ADDR`) и метрики (`METRICS_ADDR`) |
| `maxim worker` | Обработчик заданий из таблицы `job` и метрики (`METRICS_ADDR`) |
| `maxim migrate up\|down\|status` | Миграции. `DATABASE_URL` — владелец схемы; права выдаются роли `DB_APP_ROLE` |
| `maxim legal publish --kind <kind> --file <pdf> [--published-at <RFC3339>]` | Новая версия оферты или правил (в API администратора этой операции нет) |
| `maxim legal list` | Действующие версии документов |
| `maxim admin grant --email <почта> --role moderator\|senior` | Роль администратора (в API администратора этой операции нет). Пользователя без учётной записи создаёт; повтор ничего не меняет |
| `maxim admin revoke --email <почта>` | Снять роль администратора |
| `maxim admin ord errors [--limit N]` | Объекты ОРД в `error` (S-20; в API — `listOrdErrors`): вид и идентификатор, статус ОРД, попытки, ошибка с маскированным ИНН |
| `maxim admin ord retry --object <вид>:<id> --reason <основание> --email <администратор>` | «Повторить» объект ОРД; вид — `organization`, `contract`, `platform`, `creative`, `statistics`, `invoice`. Пишется в журнал действий |
| `maxim admin ord report show|build|send --period YYYY-MM [--reason … --email <senior>]` | Ежемесячный отчёт ОРД (S-31): состояние, строки статистики и акты; `build` — сформировать заново, `send` — поставить в очередь отправки (только `senior`, с основанием) |
| `maxim admin pd export --email <субъект> --operator-email <senior>` | Данные субъекта ПДн в JSON для ответа на запрос доступа ([docs/launch/personal-data.md](docs/launch/personal-data.md)) |
| `maxim admin pd anonymize --email <субъект> --reason … --operator-email <senior>` | Обезличить субъекта: почта, сессии, коды входа, контакты организации единственного участника; данные учёта сохраняются |
| `maxim admin money statement import --file <путь> --email <администратор>` | Загрузить выписку «1С» (S-30): поступления сопоставляются со счетами и зачисляются, остальные — в `unmatched` с оповещением. Повтор того же файла — отказ |
| `maxim admin money bank-tx list\|match\|ignore …` | Поступления по статусу (`--status unmatched`); ручное сопоставление `--id <id> --org <id\|ИНН> [--invoice MX-…] --reason …`; пометка «не наше» |
| `maxim admin money invoice cancel --number MX-… --reason … --email …` | Отменить неоплаченный счёт |
| `maxim admin money ledger manual\|list …` | Ручная проводка `--debit <вид[:разрез]> --credit <вид[:разрез]> --amount <руб.коп>` (выше `LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD` — с `--approver-email` второго senior); журнал денег с фильтрами `--org`, `--kind`, `--since` |
| `maxim admin money balance-return --org <id\|ИНН> --amount <руб.коп> --reason … --email … [--key …]` | Возврат остатка рекламодателю (не больше доступного) |
| `maxim admin money reconcile` | Сверка последней выписки с журналом: конечный остаток, счёт `bank`, разница за вычетом несопоставленных |
| `maxim admin money closing-acts --period YYYY-MM --email …` | Закрывающие документы за месяц вручную; уже созданные пропускаются |
| `maxim admin money deposit-topup --amount <руб.коп> --reason … --email … [--approver-email …] [--key …]` | Отразить перевод на депозит Рокет Ворк (проводка `deposit_topup`); выше `LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD` — со вторым senior |
| `maxim admin payout list [--status] [--org <id\|ИНН>] [--from] [--to] [--limit]` | Реестр выплат: попытка, способ, сумма, состояние, код отказа |
| `maxim admin payout retry --payout <id> --reason … --email …` | Новая попытка последней выплаты `failed` размещения в `accepted` (S-19 `retry_payout`) |
| `maxim admin payout mark-paid --payout <id> --bank-ref <п/п> --reason … --email … [--approver-email …]` | Выплата блогеру-юрлицу переводом выполнена: `succeeded`, проводка из `bank`, размещение `paid` (A-36) |
| `maxim admin payout contractor refresh --org <id\| `maxim admin dispute list [--all] [--overdue] [--limit N]` | Споры: срок T-06, размещение, причина, кто открыл, предложенная доля, решение |
| `maxim admin dispute show --placement <id>` | Спор целиком: претензия с текстом, нарушения, публикация, история, предпросмотр сумм |
| `maxim admin dispute resolve --placement <id> --resolution full_payout|proportional|full_refund [--share 1…9999] --comment … --email …` | Решение спора (P-15, S-18): распределение, выплата или возврат, рейтинг канала, уведомления. Администратор `moderator` или `senior` |
| `maxim admin dispute open --placement <id> --reason … --email …` | Открыть спор администратором (`admin_open_dispute`, P-14) |
| `maxim admin dispute remove-post --placement <id> --reason … --email …` | Удалить пост сейчас (`admin_remove_post`); из `published` сначала открывает спор |
|ИНН>` | Внеочередной опрос исполнителя Рокет Ворк |
| `maxim dev balance deposit --email <пользователь> --amount <руб.коп>` | Пополнить баланс организации пользователя: счёт и выписка с оплатой по нему, тем же путём, что у администратора. Только `APP_ENV` `development` и `test` |
| `maxim dev statement --invoice MX-… [--amount <руб.коп>] [--out <путь>]` | Файл выписки с оплатой счёта для `admin money statement import` (без `--out` — в вывод команды, где ещё и журнал). Только `development` и `test` |
| `maxim dev rocketwork activate\|block --org <id\|ИНН> [--reason tax_status_lost] [--webhook <url>]` | Подделка Рокет Ворк: исполнитель прошёл онбординг или заблокирован. Только `ROCKETWORK_MODE=fake` |
| `maxim dev rocketwork deposit --amount <руб.коп>` | Остаток депозита в подделке |
| `maxim dev rocketwork complete\|fail --payout <id> [--fee <руб.коп>] [--reason <код>] [--webhook <url>]` | Подделка завершила или отклонила выплату; с `--webhook` на `api` уходит подписанный вебхук, без него изменение применяется сразу |
| `maxim dev placement accept --placement <id> [--share <б.п.>]` | Приёмка размещения в `removed` с долей `--share` тем же путём, что P-12 (распределение удержания и выплата) — служебная. Только `development` и `test` |
| `maxim dev seed [--wait 5m] [--orders=true]` | Тестовые данные: пользователи, организации, каналы каталога, кампании и заказы в разных состояниях (раздел «Тестовые данные в разработке»). Нужен запущенный `worker`. Только `APP_ENV` `development` и `test` |
| `maxim dev post-version --order <номер> approve\|ready\|reject [--erid X] [--reason Y]` | Двинуть текущую версию поста заказа, как это сделает модерация (в API — `approveOrderPost` и `rejectOrderPost`). После `approve` erid выдаёт маркировка в `worker`; `ready` в обход маркировки — только при `ORD_ENABLED=false`. Только `APP_ENV` `development` и `test` |

Виды документов: `advertiser_offer`, `blogger_offer`, `platform_rules`, `privacy_policy`. Публикация в локальном окружении:

```bash
docker compose -f compose.dev.yml run --rm --no-deps -v "$PWD/offer.pdf:/tmp/offer.pdf:ro" \
  api legal publish --kind advertiser_offer --file /tmp/offer.pdf
```

В Git Bash на Windows перед командой нужен `MSYS_NO_PATHCONV=1`, иначе путь `/tmp/...` будет переписан.

## Фоновые задания и оповещения

`worker` берёт задания из таблицы `job` (07 §2). При запуске он сравнивает свою отметку с отметками других экземпляров: после простоя дольше `DOWNTIME_THRESHOLD` пишет ERROR и оповещает дежурных. Служебные виды заданий:

| Вид | Когда | Что делает |
|---|---|---|
| `process_inbound_event` | Сразу | Разбор входящего события MAX или Рокет Ворк по виду; неизвестные — `ignored` |
| `send_alert` | Сразу | Письмо на `ALERT_EMAILS` и сообщение в чат дежурных `ALERT_MAX_CHAT_ID` |
| `check_stuck_objects` | Каждый час | Поиск зависших объектов (07 §3), оповещение один раз на объект |
| `cleanup` | 04:00 МСК | Удаление устаревших записей; сроки — `RETENTION_*` |
| `refresh_channel_info` | Сразу | Сведения о канале и права бота из MAX после `bot_added` и смены прав |
| `delete_max_message` | Сразу | Удаление сообщения с кодом подключения из канала |
| `leave_max_chat` | Сразу | Бот покидает чат, который не канал, или канал без кода |
| `reply_bot_dialog` | Через 5 с | Ответ инструкцией на сообщение боту в личном диалоге |
| `release_unclaimed_channels` | 05:00 МСК | Каналы без кода дольше `CHANNEL_UNCLAIMED_TTL` — `disconnected`, бот уходит |
| `schedule_channel_metrics` | 00:05 МСК | Ставит `refresh_channel_metrics` каналам в `moderation`, `active`, `suspended`, распределяя запуски по суткам |
| `refresh_channel_metrics` | В течение суток | Подписчики и сообщения из MAX, суточный снимок метрик, флаги аномалий; 10 000 подписчиков без подтверждённого номера РКН — `suspended` с причиной `rkn_required` |
| `build_media_derivative` | Сразу | Превью при загрузке, копия или копия с плашкой при сохранении черновика; не больше `MEDIA_BUILD_CONCURRENCY` построений одновременно |
| `publish_media_derivative` | Сразу | Копия производной из версии поста в публичной корзине и её постоянная ссылка |
| `purge_media` | 04:30 МСК | Удаляет из хранилища удалённые оригиналы и производные вне версий старше `MEDIA_RETENTION_DELETED` |
| `placement_timer` | По сроку | Таймеры размещения: T-01, T-13, T-14 — истечение; T-05 — автоприёмка (P-13); T-06 — оповещение о нерешённом споре |
| `publish_placement` | `publish_at` | Публикация поста в канал (S-21): повторы каждые 30 с до `PUBLISH_RETRY_WINDOW`, затем `failed`; взятое после простоя позже этого окна — `failed` без вызова MAX |
| `remove_placement` | `remove_at` | Итоговые просмотры и удаление поста (S-22; при полном возврате и `admin_remove_post` — сразу); ошибка — `removal_overdue` и повторы до `REMOVE_RETRY_WINDOW` |
| `reconcile_publications` | Каждые `RECONCILE_INTERVAL` | Ставит `check_publication` на каждую живую публикацию; пересоздаёт пропавшее задание публикации |
| `check_publication` | Внутри интервала сверки | Пост на месте, не изменён, «часы в топе» не нарушены; просмотры — не чаще `PUBLICATION_VIEWS_REFRESH` |

Оповещения по одной причине уходят не чаще раза в час. Причины: задание исчерпало попытки, сработал предохранитель внешней системы, внешний вызов окончательно не удался, простой, зависший объект.

**MAX.** Без `MAX_BOT_TOKEN` в разработке и всегда в `APP_ENV=test` работает поддельный адаптер. С токеном запросы идут в MAX через общий ограничитель: 25 запросов в секунду на все экземпляры и 1 сообщение в секунду в чат. Каждая попытка пишется в `outbound_request` без токена. В продакшене `worker` при запуске проверяет бота (`GET /me`) и не стартует без связи. Затем подписывает бота на Webhook `MAX_WEBHOOK_URL` + `/` + `MAX_WEBHOOK_PATH_SECRET`, если такой подписки ещё нет или в ней не хватает видов событий (в том числе `message_edited` и `message_removed`); неудача — ERROR и оповещение, `worker` продолжает работу. Нужен корневой сертификат Минцифры: [deploy/certs/README.md](deploy/certs/README.md).

**Webhook MAX.** `POST /api/v1/webhooks/max/{MAX_WEBHOOK_PATH_SECRET}` с заголовком `X-Max-Bot-Api-Secret: {MAX_WEBHOOK_SECRET}`. Неверный секрет — `401`, повторные отказы — оповещение. Событие сохраняется в `inbound_event` и разбирается заданием; то же тело второй раз не обрабатывается. Секретная часть пути в журнал и метрики не попадает.

**Метрики `worker`.** `maxim_jobs*` (очередь, просроченные, задержка `maxim_jobs_lag_seconds`), `maxim_placements{status}`, задержка публикации и снятия `maxim_placement_publish_delay_seconds` и `maxim_placement_remove_delay_seconds`, нарушения `maxim_placement_violations_total{kind,source}`, приёмки `maxim_placements_accepted_total{transition}`, споры `maxim_disputes_opened_total{opened_by,reason}` и `maxim_disputes_resolved_total{resolution}` (у `api` — приёмка и претензии рекламодателя), `maxim_outbound_requests_total` и `maxim_outbound_request_duration_seconds`, `maxim_outbound_circuit_open`, `maxim_ratelimit_wait_seconds`, `maxim_inbound_event_oldest_unprocessed_seconds`. У `api` — ещё `maxim_max_webhook_requests_total{outcome}`.

## Тестовые данные в разработке

Одна команда наполняет базу так, что кабинеты рекламодателя, блогера и администратора сразу показывают данные. Нужны запущенные `api` и `worker` из `compose.dev.yml`:

```bash
docker compose -f compose.dev.yml run --rm --no-deps api dev seed
```

Команда работает через сервисы приложения, тем же путём, что API. Шаги, которым нужны MAX и ОРД (регистрация площадок, erid, публикация), выполняет `worker`, а команда ждёт результата в базе: на чистой базе это несколько минут. Если `worker` не довёл шаг за `--wait` (по умолчанию 5 мин на шаг), команда останавливается с подсказкой запустить `worker`. В конце печатается сводка: учётки, что создано и что пропущено.

Вход — по коду из письма в Mailpit:

| Почта | Кто |
|---|---|
| `adv@example.test` | Рекламодатель, ООО, баланс 500 000 ₽ (счёт и выписка) |
| `blg@example.test` | Блогер-самозанятый, исполнитель Рокет Ворк активен (подделка) |
| `blg-ul@example.test` | Блогер-юрлицо, выплаты переводом (`bank_transfer`) |
| `admin@example.test` | Администратор `senior` |
| `moderator@example.test` | Модератор |

Что создаётся:
- по действующей версии каждого юридического документа, если их нет (сгенерированный PDF), и принятые документы всех организаций;
- 8 каналов в каталоге (три формата с ценами, площадки зарегистрированы в ОРД) и 1 канал в очереди модерации;
- кампании «Демо: черновик» (текст и изображение) и «Демо: пустая»;
- по заказу на каждое состояние размещения, кампания «Демо: <состояние>»: `moderation`, `rejected`, `cancelled`, `awaiting_blogger`, `awaiting_schedule`, `scheduled`, `published`, `disputed`, `removed`, `accepted`, `paid`, `bank_transfer` (принято, выплата ждёт `admin payout mark-paid`).

Повторный запуск ничего не дублирует. Пользователи находятся по почте, каналы — по `max_chat_id` из диапазона `-7700000001 … -7700000009`, заказы — по названию кампании; сценарий с уже созданной кампанией пропускается (`exists`). Баланс пополняется, только если свободный остаток ниже 100 000 ₽. `--orders=false` — без заказов, быстро и без ожидания публикаций. Если подделка Рокет Ворк не общая с `worker` (`ROCKETWORK_MODE` не `fake` или пустой `ROCKETWORK_FAKE_STATE_PATH`), сценарии, которым нужен активный исполнитель, пропускаются (`rocketwork_not_fake`).

Ограничения:
- Часы настоящие: `scheduled` публикуется примерно через час, `published` снимается по сроку формата, неотвеченные заявки истекают через сутки (T-01, T-13), `removed` принимается сам после окна претензий.
- Поддельный MAX `worker` не знает засеянных каналов, поэтому суточное обновление метрик по ним завершается ошибкой и оповещением в Mailpit — как в ручном пути ниже.
- Посты и удаления поддельного MAX живут в памяти `worker`: после его перезапуска контроль размещений `published` не найдёт пост.
- Сброс данных — `docker compose -f compose.dev.yml down -v` (удаляет тома базы, хранилища и подделки Рокет Ворк).

## Подключение канала в разработке

Без `MAX_BOT_TOKEN` события MAX отправляются на приём вручную; образцы тел — в [internal/integration/max/testdata/updates](internal/integration/max/testdata/updates). Секреты — из [compose.dev.yml](compose.dev.yml).

1. Блогер входит, создаёт организацию с кабинетом `blogger` и получает код: `POST /api/v1/blogger/channels/link-code`.
2. Бот добавлен в канал:

```bash
curl -X POST "http://localhost:8080/api/v1/webhooks/max/dev-only-webhook-path-secret-0123456789" \
  -H "Content-Type: application/json" -H "X-Max-Bot-Api-Secret: dev-only-webhook-header-secret-012345678" \
  --data @internal/integration/max/testdata/updates/bot_added_channel.json
```

3. Код в канале: тот же запрос с телом `message_created_channel.json`, где `K7M2Q9XA` заменён на полученный код. Канал появляется в `GET /api/v1/blogger/channels`.

Ограничение: поддельный адаптер не знает сведений о канале и прав бота, а через API они не задаются. Поэтому в разработке `refresh_channel_info` завершается ошибкой (после попыток — оповещение в Mailpit), канал остаётся без названия и после кода получает `pending_rights` с `missing_permissions: [write, delete]`. Путь до `moderation` и реакция на отзыв прав проверяются тестами `internal/channel/app` и `internal/bootstrap/channel_e2e_test.go` или на тестовом боте с `MAX_BOT_TOKEN`. Повторная отправка того же тела — дубликат, его не разберут: меняйте `timestamp`.

## Канал до каталога в разработке

Продолжение предыдущего раздела. Права бота поддельный адаптер не даёт, поэтому шаг 1 заменяет их запись в базу; остальное — через API.

1. Канал с правами и публичностью — на модерацию:

```bash
docker compose -f compose.dev.yml exec postgres psql -U maxim -d maxim -c \
  "UPDATE channel SET is_public = true, bot_is_admin = true, bot_permissions = '{write,delete}',
   status = 'moderation', status_reason = NULL WHERE status = 'pending_rights'"
```

2. Блогер: категория — `PATCH /api/v1/blogger/channels/{id}` с `category_id` из `GET /api/v1/channel-categories`; цены — `PUT /api/v1/blogger/channels/{id}/prices` (форматы — `GET /api/v1/placement-formats`); интервал недоступности — `POST .../unavailability` (необязательно).
3. Роль администратора, затем вход этой почтой:

```bash
docker compose -f compose.dev.yml run --rm --no-deps api admin grant --email admin@maxim.local --role moderator
```

4. Администратор: очередь — `GET /api/v1/admin/channels?status=moderation`, одобрение — `POST /api/v1/admin/channels/{id}/approve` (`409` с `details.missing` назовёт, чего не хватает).
5. Канал появится в каталоге через несколько секунд: `worker` регистрирует в ОРД (по умолчанию — поддельном) контрагента блогера, договор с ним и площадку канала; без площадки канала нет ни в каталоге, ни в корзине. При `ORD_ENABLED=false` канал виден сразу.
6. Рекламодатель (организация с кабинетом `advertiser`): `GET /api/v1/catalog/channels`, карточка — `GET /api/v1/catalog/channels/{id}`. Цена в каталоге — с комиссией `PLATFORM_FEE_RATE_BP`.

Суточные метрики с поддельным адаптером не считаются (он не знает канал). Весь путь, включая метрики и правило РКН, проверяет `internal/bootstrap/catalog_e2e_test.go`.

## Пост с плашкой в разработке

Нужна организация с кабинетом `advertiser` (`POST /api/v1/organizations`, название и ИНН попадут на плашку). Всё — через Swagger UI или `curl` с cookie сессии и заголовком `X-CSRF-Token`.

1. Изображение: `POST /api/v1/media`, `multipart/form-data` с частью `file` (JPEG, PNG или WebP; сторона от 160 до 7680, до 50 МБ):

```bash
curl -b cookies.txt -H "X-CSRF-Token: $CSRF" -F "file=@photo.jpg" http://localhost:8080/api/v1/media
```

2. Кампания: `POST /api/v1/campaigns` с `{"name": "Осень"}` — вместе с ней создаётся пустой пост.
3. Пост: `PUT /api/v1/campaigns/{id}/post` с `content_doc` (документ редактора, пример — в Swagger UI) и `media_file_ids`. Адрес `https://…` в тексте станет ссылкой. По умолчанию пометка — на плашке, erid — под постом.
4. Предпросмотр: `POST /api/v1/campaigns/{id}/post/preview`. Пока `worker` строит изображения, в `problems` будет `media_processing` — повторите через пару секунд. Ссылки `media_urls` открываются в браузере: это изображения с плашкой из MinIO, подписанные для `localhost:9000` (`S3_PRESIGN_ENDPOINT`).

Создание версии поста, публикация производных в `maxim-public` и переходы версии — сервис для этапов заказа, модерации и маркировки; их API появится там. Весь путь проверяет `internal/bootstrap/post_e2e_test.go`.

## Заказ в разработке

Продолжение двух предыдущих разделов: нужен канал в каталоге и кампания с постом. Даты кампании (`date_from`, `date_to`, `time_from`, `time_to`) обязательны к оплате; период — не раньше сегодняшнего дня по Москве.

1. Рекламодатель, корзина (S-03): `POST /api/v1/cart/items` с `channel_id` и `format_id`; `GET /api/v1/cart`. Изменившуюся цену подтверждает `PATCH /api/v1/cart/items/{id}` с тем же форматом.
2. Оплата (S-06): `POST /api/v1/campaigns/{id}/checkout` (необязательный заголовок `Idempotency-Key`). Отказ — `409` с кодом `campaign_incomplete`, `cart_changed` или `insufficient_balance`. Заказ — в `moderation`, размещения — в `created`, номер первого заказа — `1001`.
3. Модерация — командой разработчика, erid — маркировка в `worker` (раздел «Маркировка в разработке»):

```bash
docker compose -f compose.dev.yml run --rm --no-deps api dev post-version --order 1001 approve
```

Заказ — в `marking`; через несколько секунд версия получает erid, размещения уходят блогерам (`awaiting_blogger`) или отменяются, если канал не `active` или окно почти закончилось. `reject --reason "…"` — отклонение (удержания снимаются, заказ `rejected`). `ready [--erid X]` — сразу к erid в обход ОРД, только при `ORD_ENABLED=false`.

4. Блогер (S-14): `GET /api/v1/blogger/placements`, затем `POST .../{id}/accept`, `POST .../{id}/schedule` с `{"mode": "at", "publish_at": "…"}` или `{"mode": "now"}`, `.../decline`, `.../reschedule`.
5. Рекламодатель: `GET /api/v1/orders/{id}`, отмена размещения — `POST /api/v1/placements/{id}/cancel`, заказа — `POST /api/v1/orders/{id}/cancel`, новая версия поста — `POST /api/v1/orders/{id}/post-versions`.

Оплата удерживает сумму на балансе рекламодателя: перед `checkout` пополните баланс (раздел «Деньги в разработке»). Пока работают заглушки следующих этапов: исполнитель Рокет Ворк всегда допущен (`AllowAllContractors`, этап «Выплаты»). Публикацию и снятие выполняет `worker` — раздел «Публикация и контроль в разработке»; уведомления и отчёт — раздел «Уведомления и отчёт в разработке». Приёмку (`acceptPlacement`) и претензии (`createPlacementClaim`) описывает раздел «Приёмка и споры в разработке». Весь путь, включая таймеры и потерю канала, проверяет `internal/bootstrap/placement_e2e_test.go`.

## Публикация и контроль в разработке

Продолжение раздела «Заказ в разработке»: размещение в `scheduled`. Без `MAX_BOT_TOKEN` работает поддельный MAX: посты и удаления остаются в памяти процесса `worker`, их видно только в журнале `worker` (`placement published`, `placement removed` — без текста поста).

1. Блогер нажимает «Опубликовать сейчас»: `POST /api/v1/blogger/placements/{id}/schedule` с `{"mode": "now"}` (текущий момент внутри периода дат и времени кампании) или выбирает время `{"mode": "at", "publish_at": "…"}`.
2. `worker` в течение нескольких секунд выполняет `publish_placement`: размещение `published`, в `GET /api/v1/orders/{id}` и `GET /api/v1/blogger/placements/{id}` появляются `publication_url`, `published_at` и `remove_at` (от фактического времени публикации).
3. Через `duration_hours` формата (24, 48 или 72 ч) `remove_placement` снимает пост: размещение `removed`, `views` — итоговые, `claim_deadline_at` — конец окна претензий.
4. Нарушения (чужой пост во время топа, удаление или правка поста) находят события MAX и сверка раз в `RECONCILE_INTERVAL`: размещение `disputed`, в ответах — `dispute` с причиной и сроком решения. С поддельным MAX их можно вызвать, отправив событие на Webhook (раздел «Фоновые задания и оповещения»), например `{"update_type":"message_removed","timestamp":…,"message_id":"mid.post.1","chat_id":…,"user_id":42}`.

Границы этапа: автоприёмка по окну претензий (T-05, P-13) и решение спора — этап «Приёмка, споры и возвраты»; `admin_mark_removed` — этап API администратора. Все пути, включая неизвестный исход отправки, простой и потерю прав бота, проверяют `internal/bootstrap/autoposting_e2e_test.go` и тесты `internal/placement/app`.

## Маркировка в разработке

Маркирует платформа через Яндекс ОРД (API v8). В `compose.dev.yml` `ORD_OAUTH_TOKEN` пуст — работает поддельный ОРД в памяти процесса `worker`: объекты принимаются, статус доходит до `ERIR async success` за несколько опросов, креатив сразу получает токен `fake-erid-N`. Перезапуск `worker` стирает поддельный ОРД: объекты, отправленные до него, через 24 ч уйдут в `status_timeout`, их повторяет `maxim admin ord retry`. Платформа-контрагент — строка `organization` с `is_platform`, её создаёт запуск `api` и `worker` из `ORD_PLATFORM_*` (в разработке — значения по умолчанию).

1. Канал после одобрения (раздел «Канал до каталога в разработке») — контрагент блогера, договор с блогером, площадка; канал виден в каталоге, когда площадка принята ОРД.
2. Заказ, затем `dev post-version --order 1001 approve` (раздел «Заказ в разработке»). Рекламодатель должен принять оферту `advertiser_offer` (`POST /api/v1/legal-acceptances`): её дата — дата договора, без неё договор уйдёт в ошибку `offer_not_accepted`.
3. `worker` регистрирует контрагентов платформы и рекламодателя, изначальный договор, затем креатив (после того как все изображения версии скопированы в `maxim-public`). Получив erid, он собирает итоговый текст, версия становится `ready`, размещения уходят блогерам (P-02). Журнал `worker`: `ord object sent`, `ord object confirmed`, `erid received` — без ИНН, наименований и текста поста.
4. Ошибки — `maxim admin ord errors`, повтор — `maxim admin ord retry` (раздел «Команды»). Правка наименования или КПП организации сама отправляет контрагента заново.

Тестовый контур: токен пользователя кабинета `ord-prestable` в `ORD_OAUTH_TOKEN` (адрес по умолчанию в разработке — `https://ord-prestable.yandex.net`), `ORD_KKTU_CODE` (вид `30.15.1`, обязателен при настоящем адаптере), реквизиты платформы `ORD_PLATFORM_INN`, `ORD_PLATFORM_NAME`, `ORD_PLATFORM_LEGAL_TYPE` — ИНН кабинета. Каждый вызов настоящего ОРД пишется в `outbound_request` (ИНН маскирован, вместо текста креатива — длина).

Границы этапа: ежемесячная статистика и акты — этап «API администратора и отчётность ОРД»; экран «Ошибки ОРД» и модерация поста через API — этап «API администратора». Все пути, включая отказы ОРД, отклонение креатива ЕРИР и новую версию поста, проверяют `internal/bootstrap/marking_e2e_test.go` и тесты `internal/marking/app`.

## Деньги в разработке

Баланс рекламодателя пополняется только банковским переводом по счёту (S-06, S-30): счёт `MX-…` выставляет `POST /api/v1/invoices`, деньги зачисляет загрузка выписки «1С», когда в назначении платежа есть номер счёта (или совпали ИНН и сумма единственного открытого счёта). В разработке банк заменяют команды:

```bash
# Счёт и оплата одной командой: баланс организации пользователя +5 000 ₽
docker compose -f compose.dev.yml run --rm --no-deps api dev balance deposit --email adv@example.ru --amount 5000.00

# Или по шагам: счёт через API, файл выписки, загрузка администратором senior
mkdir -p tmp
MSYS_NO_PATHCONV=1 docker compose -f compose.dev.yml run --rm --no-deps --user "$(id -u):$(id -g)" -v "$PWD/tmp:/statements" \
  api dev statement --invoice MX-000001 --out /statements/statement.txt
MSYS_NO_PATHCONV=1 docker compose -f compose.dev.yml run --rm --no-deps -v "$PWD/tmp:/statements:ro" \
  api admin money statement import --file /statements/statement.txt --email admin@maxim.local
```

Роль senior выдаёт `maxim admin grant --role senior`. Баланс и операции — `GET /api/v1/balance` и `GET /api/v1/balance/transactions`, счета и их PDF — `GET /api/v1/invoices`, закрывающие документы — `GET /api/v1/closing-acts`. Неопознанные поступления видны в `admin money bank-tx list --status unmatched`, сверка с банком — `admin money reconcile`. `worker` раз в `LEDGER_CHECK_INTERVAL` проверяет сходимость журнала (`check_ledger_balance`, оповещения `ledger_*`), раз в `INVOICE_EXPIRE_INTERVAL` закрывает просроченные счета, а `CLOSING_ACTS_DAY` в `CLOSING_ACTS_TIME` по Москве формирует отчёт агента и акт за прошлый месяц. Журнал — `payment credited`, `payment unmatched`, `bank statement imported` — без ИНН полностью, наименований, назначений и номеров счетов банка.

Границы этапа: возвраты по спорам и выплаты блогерам — этапы «Приёмка, споры и возвраты» и «Выплаты»; экраны администратора — этап «API администратора». Весь путь от счёта до акта проверяет `internal/bootstrap/billing_e2e_test.go`.

## Выплаты в разработке

API Рокет Ворк не известен (Q-10…Q-12), поэтому в разработке и тестах работает подделка (`ROCKETWORK_MODE=fake`): `api`, `worker` и команды делят её состояние через файл `ROCKETWORK_FAKE_STATE_PATH` (в `compose.dev.yml` — том `rocketwork-fake`). Путь блогера-самозанятого:

```bash
# Блогер нажал «Подключить выплаты» (POST /api/v1/blogger/rocketwork/invite) — worker создал исполнителя
docker compose -f compose.dev.yml run --rm --no-deps api dev rocketwork activate --org <ИНН блогера>
# Депозит: проводка в журнале и остаток в подделке
docker compose -f compose.dev.yml run --rm --no-deps api admin money deposit-topup --amount 5000.00 --reason пополнение --email admin@example.test
docker compose -f compose.dev.yml run --rm --no-deps api dev rocketwork deposit --amount 5000.00
# Размещение снято по сроку (removed) — приёмка и выплата; worker отправит её в подделку
docker compose -f compose.dev.yml run --rm --no-deps api dev placement accept --placement <id>
# Подделка выплатила и прислала вебхук — размещение paid
docker compose -f compose.dev.yml run --rm --no-deps api dev rocketwork complete --payout <id> --webhook http://api:8080/api/v1/webhooks/rocketwork
```

Без вебхука `worker` узнаёт исход опросом (`PAYOUT_POLL_FAST`, затем `PAYOUT_POLL_SLOW`). Выплаты блогера и итоги — `GET /api/v1/blogger/payouts` и `/blogger/payouts/summary`, статус исполнителя — `GET /api/v1/blogger/rocketwork`. Блогеру-юрлицу Рокет Ворк не нужен: выплата `bank_transfer` ждёт `admin payout mark-paid`. `worker` раз в `ROCKETWORK_DEPOSIT_CHECK_INTERVAL` сверяет депозит с журналом (`rocketwork_deposit_low`, `rocketwork_deposit_mismatch`), сверку печатает и `admin money reconcile`. В продакшене по умолчанию `ROCKETWORK_MODE=disabled`; клиент `live` написан по придуманному контракту и включается после сверки с документацией (этап «Проверки и доступы»). Весь путь проверяет `internal/bootstrap/payout_e2e_test.go`.

## Уведомления и отчёт в разработке

События заказа, размещений, оплаты, закрывающих документов и каналов создают уведомления в той же транзакции (07 §6). Кабинет получает их сразу: `GET /api/v1/notifications` (`?unread=true`, курсор `cursor`), `POST /api/v1/notifications/{id}/read`, `POST /api/v1/notifications/read-all`. Письма и сообщения бота отправляет `worker` заданием `send_notification`: блогеру — бот MAX (по `added_by_max_user_id` канала) и почта, рекламодателю — почта. Письма видно в Mailpit (http://localhost:8025); с поддельным MAX сообщения бота остаются в памяти `worker`, в журнале — `notification sent` с шаблоном и каналом, без адресов и текста. Ссылки в письмах и боте ведут на `CABINET_BASE_URL` (в разработке `http://localhost:5173`). Тексты — `templates/notification/{код}.tmpl`, каталог кодов — `internal/notification/domain/catalog.go`; все коды каталога подключены (`TestCatalogCodesHaveCallers`).

Отчёт по заказу (S-09): `GET /api/v1/orders/{id}/report` — строки по размещениям с просмотрами, ссылкой на пост, суммами и CPV, итог, ссылки поста с UTM-метками; `GET /api/v1/orders/{id}/report.csv` — то же для Excel. Весь путь от оформления до отчёта проверяет `internal/bootstrap/notification_e2e_test.go`.

## Приёмка и споры в разработке

После снятия поста по сроку (`removed`) рекламодатель принимает работу — `POST /api/v1/placements/{id}/accept` — или подаёт претензию — `POST /api/v1/placements/{id}/claims` с `{"reason": "removed_early|top_violated|content_mismatch|other", "text": "…"}`; оба принимают `Idempotency-Key`. Претензия из `published` возможна сразу после публикации. Без действий через `PLACEMENT_CLAIM_WINDOW` (24 ч) `worker` принимает размещение сам (T-05, P-13). Приёмка распределяет удержание и создаёт выплату в одной транзакции. Спор решает администратор (`moderator` или `senior`):

```bash
docker compose -f compose.dev.yml run --rm --no-deps api admin dispute list --overdue
docker compose -f compose.dev.yml run --rm --no-deps api admin dispute show --placement <id>
docker compose -f compose.dev.yml run --rm --no-deps api admin dispute resolve --placement <id> --resolution proportional --share 7500 --comment "пост снят раньше срока" --email admin@example.test
```

`full_refund` возвращает удержание на баланс рекламодателя и, если пост ещё в канале, `worker` удаляет его сразу; `proportional` и `full_refund` снижают рейтинг канала (`DISPUTE_RATING_PENALTY_PROPORTIONAL`, `DISPUTE_RATING_PENALTY_REFUND`). Обе стороны получают `placement.dispute_resolved`. Переписка по спору, отмена и блокировка канала администратором — этап API администратора. Весь путь проверяет `internal/bootstrap/acceptance_e2e_test.go`.

## Конфигурация

Только переменные окружения. Все они с пояснениями и безопасными для разработки значениями перечислены в [.env.example](.env.example). При ошибке в конфигурации процесс пишет в журнал `config invalid` со списком полей и не стартует. Обязательные переменные зависят от режима: `migrate` нужна только база. `worker` в продакшене требует `MAX_BOT_TOKEN`, хотя бы одного адресата оповещений, `MAX_WEBHOOK_URL` (https) и оба секрета Webhook; `api` в продакшене — `MAX_WEBHOOK_PATH_SECRET` и `MAX_WEBHOOK_SECRET` (не короче 32 символов). Подключение каналов: `MAX_BOT_NAME` (имя бота в инструкции), `CHANNEL_LINK_CODE_TTL` (60 мин), `CHANNEL_LINK_CODES_PER_HOUR` (10), `CHANNEL_UNCLAIMED_TTL` (7 суток), `RKN_SUBSCRIBERS_THRESHOLD` (10 000). Каталог: `PLATFORM_FEE_RATE_BP` (1500 — комиссия 15 % сверху цены блогера, 0…10 000), `RKN_RECHECK_INTERVAL` (90 суток до повторной проверки номера), пороги флагов аномалий `METRICS_SUBSCRIBERS_SPIKE_BP` (2000) и `METRICS_VIEWS_MIN_BP` (100), пределы интервалов недоступности `CHANNEL_UNAVAILABILITY_MAX_DAYS` (90), `CHANNEL_UNAVAILABILITY_HORIZON_DAYS` (365), `CHANNEL_UNAVAILABILITY_MAX_COUNT` (50), хранение закончившихся интервалов `RETENTION_CHANNEL_UNAVAILABILITY` (30 суток). Медиатека и пост: хранилище нужно `api` и `worker`; публичная корзина `S3_BUCKET_PUBLIC` и её адрес `S3_PUBLIC_BASE_URL` (https в продакшене), адрес в подписанных ссылках `S3_PRESIGN_ENDPOINT` (если браузер видит хранилище иначе, чем сервер), `MEDIA_MAX_BYTES` (50 МБ, не больше), `MEDIA_MIN_SIDE` (160), `MEDIA_PREVIEW_SIDE` (640), `MEDIA_UPLOADS_PER_HOUR` (60), `MEDIA_UPLOAD_CONCURRENCY` (4), `MEDIA_BUILD_CONCURRENCY` (2), `MEDIA_ALBUM_LIMIT` (10, не больше), `MEDIA_URL_TTL` (1 ч), `MEDIA_RETENTION_DELETED` (30 суток), `POST_BODY_LIMIT` (3800) и `POST_FINAL_TEXT_LIMIT` (4000). Заказ и размещения: `PLACEMENT_ACCEPT_TIMEOUT` (24 ч, T-01), `PLACEMENT_SCHEDULE_TIMEOUT` (24 ч, T-13), `PLACEMENT_WINDOW_GUARD` (1 ч до конца окна дат, не больше `PLACEMENT_ACCEPT_TIMEOUT`), `PLACEMENT_MIN_LEAD` (1 ч до публикации), `PLACEMENT_SLOT_STEP` (5 мин, целое число минут, делящее час), `PLACEMENT_REEDIT_GUARD` (24 ч), `CART_MAX_ITEMS` (50, от 1 до 200), `STUCK_SCHEDULED_PLACEMENT_AGE` (35 мин). Автопостинг и контроль: `PUBLISH_RETRY_WINDOW` (30 мин, T-03), `PUBLISH_RETRY_INTERVAL` (30 с, меньше окна), `PUBLISH_FIND_LOOKBACK` (40 мин — поиск своего поста после таймаута отправки, больше окна повторов), `PUBLISH_MAX_IMAGES` (10, от 1 до 10), `REMOVE_RETRY_WINDOW` (24 ч, T-10), `PLACEMENT_CLAIM_WINDOW` (24 ч, T-05), `DISPUTE_DEADLINE` (72 ч, T-06), `DISPUTE_TOP_VIOLATION_SHARE` (5000 — предложение доли при нарушении топа), `RECONCILE_INTERVAL` (5 мин, T-09), `RECONCILE_SCHEDULED_GRACE` (5 мин), `PUBLICATION_VIEWS_REFRESH` (1 ч), `STUCK_REMOVAL_OVERDUE_AGE` (1 ч), `MAX_BOT_USER_ID` (пусто — `worker` узнаёт бота через `GET /me`; по нему отличаются свои сообщения в канале). Маркировка (`api` и `worker`): `ORD_ENABLED` (true; в продакшене выключить нельзя), `ORD_API_URL` (в продакшене обязателен, https), `ORD_API_VERSION` (v8), `ORD_OAUTH_TOKEN` (секрет; пусто в разработке — поддельный ОРД), `ORD_RATE` (5 запросов в секунду), `ORD_KKTU_CODE`, `ORD_PLATFORM_INN`, `ORD_PLATFORM_NAME`, `ORD_PLATFORM_LEGAL_TYPE` (`sole_proprietor`), `ORD_PLATFORM_TYPE` (`site`), параметры договоров `ORD_INITIAL_*` и `ORD_PUBLISHER_*` (значения из перечислений API v8), `ORD_CONTRACT_NUMBER_PREFIX`, опрос статуса `ORD_POLL_FAST_INTERVAL` (1 мин), `ORD_POLL_FAST_PERIOD` (10 мин), `ORD_POLL_SLOW_INTERVAL` (10 мин), `ORD_STATUS_TIMEOUT` (24 ч), ожидание зависимостей `ORD_DEPENDENCY_WAIT` (30 с) и `ORD_DEPENDENCY_WAIT_FAILED` (10 мин), `ORD_ERID_WAIT` (1 ч, T-11), `STUCK_ORD_SENT_AGE` (24 ч). Деньги: реквизиты платформы для счетов и закрывающих документов `PLATFORM_OGRNIP`, `PLATFORM_ADDRESS`, `PLATFORM_BANK_ACCOUNT`, `PLATFORM_BANK_NAME`, `PLATFORM_BANK_BIK`, `PLATFORM_BANK_CORR_ACCOUNT` (в продакшене обязательны, счета проверяются по ключу для БИК; в разработке пусто — значения разработки), `INVOICE_TTL` (14 суток), `INVOICE_VAT_NOTE` («Без НДС»), лимиты счетов в час `INVOICE_RATE_PER_USER` (10) и `INVOICE_RATE_PER_IP` (30), `INVOICE_EXPIRE_INTERVAL` (1 ч), `STATEMENT_MAX_BYTES` (10 МБ), `LEDGER_CHECK_INTERVAL` (1 ч), `LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD` (10 000 000 копеек — 100 000 ₽), `CLOSING_ACTS_DAY` (1, от 1 до 28) и `CLOSING_ACTS_TIME` (03:00 по Москве). Выплаты: `ROCKETWORK_MODE` (`fake` в разработке, `disabled` в продакшене, `live`), `ROCKETWORK_BASE_URL`, `ROCKETWORK_TOKEN` и `ROCKETWORK_WEBHOOK_SECRET` (для `live` обязательны), `ROCKETWORK_RATE` (5), `ROCKETWORK_FAKE_STATE_PATH`, `ROCKETWORK_DEPOSIT_CHECK_INTERVAL` (1 ч), `ROCKETWORK_DEPOSIT_TOLERANCE` (100 копеек), `ROCKETWORK_CONTRACTOR_POLL_MAX_AGE` (30 суток), `PAYOUT_POLL_FAST` (5 мин), `PAYOUT_POLL_SLOW` (1 ч), `PAYOUT_NPD_ANNUAL_LIMIT` (2 400 000 ₽). `maxim admin` и `maxim dev` нужна только база (`admin money`, `dev balance` и `dev seed` — ещё хранилище); `maxim dev` работает только при `APP_ENV` `development` или `test`. Уведомления: `CABINET_BASE_URL` (адрес кабинета для ссылок; в продакшене обязателен, https), `NOTIFICATION_SEND_ATTEMPTS` (5, от 1 до 20), `NOTIFICATION_RETENTION` (8760 ч — 365 суток). `maxim admin` и `maxim dev` нужна только база (`admin money`, `dev balance` и `dev seed` — ещё хранилище); `maxim dev` работает только при `APP_ENV` `development` или `test`.

## Разработка

| Задача | Команда |
|---|---|
| Тесты | `go test ./...` |
| Код API из спецификации | `go generate ./internal/apigen` |
| Код доступа к базе из SQL | `docker run --rm -v "$PWD:/src" -w /src sqlc/sqlc:1.30.0 generate` |

Тестам нужен Docker: PostgreSQL 17, MinIO и Mailpit запускаются через testcontainers. Без Docker тесты с ними пропускаются, в CI (`CI=true`) падают. Тест отправки письма через Mailpit пропускается, если локальное ПО (например, антивирус) перехватывает SMTP через проброс портов.

Порядок изменений: сначала [api/openapi.yaml](api/openapi.yaml), затем генерация, затем обработчик. Схема базы меняется только новой миграцией в [migrations/](migrations/); запросы пишутся в `internal/*/postgres/queries/*.sql` и `internal/platform/*/queries/*.sql`.

## Эксплуатация

Тестовое окружение и продакшен: стек, выпуск без простоя и откат, резервные копии, восстановление, типовые сбои — [deploy/app/README.md](deploy/app/README.md). Запуск и пилот (юридические документы, персональные данные, уведомление РКН, план пилота) — [docs/launch/README.md](docs/launch/README.md). Репетиции на своей машине (нужен Docker; проекты `maxim-rehearsal*`, порт `127.0.0.1:18480`): `deploy/local/rehearse-release.sh`, `deploy/local/rehearse-backup.sh`.

## CI

| Workflow | Что делает |
|---|---|
| [backend.yml](.github/workflows/backend.yml) | Генерация без изменений в git, `sqlc vet`, `go vet`, `go test -race` с контейнерами, сборка образа |
| [deploy.yml](.github/workflows/deploy.yml) | PR: проверки; push/merge master: автоматический выпуск настоящего бэкенда на chanly.ru |
| [ops.yml](.github/workflows/ops.yml) | Скрипты и шаблоны эксплуатации: `shellcheck`, `check-env.sh`, `check-migrations.sh` (миграции совместимы с прошлым выпуском), `compose config`, `caddy validate`, `hadolint`, `maxim config check` шаблонов; репетиции копий с восстановлением и выпуска без простоя |
| [release.yml](.github/workflows/release.yml) | Общие проверки и выпуск фиксированного SHA; вызывается автодеплоем, вручную — выпуск/откат staging или production |

## Структура

| Путь | Что |
|---|---|
| `cmd/maxim` | Точка входа |
| `internal/bootstrap` | Конфигурация и сборка режимов; `bridge` — переходники портов между модулями |
| `internal/platform` | Общее ядро: журнал, база, очередь заданий, входящие и исходящие обмены, ограничитель, оповещения, HTTP, хранилище, аудит |
| `internal/account` | Модуль аккаунтов: `domain`, `app`, `postgres`, `httpapi`, `jobs` |
| `internal/channel` | Модуль каналов: подключение кодом, права бота, приём Webhook MAX, кабинет блогера, каталог, модерация, метрики |
| `internal/post` | Модуль поста: медиатека и производные (`imaging`), кампании, черновик и сериализация в MAX, предпросмотр, версии |
| `internal/placement` | Модуль размещений: корзина, оформление заказа, таблица переходов P-01 … P-18, таймеры, кабинет блогера, публикация и снятие, контроль, нарушения и спор, сверка |
| `internal/integration` | Адаптеры внешних систем и их подделки: почта, MAX |
| `migrations`, `templates` | Миграции goose и шаблоны писем |
| `docs`, `api` | Проектная документация и договор HTTP API |
| `mockapi` | Тестовый сервер API с выдуманными данными; его стенд — `deploy/compose.yml`, `deploy/maxim.caddy` |
| `deploy/app`, `deploy/postgres`, `deploy/local`, `deploy/scripts` | Стек окружений и выпуск, образ PostgreSQL с копиями, репетиции, проверки ([руководство](deploy/app/README.md)) |
