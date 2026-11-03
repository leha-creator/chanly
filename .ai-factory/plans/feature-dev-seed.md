# План реализации: тестовые данные `maxim dev seed`

Branch: feature/dev-seed
Created: 2026-10-25

## Settings
- Testing: yes. Сквозной тест в `internal/bootstrap` на собранных `api` и `worker` (harness, настоящая PostgreSQL, `clock.Manual`, `maxfake`, `ordfake`, `rocketworkfake`, `storagefake`). Проверяется: команда проходит; созданы пользователи, организации, каналы в каталоге, кампании и заказы в целевых состояниях; повторный запуск ничего не дублирует и не меняет; журнал проводок сходится (`ledger.CheckBalance`); команда отказывает вне `APP_ENV` `development` и `test`. Табличные тесты чистых помощников: окно кампании и время `mode=at`, генерация PDF и PNG.
- Logging: verbose. `log/slog` JSON, компонент `cli`, `op` `Bootstrap.DevSeed`. WARN `dev command used` при запуске (как у других `dev`-команд). INFO на каждый этап: `seed stage started` и `seed stage done` (`stage`, `created`, `skipped`, `duration_ms`). INFO на каждый созданный объект: `seed user`, `seed organization`, `seed channel`, `seed campaign`, `seed order` (идентификаторы, состояние; почта и ИНН маскированы, 07 §5.4). DEBUG на каждое ожидание `worker`: `seed waiting` (`what`, `elapsed_ms`). WARN на пропуск сценария (`seed scenario skipped`, `scenario`, `reason`: `exists`, `rocketwork_not_fake`, `worker_timeout`). ERROR `seed failed` с этапом и ошибкой. В stdout — человекочитаемая сводка: почты для входа, что создано, что пропущено и почему.
- Docs: yes. В конце обязательная точка: README (новый раздел «Тестовые данные в разработке», строка в таблице команд, поправка устаревших мест из сессии: «остальные операции отвечают 501», «до появления API администратора»), `usage` в `cmd/maxim/main.go`, `.ai-factory/ARCHITECTURE.md` (строка о dev-only методах в `app`), `docs/launch/README.md` или чек-лист тестового окружения (если упоминает данные), `deploy/app/README.md` (seed запрещён в продакшене, на `staging` — только при `APP_ENV=development`/`test`; иначе явно указать, что не применяется).

## Roadmap Linkage
Milestone: "Юридическая и эксплуатационная готовность, пилот"
Rationale: этап включает тестовое окружение с тестовыми контурами внешних систем; команда наполняет локальное и тестовое окружение правдоподобными данными, чтобы проверять кабинеты фронтенда и сценарии администратора без ручного прохода всего пути.

## Цель и границы

Команда `maxim dev seed` одним запуском наполняет базу разработки демонстрационными данными, с которыми фронтенд сразу показывает непустые кабинеты: каталог, корзину, кампании, заказы и размещения в разных состояниях, выплаты, споры, очереди администратора.

Принципы:
- **Только через сервисы приложения**, тем же путём, что API и `worker`. Так сохраняются инварианты: сходимость журнала проводок, допустимые переходы размещений (P-01 … P-18), связи с ОРД, журнал действий. Прямые вставки SQL из `bootstrap` запрещены. Где сервиса нет, добавляется метод `Dev…` в `app` модуля (как `placement.DevAccept`), он работает только из `dev`-команды.
- **Шаги, которым нужен MAX или ОРД, выполняет запущенный `worker`.** Поддельные MAX и ОРД живут в памяти процесса `worker` без файла состояния. Если команда выполнит задания сама со своими подделками, `worker` потом не найдёт пост или объект ОРД и откроет спор `removed_early` или получит постоянную ошибку ОРД. Поэтому команда ставит задания тем же путём, что сервисы, и **ждёт результата в базе** (erid версии, публикация, отправка выплаты) с пределом `--wait`. Без `worker` команда останавливается с понятным сообщением «запустите worker».
- **Повторный запуск безопасен.** Объекты находятся по естественным ключам: почта пользователя, организация пользователя, `max_chat_id` канала из зарезервированного диапазона, название кампании в организации. Найденное не создаётся и не меняется; сценарий заказа, кампания которого уже есть, пропускается.
- **Почты как в `mockapi`**, чтобы фронтенд и тестировщики пользовались одними учётками: `adv@example.test`, `blg@example.test`, `blg-ul@example.test`, `admin@example.test` (senior), `moderator@example.test`. Вход — обычный: код из письма в Mailpit.
- Запрещена вне `APP_ENV` `development` и `test` (как все `dev`-команды).

