# 04. Модель данных

**Назначение.** Логическая схема данных: сущности, поля, связи, перечисления. По ней пишутся миграции и типы.

**Связанные документы.** [glossary.md](glossary.md) — названия; [03-placement.md](03-placement.md) — состояния и суммы размещения; [05-integrations.md](05-integrations.md) — внешние объекты; [06-finance-legal.md](06-finance-legal.md) — проводки.

**Затронутые записи реестра.** D-02, D-03, D-04, D-07, D-10, D-11, D-14, D-15, D-16, D-17, D-18, D-22, D-23, D-24, D-25, D-26, D-27, D-29, D-30, A-05, A-08, A-13, A-14, A-20, A-23, A-24, A-25, A-26, A-31, A-32, A-33, A-35, A-36, A-38, A-39, A-41, A-42, Q-33, Q-37, Q-38, Q-39.

## 1. Общие правила

- СУБД — PostgreSQL (A-26). Имена таблиц — в единственном числе, как идентификаторы в глоссарии.
- Первичный ключ каждой таблицы — `id uuid` (UUID версии 7, создаётся приложением). Исключения указаны явно.
- Каждая таблица имеет `created_at timestamptz not null`. Изменяемые — ещё и `updated_at timestamptz not null`. В таблицах полей ниже эти столбцы не повторяются.
- Время — `timestamptz` в UTC. Деньги — `bigint`, копейки, с ограничением `>= 0`, если не сказано иное.
- Перечисления хранятся как `text` с ограничением `check`; полный список значений — в §9.
- Внешние ключи объявляются всегда. Каскадного удаления нет.
- Удаление пользовательских данных — мягкое, полем `deleted_at`, и только там, где оно указано. Остальное не удаляется.
- **Неизменяемые таблицы** (только добавление, без `update` и `delete`): `placement_state_history`, `ledger_transaction`, `ledger_entry`, `audit_log`, `legal_acceptance`, `inbound_event` (кроме полей обработки), `outbound_request`. Запрет закрепляется правами роли базы данных.
- **Неизменяемые после фиксации строки**: `post_version` и `post_version_media` после перехода версии в `ready` — запрет закреплён триггером `post_version_ready_immutable` (запрет `UPDATE` и `DELETE` версии в `ready`, вставки, изменения и удаления её изображений); суммы и ставка комиссии `placement` после создания (I-05) — триггер `placement_amounts_immutable`; выход размещения из конечного состояния (I-04) — триггер `placement_terminal_guard`.
- Оптимистическая блокировка: у `placement`, `channel`, `payout`, `post_version` есть `lock_version integer not null default 0`, увеличивается при каждом обновлении.
- Объекты с отражением во внешней системе имеют поля синхронизации (§6.1).

Обозначения в таблицах: «Об.» — обязательность (`да` — `not null`).

## 2. Аккаунты и организации

```mermaid
erDiagram
    user ||--o{ session : has
    user ||--o{ organization_member : is
    organization ||--o{ organization_member : has
    organization ||--o| advertiser_profile : has
    organization ||--o| blogger_profile : has
    legal_document ||--o{ legal_acceptance : accepted
    user ||--o{ legal_acceptance : makes
    organization ||--o{ legal_acceptance : for
```

### user

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `email` | `citext` | да | Уникально. Единственный идентификатор для входа (A-13) |
| `admin_role` | `text` | нет | `moderator`, `senior`. Пусто — не администратор |
| `status` | `text` | да | `active`, `blocked` |
| `last_login_at` | `timestamptz` | нет | |
| `blocked_at`, `blocked_reason` | `timestamptz`, `text` | нет | Блокировка администратором (`POST /admin/users/{id}/block`) или при обезличивании (`personal_data_request`). Есть только у `blocked`; администратора заблокировать нельзя |
| `anonymized_at` | `timestamptz` | нет | Данные обезличены по запросу субъекта (`maxim admin pd anonymize`): почта заменена на `anonymized-<id>@anonymized.invalid`, снять блокировку нельзя |

Идентификатора пользователя MAX у `user` нет: привязки аккаунта MAX нет (D-25). Бот пишет блогеру по `channel.added_by_max_user_id` (A-41, Q-38).

### auth_code

Одноразовые коды: вход по почте и подключение канала (A-41).

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `purpose` | `text` | да | `login`, `channel_link` |
| `email` | `citext` | нет | Для `login` |
| `organization_id` | `uuid` | нет | Для `channel_link` — организация блогера, к которой привяжется канал |
| `code_hash` | `text` | да | HMAC-SHA256 кода с секретом окружения; сам код не хранится. Для `channel_link` назначение входит в хеш, поиск — по хешу среди живых кодов. У организации один живой код `channel_link`: новый гасит прежний |
| `expires_at` | `timestamptz` | да | 10 минут для `login`, 60 для `channel_link` |
| `attempts` | `integer` | да | Не более 5 |
| `consumed_at` | `timestamptz` | нет | Код использован или погашен новым кодом того же адреса |
| `requested_ip` | `inet` | нет | IP-адрес, с которого запрошен код: лимит 20 кодов на IP в час (07 §4) |

### session

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `user_id` | `uuid` | да | |
| `token_hash` | `text` | да | Уникально |
| `expires_at` | `timestamptz` | да | 30 суток от последнего использования |
| `ip`, `user_agent` | `inet`, `text` | нет | |
| `revoked_at` | `timestamptz` | нет | |

### organization

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `legal_type` | `text` | да | `legal_entity`, `sole_proprietor`, `self_employed` |
| `inn` | `text` | да | Уникально. 10 цифр для `legal_entity`, 12 для остальных; проверяется контрольное число |
| `name` | `text` | да | Официальное наименование или ФИО. Для `legal_entity` — сокращённое наименование, например «ООО «Ромашка»». Идёт в плашку, строку маркировки и ОРД |
| `contact_name` | `text` | да | |
| `phone` | `text` | да | |
| `kpp` | `text` | нет | 9 цифр. Только для `legal_entity` |
| `address` | `text` | нет | Юридический адрес для `legal_entity`, адрес регистрации для `sole_proprietor`. У `self_employed` не запрашивается |
| `tax_regime` | `text` | нет | Режим налогообложения (§9). Допустимые значения зависят от `legal_type` — таблица ниже |
| `vat_rate` | `text` | нет | Ставка НДС (§9). Допустимые значения зависят от `tax_regime` — таблица ниже |
| `tax_document_storage_key`, `tax_document_sha256` | `text` | нет | Файл документа, подтверждающего режим налогообложения |
| `signer_position`, `signer_name` | `text` | нет | Подписант: должность и ФИО. Только для `legal_entity` |
| `signer_basis_name`, `signer_basis_number` | `text` | нет | Документ, на основании которого действует подписант: название и номер. Только для `legal_entity` |
| `bank_account`, `bank_name`, `bank_corr_account`, `bank_bik` | `text` | нет | Банковские реквизиты рекламодателя: расчётный счёт, банк, корреспондентский счёт, БИК. Вводятся в разделе «Баланс», печатаются в счёте как реквизиты плательщика (D-26) |
| `is_platform` | `boolean` | да | `true` у единственной строки — самой платформы (ИП-владельца). Нужна как сторона договоров ОРД |
| `status` | `text` | да | `active`, `blocked` |

`address` заполняется при регистрации рекламодателя (S-01, 08 §4.2) и блогера (S-10, 08 §4.3) и обязателен для `sole_proprietor` и `legal_entity`. Поля `kpp`, `tax_regime`, `vat_rate`, файл документа и поля подписанта заполняются только у блогера (D-24) и обязательны в пределах его типа лица: `tax_regime`, `vat_rate` и файл документа — всем; `kpp` и поля подписанта — `legal_entity`. У организации, которая только рекламодатель, они пустые; если такая организация позже становится блогером, кабинет запрашивает их при подключении роли.

