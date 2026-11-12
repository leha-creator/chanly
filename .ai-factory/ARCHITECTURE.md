# Architecture: Explicit Architecture (модульный монолит, по слоям)

Dev UI диагностики RocketWork (2026-11-11): `rocketwork-check serve` — отдельная
доверенная сборка с control-ролью, основным токеном и ровно одним установленным
прогоном. Обычный dev API обращается к ней через узкий authenticated gateway,
без provider/policy/control-секретов. Сумму и получателя браузер не задаёт;
подтверждение связывается с просмотренным run_id через If-Match. Владелец политики
может подготовить свой неизменяемый прогон без HTTP/денежных прав. Настройка
находится в [ROCKETWORK-DEV.md](../deploy/app/ROCKETWORK-DEV.md).

## Overview

SSE кабинетов: `internal/platform/realtime` читает транзакционный журнал из
миграции 00107 одним reader на API-реплику и рассылает через bounded hub.
SQL-триггеры покрывают HTTP/worker/бот/CLI; PostgreSQL snapshot xmin закрывает
гонку commit-порядка, durable epoch обнаруживает очистку журнала. Короткие
инфраструктурные pgx-запросы reader/cleanup — явное исключение из sqlc, подобно
каталогу; новые схемы всё равно включены в генерацию моделей. Streaming routes
находятся внутри Session/AdminGate/OpenAPI. Подробности: [docs/realtime.md](../docs/realtime.md).

С 2026-11-11 публичность MAX-канала не ограничивает допуск во всех окружениях,
включая production. Удалена политика публичности из домена и конфигурации сборки.
`is_public` хранится как факт MAX; права бота, модерация, цена, категория и РКН
проверяются для публичных и приватных каналов одинаково. Устаревшая причина
`not_public` снимается обычным обновлением состояния по правилам домена.

Основной сценарий подключения (2026-11-11): `channel_connection.channel_name`
содержит введённое название, @тег, ссылку или ID MAX. HTTP создаёт сессию checking,
worker ищет точное совпадение среди обнаруженных каналов (максимум два результата),
проверяет только сведения и права бота write/delete и привязывает канал атомарно.
Проверка личности человека в MAX исключена по решению владельца продукта.
Совпадения названий не разрешаются автоматически; чужая организация не меняется.
`refresh` повторяет этот сценарий по сохранённому имени. Сессия ограничена
инициатором кабинета и организацией; вызовы MAX выполняются вне транзакции.

Совместимость прежнего сценария: POST без тела создаёт сессию, которая принадлежит
организации и инициатору кабинета; одноразовая ссылка запускает `bot_started`.
Приём Webhook заменяет bearer-токен хешем до записи в `inbound_event`.
Worker постранично проверяет обнаруженные каналы через членство конкретного
пользователя MAX; разрешён владелец либо администратор с `write`. Кандидаты
сохраняются в `channel_connection_candidate`. Подтверждение выполняется отдельным
заданием со свежей проверкой прав пользователя и бота вне транзакции, затем
блокировкой сессии и канала и проверкой версии. HTTP никогда не вызывает MAX.
Истёкшие сессии и кандидаты удаляются служебной очисткой. Старый API кодов deprecated.

Бэкенд — модульный монолит на Go: один исполняемый файл с режимами `api` и `worker` и одна база PostgreSQL (07 §1). Код делится на модули по группам модели данных (04 §2–§8). Внутри модуля есть четыре слоя: `domain`, `app`, адаптеры (`postgres`, `httpapi`, `jobs`) и точка сборки в `cmd/`. Зависимости направлены только внутрь, к `domain`.

Такой выбор объясняется предметной областью. Машина состояний размещения (P-01 … P-18), инварианты I-01 … I-13, журнал проводок, расчёт долей и возвратов — это правила, ошибка в которых стоит денег. Поэтому они живут в чистом `domain` без базы, сети и журнала и проверяются быстрыми тестами. Внешние системы (MAX, Яндекс ОРД, Рокет Ворк, почта, хранилище) подключаются через порты. По 07 §9 в тестах вместо них работают поддельные реализации, а база настоящая.

Микросервисы не нужны: команда маленькая, объёмы скромные (07 §7), а сценарии вроде «оформить заказ и удержать деньги» требуют одной транзакции на несколько модулей.

## Decision Rationale

- **Project type:** бэкенд биржи рекламы в каналах MAX: HTTP API кабинетов, приём Webhook, планировщик заданий, интеграции.
- **Tech stack:** Go 1.25+, PostgreSQL, S3-совместимое хранилище (D-09, A-26). Библиотеки — в разделе «Технические решения».
- **Key factor:** сложные денежные и юридические правила, которые 07 §9 требует собрать в одном месте и покрыть тестами без внешних систем; код пишут AI-агенты по ТЗ этапов, поэтому границы должны быть видны по путям импорта.

## Технические решения

Выбор библиотек, отложенный в 07 §1 до этапа «Каркас бэкенда и аккаунты». Смена любой строки — отдельное решение с записью сюда.