Не входит:
- Перенос данных `mockapi` один в один и фиксированные идентификаторы. Идентификаторы — UUID v7 приложения.
- Сдвиг часов. Состояния, зависящие от времени, «стареют» как настоящие: T-01 и T-13 через 24 ч, `scheduled` публикуется примерно через час, T-05 принимает `removed` после окна претензий. Сводка команды об этом предупреждает.
- Метрики каналов. Поддельный MAX `worker` не знает засеянные каналы, поэтому суточный `refresh_channel_metrics` по ним завершается ошибкой и оповещением в Mailpit. Это то же ограничение, что у ручного пути из README; оно описывается в документации.
- Очистка засеянных данных. Сброс — `docker compose -f compose.dev.yml down -v`.

## Состав данных

| Что | Количество и содержание |
|---|---|
| Юридические документы | По одной действующей версии каждого вида, если версий нет: сгенерированный PDF «Демонстрационный документ» |
| Администраторы | `admin@example.test` — senior, `moderator@example.test` — moderator |
| Рекламодатель | `adv@example.test`, ООО, все реквизиты, принятые документы, баланс до 500 000 ₽ (досыпается до этой суммы счётом и выпиской, как `dev balance deposit`) |
| Блогер-самозанятый | `blg@example.test`, документы приняты, исполнитель Рокет Ворк активен (подделка) |
| Блогер-юрлицо | `blg-ul@example.test`, КПП и подписант, выплаты `bank_transfer` |
| Каналы в каталоге | 8 каналов двух блогеров в разных категориях, три формата с ценами, подписчики 1 200 … 9 500 (ниже порога РКН), описания; одобрены администратором, площадка зарегистрирована в ОРД |
| Каналы в очереди | 1 канал в `moderation` (очередь `adm/channels`) |
| Кампании без заказа | «Демо: черновик» с текстом и изображением (сгенерированный PNG, плашку строит `worker`), «Демо: пустая» |
| Заказы по сценариям | По заказу на сценарий, кампания «Демо: <сценарий>», один канал, окно сегодня … +2 дня, 00:00–23:59 МСК |

Сценарии заказов (кратчайший законный путь в реальном времени):

| Сценарий | Путь |
|---|---|
| `moderation` | Оформление заказа, дальше ничего |
| `rejected` | `post.RejectVersion` |
| `cancelled` | `placement.CancelOrder` после оформления |
| `awaiting_blogger` | `post.ApproveVersion` → ждать erid (`worker`, ordfake) или `MarkVersionReady` при `ORD_ENABLED=false` → P-02 |
| `awaiting_schedule` | + `placement.Accept` блогером |
| `scheduled` | + `placement.Schedule(mode=at)`, время — ближайшее кратное 5 мин не раньше `now + PLACEMENT_MIN_LEAD + 5 мин` |
| `published` | + `Schedule(mode=now)` → ждать `published` (`worker`) |
| `disputed` | из `published`: `placement.CreateClaim` рекламодателем (`removed_early`) |
| `removed` | из `published`: `placement.MarkRemovedByAdmin(removedAt = now)` администратором |
| `accepted` | из `removed`: `placement.AcceptPlacement` рекламодателем (P-12), выплата создана |
| `paid` | из `accepted`: депозит Рокет Ворк (`ledger.DepositTopup` + депозит подделки), ждать выплату `sent` (`worker`), затем `rocketworkfake.CompletePayout` + `payout.ApplyPayout` |
| `bank_transfer` | заказ на канал блогера-юрлица до `accepted`: выплата `bank_transfer` ждёт `mark-paid` в админке |

