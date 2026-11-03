# Dev на общем сервере

Push/merge в `dev` выпускает бэкенд на `https://dev.chanly.ru`, push/merge в
`master` — на `https://chanly.ru`. Разработка конфигурации может продолжаться в
master без переключения рабочей копии. Ветки синхронизируются обычным merge;
автоматического переноса изменений между ними нет.

GitHub Environment `dev` разрешает деплой только из ветки `dev`. Переменные:
`DEPLOY_HOST=194.58.120.196`, `DEPLOY_USER=deploy`, `DEPLOY_DOMAIN=dev.chanly.ru`.
Секреты: `DEPLOY_SSH_KEY`, `DEPLOY_KNOWN_HOSTS`. Production сохраняет ограничение
на master. Выпуски на сервере сериализуются через `/opt/edge/.release.lock`;
учётные данные GHCR раздельны для окружений.

Первичная настройка из checkout на сервере от root:

```sh
python3 deploy/app/bootstrap-dev.py ghcr.io/leha-creator/maxim-postgres:sha-<full-sha>
```

Требуются работающий production MinIO и общий Caddy. Скрипт генерирует dev-секреты
на сервере, сохраняет существующие env-файлы и создаёт только dev-корзины/политики.
Файлы находятся в `/opt/maxim-dev`, Compose-проект — `maxim-dev`.
`compose.dev.yml` доставляется как `compose.override.yml`: ограничивает CPU/память
API, worker, PostgreSQL и backup. API/worker обновляются двумя ячейками.
Тома, сеть backend, БД, секреты и S3-ключи отделены от production.
Это разделение данных; общий сервер и Docker остаются общей границей доверия.

Начальная конфигурация: `APP_ENV=development`, MAX/ORD используют предусмотренные
кодом тестовые адаптеры, Rocket Work отключён. Реальный dev-бот должен быть отдельным
от production. Письма попадают в Mailpit; интерфейс доступен через
`ssh -L 8026:127.0.0.1:8026 deploy@194.58.120.196` на `http://localhost:8026`.
Админский API по умолчанию закрыт для внешних адресов; нужные CIDR добавляются в
`EDGE_ADMIN_ALLOW` dev stack.env. Production-секреты и реальные данные в dev не копируются.

Dev-копии хранятся в `maxim-dev-backups` общего MinIO и не защищают от потери сервера.
Production по-прежнему требует внешнее резервное хранилище.
Проверка выпуска: `https://dev.chanly.ru/readyz` (200), здоровье worker и состояние
`/opt/maxim-dev/slots.env`.

React-фронтенд находится в `leha-creator/maxim-frontend`: push в его ветку `dev`
запускает проверки, сборку и атомарную смену каталога `frontend/current`.
Caddy отдаёт `/` и маршруты кабинетов из `/opt/maxim-dev/frontend`, а `/api/*`,
`/docs`, `/openapi.json` и проверки здоровья направляет в бэкенд. Запросы API идут
на тот же домен, cookie/CSRF работают без междоменных исключений.
Выпуски двух репозиториев независимы: push бэкенда не меняет версию фронтенда.
После добавления mount frontend в `deploy/edge/compose.yml` общий Caddy нужно один
раз пересоздать через `docker compose up -d` в `/opt/edge`.