| Задача | Решение | Почему |
|---|---|---|
| Модуль Go | `github.com/leha-creator/chanly` в корне репозитория | `mockapi/` остаётся отдельным модулем и в сборку не входит |
| HTTP | `net/http` (`ServeMux` Go 1.22+), код из `api/openapi.yaml` через `oapi-codegen` v2: модели + strict server | Как в `mockapi`; сервер и спецификация не расходятся |
| Проверка запросов | `oapi-codegen/nethttp-middleware` по встроенной спецификации | Схема проверяется до обработчика |
| PostgreSQL | `jackc/pgx/v5` (пул `pgxpool`) | Нативные типы, `FOR UPDATE SKIP LOCKED`, `LISTEN/NOTIFY` при необходимости |
| Запросы | `sqlc`: SQL пишется руками, Go-код генерируется | Видно каждый запрос и блокировку; без ORM |
| Переходники портов | Пакет `bootstrap/bridge`: порт одного модуля поверх сервиса другого; его используют `bootstrap` и сценарные тесты модулей (этап «Заказ и размещения») | Тесты сценариев собирают те же переходники, что и приложение |
| Исключение из `sqlc` | Выборка каталога `channel/postgres/catalog.go` — руками на `pgx` | `sqlc` не умеет переменный `ORDER BY` и необязательные фильтры с ключевым курсором. Условия — из закрытого списка, сортировка — из таблицы выражений, все значения — параметры (этап «Каталог», R-11). Новые исключения — только с записью сюда |
| Миграции | `pressly/goose/v3`, SQL-файлы, встроены в бинарник, команда `maxim migrate` | 07 §8: миграции до запуска новой версии |
| Идентификаторы | `google/uuid`, `uuid.NewV7()` | 04 §1: UUID v7 создаёт приложение |
| Конфигурация | `caarlos0/env/v11` + своя проверка при запуске | 07 §8: только окружение, не стартовать при ошибке |
| Журнал | `log/slog`, JSON в stdout | 07 §5.1 |
| Метрики | `prometheus/client_golang`, отдельный адрес `/metrics` | 07 §5.5 |
| Хранилище | `minio/minio-go/v7` | Любое S3-совместимое, подписанные ссылки |
| Изображения | `golang.org/x/image` (декодер WebP, шрифт Go Bold, `opentype`), `disintegration/imaging` (декодирование с поворотом EXIF, масштабирование) | Плашка и превью без внешних процессов (D-04). Кодировщика WebP нет: производные из WebP — PNG или JPEG (06 §6.2) |
| Тесты с базой | `testcontainers-go` (модуль postgres), пакет `platform/dbtest`: контейнер на тестовый пакет, миграции от владельца, тесты под ролью приложения | 07 §9: настоящая PostgreSQL; права роли проверяются тестами |
| Почта | `wneessen/go-mail` (SMTP), шаблоны `templates/email/*.tmpl` | 07 §6: тексты в репозитории; в разработке — Mailpit |
| Секреты в базе | `platform/secretbox`: AES-256-GCM, ключ `APP_SECRETS_KEY` | 07 §4: секрет в базе — только зашифрованным (R-1) |
| Права роли в миграциях | Параметр сессии `maxim.app_role`, который выставляет `maxim migrate` из `DB_APP_ROLE` | Миграции остаются разбираемыми `sqlc` |
| Клиент MAX | Свой тонкий HTTP-клиент поверх `platform/outbound`, без `max-bot-api-client-go` | Каждая попытка пишется в `outbound_request`; повторы и предохранитель общие для всех систем (05 §2.1) |
| Ограничитель и общее состояние интеграций | Таблицы PostgreSQL: `rate_limit_bucket`, `outbound_circuit`, `alert`, `worker_heartbeat` | 07 §1: общее состояние экземпляров — только в базе; брокер и Redis не нужны |
| Клиент Яндекс ОРД | Свой тонкий HTTP-клиент поверх `platform/outbound` (API v8, версия — конфигурация); тело для `outbound_request` адаптер задаёт сам (`outbound.Call.RecordBody`): ИНН маскирован, вместо текста креатива — длина | Как у MAX: повторы, предохранитель и запись попыток общие; маскирование по полям, которое `Redact` не умеет (этап «Маркировка», R-16) |
| Ожидание без счёта попытки | `job.Postpone(reason, after)`: задание откладывается, `attempts` не растёт, журнал DEBUG | Объект ОРД ждёт зависимость и следующий опрос статуса: такие ожидания не должны тратить `max_attempts` (этап «Маркировка», R-4, R-10) |

## Folder Structure