Если `ROCKETWORK_MODE` не `fake`, сценарии, которым нужен активный исполнитель (`awaiting_schedule` и дальше у самозанятого, `paid`), пропускаются с WARN и строкой в сводке; блогер-юрлицо работает без Рокет Ворк.

## Решения плана

| # | Вопрос | Решение |
|---|---|---|
| R-1 | Создание пользователя без кода входа | Метод `account/app.Service.DevEnsureUser(ctx, email) (domain.User, bool, error)`: найти по почте или создать (`domain.NewUser`), журнал INFO. Вход в кабинет остаётся обычным, по коду |
| R-2 | Канал без настоящего MAX | Метод `channel/app.Service.DevSeedChannel(ctx, orgID, DevChannelInput)`: канал в `moderation` с `is_public`, правами бота `write, delete`, подписчиками, названием и ссылкой; `max_chat_id` из зарезервированного диапазона `-7_700_000_000 - n`. Повтор по `max_chat_id` возвращает существующий канал. Дальше — настоящие `Update` (описание, категория), `SetPrices`, `Approve` администратором: одобрение передаётся маркировке, `worker` регистрирует площадку в ordfake, канал появляется в каталоге. Ждать `ord_platform_ready_at` с пределом `--wait` |
| R-3 | Хранилище для документов и изображений | Режим сборки `modeDevSeed`: хранилище (как `modeAdminMoney`) и Рокет Ворк; MAX, ОРД и почта не подключаются. Добавить в условие `newInfra` |
| R-4 | Ожидание `worker` | Интерфейс `seedWaiter{ Wait(ctx, what string, done func(ctx) (bool, error)) error }`. По умолчанию — опрос раз в 2 с до `--wait` (по умолчанию 5 мин), по истечении — ошибка `worker did not ... within ...: is the worker running?`. В тесте — ожидание, которое прогоняет `worker.RunOnce` и двигает `clock.Manual` на минуту между проверками (как `ordSettle`) |
| R-5 | Активный исполнитель Рокет Ворк | Как `activeContractor` в тестах: `payout.Invite`, `payout.EnsureContractor` (вызов сервиса, а не задание), внешний идентификатор из `ContractorByOrg`, `rocketworkfake.ActivateContractor`, `payout.ApplyContractor`. Состояние подделки общее с `api` и `worker` через `ROCKETWORK_FAKE_STATE_PATH` |
| R-6 | Пополнение баланса | Общий помощник с `dev balance deposit`: `billing.CreateInvoice` → `devStatementFile` → `billing.ImportStatement`. Досыпается разница до 500 000 ₽ |
| R-7 | Флаги | `maxim dev seed [--wait 5m] [--orders=true]`. `--orders=false` — только пользователи, каналы и кампании без заказов (быстро, без ожидания erid и публикаций) |
| R-8 | Сгенерированные файлы | PDF — минимальный валидный документ (`%PDF-1.7`, одна страница, текст «Демонстрационный документ»); PNG — 800×600, однотонный фон и полосы, кодировщик `image/png`. Проходят проверку по содержимому (`application/pdf`, сторона ≥ `MEDIA_MIN_SIDE`) |

## Tasks

### Фаза 1. Опоры в модулях