Допустимые сочетания — рабочие, проверить с бухгалтером (Q-37).

| `legal_type` | Допустимые `tax_regime` |
|---|---|
| `self_employed` | `npd` |
| `sole_proprietor` | `osno`, `usn`, `ausn`, `eshn`, `psn`, `npd` |
| `legal_entity` | `osno`, `usn`, `ausn`, `eshn` |

| `tax_regime` | Допустимые `vat_rate` |
|---|---|
| `osno` | `vat_22`, `none` (освобождение по ст. 145 НК) |
| `usn` | `none`, `vat_5`, `vat_7`, `vat_22` |
| `eshn` | `vat_22`, `none` (освобождение) |
| `ausn`, `psn`, `npd` | `none` |

### organization_member

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `organization_id`, `user_id` | `uuid` | да | Уникальная пара |
| `role` | `text` | да | `owner`. Других значений в первой версии нет (A-13) |

### advertiser_profile, blogger_profile

Первичный ключ — `organization_id`. Наличие строки означает, что у организации есть эта роль.

| Таблица | Поле | Тип | Об. | Пояснение |
|---|---|---|---|---|
| `advertiser_profile` | — | — | — | Дополнительных полей нет. Баланс берётся из счёта учёта |
| `blogger_profile` | `full_name` | `text` | нет | ФИО для обращения и выплат. Обязательно для `self_employed` и `sole_proprietor`; у `legal_entity` пусто |

Ограничение: `advertiser_profile` и `blogger_profile` допустимы при любом `legal_type` (D-24, D-14). Для блогера с `legal_entity` запись `rocketwork_contractor` не создаётся: выплаты идут вне Рокет Ворк (A-36).

### legal_document, legal_acceptance

| Таблица | Поле | Тип | Об. | Пояснение |
|---|---|---|---|---|
| `legal_document` | `kind` | `text` | да | `advertiser_offer`, `blogger_offer`, `platform_rules`, `privacy_policy` |
| | `version` | `integer` | да | Уникально в паре с `kind` |
| | `published_at` | `timestamptz` | да | |
| | `storage_key`, `sha256` | `text` | да | Файл документа |
| `legal_acceptance` | `legal_document_id`, `user_id`, `organization_id` | `uuid` | да | |
| | `accepted_at` | `timestamptz` | да | |
| | `ip` | `inet` | нет | |

Действующая версия документа — с наибольшим `version`. При выходе новой версии оформление заказа и принятие размещений требуют нового принятия (A-25). Повторное принятие той же версии организацией не создаёт второй строки.

До появления API администратора новую версию публикует служебная команда `maxim legal publish --kind <kind> --file <pdf>`: файл кладётся в закрытую область хранилища, `version` — наибольший для вида плюс один, действие записывается в `audit_log` с `actor_type = system`.

## 3. Каналы и каталог

```mermaid
erDiagram
    organization ||--o{ channel : owns
    channel_category ||--o{ channel : groups
    channel ||--o{ channel_metrics : measured
    channel ||--o{ channel_price : priced
    placement_format ||--o{ channel_price : for
    channel ||--o{ channel_unavailability : blocks
    channel ||--o| ord_platform : registered
```

### channel

Права бота и запись реестра РКН связаны с каналом один к одному, поэтому хранятся полями канала, а не отдельными таблицами.

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `organization_id` | `uuid` | нет | Владелец-блогер. Пусто у `unclaimed` |
| `max_chat_id` | `bigint` | да | Уникально |
| `title`, `max_description`, `icon_url`, `link` | `text` | нет | Из MAX, обновляются суточно и по событию `chat_title_changed` |
| `is_public` | `boolean` | да | Факт из MAX; не ограничивает подключение, модерацию или каталог |
| `added_by_max_user_id` | `bigint` | да | Кто добавил бота (из `bot_added`). Адресат личных сообщений бота блогеру (A-41) |
| `description` | `text` | нет | Описание для каталога, пишет блогер |
| `category_id` | `uuid` | нет | Обязательно для выхода из `moderation` |
| `status` | `text` | да | См. §9 |
| `status_reason` | `text` | нет | Код причины: `missing_rights`, `rkn_required`, текст отказа модерации. Исторический `not_public` снимается при обновлении канала |
| `bot_is_admin` | `boolean` | да | |
| `bot_permissions` | `text[]` | да | Список прав из MAX |
| `bot_permissions_checked_at` | `timestamptz` | нет | |
| `subscribers_count` | `integer` | да | Последнее значение `participants_count` |
| `rkn_number` | `text` | нет | Номер в реестре РКН (D-11) |
| `rkn_status` | `text` | да | `not_required`, `pending`, `confirmed`, `rejected` |
| `rkn_checked_at`, `rkn_checked_by` | `timestamptz`, `uuid` | нет | Кто и когда проверил вручную |
| `rating` | `smallint` | да | От 0 до 100, начальное 100. Уменьшается по решениям споров (A-08) |
| `status_before_disconnect` | `text` | нет | Состояние до удаления бота: по нему повторное добавление возвращает канал в `active` (05 §2.4) |
| `unclaimed_since` | `timestamptz` | нет | С какого момента канал ждёт кода подключения; через 7 суток бот уходит (A-41) |
| `moderated_at`, `moderated_by` | `timestamptz`, `uuid` | нет | |
| `ord_platform_ready_at` | `timestamptz` | нет | Площадка канала зарегистрирована в ОРД (принята или подтверждена). Каталог и корзина показывают канал только при непустом значении. Ставит модуль каналов по сигналу маркировки; при `ORD_ENABLED=false` — при одобрении |

Ограничение: канал может быть `active` только при `organization_id is not null`, правах `write` и `delete`, заполненной категории, хотя бы одной включённой цене и (`subscribers_count < 10000` или `rkn_status = 'confirmed'`). В базе ограничение закреплено проверкой `channel_active_requirements` и отложенным триггером-ограничением `channel_active_requires_price` на `channel` и `channel_price`: при фиксации транзакции у канала в `active` есть включённая цена активного формата. Порог РКН — параметр конфигурации `RKN_SUBSCRIBERS_THRESHOLD`, его проверяет модуль каналов.

### channel_connection

Краткоживущая сессия подключения принадлежит `organization_id` и инициатору
`user_id` кабинета. Основной путь сохраняет `channel_name`: название, @тег,
ссылку или ID MAX. `max_user_id` и `token_used_at` остаются NULL: подтверждение
личности MAX не требуется. Состояние `checking` переходит в `connected` или
`failed`, результат привязки — `channel_id`; истечение вычисляется по `expires_at`.
Имя неизменно в пределах сессии; refresh повторяет поиск с новым `scan_id`.

Для совместимости сессия без `channel_name` использует одноразовый `token_hash`,
`bot_started`, MAX-идентичность и таблицу `channel_connection_candidate`.
Оба варианта используют блокировку канала и не переписывают чужую организацию.

### channel_metrics