```
maxim/
├── api/
│   └── openapi.yaml                 # ЕДИНСТВЕННЫЙ договор HTTP API (кабинеты, администратор, Webhook)
├── cmd/
│   └── maxim/
│       └── main.go                  # точка сборки: режимы api | worker | migrate
├── internal/
│   ├── apigen/                      # СГЕНЕРИРОВАНО из api/openapi.yaml (тег _mock исключён). Руками не править
│   │   ├── generate.go
│   │   └── api.gen.go
│   │
│   ├── bootstrap/                   # СБОРКА: конфиг, пулы, адаптеры, модули, HTTP-сервер, worker
│   │   ├── config.go                #   вся конфигурация и её проверка (07 §8)
│   │   ├── api.go                   #   режим api
│   │   ├── worker.go                #   режим worker, регистрация видов заданий
│   │   ├── server.go                #   сборка strict server из обработчиков модулей
│   │   ├── dev.go                   #   maxim dev post-version — только development и test
│   │   ├── integration_check.go     #   явная диагностика MAX/ОРД, только development, без запуска worker
│   │   ├── integration_check_ord.go # чтение sandbox ОРД через outbound и ограничитель, без записи объектов
│   │   ├── integration_check_state.go # состояние тестового MAX-прогона и проверка срока удаления
│   │   └── bridge/                  #   переходники портов между модулями (используют bootstrap и сценарные тесты)
│   │
│   ├── platform/                    # ── ОБЩЕЕ ЯДРО И ИНФРАСТРУКТУРА (без предметных правил) ──
│   │   ├── clock/                   #   Clock: подменяемое текущее время (07 §9)
│   │   ├── money/                   #   Kopecks (int64), доли, округление вниз (06 §3)
│   │   ├── ids/                     #   UUID v7
│   │   ├── apperr/                  #   коды ошибок: validation_failed, not_found, invalid_transition…
│   │   ├── tx/                      #   Runner: транзакция в context; интерфейс + реализация на pgx
│   │   ├── db/                      #   пул pgxpool, helpers для sqlc
│   │   ├── job/                     #   очередь на таблице job: Enqueue (в той же tx), Worker, повторы (07 §2)
│   │   ├── inbound/                 #   запись и разбор inbound_event, дедупликация
│   │   ├── outbound/                #   обёртка внешних вызовов: outbound_request, повторы, предохранитель (05 §1)
│   │   ├── ratelimit/               #   общий ограничитель на корзинах токенов в базе, приоритеты (05 §2.8)
│   │   ├── alert/                   #   оповещения администратору: почта и чат дежурных, не чаще раза в час (07 §3)
│   │   ├── stuck/                   #   поиск зависших объектов раз в час; проверки регистрируют модули (07 §3)
│   │   ├── cleanup/                 #   ежесуточная очистка; запросы удаления остаются у владельцев таблиц
│   │   ├── audit/                   #   запись audit_log
│   │   ├── logx/                    #   slog: поля из context, маскирование (07 §5.4)
│   │   ├── metrics/                 #   реестр Prometheus, HTTP-метрики (07 §5.5)
│   │   ├── secretbox/               #   шифрование секретов в полезной нагрузке заданий (R-1)
│   │   ├── dbtest/                  #   PostgreSQL в testcontainers для тестов
│   │   ├── httpx/                   #   middleware: request_id, сессия, CSRF, лимиты, ошибки → JSON; роли администратора (AdminContext, AdminGate)
│   │   ├── adminpage/               #   курсор-смещение и limit списков администратора (total)
│   │   ├── adminref/                #   ссылки на организацию и пользователя в ответах администратора (ИНН и почта с маской)
│   │   └── storage/                 #   порт ObjectStorage на minio-go: закрытая и публичная корзины, подписанные ссылки; storagefake/
│   │
│   ├── integration/                 # ── АДАПТЕРЫ ВНЕШНИХ СИСТЕМ ──
│   │   ├── max/                     #   клиент MAX Bot API через outbound и ratelimit (25 rps, 1/с на чат), разбор Webhook
│   │   │   └── maxfake/             #   поддельная реализация для тестов и разработки
│   │   ├── ord/                     #   Яндекс ОРД, API v8: контрагент, договор, площадки, креатив, статистика, акт и разбивка, статус; корзина ord:global
│   │   │   └── ordfake/             #   ОРД в памяти: статусы по шагам, токен fake-erid-N, отказы по запросу
│   │   ├── bank/tbank/              #   GET statement через BankSource/outbound/ratelimit, точный decimal; опрос disabled/fake/live
│   │   ├── bank/clientbank/         #   разбор выписки «1С:Предприятие» (UTF-8, cp866, windows-1251): поступления, external_id; адаптер billing/domain.StatementParser
│   │   ├── rocketwork/              #   Public API v1: исполнители, договор/акт/pay, документы, Withdrawal, подписки/история; точные суммы, sensitive outbound, disabled
│   │   │   ├── rocketworktest/      #   независимый HTTP-эмулятор и валидатор исходного OpenAPI для тестов
│   │   │   └── rocketworkfake/      #   Рокет Ворк в памяти или в файле (общий для api, worker и maxim dev): исполнители, депозит, выплаты, вебхуки
│   │   └── mail/
│   │       └── mailfake/
│   │
│   ├── rocketworkcheck/              # Контроль диагностики основного RW-аккаунта: policy/grant/run, общий бюджет
│   │   ├── domain/                  # Чистая политика, immutable manifest/run и порты
│   │   ├── app/                     # Подготовка, intent, пошаговый workflow, reconcile и atomic settlement
│   │   └── postgres/                # Доверенный контрольный журнал; отдельные SQL-права, sqlc
│   ├── account/                     # ── МОДУЛЬ: аккаунты и организации (04 §2) ──
│   │   ├── domain/                  #   сущности, правила legal_type → tax_regime → vat_rate, порты
│   │   ├── app/                     #   сценарии: запросить код, войти, создать организацию…
│   │   ├── postgres/                #   реализация портов-репозиториев
│   │   │   ├── queries/*.sql        #   исходники sqlc
│   │   │   └── sqlc/                #   СГЕНЕРИРОВАНО
│   │   ├── httpapi/                 #   обработчики операций OpenAPI этого модуля
│   │   ├── accounttest/             #   тестовые данные модуля (корректные ИНН, реквизиты)
│   │   └── jobs/                    #   обработчики видов заданий модуля (если есть)
│   │
│   ├── channel/                     # каналы, метрики, категории, форматы, цены, каталог (04 §3)
│   │   ├── domain/                  #   канал, правила состояния (05 §2.4) и РКН, цены, недоступность, модерация, метрики, порты
│   │   ├── app/                     #   кабинет блогера, каталог, модерация, события бота MAX, задания refresh/metrics/delete/leave/reply
│   │   ├── postgres/                #   channel, channel_price, channel_unavailability, channel_metrics, справочники; catalog.go — pgx
│   │   ├── httpapi/                 #   операции кабинетов, каталога, администратора (admin-channels) и приём Webhook MAX
│   │   ├── jobs/                    #   разбор событий MAX (max.ParseUpdate) и регистрация заданий
│   │   └── channeltest/             #   тестовые данные: организация-владелец, одобрение канала
│   ├── post/                        # медиатека, кампании, пост, версии, сериализация в MAX (04 §4)
│   │   ├── domain/                  #   документ редактора, распознавание ссылок, сериализатор, итоговый текст, плашка, кампания, черновик, проблемы, версии, порты
│   │   ├── app/                     #   медиатека и производные, кампании, черновик, предпросмотр, CreateVersion и переходы версии, публикация, очистка
│   │   ├── imaging/                 #   адаптер порта Renderer: превью, копия, плашка (x/image, imaging, шрифт Go Bold); imagingtest/
│   │   ├── postgres/                #   media_file, media_derivative, campaign, post, post_version(_media)
│   │   ├── httpapi/                 #   операции кабинета рекламодателя: медиатека, кампании, пост, предпросмотр
│   │   ├── jobs/                    #   build_media_derivative, publish_media_derivative, purge_media
│   │   └── posttest/                #   тестовые данные: организация рекламодателя
│   ├── placement/                   # корзина, заказ, размещения, публикации, нарушения, споры (04 §5)
│   │   ├── domain/                  #   состояния и ЕДИНСТВЕННАЯ таблица переходов, окно, таймеры, выбор времени, корзина, оформление, заказ, публикация, контроль, спор, порты
│   │   ├── app/                     #   корзина, оформление, заказы, правка поста, действия блогера, таймеры, события версии и канала, публикация, снятие, нарушения, сверка
│   │   ├── postgres/                #   cart_item, order, placement, placement_state_history, publication, violation, dispute; метрики maxim_placements и контроля
│   │   ├── httpapi/                 #   операции корзины, заказов и кабинета блогера; checkoutCampaign
│   │   ├── jobs/                    #   placement_timer, publish_placement, remove_placement, reconcile_publications, check_publication; события message_edited, message_removed
│   │   └── placementtest/           #   поддельные удержания, уведомления, исполнитель; блогер с каналом
│   ├── marking/                     # объекты ОРД, erid, отчётность (04 §6)
│   │   ├── domain/                  #   состояния синхронизации, классификация статусов ОРД, расписание опроса, цепочки зависимостей, запросы v8 и хеш, месяц отчёта и правила статистики и актов, порты
│   │   ├── app/                     #   sync_ord_object, poll_ord_status, креатив и erid, площадки каналов, отклонение ЕРИР, ошибки S-20 и повтор, зависшие, снимок, формирование и отправка отчёта месяца
│   │   ├── postgres/                #   ord_organization, ord_contract, ord_platform, creative, ord_statistics, ord_invoice(_item), ord_report, publication_views_snapshot; выборки S-20; метрики maxim_ord_objects, maxim_ord_erid_wait_seconds
│   │   ├── httpapi/                 #   операции администратора admin-ord: ошибки, объект, повтор, отчёты месяца; порты статуса ОРД для карточек организации и поста
│   │   ├── jobs/                    #   sync_ord_object, poll_ord_status, ord_monthly_report, ord_report_check
│   │   └── markingtest/             #   оферты и их принятие, организации с корректным ИНН
│   ├── ledger/                      # счета учёта, проводки, удержания — ЕДИНСТВЕННЫЙ вход в ledger_* и hold (06 §2)
│   │   ├── domain/                  #   виды счетов и транзакций, правила вида (06 §2.3), отпечаток и ключи идемпотентности, распределение, порты Store и Reports
│   │   ├── app/                     #   Post (только в транзакции вызывающего), HoldAll/Release/Settle, Deposit, остатки и операции кабинета, CheckBalance, Manual, BalanceReturn, журнал денег
│   │   ├── postgres/                #   ledger_account, ledger_transaction, ledger_entry, hold; сверочные выборки; метрики maxim_ledger_*
│   │   ├── httpapi/                 #   getBalance, listBalanceTransactions
│   │   ├── jobs/                    #   check_ledger_balance
│   │   └── ledgertest/              #   организации, пользователи, размещения для проводок
│   ├── billing/                     # счета на оплату, выписки, сопоставление поступлений, закрывающие документы (04 §7)
│   │   ├── domain/                  #   счёт и переходы, номер MX-, поиск номера в назначении, сопоставление (05 §5), период по Москве, порты
│   │   ├── app/                     #   CreateInvoice (лимиты по audit_log), истечение и отмена, ImportStatement, MatchManually, Ignore, GenerateClosingActs
│   │   ├── pdf/                     #   адаптер порта Renderer: счёт, отчёт агента, акт (go-pdf/fpdf, шрифты Go), сумма прописью
│   │   ├── postgres/                #   invoice, bank_statement, bank_transaction, closing_act; метрики maxim_invoices, maxim_bank_transactions
│   │   ├── httpapi/                 #   счета, их PDF, закрывающие документы и их PDF (тег balance)
│   │   └── jobs/                    #   expire_invoices, generate_closing_acts
│   ├── payout/                      # исполнители Рокет Ворк, выплаты, контроль депозита (04 §7)
│   │   ├── domain/                  #   исполнитель, выплата и попытки, коды отказов, расписание опроса, лимит НПД, депозит; порты Provider, Store, Placements, Ledger, Organizations, Notifier
│   │   ├── app/                     #   Invite/EnsureContractor/опрос, Create (в транзакции P-12/P-13/P-15), Send, PollPayout, вебхук, CheckDeposit, зависшие, Retry, MarkLegalPaid
│   │   ├── postgres/                #   rocketwork_contractor, payout, scoped executor bindings, immutable workflow/onboarding/Withdrawal/events checkpoints; метрики maxim_payouts_*, maxim_rocketwork_*
│   │   ├── httpapi/                 #   операции blogger-payouts; приём вебхука Рокет Ворк
│   │   └── jobs/                    #   ensure_contractor, poll_contractor, send_payout, poll_payout, check_rocketwork_deposit, события Рокет Ворк
│   ├── admin/                       # второе подтверждение денежных операций, журнал действий для экрана, счётчики меню (этап «API администратора»)
│   │   ├── domain/                  #   запрос admin_approval: виды, срок, переходы, правило «не автор»
│   │   ├── app/                     #   запрос, подтверждение (исполнитель операции — порт), отклонение и отзыв, истечение; audit_log без IP; счётчики по портам модулей
│   │   ├── postgres/                #   admin_approval; чтение audit_log
│   │   └── httpapi/                 #   admin-approvals, getAdminAuditLog, getAdminCounters
│   └── notification/                # уведомления (04 §8, 07 §6)
│       ├── domain/                  #   каталог шаблонов, адресаты и каналы по ролям, ссылки в кабинет, состояния
│       ├── render/                  #   тексты из templates/notification: кабинет, бот MAX (Markdown), письмо
│       ├── app/                     #   Notify в транзакции события, Send, кабинет
│       ├── postgres/                #   notification; метрика maxim_notifications{channel,status}; очистка
│       ├── httpapi/                 #   listNotifications, markNotificationRead, markAllNotificationsRead
│       └── jobs/                    #   send_notification
│
├── migrations/                      # goose, одна схема на всё приложение: 00001_init.sql …
├── sqlc.yaml                        # по записи на модуль: queries → <module>/postgres/sqlc
├── templates/                       # тексты уведомлений и писем (07 §6): notification/{код}.tmpl, email/*.tmpl, embed.go
├── compose.dev.yml                  # локальное окружение: PostgreSQL, MinIO, Mailpit, migrate, api, worker
├── Dockerfile                       # образ бэкенда (distroless); контекст — Dockerfile.dockerignore
├── deploy/                          # эксплуатация (deploy/app/README.md); стенд заглушки — compose.yml, maxim.caddy, bootstrap.sh
│   ├── app/                         #   стек окружения: compose (ячейки api/worker a и b, postgres, backup), сайт edge-Caddy,
│   │   └── env/<env>/               #   release.sh; шаблоны app.env и stack.env для staging и production
│   ├── edge/                        #   production Caddy, сеть edge и постоянные TLS-тома; устанавливает app/bootstrap.sh
│   ├── tests/                       #   проверки защиты выпуска без Docker
│   ├── postgres/                    #   образ maxim-postgres: архив WAL, роли, скрипты копий и восстановления (bin/), crontab
│   ├── local/                       #   репетиции выпуска и копий на своей машине (свой edge-Caddy, MinIO, Mailpit)
│   └── scripts/                     #   lib.sh (журнал JSON), check-env.sh, check-migrations.sh
├── docs/                            # проектная документация (справочно); docs/launch — запуск и пилот
└── mockapi/                         # тестовый сервер в памяти (справочно, отдельный модуль)
```