- [x] **T1. `account`: `DevEnsureUser`.** Файл `internal/account/app/dev.go`: метод `DevEnsureUser(ctx, email)` — нормализация почты как во входе, поиск `Users().GetByEmail`, иначе `domain.NewUser(ids.New(), email, now)` и `Users().Create` в транзакции. Возвращает пользователя и признак «создан». Комментарий: только для `maxim dev seed`. Журнал: INFO `dev user ensured` (`user_id`, `created`, почта маскирована `logx.MaskEmail`). Тест в `internal/account/app` на настоящей базе: создание, повтор возвращает того же пользователя.
- [x] **T2. `channel`: `DevSeedChannel`.** Файлы `internal/channel/domain` (сделано без нового конструктора: `NewChannel` → `LinkTo` → `RightsRefreshed` дают `moderation` с правами и `is_public`; канал чужой организации с тем же `max_chat_id` — 409), `internal/channel/app/dev.go` (метод в транзакции: поиск по `max_chat_id`, иначе создание через репозиторий), запрос `sqlc` при необходимости (`GetByMaxChatID` уже есть? — проверить и переиспользовать). Без заданий `refresh_channel_info` и без событий MAX. Журнал: INFO `dev channel seeded` (`channel_id`, `max_chat_id`, `created`). Тест в `internal/channel/app`: канал создан в `moderation`, повтор не создаёт второй, после `Update` + `SetPrices` проходит `Approve`. Зависит от: —.

### Фаза 2. Команда

- [x] **T3. Каркас `dev seed`.** Файлы `internal/bootstrap/dev_seed.go` (разбор флагов `--wait`, `--orders`; `modeDevSeed`; сборка `newInfra` + `newModules` + `m.startMarking`; `audit.WithActor(system)`; запуск этапов по порядку; сводка в stdout), `internal/bootstrap/dev.go` (ветка `seed`), `internal/bootstrap/modules.go` (`modeDevSeed` в условии хранилища и Рокет Ворк), `cmd/maxim/main.go` (строка `usage`). `seedWaiter` по R-4 с реализацией опросом. Проверка `ROCKETWORK_MODE` (флаг `rwFake` для этапов). Журнал: WARN `dev command used`, INFO `seed stage started|done`, ERROR `seed failed`. Зависит от: T1, T2.
- [x] **T4. Этап «люди и деньги».** Файл `internal/bootstrap/dev_seed_people.go`: документы (если у вида нет действующей версии — `PublishLegalDocument` со сгенерированным PDF), администраторы (`SetAdminRole`), рекламодатель и два блогера (`DevEnsureUser`, `CreateOrganization` с полными реквизитами и корректными ИНН из генератора с контрольными цифрами — переиспользовать `accounttest.INN` или вынести генератор в небольшой пакет, доступный не только тестам), принятие документов (`CurrentLegalDocuments` + `AcceptLegalDocuments`), баланс до 500 000 ₽ (помощник из `dev_money.go` по R-6, вынести общий код), активный исполнитель самозанятого по R-5 при `rwFake`. Сгенерированные PDF и PNG — `internal/bootstrap/dev_seed_files.go`. Журнал: INFO `seed user`, `seed organization`, `seed balance` (`amount`, `added`), WARN `seed scenario skipped` (`rocketwork_not_fake`). Зависит от: T3.
- [x] **T5. Этап «каналы и кампании».** Файл `internal/bootstrap/dev_seed_catalog.go`: таблица 9 каналов (название, описание, категория по `slug`, подписчики, цены трёх форматов, владелец); для каждого — `DevSeedChannel`, `Update`, `SetPrices`; 8 каналов — `Approve` администратором `admin@example.test`, 1 остаётся в `moderation`; ожидание `ord_platform_ready_at` у одобренных через `seedWaiter` (при `ORD_ENABLED=false` готовность ставится сразу, ожидание проходит с первой проверки). Кампании «Демо: черновик» (текст + загруженный PNG через `post.Upload`) и «Демо: пустая» (`CreateCampaign`, `SaveDraft`). Журнал: INFO `seed channel` (`channel_id`, `status`), `seed campaign`, DEBUG `seed waiting`. Зависит от: T4.
- [x] **T6. Этап «заказы».** Файл `internal/bootstrap/dev_seed_orders.go`: сценарии из таблицы «Сценарии заказов» как список шагов; общий шаг «кампания + пост + корзина + оформление» (окно сегодня … +2 дня по Москве, 00:00–23:59; текст поста без изображений, чтобы не ждать производных); шаги до целевого состояния по таблице; ожидания erid, `published` и выплаты `sent` через `seedWaiter`; депозит Рокет Ворк перед `paid` (`ledger.DepositTopup` от senior + `rocketworkfake` депозит, только если остатка не хватает). Пропуск сценария, если его кампания уже есть (`exists`), или нет `rwFake`. Время `mode=at` и окно — чистые функции с табличными тестами. Журнал: INFO `seed order` (`scenario`, `order_number`, `placement_id`, `status`), WARN `seed scenario skipped`. Зависит от: T5.
- [x] **T7. Итог и проверка сходимости.** В `dev_seed.go`: после этапов `ledger.CheckBalance` (расхождение — ERROR и ненулевой код), сводка: учётки для входа, число каналов в каталоге, список заказов «сценарий → номер → состояние», пропущенные сценарии с причиной, предупреждения (состояния стареют; перезапуск `worker` стирает поддельный MAX — живые публикации станут спорами `removed_early`). Журнал: INFO `seed done` (`created`, `skipped`, `duration_ms`). Зависит от: T6.