Суточный снимок; поля `id`, `created_at`, `updated_at`. Уникальная пара `(channel_id, date)`, дата — сутки по Москве. Повтор расчёта в те же сутки перезаписывает снимок. Снимки хранятся бессрочно (не больше 365 строк на канал в год); карточка и экран администратора показывают последние 30.

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `channel_id` | `uuid` | да | |
| `date` | `date` | да | |
| `subscribers_count` | `integer` | да | |
| `avg_views` | `integer` | нет | Средние просмотры поста; расчёт — 05 §2.7 |
| `engagement_bp` | `integer` | нет | `avg_views / subscribers_count`, в базисных пунктах |
| `posts_per_week` | `numeric(6,2)` | нет | Показывается в карточке канала, в строке каталога — нет (D-29) |
| `anomaly_flags` | `text[]` | да | `subscribers_spike`, `views_disproportion` |

### channel_category

`slug text unique`, `name text`, `position integer`, `is_active boolean`. Список фиксированный, меняется администратором (A-14).

### placement_format

Первичный ключ — `id text`. Значения — 03 §2.

| Поле | Тип | Об. |
|---|---|---|
| `id` | `text` | да |
| `name` | `text` | да |
| `top_hours` | `integer` | да |
| `duration_hours` | `integer` | да |
| `is_active` | `boolean` | да |
| `position` | `integer` | да |

### channel_price

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `channel_id`, `format_id` | `uuid`, `text` | да | Уникальная пара |
| `amount` | `bigint` | да | Цена блогера без комиссии. От 10 000 до 1 000 000 000 копеек |
| `is_enabled` | `boolean` | да | |

Также `id`, `created_at`, `updated_at`. Набор цен канала заменяется целиком (S-13); строки удаляются.

### channel_unavailability

`id uuid`, `channel_id uuid`, `starts_at timestamptz`, `ends_at timestamptz`, `created_at timestamptz`, проверка `starts_at < ends_at`. Интервалы, в которые блогер не принимает публикации (S-13). `publish_at` не может попасть в такой интервал. Закончившиеся интервалы удаляет задание `cleanup` через 30 суток после окончания.

## 4. Медиатека и посты

```mermaid
erDiagram
    organization ||--o{ media_file : owns
    media_file ||--o{ media_derivative : produces
    organization ||--o{ campaign : owns
    campaign ||--|| post : has
    post ||--o{ post_version : has
    post_version ||--o{ post_version_media : includes
    media_file ||--o{ post_version_media : source
    media_derivative ||--o{ post_version_media : sent
    post_version ||--o| creative : registered
```

### media_file

Оригинал. Содержимое не меняется (D-04).

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `organization_id` | `uuid` | да | |
| `storage_key` | `text` | да | Ключ в объектном хранилище. Уникально |
| `mime` | `text` | да | `image/jpeg`, `image/png`, `image/webp` |
| `size_bytes` | `bigint` | да | Не более 50 МБ |
| `width`, `height` | `integer` | да | Не более 7680 каждое. Размеры после поворота по EXIF |
| `sha256` | `text` | да | Уникально в организации среди неудалённых: повторная загрузка того же файла возвращает существующую запись |
| `original_name` | `text` | да | Очищено от пути и управляющих символов, не длиннее 255; в журнал не пишется |
| `created_by` | `uuid` | да | |
| `deleted_at` | `timestamptz` | нет | Мягкое удаление. Файл, использованный в версии поста, из хранилища не удаляется |
| `purged_at` | `timestamptz` | нет | Объект удалён из хранилища заданием `purge_media` (07 §2) |

### media_derivative

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `media_file_id` | `uuid` | да | |
| `kind` | `text` | да | `labeled` — с плашкой; `plain` — копия для публикации без плашки; `preview` — уменьшенная |
| `params_hash` | `text` | да | sha256 параметров построения: вид, версия шаблона, для `labeled` — название и ИНН. Уникально в тройке с `media_file_id`, `kind` |
| `status` | `text` | да | `pending` — ждёт задания `build_media_derivative`; `ready` — построена |
| `storage_key` | `text` | нет | Ключ в закрытой области. Обязателен в `ready` |
| `mime` | `text` | нет | `image/jpeg` или `image/png` (06 §6.2). Обязателен в `ready` |
| `public_storage_key` | `text` | нет | Ключ копии в публичной области (07 §4). Есть только у производных, вошедших в версию поста |
| `public_url` | `text` | нет | Постоянная публичная ссылка. Появляется, когда производная вошла в версию поста: её получает ОРД как образец (05 §3.4) |
| `published_at` | `timestamptz` | нет | Когда копия опубликована. Опубликованное не удаляется |
| `width`, `height`, `size_bytes` | | нет | Обязательны в `ready` |
| `purged_at` | `timestamptz` | нет | Объект удалён заданием `purge_media`; запись снова в `pending` и строится при следующем запросе |

### campaign

Кампания: наименование, единый пост для всех каналов и общие требования к публикации (D-16, D-17).

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `organization_id` | `uuid` | да | |
| `name` | `text` | да | Наименование кампании, от 1 до 200 символов |
| `status` | `text` | да | `draft` — до оплаты; `ordered` — оплачена, есть заказ; `archived`. В кампании может быть несколько заказов; в первой версии — один |
| `date_from`, `date_to` | `date` | нет | Период дат публикации (A-39). Обязательны к оплате |
| `time_from`, `time_to` | `time` | нет | Период времени суток, МСК (A-39). Обязательны к оплате |
| `extra_requirements` | `text` | нет | «Дополнительные требования» — комментарий исполнителю, не длиннее 2000 |
| `order_id` | `uuid` | нет | Заказ кампании; есть с `ordered` (проверка `campaign_ordered_has_order`). Пишет модуль `post` по вызову оформления заказа |
| `created_by` | `uuid` | да | |

Ограничения: `date_from <= date_to`, `time_from < time_to`, время задаётся парой. Проверки формы (A-39): период — не длиннее 14 суток и не раньше сегодняшнего дня по Москве, окно времени — не короче 1 часа. Переходы: `draft → ordered` при оформлении заказа; `ordered → archived`, когда заказ становится `completed` или `cancelled`. Период и требования меняются только в `draft`; в `archived` черновик поста не сохраняется, название меняется всегда.

### post

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `organization_id` | `uuid` | да | |
| `campaign_id` | `uuid` | да | Уникально: у кампании один пост (D-16) |
| `status` | `text` | да | `draft`, `archived` |
| `draft` | `jsonb` | да | Текущее редактируемое содержимое: документ редактора, идентификаторы файлов по порядку, место токена. Формат с номером версии (`v`) — у `post/postgres` |

### post_version

Снимок поста. Создаётся при оформлении заказа и при каждой правке после него (03 §9). После перехода в `ready` не изменяется (D-03).

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `post_id` | `uuid` | да | |
| `number` | `integer` | да | Уникально в паре с `post_id`, с 1 |
| `status` | `text` | да | `pending_moderation`, `rejected`, `approved`, `ready` (03 §1.1) |
| `content_doc` | `jsonb` | да | Документ редактора |
| `body` | `text` | да | Текст в формате API MAX без строки маркировки, с UTM-метками заказа. Не более 3800 кодовых единиц UTF-16 вместе с разметкой (A-38, 05 §2.5) |
| `final_text` | `text` | нет | Итоговый текст: `body` и строка маркировки по месту токена (05 §2.5). Заполняется при переходе в `ready`. Не более 4000 символов |
| `token_carrier` | `text` | да | Где пометка «Реклама» с названием и ИНН: `text` или `media` (D-18, A-38). По умолчанию `media`, без изображений — `text` (D-30) |
| `token_position` | `text` | да | Где erid: `link`, `above`, `below` (D-18, A-38). По умолчанию `below` (D-30) |
| `erid` | `text` | нет | erid креатива версии. Заполняется вместе с `final_text` при переходе в `ready` |
| `advertiser_name`, `advertiser_inn` | `text` | да | Снимок реквизитов на момент создания версии |
| `moderated_by`, `moderated_at` | | нет | |
| `rejection_reason` | `text` | нет | |
| `created_by` | `uuid` | да | |