Все модули устроены одинаково (`domain`, `app`, `postgres`, `httpapi`, `jobs`). Пустой слой не создаётся.

## Dependency Rules

```
cmd/maxim ─► bootstrap ─► всё остальное (только сборка)

<module>/httpapi ─┐
<module>/jobs    ─┼─► <module>/app ─► <module>/domain ─► platform/{money, ids, clock, apperr}
<module>/postgres┘                          ▲
         │                                  │ реализует порты
integration/* ──────────────────────────────┘
```

- ✅ `domain` импортирует только стандартную библиотеку и `platform/{money, ids, clock, apperr}`.
- ✅ `app` импортирует свой `domain` и `platform/{tx, job, audit, logx, clock, money, ids, apperr}`, причём из `tx` и `job` — только интерфейсы.
- ✅ `postgres` импортирует свой `domain`, свой `sqlc` и `platform/{db, tx}`.
- ✅ `httpapi` импортирует свой `app`, свой `domain` (типы для преобразования) и `apigen`.
- ✅ `jobs` импортирует свой `app` и `platform/job`.
- ✅ `integration/*` реализуют порты: импортируют `platform/{outbound, ratelimit, job, alert, logx, clock}` и не импортируют модули. Из `job` — только `Retry`/`Permanent`, из `alert` — тип `Notice`.
- ✅ Пакеты `platform/` друг от друга зависят только «вниз»: `alert`, `stuck`, `cleanup`, `inbound` используют `job`, `tx`, `db`; `job` и `outbound` об оповещениях знают только через свой интерфейс `Alerter`.
- ✅ Только `bootstrap` знает обо всех пакетах и связывает порты с реализациями.
- ❌ `domain` не импортирует `pgx`, `net/http`, `log/slog`, `apigen`, `integration/*` и любой `app`.
- ❌ Модуль не импортирует внутренности другого модуля (`placement` → `channel/postgres`, `channel/domain`). Связь идёт через порт (см. ниже).
- ❌ `httpapi` не вызывает `postgres` в обход `app`.
- ❌ `app` и `httpapi` не вызывают `integration/*`. Внешний вызов делается из обработчика задания через порт (07 §1, 07 §9).
- ❌ Никто, кроме `ledger/postgres`, не пишет в `ledger_account`, `ledger_transaction`, `ledger_entry`, `hold`.
- ❌ Никто, кроме `placement/domain`, не меняет `placement.status`.