### Фаза 3. Тесты и документация

- [x] **T8. Сквозной тест `dev seed`.** Файл `internal/bootstrap/dev_seed_e2e_test.go`: harness с `api` и `worker`, `withCommandClock`, ожидание-заглушка по R-4 (прогон `RunOnce` и шаг часов), `ROCKETWORK_MODE=fake` с временным файлом состояния. Проверки: каналы видны в каталоге (`GET /catalog/channels` от рекламодателя — 8), заказы в целевых состояниях, выплата `paid`, выплата `bank_transfer` ожидает, `ledger.CheckBalance` без расхождений; второй запуск — ни одной новой строки в `user`, `organization`, `channel`, `campaign`, `order` и все сценарии `exists`; `APP_ENV=production` — отказ `errDevForbidden`; `--orders=false` — без заказов. Зависит от: T7.
- [x] **T9. Документация.** README: раздел «Тестовые данные в разработке» (запуск при работающем `worker`, учётки, состав, флаги, ограничения из «Не входит», сброс через `down -v`), строка в таблице команд, поправка устаревших мест («остальные операции отвечают 501», «до появления API администратора», `--email admin.local`); `cmd/maxim/main.go` `usage` (если не сделано в T3); `.ai-factory/ARCHITECTURE.md` — правило о методах `Dev…` в `app` (только для `dev`-команд, без HTTP); `deploy/app/README.md` — команда не для продакшена. Зависит от: T8.

## Commit Plan
- **Коммит 1** (после T1–T2): `feat(dev): dev-only user and channel helpers for seeding`
- **Коммит 2** (после T3–T5): `feat(dev): maxim dev seed — people, money, catalog, campaigns`
- **Коммит 3** (после T6–T7): `feat(dev): seed orders and placements across states`
- **Коммит 4** (после T8–T9): `test(dev): seed e2e; docs: test data in development`

## Риски
- **Время запуска.** Ожидание erid (ordfake опрашивает раз в `ORD_POLL_FAST_INTERVAL`, 1 мин) и публикаций занимает минуты. Ожидания по сценариям идут параллельно там, где это безопасно (одобрение всех версий сразу, затем общее ожидание), чтобы уложиться примерно в 3–5 минут; `--orders=false` — секунды.
- **Запуск около полуночи по Москве.** Окно 00:00–23:59: `mode=now` не работает в 23:59–24:00, `mode=at` может выйти за окно. Команда проверяет и при необходимости сдвигает окно на завтра для `scheduled`, а для `published` и дальше — ждёт начала суток с понятным сообщением.
- **Состояние поддельного Рокет Ворк** в файле: если файл удалили, а база осталась, активация исполнителя повторяется по R-5 (подделка создаёт исполнителя заново).