### post_version_media

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `post_version_id` | `uuid` | да | |
| `position` | `integer` | да | С 0. Уникально в паре с версией. Альбом (D-10): не более 10 строк на версию (A-23) |
| `media_file_id` | `uuid` | да | Оригинал |
| `media_derivative_id` | `uuid` | да | Файл, который уходит в канал: `labeled` или `plain` |

## 5. Заказы и размещения

```mermaid
erDiagram
    organization ||--o{ cart_item : has
    organization ||--o{ order : places
    campaign ||--o{ order : groups
    post ||--o{ order : used
    order ||--o{ placement : contains
    channel ||--o{ placement : hosts
    post_version ||--o{ placement : shown
    placement ||--o{ placement_state_history : logs
    placement ||--o| publication : results
    placement ||--o{ violation : has
    placement ||--o| claim : has
    placement ||--o| dispute : has
    dispute ||--o{ dispute_message : has
```

### cart_item

Корзина — набор строк организации; отдельной таблицы `cart` нет.

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `organization_id`, `channel_id` | `uuid` | да | Уникальная пара |
| `format_id` | `text` | да | |
| `shown_total_amount` | `bigint` | да | Итог, показанный рекламодателю при добавлении или подтверждении цены. Сравнивается при оформлении |
| `created_by` | `uuid` | да | |

### order

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `number` | `bigint` | да | Человекочитаемый номер, уникальный, из последовательности `order_number_seq` с 1001. Берётся до создания версии поста (UTM-метки) |
| `organization_id` | `uuid` | да | Рекламодатель |
| `campaign_id` | `uuid` | да | В первой версии у кампании один заказ (D-16): уникально |
| `post_id` | `uuid` | да | |
| `current_post_version_id` | `uuid` | да | Последняя версия |
| `status` | `text` | да | Производное (03 §1.2): `moderation`, `rejected`, `marking`, `in_progress`, `completed`, `cancelled` |
| `total_amount` | `bigint` | да | Сумма `total_amount` размещений при создании |
| `idempotency_key` | `text` | нет | Ключ оформления (03 §11); уникальна пара `(organization_id, idempotency_key)` |
| `post_version_idempotency_key` | `text` | нет | Ключ последней правки поста после оформления (S-29, 03 §11) |
| `created_by` | `uuid` | да | |

Имя таблицы — `"order"` (в SQL в кавычках). Индекс `(organization_id, created_at DESC, id DESC)` — список заказов.

### placement

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `order_id`, `channel_id` | `uuid` | да | Уникальная пара. Размещение — единица работы (D-02) |
| `blogger_organization_id` | `uuid` | да | Снимок владельца канала при создании |
| `format_id` | `text` | да | |
| `top_hours`, `duration_hours` | `integer` | да | Снимок формата |
| `window_start`, `window_end` | `timestamptz` | да | Снимок периода дат кампании: начало первого и конец последнего дня |
| `time_from`, `time_to` | `time` | да | Снимок периода времени суток кампании (D-17) |
| `post_version_id` | `uuid` | да | Версия, с которой выйдет или вышло размещение |
| `status` | `text` | да | 03 §3 |
| `status_changed_at` | `timestamptz` | да | |
| `status_reason` | `text` | нет | Код причины последнего перехода |
| `sent_to_blogger_at` | `timestamptz` | нет | Первый P-02: с него размещение видно блогеру (07 §4) |
| `fee_rate_bp` | `integer` | да | Ставка комиссии на момент создания |
| `blogger_amount`, `fee_amount`, `total_amount` | `bigint` | да | Неизменяемы (I-05) |
| `accept_deadline_at` | `timestamptz` | нет | T-01 |
| `schedule_deadline_at` | `timestamptz` | нет | T-13 |
| `publish_at`, `remove_at` | `timestamptz` | нет | Заполняются при P-17 |
| `published_at`, `removed_at` | `timestamptz` | нет | Факт |
| `claim_deadline_at` | `timestamptz` | нет | T-05 |
| `performance_share` | `integer` | нет | От 0 до 10000 (03 §7) |
| `payout_amount`, `fee_charged`, `refund_amount` | `bigint` | нет | Заполняются при распределении удержания (I-12). Проверка `placement_distribution_sum`: в `accepted`, `paid`, `refunded` все три заполнены и в сумме дают `total_amount` |
| `final_views` | `integer` | нет | Просмотры на момент снятия |
| `lock_version` | `integer` | да | |

Индексы: `(channel_id, status)`, `(status, accept_deadline_at)`, `(order_id)`, `(blogger_organization_id, created_at DESC, id DESC)` для отправленных блогеру, `publish_at` для `scheduled` (07 §3). Проверки: `total_amount = blogger_amount + fee_amount`, `window_start < window_end`, `time_from < time_to`, `publish_at` и `remove_at` задаются парой, `scheduled` — только с `publish_at`. I-06 проверяется в транзакции с блокировкой строки канала. Порядок блокировок модуля: каналы (по идентификатору) → заказ → размещения.

### placement_state_history

Поля — 03 §12: `placement_id`, `transition` (`P-NN`), `from_status`, `to_status`, `actor_type`, `actor_user_id`, `reason_code`, `reason_text`, `job_id`, `inbound_event_id`, `outbound_request_id`, `idempotency_key` (уникальна пара `(placement_id, idempotency_key)`, 03 §11). Неизменяема: роль приложения только добавляет строки. Источники — без внешних ключей: задания и запросы очищаются по сроку хранения.

### publication

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `placement_id` | `uuid` | да | Уникально (I-11) |
| `channel_id` | `uuid` | да | |
| `max_message_id` | `text` | да | Уникально |
| `url` | `text` | нет | Публичная ссылка на пост из ответа MAX |
| `published_at` | `timestamptz` | да | |
| `removed_at` | `timestamptz` | нет | |
| `removal_reason` | `text` | нет | `schedule`, `external`, `admin`, `refund` |
| `removal_overdue` | `boolean` | да | Удаление по сроку не удалось (T-10) |
| `views` | `integer` | нет | Последнее считанное значение |
| `views_checked_at` | `timestamptz` | нет | |
| `content_hash` | `text` | да | Хеш сообщения в том виде, в каком его хранит MAX: нормализованный текст, разметка и вложения по порядку (05 §2.6). Сравнивается при `message_edited` и сверке |

Индексы: живые публикации (`removed_at IS NULL`) — по `id` для сверки и по `channel_id` для контроля топа; `removal_overdue` — для поиска зависших (07 §3). Своё удаление записывает `removed_at` с причиной `schedule` до вызова MAX; при ошибке запись откатывается и ставится `removal_overdue`.

### violation

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `placement_id` | `uuid` | да | |
| `kind` | `text` | да | `removed_early`, `content_modified`, `top_violated`, `bot_access_lost` |
| `occurred_at` | `timestamptz` | да | Когда произошло |
| `source` | `text` | да | `event`, `reconciliation` |
| `inbound_event_id` | `uuid` | нет | |
| `details` | `jsonb` | да | Например, идентификатор постороннего сообщения. Текста сообщений нет |

Неизменяема: роль приложения только добавляет строки. Пара `(placement_id, inbound_event_id)` уникальна для нарушений из событий: повтор события второй записи не создаёт.

### claim