## Layer/Module Communication

- **HTTP → модуль.** `oapi-codegen` генерирует один интерфейс `apigen.StrictServerInterface`. Каждый `<module>/httpapi.Handlers` реализует методы своих операций. `bootstrap/server.go` собирает их встраиванием структур в одну реализацию интерфейса. Имена методов совпадают с `operationId`, поэтому конфликтов нет.
- **Модуль → модуль, синхронно.** Потребитель объявляет порт в своём `domain` на своём языке, например `placement/domain.ChannelPricing`. `bootstrap` передаёт реализацию: сервис `channel/app`, если сигнатуры совпадают, иначе тонкий переходник в `bootstrap`. Циклов импорта нет, потому что модули друг друга не импортируют.
- **Общая транзакция.** `tx.Runner.InTx(ctx, fn)` кладёт транзакцию в `context`. Все репозитории и `job.Enqueue` внутри `fn` работают в ней. Так оформление заказа, удержание в журнале проводок и создание заданий выполняются атомарно (I-10).
- **Модуль → модуль, асинхронно.** Последствие перехода, которое не обязано выполниться в той же транзакции, ставится заданием в той же транзакции, что и переход. Обработчик задания в другом модуле идемпотентен (03 §11).
- **Модуль → внешняя система.** Порт объявлен в `domain` модуля, например `placement/domain.ChannelPublisher`. Реализация — `integration/max`. Вызывает его только обработчик задания из `<module>/jobs` через `app`. Каждый вызов проходит через `platform/outbound`, который пишет `outbound_request`, повторяет запрос и маскирует секреты.
- **Входящие события.** `httpapi` проверяет подлинность Webhook, записывает `inbound_event` и ставит задание `process_inbound_event`. Затем отвечает `200`. Разбор и действия выполняются в `jobs` (07 §7: ответ за 200 мс). Приём Webhook — обычный `http.Handler` модуля, который `bootstrap` монтирует перед strict server (`serverDeps.Routes`): ключ события — SHA-256 сырого тела, секрет — в заголовке. Обработчик события, решивший, что действий не нужно, возвращает `inbound.ErrIgnore` — событие получает `ignored`. `jobs` модуля может импортировать `integration/*` только ради чистого разбора тела события (`max.ParseUpdate`).
- **Доступ по организации.** Методы репозиториев для данных организации принимают `organizationID` явным параметром. Выборки без него для кабинетных сценариев не пишутся (07 §4). Администраторские выборки — отдельные методы с суффиксом `ForAdmin`, вызываются только после проверки роли: `httpx.RequireAdmin(ctx, log, op, roles...)` в `httpapi` (`401` без сессии, `403` с `admin_required` без роли, WARN `admin access denied`). Выборка администратора может читать название и ИНН владельца из `organization` только для показа (ИНН в ответе маскируется). Операции `/admin/*` (этап «API администратора»): `httpx.AdminContext(ctx, log, op, httpx.AdminAny|AdminSeniorOnly...)` проверяет роль и ставит действующее лицо `audit_log`; `httpx.AdminGate` до проверки по спецификации отказывает пользователю без роли (`403`, а не `422` о полях); матрица ролей — в описаниях операций контракта, её проверяет `bootstrap/admin_e2e_test.go`. Списки — `platform/adminpage` и `total`; ссылки на организацию — `platform/adminref` (ИНН всегда с маской). Денежная операция выше порога идёт через модуль `admin`: запрос `admin_approval`, исполнение после подтверждения другим `senior` через порт исполнителя модуля денег. Статусы ОРД в карточках организации и поста дают порты, реализованные `marking/httpapi` (подключаются при `ORD_ENABLED=true`).
- **Реквизиты рекламодателя для поста.** `post/domain.AdvertiserRequisites` — название и ИНН текущей организации для плашки и снимка версии (этап «Медиатека и пост», R-14). Реализация — переходник `postRequisites` в `bootstrap` поверх `account/postgres` (чтение без блокировки).
- **Версия поста для следующих этапов.** `post/app.CreateVersion(ctx, orgID, campaignID, userID, orderNumber)` работает в транзакции вызывающего: оформление заказа создаёт версию вместе с заказом. Переходы `ApproveVersion`, `RejectVersion`, `MarkVersionReady` — сервисные методы `post/app`; роль администратора и получение erid проверяют вызывающие этапы. Неизменяемость версии в `ready` закреплена триггером базы.
- **Порты к будущим модулям.** Модуль, которого ещё нет, представлен портом с пустой реализацией в `bootstrap/bridge`; после сборки модуля порт подменяется его реализацией. Так `placement/domain.ContractorGate` и `Payouts` — `AllowAllContractors` и `NoPayouts`, пока `newPayoutService` не вызовет `SetPayoutPorts` (гейт и выплаты — модуль `payout`). Вызовы — в транзакции перехода (I-10).
- **Уведомления.** Модули объявляют свой порт `Notifier` в `domain` (`placement`, `billing`, `channel`) со своим `Notice`; переходники `bridge.PlacementNotifier`, `bridge.BillingNotifier`, `bridge.ChannelNotifier` переводят его в `notification/domain.Notice` (роль, организация, объект, данные) и вызывают `notification/app.Notify` в транзакции события (I-10): строки `notification` и задания `send_notification` фиксируются вместе с переходом. Адресаты — `bridge.NotificationRecipients` над `account/postgres`; название канала и `added_by_max_user_id` — `bridge.NotificationChannels` над `channel/postgres`; личные сообщения бота — `bridge.NotificationMessenger` над `integration/max` (приоритет ограничителя «ответы и уведомления»); письма — `integration/mail`. Бот и почта подключены только в `worker`, `api` создаёт уведомления и задания. Модуль выплат подключится так же: порт `payout/domain.Notifier` и `bridge.PayoutNotifier`.
- **Связи модуля размещений.** `bootstrap` подставляет `placement/app` в порты модуля каналов (`AvailabilityListener` — отмена размещений при потере канала, `PlacementSchedule` — занятые топы в карточке и запрет интервала недоступности поверх запланированного размещения, `CartLookup` — признак `in_cart`) и в `post/domain.VersionListener` (последствия смены версии поста в той же транзакции). Модули собираются по порядку, поэтому порты задаются методами `SetPlacementPorts` и `SetVersionListener`. Оформление заказа принадлежит `placement/app.Checkout`: кампанию блокирует и переводит в `ordered` модуль `post` (порт `Campaigns`), версию создаёт `post/app.CreateVersion` в той же транзакции.
- **Публикация и контроль.** Внешние вызовы MAX — порт `placement/domain.ChannelPublisher` (переходник `bridge.PlacementPublisher` над `integration/max`), содержание версии и файлы изображений — порт `placement/domain.PublicationContent` (`bridge.PlacementContent` над `post/app.PublicationContent` и `OpenDerivative`). Приоритет ограничителя MAX задаётся в `context` (`domain.WithCallPriority`): публикация и снятие — `PriorityPublish`, сверка — `PriorityReconcile`. Сообщения каналов для контроля топа модуль каналов передаёт через порт `channel/domain.ChannelPostListener` (`bridge.ChannelPosts`, `SetPostListener`); `message_edited` и `message_removed` модуль размещений разбирает сам (`placement/jobs.RegisterEvents`). Публикация идёт в три шага: предусловия в транзакции, вызовы MAX вне её, публикация и P-08 в транзакции (I-13).
- **Порядок блокировок.** Сценарии модуля размещений блокируют строки в одном порядке: каналы (по `channel_id`) → заказ → размещения. Так I-06 и I-08 проверяются под блокировкой канала, а одновременные действия блогера, события версии и потеря канала не блокируют друг друга навстречу.
- **Маркировка.** Яндекс ОРД — порт `marking/domain.OrdAPI` (переходник `bridge.MarkingOrd` над `integration/ord` или `ordfake`: поля запросов переносятся один в один, поэтому хеш нагрузки совпадает с отправленным телом). Вызывают его только обработчики `sync_ord_object` и `poll_ord_status` в три шага (I-13): транзакция — вызов вне неё — транзакция. Связи с другими модулями — в транзакции их сценариев: `post/domain.VersionListener` — `bridge.VersionListeners` вызывает по порядку модуль размещений и маркировку (`bridge.MarkingVersionListener`), одобренная версия получает креатив; `channel/domain.ChannelApprovalListener` (`bridge.ChannelApprovals`, `SetMarkingPorts`) — одобренный канал получает площадку, а видимым в каталоге его делает `channel/app.SetOrdPlatformReady` (порт `marking/domain.ChannelPlatformReady`); `account/domain.OrganizationListener` (`SetOrganizationListener`) — правка наименования или КПП ставит контрагента на повторную отправку; `marking/domain.CreativeListener` — `placement/app.CreativeRejected` (P-11 `erir_rejected`). Реквизиты организаций и дату принятия оферты маркировка читает через `bridge.MarkingOrganizations` над `account/postgres`, содержание версии и переход в `ready` — через `bridge.MarkingPosts` над `post/app`. Строку организации-платформы обеспечивает `account/app.EnsurePlatformOrganization` при запуске `api`, `worker` и команд. При `ORD_ENABLED=false` (только разработка) слушатели маркировки не подключаются, а одобрение канала сразу делает его видимым.
- **Деньги.** `ledger/app` работает только внутри транзакции вызывающего (`Post` без открытой транзакции — ошибка) и не открывает своих, кроме административных операций (`Manual`, `BalanceReturn`). Модуль размещений удерживает и распределяет через порт `placement/domain.Holds` (`bridge.LedgerHolds`) в транзакции перехода. `billing/app` зачисляет поступления через `bridge.BillingLedger`, читает реквизиты организаций через `bridge.BillingOrganizations` (над `account/postgres`), сведения о размещениях для отчёта агента — через `bridge.BillingPlacements` (над `placement/postgres`). Ссылки проводок на организации и размещения ledger проверяет через `bridge.LedgerReferences`. Файлы выписок и PDF лежат в хранилище (`statements/`, `invoices/`, `closing/`).
- **Выплаты.** Рокет Ворк — порт `payout/domain.Provider` (переходник `bridge.PayoutProvider` над `bridge.RocketWorkAPI`: `integration/rocketwork`, `rocketworkfake` или `disabled` по `ROCKETWORK_MODE`). Вызывают его обработчики заданий `payout/jobs` и явно запущенные административные команды, всегда вне транзакции (I-13); вебхук в `httpapi` только проверяет подпись через `ParseWebhook` и записывает событие. Модуль размещений создаёт выплату через порт `placement/domain.Payouts` (`bridge.PlacementPayouts`; подключается `SetPayoutPorts` после сборки, чтобы не было цикла) в транзакции перехода; P-16 модуль выплат выполняет через `payout/domain.Placements` (`bridge.PayoutPlacements` над `placement/app.MarkPaid`). Проводки `payout` и остаток депозита в журнале — `bridge.PayoutLedger`; ФИО, ИНН и телефон получателя — `bridge.PayoutOrganizations` над `account/postgres`.

