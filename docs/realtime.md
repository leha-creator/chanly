[К README](../README.md) · [HTTP-контракт](../api/openapi.yaml)

# SSE v1: онлайн-обновления трёх кабинетов

`GET /api/v1/events` обслуживает пользователя, его текущую организацию и доступные
ему изменения каталога. `GET /api/v1/admin/events` обслуживает `moderator` и `senior`,
включая администратора без организации. Используется cookie `session`.
Параметры `user_id`/`organization_id` не участвуют в авторизации.

Оба обработчика зарегистрированы **внутри** Session, CSRF, AdminGate и проверки
OpenAPI; `serverDeps.Routes` для Webhook не используется. Caddy сохраняет проверку
IP по `/api/v1/admin/*` до проксирования. Прямой порт API должен оставаться во
внутренней сети, как и для остальных административных операций.

Пользовательские настройки и отдельный сигнал создания уведомления:
[правила live, дедупликации и тихих часов](account-settings.md#новое-уведомление-reconnect-и-resync).
Тема `notification.created` с `mode=live` появляется только при INSERT,
`settings.changed` требует перечитать предпочтения пользователя.

## Формат и жизненный цикл

Ответ UTF-8, `Content-Type: text/event-stream`, `Cache-Control: no-cache`.
Каждое сообщение немедленно flush-ится и заканчивается пустой строкой:

```text
event: ready
data: {"version":1}

event: heartbeat
data: {"version":1}

event: invalidate
data: {"version":1,"topic":"placement.changed","object_id":"0190a000-0000-7000-8000-000000000901","order_id":"0190a000-0000-7000-8000-000000000801","campaign_id":"0190a000-0000-7000-8000-000000000601"}

event: resync_required
data: {"version":1}

```

Первым всегда идёт `ready`, после готовности reader и регистрации ограниченной
очереди подписчика под блокировкой hub. Изменения между подпиской и flush `ready`
копятся в очереди и выдаются после `ready`. Клиент делает resync обычными GET
после каждого `ready` и `resync_required`. Heartbeat — именованное событие с JSON
каждые **15 секунд**, даже если предметных изменений нет. SSE-комментарии не используются.

`invalidate` имеет строго `version: 1`, `topic` по `^[a-z][a-z_.]{0,63}$` и только
необязательные UUID `object_id`, `order_id`, `campaign_id`, `channel_id`.
Размер JSON ограничен 4096 байт (для ASCII envelope это также ограничение символов).
Тексты, реквизиты, суммы, email, HTML, audience и данные cookie в поток не попадают.
Одна запись может породить несколько тем. Повторы безопасны: это инвалидация,
а не команда применить финансовую операцию.

Перед `ready`, каждым последующим сообщением и раз в секунду проверяются текущая
сессия, блокировка пользователя, членство, кабинеты и административная роль.
Изменение identity/прав или ошибка проверки закрывает поток. Проверки открытого
потока **не продлевают** сессию. Входящий запрос открытия проходит обычный Session.
Таймаут проверки — 2 секунды, записи/flush одного сообщения — 5 секунд.
При свободном writer отзыв обнаруживается за ≤3 секунды; с уже начатой
заблокированной записью консервативная граница прекращения серверной выдачи —
**8 секунд**. Уже отправленные сетевые байты отозвать нельзя.

До открытия: `401` — сессия отсутствует/истекла/отозвана или пользователь заблокирован;
`403 admin_required` — нет административной роли; Caddy возвращает `403` для IP вне
allowlist; `503` с `Retry-After: 1` — reader ещё не готов/восстанавливается.
После отправки заголовков ошибка выражается закрытием соединения, новый HTTP-статус
в уже открытый поток не записывается.

## Доставка и восстановление

Миграция **00107** создаёт `realtime_event`, `realtime_epoch` и AFTER-триггеры
предметных таблиц. Триггеры выполняются в транзакции бизнес-записи во всех
процессах: HTTP, worker, бот, CLI и Webhook. Rollback откатывает журнал.
Одинаковая тема/audience/набор UUID внутри одной транзакции дедуплицируются.
Сбой записи журнала откатывает бизнес-транзакцию: молча терять событие нельзя.

Выбран **транзакционный журнал с polling**, а не LISTEN/NOTIFY. Причина: надёжное
обнаружение пропусков и коммитов, пришедших в другом порядке, без зависимости от
неустойчивой LISTEN-сессии. На каждой API-реплике один `realtime.Reader` раз в
250 мс берёт короткую repeatable-read транзакцию из общего пула и передаёт
события локальному hub. Ни соединение, ни транзакция на браузер не удерживаются.
Повторные проверки доступа используют короткие обычные запросы к пулу.

`bigint id` не является порядком commit. Reader дополнительно перечитывает
транзакции с `xid8 >= xmin` **предыдущего** snapshot и хранит ограниченную
дедупликацию. Поэтому поздний commit с меньшим ID не пропускается. Чтение snapshot,
событий и epoch согласовано одной транзакцией. Только после её commit разрешены
подписки. Нет гарантии глобального порядка commit или ровно одной доставки.

Границы памяти: 128 событий на вкладку, 1024 строк на чтение и до 2048 записей
дедупликации. Переполнение очереди закрывает только медленную вкладку; публикация
в hub не ждёт браузер. Переполнение reader, ошибка чтения, потенциальный пропуск при изменении epoch или
пауза чтения >12 часов закрывают все потоки данной реплики. Во время восстановления
новые подписки получают 503; после свежего snapshot — новый `ready` и обязательный
клиентский resync. При shutdown отмена reader закрывает подписки до завершения HTTP.

Журнал очищается порциями до 10 000 строк старше 24 часов раз в минуту на API.
В той же транзакции увеличивается `realtime_epoch` и сохраняются максимальные ID
и xid8 удалённых строк. Все реплики проверяют эти границы относительно своего
последнего cursor/xmin. Если уже обработаны все удалённые строки, потоки остаются
открытыми; при потенциальном пропуске реплика закрывает свои потоки и переснимает cursor. Это покрывает также
очистку очень старой транзакции сразу после её позднего commit. Без epoch одна
только дата создания записи не давала бы гарантии обнаружения потери.

MVP не поддерживает replay и `Last-Event-ID`. При сбоях бэкенд выбирает закрытие
соединения, разрешённое контрактом, вместо попытки доставить `resync_required`
в переполненную/неработающую очередь. `resync_required` используется mockapi для
сброса и сдвига часов. Гарантия — committed invalidation **либо обнаруженный разрыв
с resync после reconnect**, при работающих клиентских GET; не exactly-once и не
гарантированное время обновления экрана. Для новых предметных таблиц матрицу и
триггеры нужно расширять явно.

## Матрица источников и получателей

`org` — текущие участники организации; `user` — только указанный адресат;
`admin` — moderator/senior; `senior` — только senior. Все источники ниже
покрывают INSERT/UPDATE/DELETE, в том числе изменения из worker и CLI.

| Источник записи / сценарий | Получатели | Темы | UUID и связанные сущности |
|---|---|---|---|
| `user`, `session`, `organization_member`: logout, блокировка, роль, членство | user; admin для изменений пользователя | `session.changed`, `admin.organization.changed`, `admin.counters.changed` | В session.changed UUID не нужны; продление expires_at не порождает feedback loop |
| `organization`, advertiser/blogger profile, legal acceptance | org, участники user, admin | `organization.changed`, `session.changed`, `admin.organization.changed`, `admin.counters.changed` | organization ID; профиль/согласие также обновляет доступ кабинета |
| `notification`: создание/прочтение/удаление кабинетного уведомления | только user в обоих потоках | `notification.changed` | ID уведомления; email/MAX-доставка не является changefeed |
| `campaign`: HTTP создание, параметры, оформление, архив | org рекламодателя | `campaign.changed` | object/campaign ID, order ID при наличии |
| `post`, `post_version`, `post_version_media`: сохранение, модерация, erid | org рекламодателя; участвующий блогер после sent_to_blogger; admin | `post.changed`, `order.changed`, `placement.changed`, `admin.order.changed`, `admin.counters.changed` | post.changed всегда campaign ID; связанные order/placement и channel IDs |
| `media_file`, `media_derivative`: загрузка, удаление, worker preview/public copy | org владельца | `media.changed` | ID оригинала; затронутые черновики инвалидируются темой media |
| `order`: оформление, новая версия, модерация, сводный статус | org рекламодателя; участвующие блогеры; admin | `order.changed`, `placement.changed`, `admin.order.changed`, `admin.counters.changed` | order/campaign; блогеру только уже отправленные размещения |
| `placement`, history, `publication`, `violation`: принятие, таймер, публикация/снятие, бот/сверка | org рекламодателя; блогер после sent_to_blogger; admin | `placement.changed`, `order.changed`, `admin.placement.changed`, `admin.counters.changed` | placement/order/campaign/channel IDs |
| `claim`, `dispute`, `dispute_message`: претензия, решение, переписка | стороны доступного размещения; admin | предыдущие темы + `admin.dispute.changed` | placement/order/campaign/channel; содержание и адресат сообщения не передаются |
| `channel`, price, unavailability, metrics: бот, подключение, модерация, цена, worker метрик | org владельца, рекламодатели каталога, org с каналом в корзине; admin | `channel.changed` / `channel.metrics.changed`, `cart.changed`, `admin.channel.changed`, `admin.counters.changed` | владельцу channel ID; каталогу только общая тема **без UUID**; при смене владельца обновляется прежний org |
| `channel_connection`, candidate: поиск/подключение worker/ботом | только инициатор user; admin | `channel.changed`, `admin.channel.changed`, `admin.counters.changed` | пользовательское событие без UUID, секреты попытки не передаются |
| `cart_item`: изменение между вкладками | org рекламодателя | `cart.changed` | ID элемента и channel ID |
| `invoice`, `bank_invoice_document`: выставление, оплата, истечение, PDF | org плательщика; senior | `invoice.changed`, `admin.finance.changed`, `admin.counters.changed` | invoice ID; проведённый платёж отдельно меняет баланс через ledger |
| `ledger_account`, `hold`: зачисление, удержание, распределение, возврат, выплата | org счёта/рекламодателя; senior | `balance.changed`, связанные placement/order, `admin.finance.changed`, `admin.counters.changed` | balance без ID счёта; hold с родителями размещения |
| `ledger_transaction`, entry, bank transaction/statement, admin approval | senior | `admin.finance.changed`, `admin.counters.changed` | ID основания при наличии; суммы отсутствуют |
| `closing_act`: ежемесячный worker/CLI | org; senior | `closing_act.changed`, `admin.finance.changed`, `admin.counters.changed` | ID закрывающего документа |
| `payout`, RW workflow/operation/return evidence: worker, Webhook, перевод юрлицу | org блогера; рекламодатель для статуса размещения; admin/senior | **отдельно** `payout.changed`, `balance.changed`, `placement.changed`, `order.changed`, `admin.placement.changed`, `admin.finance.changed`, `admin.counters.changed` | payout/placement/order/campaign/channel при наличии |
| RW contractor/binding/onboarding | org блогера; senior | `rocketwork.changed`, `admin.finance.changed`, `admin.counters.changed` | UUID локальной записи, если есть; provider ID и URL не передаются |
| `rocketwork_withdrawal` | senior | `admin.finance.changed`, `admin.counters.changed` | Общая инвалидация служебного денежного состояния |
| ORD organization/contract/platform/creative/statistics/invoice/items/creatives/report, publication views snapshot | admin | `admin.ord.changed`, `admin.counters.changed` | UUID локального объекта, если есть; период отчёта не преобразуется в UUID; изменение готовности post/channel дополнительно создаёт их события |
| `channel_category`, `placement_format` | все авторизованные потоки | `category.changed`, `format.changed` | Общая инвалидация справочника; textual format ID не выдаётся как UUID |
| `audit_log` | senior | `admin.audit.changed`, `admin.counters.changed` | Без ID и содержимого записи журнала |

Каталог: broadcast создаётся при изменении активного канала с готовой площадкой
ОРД либо при уходе из такого состояния. Дополнительные фильтры (цена/формат/РКН,
поиск пользователя) всегда применяет обычный GET. Событие широкое, без UUID;
непубличные объекты не раскрываются через идентификаторы. Запись скрытого канала
на модерации не рассылается другим организациям.

## HTTP, proxy и конфигурация

Новых env-переменных, брокера и выделенных LISTEN-соединений нет. Нужны PostgreSQL
17 (как в тестах) и миграция 00107 **до** запуска нового API. Триггеры начинают
записывать изменения и от старых API/worker, пока идёт выпуск новой версии.
Права новых таблиц/sequence выдаёт существующая политика DEFAULT PRIVILEGES.
При полном отсутствии API очистка не запускается, журнал временно растёт.

Обычный Go `WriteTimeout: 60s` сохранён. SSE выставляет срок 5 секунд на отдельный
write/flush через `http.ResponseController`, затем снимает его на время ожидания.
Middleware сохраняют `Unwrap`; заголовки `Connection`/`Keep-Alive` не добавляются.
Существующие Caddy templates подходят без специального обхода admin matcher.
Caddy автоматически немедленно flush-ит `text/event-stream` ([документация](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming)).

## Проверка и включение фронтенда

```powershell
go generate ./internal/apigen
sqlc generate
sqlc vet
go vet ./...
go test -race -count=1 ./...

# Локальный opt-in долгого теста; в CI он включён автоматически.
$env:SSE_PROXY_TEST='1'
go test ./internal/bootstrap -run '^TestSSECaddyHTTP2LongStreamAndIPGate$' -v -count=1

# Отдельный модуль mockapi
cd mockapi
go generate ./...
go test -race -count=1 ./...
```

Целевые тесты: `internal/platform/realtime` (rollback, две независимые реплики,
поздний commit, handshake, reader recovery, epoch/overflow, аудит audience);
`internal/bootstrap/realtime_test.go` (настоящий Session/AdminGate, поддельный
audience в query, администратор без org, logout/блокировка/роль/кабинет/членство/
expiry, отсутствие продления, настоящий Worker → две API-сборки, heartbeat);
proxy-тест использует production Caddy template, HTTP/2, IP allow/deny и **135 секунд**
живого потока с девятью heartbeat при Go WriteTimeout 60s.

В соседнем `maxim-frontend`, отдельной задачей без правок из этого checkout:

```powershell
$env:OPENAPI_PATH='D:\work\maxim\api\openapi.yaml'
yarn api:types
# В окружении сборки фронтенда:
$env:VITE_SSE_ENABLED='true'
yarn verify
```

Сначала миграция/выпуск API и проверка cookie через его proxy, затем проверка
настоящего EventSource в трёх кабинетах и флаг фронтенда. Генерация типов и флаг
сами по себе не выкладывают backend. Push/deploy и включение общего стенда
в эту реализацию не входят. Откат фронтенда: `VITE_SSE_ENABLED=false` возвращает
существующий polling. Источник OpenAPI можно выбрать через OPENAPI_PATH, чтобы
генератор не взял другой соседний checkout `chanly`.

## Конкурентная запись поста

Текущий PUT черновика не имеет серверной revision/ETag и атомарного If-Match.
SSE позволяет обнаружить внешнее изменение, но между GET и PUT сохраняется гонка
последней записи. В этой задаче контроль версии не добавлен и гарантия защиты
от lost update не заявляется. Отдельное расширение должно добавить revision в
post, вернуть её/ETag в GET и выполнить атомарный `UPDATE ... WHERE revision = ...`
с увеличением revision; отсутствие совпадения возвращает 409/412, затрагивая
также удаление медиа и сохранение/создание версии, а не только редакторский PUT.

## См. также

- [Контракт OpenAPI](../api/openapi.yaml)
- [Эксплуатация и proxy](../deploy/app/README.md)
- [Mockapi](../mockapi/README.md)