`placement_id uuid unique`, `reason text` (`removed_early`, `top_violated`, `content_mismatch`, `other`), `text text` (до 2000 символов), `created_by uuid`. Таблица — миграция `00013_disputes.sql`: текст от 1 до 2000 символов (проверка БД), роль приложения только добавляет строки (`UPDATE`, `DELETE` отозваны). Текст претензии видят рекламодатель и администратор; в журнал приложения он не пишется.

### dispute

Решение по спору (`dispute_resolution`) хранится полями этой же таблицы.

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `placement_id` | `uuid` | да | Уникально (I-11) |
| `reason` | `text` | да | 03 §6 |
| `opened_by_type` | `text` | да | `advertiser`, `system`, `admin` |
| `claim_id`, `violation_id` | `uuid` | нет | Что стало основанием; внешние ключи на `claim` и `violation` |
| `deadline_at` | `timestamptz` | да | T-06 |
| `suggested_share` | `integer` | нет | Предложение системы (03 §7) |
| `resolution` | `text` | нет | `full_payout`, `proportional`, `full_refund` |
| `performance_share` | `integer` | нет | Итоговая доля; проверка БД: `full_payout` — 10000, `full_refund` — 0, `proportional` — 1…9999 |
| `resolution_comment` | `text` | нет | Обязателен при решении, до 2000 символов; сторонам не показывается |
| `resolved_by`, `resolved_at` | | нет | Заполнены вместе с `resolution`. Решение окончательное: обновляется только строка без решения; удаление споров у роли приложения отозвано |

### dispute_message

`dispute_id uuid`, `author_type text` (`admin`, `advertiser`, `blogger`), `author_user_id uuid`, `recipient text` (только у сообщения администратора: `advertiser`, `blogger`, `both`), `text text` (до 2000 символов), `created_at`. Переписка по спору (`POST /admin/disputes/{id}/messages`); неизменяема, в `audit_log` не дублируется. Текст в журнал приложения не пишется.

## 6. Маркировка

```mermaid
erDiagram
    organization ||--o| ord_organization : mirrored
    organization ||--o{ ord_contract : party
    ord_contract ||--o{ creative : basis
    post_version ||--o| creative : is
    channel ||--o| ord_platform : is
    creative ||--o{ ord_statistics : reported
    ord_platform ||--o{ ord_statistics : at
    ord_contract ||--o{ ord_invoice : billed
    ord_invoice ||--o{ ord_invoice_item : splits
    creative ||--o{ ord_invoice_item : for
```

### 6.1. Поля синхронизации

Есть у `ord_organization`, `ord_contract`, `ord_platform`, `creative`, `ord_statistics`, `ord_invoice`.

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `ord_external_id` | `text` | нет | Идентификатор объекта в ОРД — наш идентификатор: у контрагента `organization.id`, у договора и креатива — `id` записи, у площадки — `channel.id`. ОРД регистрирует или обновляет объект по нему, поэтому повтор после неизвестного исхода — обычное обновление |
| `sync_status` | `text` | да | `pending` — ждёт отправки; `sent` — отправлено, ждём подтверждения; `synced` — подтверждено; `error` — требует вмешательства (S-20) |
| `sync_request_id` | `text` | нет | `reqid` для опроса статуса |
| `sync_error` | `text` | нет | Последняя ошибка (до 2000 символов) |
| `sync_attempts` | `integer` | да | Число вызовов ОРД, включая неудачные |
| `synced_at` | `timestamptz` | нет | |
| `payload_hash` | `text` | нет | SHA-256 канонического JSON отправленного запроса — чтобы понять, нужна ли повторная отправка |
| `ord_status` | `text` | нет | Последний статус ОРД как есть (`Request accepted by ORD`, `ORD success`, …, 05 §3.5) |
| `sent_at` | `timestamptz` | нет | Время отправки: расписание опроса, таймаут 24 ч, зависшие. Обязательно в `sent` и `synced` |

Переходы: `pending` → (запрос принят) `sent` → (финальный успех) `synced`; финальная ошибка, отказ `4xx`/`422`, исчерпание повторов или таймаут статуса → `error`. Из `error` в `pending` — только «Повторить» (S-20) или новая нагрузка; из `synced` в `pending` — новая нагрузка (кроме креатива: после erid он не отправляется, D-03). Зависимость готова для зависимых объектов, когда она `synced` или `sent` со статусом `ORD success` либо `ERIR sync success` (05 §3.2, Q-40).

### Сущности

| Таблица | Поля сверх синхронизации | Уникальность |
|---|---|---|
| `ord_organization` | `organization_id uuid`; `is_rr boolean` (организация-блогер, рекламораспространитель) | `organization_id` |
| `ord_contract` | `kind text` (`initial`, `publisher`); `client_organization_id`, `contractor_organization_id uuid`; `number text`; `signed_date date` | `(kind, client_organization_id, contractor_organization_id)` |
| `ord_platform` (A-32) | `channel_id uuid`; `organization_id uuid` (блогер); `name text`; `url text` | `channel_id` |
| `creative` (D-07) | `post_version_id uuid`; `ord_contract_id uuid` (изначальный, задаётся при отправке); `erid text`; `erid_received_at timestamptz`; `erir_status text` (`pending`, `success`, `error`); `erir_error text` | `post_version_id`; `erid` |
| `ord_statistics` | `creative_id`, `ord_platform_id`, `placement_id uuid`; `period date` (первое число месяца); `impressions integer`; `shown_from`, `shown_to date`; `amount bigint` | `(creative_id, ord_platform_id, period)` |
| `ord_invoice` | `ord_contract_id uuid`; `period date`; `type text` (тип акта ОРД, конфигурация); `number text` (`MX-ГГГГММ-N`); `invoice_date date` (последний день месяца); `amount bigint` (> 0) | `(ord_contract_id, period)`; `number` |
| `ord_invoice_item` | `ord_invoice_id`, `creative_id`, `ord_platform_id uuid`; `amount bigint` | `(ord_invoice_id, creative_id, ord_platform_id)` |

Строки объектов ОРД не удаляются — кроме креатива, который не отправлялся (версия стала `ready` в обход маркировки — задание креатива удаляет его запись), и неотправленной строки статистики, пропавшей при повторном формировании отчёта (отправленная обнуляется). Разбивка акта `ord_invoice_item` заменяется целиком при изменении.

Ежемесячная отчётность (05 §3.6, §3.7):

| Таблица | Поля | Уникальность |
|---|---|---|
| `publication_views_snapshot` | `publication_id uuid`; `period date` (месяц, на начало которого снимок); `views integer`; `views_checked_at`; `created_at`. Неизменяема (нет `UPDATE`, `DELETE`) | `(publication_id, period)` |
| `ord_report` | `period date`; `built_at`, `built_by uuid` (пусто — задание); `send_requested_at`, `send_requested_by uuid`; `updated_at`. Состояние для экрана считается по строкам: `not_built`, `built`, `sending`, `sent`, `has_errors`. Без `DELETE` | `period` |

Договор `initial`: клиент — рекламодатель, исполнитель — платформа. Договор `publisher`: клиент — платформа, исполнитель — блогер (A-31). Параметры договоров для ОРД — A-33.

Справочника категорий рекламы нет: категорий в первой версии нет (D-27). Код ККТУ для регистрации креатива в ОРД берётся из конфигурации, один для всех креативов (A-42, Q-39).

## 7. Деньги

```mermaid
erDiagram
    organization ||--o{ invoice : billed
    bank_statement ||--o{ bank_transaction : contains
    invoice ||--o{ bank_transaction : paid_by
    ledger_transaction ||--|{ ledger_entry : consists
    ledger_account ||--o{ ledger_entry : debit_or_credit
    placement ||--o{ hold : reserves
    placement ||--o{ payout : pays
    organization ||--o| rocketwork_contractor : mirrored
    rocketwork_contractor ||--o{ payout : receives
    organization ||--o{ closing_act : receives
```