## Key Principles

1. **Названия из глоссария.** Пакеты, типы, поля, константы перечислений, коды переходов (`P01AcceptByBlogger`), таймеров и причин совпадают с `docs/glossary.md` и 04 §9 без переименований.
2. **Одна таблица переходов.** Таблица живёт в `placement/domain/transitions.go` и сверяется с 03 §4. Состояние меняет только `Placement.Apply`. Результат применения — новое состояние, запись истории и список заданий, которые `app` сохраняет в той же транзакции.
3. **Деньги — `money.Kopecks` (`int64`).** `float` для денег запрещён. Доли и возвраты считаются функциями в `domain` по 03 §7 и 06 §3, округление вниз.
4. **Время — только через `clock.Clock`.** `time.Now()` вне `platform/clock` запрещён: таймеры и сроки должны проверяться тестами.
5. **Идемпотентность по умолчанию.** Каждый обработчик задания и входящего события сначала читает текущее состояние и выходит, если действие уже выполнено. Денежные операции несут ключ идемпотентности (06 §2.2).
6. **Метки реестра в коде.** Место, реализующее допущение или рабочий ответ на вопрос, помечается комментарием `// A-10`, `// Q-03`. Параметры допущений берутся из конфигурации (README, «Метки»).
7. **Журнал по 07 §5.** Пишет слой `app` и адаптеры, `domain` не пишет. Сообщение — постоянная строка на английском. Операция — в поле `op` в виде `Модуль.Операция`. Обязательные события 07 §5.3 — часть критериев приёмки.
8. **Спецификация первична.** Новая операция сначала появляется в `api/openapi.yaml`, затем `go generate ./internal/apigen`, затем обработчик. Тестовый сервер `mockapi` при этом не обязан обновляться.
9. **Методы `Dev…` — только для команд разработчика.** Где команде `maxim dev` (например, `dev seed`) нужен шаг, которого нет у сервиса, в `app` модуля появляется метод `Dev…` (`account.DevEnsureUser`, `channel.DevSeedChannel`, `placement.DevAccept`). Он собирает состояние из переходов `domain`, а не записью в базу из `bootstrap`; HTTP-обработчика и задания у него нет; вызывает его только `bootstrap` под проверкой `APP_ENV` `development` или `test`.

## Code Examples

### Машина состояний в `domain`: чистые правила, без базы и журнала