### invoice

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `number` | `text` | да | `MX-` и не меньше 6 цифр из `invoice_number_seq` (R-1). Уникально. Указывается в назначении платежа |
| `organization_id` | `uuid` | да | |
| `amount` | `bigint` | да | |
| `status` | `text` | да | `issued`, `paid`, `expired`, `cancelled` |
| `expires_at` | `timestamptz` | да | 14 суток от выставления |
| `paid_at`, `paid_amount` | | нет | Фактическая сумма может отличаться. Заполнены вместе и только в `paid` |
| `payer_name`, `payer_inn` | `text` | да | Снимок плательщика на момент выставления |
| `payer_bank_account`, `payer_bank_name`, `payer_bank_bik`, `payer_bank_corr_account` | `text` | нет | Снимок `organization.bank_*` |
| `created_by` | `uuid` | нет | Пользователь, выставивший счёт |
| `cancel_reason` | `text` | нет | Обязательна в `cancelled` |
| `pdf_storage_key` | `text` | да | PDF формируется при выставлении |

В PDF счёта как реквизиты плательщика печатаются наименование, ИНН и банковские реквизиты организации на момент выставления (`payer_*` — снимок `organization.bank_*`, D-26). Если банковские реквизиты не сохранены, в счёте только наименование и ИНН. Позднее изменение реквизитов организации на выставленный счёт не влияет.

### bank_statement, bank_transaction

| Таблица | Поле | Тип | Об. | Пояснение |
|---|---|---|---|---|
| `bank_statement` | `uploaded_by`, `storage_key` | | да | Загруженный файл выписки (A-35) |
| | `file_sha256` | `text` | да | SHA-256 файла. Уникально — тот же файл повторно не загружается (R-12) |
| | `account_number` | `text` | да | Расчётный счёт выписки; равен `PLATFORM_BANK_ACCOUNT` |
| | `date_from`, `date_to` | `date` | нет | Период выписки |
| | `opening_balance`, `closing_balance` | `bigint` | нет | Остатки из раздела счёта; `closing_balance` последней выписки сверяется со счётом `bank` (06 §2.4) |
| | `incoming_count`, `matched_count`, `unmatched_count`, `duplicate_count` | `integer` | да | Итоги загрузки |
| `bank_transaction` | `bank_statement_id` | `uuid` | да | |
| | `external_id` | `text` | да | SHA-256 от `ДатаПоступило\|Номер\|Сумма\|ПлательщикИНН\|ПлательщикСчет` (05 §5). Уникально — защита от повторного зачисления |
| | `operation_date` | `date` | да | |
| | `amount` | `bigint` | да | |
| | `payer_inn`, `payer_name`, `payer_account`, `purpose` | `text` | нет | |
| | `match_status` | `text` | да | `matched`, `unmatched`, `ignored` |
| | `invoice_id`, `organization_id` | `uuid` | нет | Результат сопоставления |
| | `matched_by` | `uuid` | нет | Пусто — сопоставлено автоматически |
| | `ignored_reason` | `text` | нет | Обязательна в `ignored` |

В `matched` всегда заполнена `organization_id`. Из `unmatched` и `ignored` администратор переводит операцию в `matched` вручную (R-13).

### ledger_account

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `kind` | `text` | да | 06 §2.1 |
| `organization_id` | `uuid` | нет | Для `advertiser_balance`, `blogger_payable` |
| `placement_id` | `uuid` | нет | Для `placement_hold` |
| `balance` | `bigint` | да | Кеш суммы проводок. Обновляется в транзакции с проводкой. Проверка `ledger_account_liability_balance`: у обязательств (все виды, кроме `bank`, `rocketwork_deposit`, `payout_fees`, `diagnostic_expense`) не меньше нуля |

Уникальность: `(kind, organization_id, placement_id)` с учётом пустых значений.

### Контрольные данные диагностики RocketWork

Модуль `rocketworkcheck` хранит `rw_check_account` и aliases канонического физического аккаунта; immutable `rw_check_grant` с оператором/сроком/отзывом/политикой; immutable terms/hash/ownership в `rw_check_run`; уникальные action intent/checkpoint в `rw_check_action`. Номинал и fee ceiling резервируются один раз, actual nominal/fee и ledger_id фиксируются вместе с settlement. Reserved_at определяет день счётчика новых операций, settled_at — trusted UTC-день контрольного расхода; provider_paid_at, если доступен, хранит отдельную дату провайдера. Она не разрешает менять контрольный день.

Отдельные `rw_check_legacy_import` и `rw_check_audit` сохраняют сверенный старый task и операторские переходы. Новый owner-issued grant может быть привязан как active_grant_id командой reauthorize, не меняя исходные terms, manifest hash, task ID, резерв и intent. Прямые UPDATE/DELETE/TRUNCATE закрыты runner/app-ролям; обычный dispatcher читает только минимальные ownership views. Подробности — [операторский процесс](launch/integration-checks.md#контролируемый-прогон-onetime); SQL-источник — миграции 22–33.

### ledger_transaction, ledger_entry

Неизменяемы.

| Таблица | Поле | Тип | Об. | Пояснение |
|---|---|---|---|---|
| `ledger_transaction` | `kind` | `text` | да | Вид операции (06 §2.3) |
| | `placement_id`, `invoice_id`, `payout_id`, `bank_transaction_id` | `uuid` | нет | Основание; заполняется подходящее. `payout_id` — внешний ключ на `payout` (с этапа выплат) |
| | `actor_type`, `actor_user_id` | | да / нет | `system`, `admin`, `advertiser` |
| | `reason` | `text` | нет | Обязателен для `manual` и `balance_return` |
| | `idempotency_key` | `text` | да | Уникально. Формат по виду операции — 06 §2.2 (R-3) |
| | `entries_hash` | `text` | да | SHA-256 канонического списка проводок. Повтор с тем же ключом и другим списком — ошибка (R-2) |
| `ledger_entry` | `ledger_transaction_id` | `uuid` | да | |
| | `debit_account_id`, `credit_account_id` | `uuid` | да | Разные счета |
| | `position` | `smallint` | да | Порядок внутри транзакции; уникален в паре с `ledger_transaction_id` |
| | `amount` | `bigint` | да | Строго больше нуля |

### hold

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `placement_id` | `uuid` | да | Частичный уникальный индекс при `status = 'active'` (I-03) |
| `post_version_id` | `uuid` | да | Версия поста, под которую удержаны деньги |
| `organization_id` | `uuid` | да | Рекламодатель |
| `amount` | `bigint` | да | Равно `placement.total_amount` |
| `status` | `text` | да | `active`, `released` — снято целиком; `distributed` — распределено при приёмке или решении спора |
| `closed_at` | `timestamptz` | нет | Пусто ровно в `active` |

### rocketwork_contractor

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `organization_id` | `uuid` | да | Уникально |
| `external_id` | `text` | нет | Идентификатор исполнителя в Рокет Ворк |
| `status` | `text` | да | `invited`, `pending`, `active`, `blocked` |
| `blocked_reason` | `text` | нет | Только при `blocked`; наш код (`tax_status_lost` — утрачен статус самозанятого), адаптер приводит к нему ответ сервиса |
| `onboarding_url` | `text` | нет | Для блогера; в журнал выше DEBUG не пишется |
| `status_checked_at` | `timestamptz` | нет | |
| `last_error` | `text` | нет | |

### payout

Одна строка — одна попытка выплаты.

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `placement_id` | `uuid` | да | Частичный уникальный индекс при `status` из `created`, `sent`, `succeeded` — не больше одной живой выплаты |
| `blogger_organization_id` | `uuid` | да | Получатель |
| `method` | `text` | да | `rocketwork` — через сервис; `bank_transfer` — юрлицу переводом вне сервиса (A-36) |
| `rocketwork_contractor_id` | `uuid` | нет | Заполнено тогда и только тогда, когда `method = rocketwork` |
| `amount` | `bigint` | да | Равно `placement.payout_amount` |
| `attempt` | `integer` | да | С 1 |
| `status` | `text` | да | `created`, `sent`, `succeeded`, `failed` |
| `external_id` | `text` | нет | Идентификатор в Рокет Ворк |
| `provider_fee` | `bigint` | нет | Комиссия Рокет Ворк за выплату |
| `receipt_url` | `text` | нет | Чек |
| `bank_reference` | `text` | нет | Номер платёжного поручения для `bank_transfer` |
| `failure_code`, `failure_message` | `text` | нет | Код — из 05 §4.3; заполнен тогда и только тогда, когда `failed` |
| `sent_at`, `completed_at` | `timestamptz` | нет | |
| `idempotency_key` | `text` | да | Уникально. `rw-payout:{payout_id}`: у каждой попытки свой. Передаётся в Рокет Ворк |
| `created_by` | `uuid` | нет | Администратор, создавший попытку повтором; пусто — система |
| `lock_version` | `integer` | да | |

Заявок на вывод остатка нет (D-26): возврат остатка — проводка `balance_return`, которую администратор вводит по обращению в поддержку (06 §2.3).

### closing_act

`organization_id uuid`, `period date` (первое число месяца), `number text unique` (`А-` и 6 цифр из `closing_act_number_seq`), `placements_amount bigint` (услуги блогеров), `fee_amount bigint` (вознаграждение платформы), `report_pdf_storage_key text` (отчёт агента, `closing/{org}/{YYYY-MM}-report.pdf`), `act_pdf_storage_key text` (акт, `closing/{org}/{YYYY-MM}-act.pdf`). Уникальная пара `(organization_id, period)` (A-20, R-16).

## 8. Служебные

### job

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `kind` | `text` | да | Вид задания (07 §2) |
| `dedupe_key` | `text` | нет | Уникально среди заданий в `pending` и `running` |
| `payload` | `jsonb` | да | |
| `run_at` | `timestamptz` | да | |
| `status` | `text` | да | `pending`, `running`, `succeeded`, `failed`, `cancelled` |
| `attempts`, `max_attempts` | `integer` | да | |
| `last_error` | `text` | нет | |
| `locked_by`, `locked_at` | | нет | Какой экземпляр обработчика взял задание |
| `finished_at` | `timestamptz` | нет | |

Индекс: `(status, run_at)`.

### inbound_event

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `source` | `text` | да | `max`, `rocketwork` |
| `external_key` | `text` | да | Ключ события во внешней системе. Уникально в паре с `source` |
| `event_type` | `text` | да | |
| `payload` | `jsonb` | да | Сырое тело |
| `status` | `text` | да | `received`, `processed`, `ignored`, `failed` |
| `processed_at`, `error` | | нет | |

### outbound_request

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `system` | `text` | да | `max`, `yandex_ord`, `rocketwork` |
| `operation` | `text` | да | Имя операции адаптера |
| `idempotency_key` | `text` | нет | |
| `attempt` | `integer` | да | |
| `request` | `jsonb` | да | Без токенов и заголовков авторизации |
| `response_status` | `integer` | нет | |
| `response_body` | `jsonb` | нет | |
| `duration_ms` | `integer` | нет | |
| `error` | `text` | нет | |
| `object_type`, `object_id` | | нет | К чему относится |

Хранится 180 суток, затем удаляется заданием очистки.

### rate_limit_bucket

Корзина токенов общего ограничителя запросов (05 §2.8). Одна на все экземпляры приложения.

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `key` | `text` | да | Первичный ключ: `max:global`, `max:chat:{chat_id}`, `max:user:{user_id}` |
| `tokens` | `double precision` | да | Остаток токенов на момент `refilled_at` |
| `refilled_at` | `timestamptz` | да | |

### outbound_circuit

Предохранитель по внешней системе (05 §1), общий для всех экземпляров.

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `system` | `text` | да | Первичный ключ: `max`, `yandex_ord`, `rocketwork` |
| `consecutive_failures` | `integer` | да | Ошибок подряд; успешный ответ или отказ `4xx` обнуляет |
| `open_until` | `timestamptz` | нет | До какого времени вызовы приостановлены |

### alert

Оповещения администратору (07 §3). Одна строка на пару «причина + объект».

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `reason` | `text` | да | Код причины: `job_failed:{kind}`, `circuit_open:{system}`, `outbound_failed:{system}:{operation}`, `downtime`, `stuck:{проверка}` |
| `object_key` | `text` | да | Объект причины; пустая строка — причина целиком. Уникально в паре с `reason` |
| `level` | `text` | да | `warn`, `error` |
| `summary` | `text` | да | Текст без персональных данных |
| `first_raised_at`, `last_raised_at` | `timestamptz` | да | |
| `last_sent_at` | `timestamptz` | нет | Последняя отправка: следующая — не раньше чем через час |
| `raised_count` | `integer` | да | |
| `resolved_at` | `timestamptz` | нет | Причина устранена (например, зависший объект обработан) |

Закрытые оповещения хранятся 90 суток.

### worker_heartbeat

Отметки экземпляров `worker`: по разрыву в отметках определяется простой (07 §2).

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `instance` | `text` | да | Первичный ключ: хост и номер процесса |
| `started_at`, `seen_at` | `timestamptz` | да | Отметка — раз в 30 секунд |

Сроки хранения служебных таблиц (задание `cleanup`, параметры конфигурации): `outbound_request` — 180 суток; `job` в `succeeded` и `cancelled` — 30 суток, в `failed` — 180 суток; `inbound_event` в `processed` и `ignored` — 180 суток; `auth_code` и `session` — 7 суток после истечения; `worker_heartbeat` — сутки; `notification` в `sent`, `failed` и `read` — 365 суток (`NOTIFICATION_RETENTION`).

### notification

Одна строка на адресата и канал доставки. Строка кабинета создаётся сразу в `sent` (доставлена в момент создания); `max_bot` и `email` — в `pending` и отправляются заданием `send_notification` в той же транзакции, что и событие (07 §6).

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `user_id` | `uuid` | да | Адресат: член организации стороны или администратор |
| `organization_id` | `uuid` | нет | Пусто у уведомлений администратора |
| `channel` | `text` | да | `max_bot`, `email`, `cabinet` |
| `template` | `text` | да | Код шаблона из каталога (07 §6) |
| `payload` | `jsonb` | да | Данные шаблона и ссылка в кабинет (`link`); почта адресата сюда не пишется — читается при отправке |
| `status` | `text` | да | `pending`, `sent`, `failed`, `read`; `read` — только у `cabinet` |
| `object_type`, `object_id` | | нет | Задаются вместе |
| `dedupe_key` | `text` | нет | Уникален вместе с `user_id` и `channel`: повтор события второго уведомления не создаёт |
| `max_user_id` | `bigint` | нет | Только у `max_bot` и обязателен для него: `channel.added_by_max_user_id` |
| `attempts` | `integer` | да | Попытки отправки |
| `last_error` | `text` | нет | Последняя ошибка отправки без персональных данных |
| `sent_at`, `read_at` | | нет | `read_at` есть тогда и только тогда, когда `status = read` |
| `created_at`, `updated_at` | | да | |

### audit_log

Неизменяем.

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `actor_type` | `text` | да | `advertiser`, `blogger`, `admin`, `system` |
| `actor_user_id` | `uuid` | нет | |
| `organization_id` | `uuid` | нет | |
| `action` | `text` | да | Код действия, например `channel.approve`, `placement.admin_cancel`, `ledger.manual` |
| `object_type`, `object_id` | | да | |
| `reason` | `text` | нет | Обязателен для действий администратора из S-19 |
| `data` | `jsonb` | да | Что изменилось: значения до и после |
| `ip` | `inet` | нет | |

Обязаны попадать в журнал действий: вход и выход; создание и изменение организации и реквизитов; принятие юридических документов; подключение, модерация, блокировка канала и изменение цен; оформление заказа; создание версии поста; решения модерации; действия блогера по размещению; претензии и решения по спорам; все ручные операции S-19; загрузка выписки и ручное сопоставление; все операции с деньгами, запущенные человеком; изменение справочников.

Переходы размещения в журнал действий не дублируются: их источник — `placement_state_history`.

### admin_approval

Запрос второго подтверждения денежной операции администратора выше порога (этап «API администратора», 07 §4).

| Поле | Тип | Об. | Пояснение |
|---|---|---|---|
| `kind` | `text` | да | `ledger_manual`, `deposit_topup`, `payout_mark_paid` |
| `payload` | `jsonb` | да | Что исполнить после подтверждения |
| `amount` | `bigint` | да | Сумма операции, копейки |
| `summary`, `reason` | `text` | да | Описание для экрана и основание автора |
| `idempotency_key` | `text` | да | Ключ операции: повтор после сбоя не создаёт второй проводки |
| `requested_by` | `uuid` | да | Автор (`senior`) |
| `status` | `text` | да | `pending`, `approved`, `rejected`, `expired` (`ADMIN_APPROVAL_TTL`, по умолчанию 72 ч) |
| `decided_by`, `decided_at`, `decision_reason` | | нет | Решение другого `senior`; отклонение автором — отзыв |
| `result_id` | `uuid` | нет | Исполненная операция (проводка, выплата) |
| `expires_at` | `timestamptz` | да | |

## 9. Перечисления

| Перечисление | Значения |
|---|---|
| `organization.legal_type` | `legal_entity`, `sole_proprietor`, `self_employed` |
| `organization.tax_regime` | `osno` — общая система; `usn` — упрощённая; `ausn` — автоматизированная упрощённая; `eshn` — единый сельхозналог; `psn` — патент; `npd` — налог на профессиональный доход |
| `organization.vat_rate` | `none` — без НДС; `vat_5`; `vat_7`; `vat_22` |
| `channel.status` | `unclaimed` — бот добавлен, канал ещё не связан с организацией кодом подключения (A-41); `pending_rights` — не хватает прав или канал закрытый; `moderation`; `active`; `rejected`; `suspended` — временно недоступен (права, РКН); `disconnected` — бот удалён; `blocked` — заблокирован администратором |
| `channel.rkn_status` | `not_required`, `pending`, `confirmed`, `rejected` |
| `post_version.status` | `pending_moderation`, `rejected`, `approved`, `ready` |
| `campaign.status` | `draft`, `ordered`, `archived` |
| `post_version.token_carrier` | `text`, `media` |
| `post_version.token_position` | `link`, `above`, `below` |
| `order.status` | `moderation`, `rejected`, `marking`, `in_progress`, `completed`, `cancelled` |
| `placement.status` | `created`, `awaiting_blogger`, `awaiting_schedule`, `scheduled`, `published`, `removed`, `disputed`, `accepted`, `paid`, `declined`, `expired`, `cancelled`, `failed`, `refunded` |
| `publication.removal_reason` | `schedule`, `external`, `admin`, `refund` |
| `violation.kind` | `removed_early`, `content_modified`, `top_violated`, `bot_access_lost` |
| `claim.reason` | `removed_early`, `top_violated`, `content_mismatch`, `other` |
| `dispute.reason` | `removed_early`, `content_modified`, `top_violated`, `bot_access_lost`, `content_mismatch`, `other`, `admin` |
| `dispute.resolution` | `full_payout`, `proportional`, `full_refund` |
| `sync_status` | `pending`, `sent`, `synced`, `error` |
| `creative.erir_status` | `pending`, `success`, `error` |
| `invoice.status` | `issued`, `paid`, `expired`, `cancelled` |
| `bank_transaction.match_status` | `matched`, `unmatched`, `ignored` |
| `ledger_account.kind` | `bank`, `rocketwork_deposit`, `payout_fees`, `diagnostic_expense` — активы; `advertiser_balance`, `placement_hold`, `blogger_payable`, `platform_revenue` — обязательства |
| `ledger_transaction.kind` | `deposit`, `hold`, `hold_release`, `settlement`, `payout`, `deposit_topup`, `balance_return`, `manual`, `rw_diagnostic` |
| `hold.status` | `active`, `released`, `distributed` |
| `payout.status` | `created`, `sent`, `succeeded`, `failed` |
| `rocketwork_contractor.status` | `invited`, `pending`, `active`, `blocked` |
| `job.status` | `pending`, `running`, `succeeded`, `failed`, `cancelled` |
| `inbound_event.status` | `received`, `processed`, `ignored`, `failed` |
| `notification.channel` | `max_bot`, `email`, `cabinet` |
| `actor_type` | `advertiser`, `blogger`, `admin`, `system` |

Причины перехода (`placement.status_reason`, `placement_state_history.reason_code`): `advertiser_cancel`, `admin_cancel`, `channel_unavailable`, `window_elapsed`, `blogger_declined`, `accept_timeout`, `schedule_timeout`, `publish_error`, `precondition_failed`, `new_post_version`, `erir_rejected`, `auto_acceptance`.

## 10. Персональные данные

| Что хранится | Где | Зачем |
|---|---|---|
| Почта | `user.email` | Вход, уведомления |
| Идентификатор пользователя MAX | `channel.added_by_max_user_id` | Сообщения бота блогеру (A-41) |
| ФИО | `organization.name`, `organization.contact_name`, `organization.signer_name`, `blogger_profile.full_name` | Договор, маркировка, выплаты |
| ИНН | `organization.inn` | Договор, ОРД, выплаты |
| Телефон | `organization.phone` | Связь |
| Адрес регистрации ИП | `organization.address` | Договор, закрывающие документы |
| Документ о режиме налогообложения | `organization.tax_document_storage_key` | Подтверждение режима и ставки НДС |
| Банковские реквизиты рекламодателя | `organization.bank_account`, `bank_name`, `bank_corr_account`, `bank_bik`; снимок в `invoice.payer_*` | Реквизиты плательщика в счёте, возврат остатка через поддержку (D-26) |
| Плательщик из выписки | `bank_transaction.payer_name`, `payer_inn`, `payer_account` | Сопоставление платежа с организацией (R-12) |
| IP-адрес | `session.ip`, `auth_code.requested_ip`, `legal_acceptance.ip`, `audit_log.ip` | Безопасность, лимиты частоты, доказательство принятия документов |

Не хранится: паспортные данные, фотографии документов, удостоверяющих личность, банковские карты и счета блогеров — всё это находится в Рокет Ворк (A-24). В `outbound_request` и журнале приложения персональные данные маскируются (07 §5).

Состав и основания обработки — проверить с юристом (Q-33).