```go
// internal/placement/domain/transitions.go
package domain

import (
	"time"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

type Status string

const (
	StatusAwaitingBlogger  Status = "awaiting_blogger"
	StatusAwaitingSchedule Status = "awaiting_schedule"
	StatusDeclined         Status = "declined"
	// … полный список — 04 §9
)

type TransitionCode string

const (
	P03AcceptByBlogger  TransitionCode = "P-03"
	P04DeclineByBlogger TransitionCode = "P-04"
	// … P-01 … P-18 — 03 §4
)

type transition struct {
	from  []Status
	to    Status
	actor ActorType
}

// Единственная таблица переходов (07 §9). Сверяется с 03 §4 тестом.
var transitions = map[TransitionCode]transition{
	P03AcceptByBlogger:  {from: []Status{StatusAwaitingBlogger}, to: StatusAwaitingSchedule, actor: ActorBlogger},
	P04DeclineByBlogger: {from: []Status{StatusAwaitingBlogger}, to: StatusDeclined, actor: ActorBlogger},
}

// Apply — единственный способ сменить состояние размещения.
func (p *Placement) Apply(code TransitionCode, actor ActorType, now time.Time) (StateChange, error) {
	t, ok := transitions[code]
	if !ok || t.actor != actor || !t.allows(p.Status) {
		return StateChange{}, apperr.InvalidTransition(string(code), string(p.Status))
	}
	change := StateChange{PlacementID: p.ID, Code: code, From: p.Status, To: t.to, At: now}
	p.Status = t.to
	p.LockVersion++
	change.Jobs = p.jobsAfter(code, now) // таймеры 03 §5 — значения, а не вызовы
	return change, nil
}
```

### Сценарий в `app`: транзакция, порты, журнал

```go
// internal/placement/app/accept.go
package app

func (s *Service) AcceptByBlogger(ctx context.Context, orgID, placementID uuid.UUID) error {
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		p, err := s.placements.GetForUpdate(ctx, orgID, placementID) // выборка ограничена организацией
		if err != nil {
			return err
		}
		change, err := p.Apply(domain.P03AcceptByBlogger, domain.ActorBlogger, s.clock.Now())
		if err != nil {
			s.log.WarnContext(ctx, "placement transition rejected",
				"op", "Placement.AcceptByBlogger", "placement_id", placementID, "transition", domain.P03AcceptByBlogger)
			return err
		}
		if err := s.placements.Save(ctx, p, change); err != nil { // placement + placement_state_history
			return err
		}
		if err := s.jobs.Enqueue(ctx, change.Jobs...); err != nil { // в той же транзакции (I-10)
			return err
		}
		s.log.InfoContext(ctx, "placement transition applied",
			"op", "Placement.AcceptByBlogger", "placement_id", placementID,
			"transition", change.Code, "from", change.From, "to", change.To)
		return nil
	})
}
```

### Порт объявляет потребитель, реализацию подставляет `bootstrap`

```go
// internal/placement/domain/ports.go
package domain

// ChannelPublisher — публикация и снятие поста в канале. Реализация: integration/max.
// Вызывается только из обработчиков заданий publish_placement / remove_placement.
type ChannelPublisher interface {
	Publish(ctx context.Context, req PublishRequest) (PublishedMessage, error)
	Remove(ctx context.Context, channelExternalID, messageID string) error
}

// ChannelPricing — цена формата в канале. Реализация: сервис channel/app.
type ChannelPricing interface {
	Price(ctx context.Context, channelID uuid.UUID, format string) (money.Kopecks, error)
}
```

```go
// internal/bootstrap/worker.go (фрагмент)
maxClient := max.NewClient(cfg.MAX, outbound.New(pool, log, clk)) // или maxfake.New() в разработке
placementSvc := placementapp.New(placementapp.Deps{
	Tx:        txRunner,
	Repo:      placementpg.New(pool),
	Jobs:      jobQueue,
	Publisher: maxClient,             // integration/max реализует domain.ChannelPublisher
	Pricing:   channelapp.New(/*…*/), // structural typing: метод Price совпадает
	Clock:     clk,
	Log:       log,
})
jobs.Register(worker, placementSvc) // publish_placement, remove_placement, placement_timer
```

### Нарушение: внешний вызов из HTTP-обработчика

```go
// ❌ httpapi не должен ходить во внешние системы и менять статус напрямую
func (h *Handlers) PublishNow(ctx context.Context, req apigen.PublishNowRequestObject) (apigen.PublishNowResponseObject, error) {
	h.max.SendMessage(ctx, ...)                               // нарушает 07 §1: только через задание
	h.db.Exec(ctx, "UPDATE placement SET status='published'") // нарушает 07 §9: только через Apply
	...
}
```

## Тестирование

Контролируемая диагностика RW (`internal/rocketworkcheck`, 2026-11-11) имеет собственные domain/app/postgres и не импортирует payout/ledger. Связь с провайдером и владельцем ledger — через порты и `bootstrap/bridge`. Отдельный `maxim rocketwork-check` собирает только доверенный control pool, outbound/limiter, checks и узкий ledger adapter; он не стартует ordinary workers. Таблицы `rw_check_*` и SECURITY DEFINER-функции с закрытым PUBLIC EXECUTE принадлежат доверенной БД: runner исполняет разрешённые переходы, policy role выдаёт/отзывает grants и меняет потолки, обычная app role читает только ownership views. Основной HTTP-клиент по умолчанию readonly; controlled capability связывает exact wire, свежий task snapshot и durable intent. Production automation — отдельная capability.

Ledger-владелец проводит `rw_diagnostic:<run_id>` через ограниченную функцию из `ledger/postgres`: `diagnostic_expense` для номинала и `payout_fees` для комиссии кредитуют общий RW-депозит. Ledger и settlement резерва находятся в общей транзакции. Это не размещение, blogger_payable или P-16. Проверки PostgreSQL + настоящий Client + независимый HTTP-эмулятор включают concurrent reserves, midnight settlement, потерю ответа, revoke/kill/restore arming, legacy import, смешанные callbacks и сбой до ledger commit.

Состояние интеграции Рокет Ворк: `payout/domain/workflow.go` описывает неизменяемые условия сделки и checkpoint операции, `payout/postgres/workflow.go` сохраняет их через sqlc. Миграция 18 разделяет исполнителей по среде/аккаунту и защищает снимок, захват действия и историю неизвестного исхода. Старые provider ID не конвертируются в подтверждённые связи. Пока внешний процесс ждёт, payout остаётся created/sent и сохраняет ограничение одной живой выплаты размещения.

| Слой | Чем | Что |
|---|---|---|
| `domain` | Обычные табличные тесты, без базы | Каждый переход P-01 … P-18 из каждого состояния; инварианты; формулы 03 §7, 06 §3 на граничных долях 0, 1, 9999, 10000; сериализатор поста (05 §2.5) |
| `app` + `postgres` | `testcontainers-go`, настоящая PostgreSQL, миграции goose, поддельные интеграции, `clock` с ручным ходом | Сценарии, таймеры, идемпотентность повторов, строки таблицы проводок 06 §2.3 и сходимость |
| `httpapi` | `httptest` поверх собранного сервера | Коды ответов, проверка организации, CSRF, соответствие спецификации |
| `integration/*` | `httptest`-сервер с записанными ответами | Разбор ответов и ошибок, маскирование, повторы |

Тест, сверяющий таблицу переходов с 03 §4 и перечисления с 04 §9, обязателен.

## Anti-Patterns

- ❌ **Анемичное размещение.** Проверки состояния и расчёт сумм в `app` или `httpapi` вместо методов `domain`.
- ❌ **Прямой `UPDATE placement SET status`** или запись в `ledger_entry` в обход владельца.
- ❌ **Внешний вызов в транзакции базы или в HTTP-запросе кабинета.** Внешние вызовы — только в заданиях, вне открытой транзакции.
- ❌ **Импорт чужого модуля** ради одного типа или функции. Объявите порт у себя и свяжите в `bootstrap`.
- ❌ **Общий пакет `utils/` или `common/`** с предметной логикой. В `platform/` — только то, что не знает о рекламе, каналах и деньгах сверх типа `Kopecks`.
- ❌ **`time.Now()`, `uuid.New()` (v4), `float64` для денег** в коде модулей.
- ❌ **Правка `apigen/` или `sqlc/` руками.** Меняется спецификация или SQL, затем генерация.
- ❌ **Логирование текста поста, ИНН, почты без маскирования, токенов и кодов** (07 §5.4).
- ❌ **Глобальные переменные с состоянием** (пулы, клиенты, конфигурация). Зависимости передаются через конструкторы из `bootstrap`.

### Диагностика ОРД sandbox

integration_check_ord.go создаёт клиент с outbound, ограничителем, запретом redirect и сетевых повторов. integration_check_ord_fixtures.go содержит тестовую агентскую цепочку. integration_check_ord_state.go сохраняет состояние перед POST, опрашивает квитанции, блокирует неизвестный исход и чужие идентификаторы. integration_check_ord_state_test.go проверяет восстановление и официальную сохранённую OpenAPI через jsonschema/v6 и regexp2 (ECMAScript lookahead). Эти зависимости используются тестами; raw sandbox fixtures не заменяют производственные DTO отчётности.
Транспорт отчётности API v8: internal/integration/ord/reporting_v8.go содержит отдельные wire DTO и Submit*V8; ими пользуются sandbox и bootstrap/bridge/marking_reports.go. Внутренний correlation ID статистики не сериализуется. outbound.Call.NoRetry запрещает повтор отдельной операции и не ослабляет глобальный Options.NoRetry. Реальные HTTP-тела сверяются с OpenAPI в reporting_v8_test.go; legacy-fixture тест не считается подтверждением спецификации.

Акт и разбивка — независимые объекты invoice/invoice_creatives с отдельными квитанциями и dedupe keys. ord_invoice_creatives имеет PK/FK на акт; обе стадии сериализуются блокировкой родительской строки. Перед POST отчётности marking/app сохраняет checkpoint, сеть вызывается вне транзакции. Неопределённый исход не допускает replay; известная квитанция после timeout возобновляет polling. Миграция 17 сохраняет старую квитанцию разбивки и блокирует исторический акт для сверки. Финансовые снимки в копейках хранятся вместе с отчётом; комиссия приходит из placement, НДС — из account, без импортов чужих модулей внутри marking. Неизвестные метрики не превращаются в нули.

## Автоматический источник Т-Банка (2026-11-11)

Порт billing/domain.BankSource читает страницы фиксированного UTC-периода; StatementParser остаётся файловым. billing/app использует общий importIncoming и credit, billing/postgres хранит алиасы/наблюдения и checkpoint. billing/jobs регистрирует sync_bank_statements только при явном включении источника; bootstrap/config_tbank.go валидирует режимы и TLS, integration_check_tbank.go собирает отдельный диагностический контур. Advisory lock держится на выделенном соединении вне сетевой транзакции; SQL бизнес-запросов генерируется sqlc. Среды проверки используют отдельные БД, storage/отправители не подключаются.

### Ограниченные dev-зачисления Т-Банка

Billing/app проверяет явный dev-флаг и целый токен DEV-TBANK-<код> до deposit, одинаково для API/файла/ручного match. Bootstrap разрешает controlled только development с реальным endpoint и TBANK_DEV_REAL_ENABLED; readonly не подключает фоновый источник. Нет scope по организации, expiry или бюджета. Исторические таблицы bank_credit_policy/spend (00101) больше не используются. Исходящие выплаты Рокет Ворк независимы.


### Ручная проверка оплаты счёта

POST /invoices/{id}/check-payment доступен только владельцу счёта в кабинете рекламодателя. Результаты: paid, not_found, busy, unavailable; checked_at выдаётся только после полного чтения периода, retry_after задаёт паузу для кнопки. Ошибки/неполная пагинация не объявляются отсутствием платежа. Отмена счёта — 409. Уже оплаченный счёт возвращается без обращения к банку.

Ручной опрос использует настроенный BankSource и общий importIncoming/credit, поэтому сохраняются dev-код, идентификаторы операций, транзакции ledger/уведомления и защита от повторов. Account advisory lock общий с фоновым импортом. LastManualCheck хранится в JSON bank_sync_progress: глобальная пауза 60 секунд, в том числе после ошибки; новые пользователи/процессы её не обнуляют. Миграция не нужна. Читается период от создания счёта (с минутным запасом, не раньше TBANK_IMPORT_FROM), максимум последние 31 сутки, до 10 страниц и 20 секунд. Фоновый checkpoint, LastSuccess и сверка остатков не продвигаются ручным опросом. Полная выписка/чужие операции в ответ не выдаются.

Frontend показывает кнопку рядом с PDF, таймер повтора и отдельные сообщения результата. После запроса сбрасываются Invoice/Balance/BalanceTransaction/Notification; реквизиты нового счёта сохраняются при обновлении его статуса. Mockapi показывает только уже подтверждённую mock-оплату, для остальных счетов unavailable; сама кнопка оплату не симулирует.

## Настройки аккаунта (2026-11-12)

Миграция 00108: предпочтения пользователя хранит notification/postgres; чистая
политика каталога и тихих часов — notification/domain/settings.go. HTTP настроек
принадлежит notification, смена почты и управление сеансами — account.
bootstrap/bridge/settings.go получает доступные кабинеты и адресуемость MAX через
account/postgres, прямых импортов соседних модулей в app/domain нет.
Коды смены почты идут через существующую зашифрованную job-очередь и отдельные
почтовые шаблоны; вход и подтверждение сериализуют адреса advisory-блокировкой.
SSE сохраняет v1, добавляет user-only settings.changed и INSERT-only
notification.created с mode=live. Короткий запрос PostgreSQL clock_timestamp
при открытии подписки — часть инфраструктурного исключения realtime из sqlc,
постоянного соединения с БД на браузер не создаёт.
Общая политика mockapi генерируется scripts/mock-settings/main.go из production
каталога и правил; см. docs/account-settings.md.
