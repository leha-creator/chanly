# Исторический стенд заглушки API

> Заглушка снята с автодеплоя. Теперь push/merge в master выпускает настоящий бэкенд на chanly.ru; актуальное руководство — [production](app/README.md). Ниже сохранено историческое устройство стенда; команды старого workflow больше не применяются.

Адрес: <https://dev.plwedding.ru/docs> (Swagger UI заглушки). Корень `/` перенаправляет на `/docs`.

Стенд нужен, чтобы смотреть и пробовать API руками. Это не production: данные выдуманные и живут в памяти (каждый деплой их сбрасывает), нет мониторинга и резервных копий, сайт закрыт от индексации (`X-Robots-Tag: noindex`), но открыт всем, у кого есть ссылка.

Тестовое окружение и продакшен бэкенда (две ячейки `api` и `worker`, PostgreSQL с резервными копиями, выпуск без простоя) — отдельный стек: [deploy/app/README.md](app/README.md).

## Как устроено

```
push в master ──► GitHub Actions ──► образ в GHCR ──► SSH на сервер ──► docker compose up
                                     ghcr.io/leha-creator/maxim-mockapi:sha-xxxxxxx

интернет ──► :443 edge-Caddy (/opt/edge, Let's Encrypt) ──► maxim-mockapi:8080 (общая сеть edge)
```

| Что | Где |
|---|---|
| Сервер | `95.163.229.132`, Ubuntu 26.04, 1 CPU, 1 ГБ RAM + 2 ГБ swap |
| Домен | `dev.plwedding.ru` → A-запись на сервер |
| Файлы стенда | `/opt/maxim/`: `compose.yml`, `.env` (владелец `deploy`); сайт — `/opt/edge/sites/maxim.caddy` |
| Прокси | общий edge-Caddy сервера в `/opt/edge`, конфиг и порядок работы — `deploy/edge/README.md` в репозитории `leha-creator/tracker`; сертификаты — том `edge_caddy_data` (не удалять — иначе перевыпуск и риск упереться в лимиты Let's Encrypt) |
| Журнал подготовки сервера | `/var/log/maxim-bootstrap.log` |

Файлы в `/opt/maxim` руками не правятся: workflow перезаписывает их из репозитория (`deploy/compose.yml`, `deploy/maxim.caddy` с подставленным доменом) при каждом деплое и делает `caddy reload` edge-прокси.

Наружу открыты только порты 22, 80 и 443 (`ufw`). Порт заглушки на хост не публикуется. Вход по SSH только по ключам; вход `root` по паролю отключён, в том числе блоком хостера `99-qemu.conf` (см. `bootstrap.sh`, шаг 8).

## CI/CD — `.github/workflows/deploy.yml`

| Событие | Что происходит |
|---|---|
| PR в `master` | `go generate` (сгенерированный код не устарел), `go vet`, `go test -race` |
| Пуш в `master` | проверки → сборка образа (теги `sha-<7 символов>` и `master`) → деплой → проверка `https://dev.plwedding.ru/openapi.json` |
| Run workflow с `tag` | откат: деплой указанного тега без сборки |

Проверки идут на Go 1.27 — той же версии, на которой локально запускают `go generate`. Другая минорная версия Go сжимает встроенную в `api.gen.go` спецификацию иначе, и проверка «сгенерированный код не устарел» падает. При обновлении Go менять версию в workflow, в `mockapi/Dockerfile` и локально одновременно, затем перегенерировать код.

Пока в `master` нет `mockapi/go.mod`, проверки и сборка пропускаются, а деплой публикует только файл сайта: edge-прокси отвечает `503 Заглушка API ещё не развёрнута`.

Деплои идут по очереди (`concurrency: deploy-dev`). Если проверка после деплоя не прошла, job падает и печатает `docker compose ps` и последние строки журналов. Если не готов TLS (например, DNS только что поменяли), это предупреждение, а не ошибка: Caddy выпустит сертификат сам.

### Настройки репозитория

Settings → Secrets and variables → Actions:

| Имя | Тип | Значение |
|---|---|---|
| `DEPLOY_SSH_KEY` | secret | приватный ключ `~/.ssh/maxim_deploy` (пользователь `deploy` на сервере) |
| `DEPLOY_KNOWN_HOSTS` | secret | `ssh-keyscan -t ed25519 95.163.229.132`, отпечаток `SHA256:piIGaB26Hj6jkU0XXDkyx5UGBQeZMZt8V3Oezrpiufk` |
| `DEPLOY_HOST` | variable | `95.163.229.132` |
| `DEPLOY_USER` | variable | `deploy` |
| `DEPLOY_DOMAIN` | variable | `dev.plwedding.ru` |
| `SMOKE_PATH` | variable, необязательно | путь для проверки после деплоя, по умолчанию `/openapi.json` |

Окружение `dev` (Settings → Environments) показывает ссылку на стенд в запуске. Образ в GHCR приватный; сервер скачивает его по временному `GITHUB_TOKEN` запуска (`docker login` → `pull` → `docker logout`), постоянного токена на сервере нет.

## Частые действия

```bash
# Журналы заглушки и Caddy
ssh deploy@95.163.229.132 'cd /opt/maxim && docker compose logs -f --tail=100 mockapi'
ssh deploy@95.163.229.132 'cd /opt/edge && docker compose logs --tail=100 caddy'

# Состояние и перезапуск (сбрасывает данные заглушки)
ssh deploy@95.163.229.132 'cd /opt/maxim && docker compose ps'
ssh deploy@95.163.229.132 'cd /opt/maxim && docker compose restart mockapi'

# Откат: Actions → deploy → Run workflow → tag = sha-xxxxxxx
# или из консоли:
gh workflow run deploy --ref master -f tag=sha-xxxxxxx
```

Заглушка пишет журнал в JSON в stdout, Caddy — журнал доступа тоже в JSON. Docker хранит до 3 файлов по 10 МБ на контейнер. Уровень журнала заглушки — `LOG_LEVEL` в `/opt/maxim/.env` (пишет workflow, по умолчанию `debug`).

## Новый сервер

1. Ключ администратора — в `/root/.ssh/authorized_keys` (первый вход по паролю хостера).
2. Подготовка (повторный запуск безопасен):

   ```bash
   scp deploy/bootstrap.sh root@<host>:/root/
   ssh root@<host> "bash /root/bootstrap.sh \
     --deploy-pubkey '$(cat ~/.ssh/maxim_deploy.pub)' \
     --admin-pubkey  '$(cat ~/.ssh/id_ed25519.pub)' | tee /var/log/maxim-bootstrap.log"
   ```

3. Проверить вход `ssh -i ~/.ssh/maxim_deploy deploy@<host> docker ps`, затем запустить скрипт ещё раз с `--harden-ssh` (отключает вход по паролю).
4. Обновить `DEPLOY_HOST` и `DEPLOY_KNOWN_HOSTS`, A-запись домена — на новый адрес; запустить workflow вручную.

Если Docker Hub недоступен с сервера, запустить скрипт с `--registry-mirror https://mirror.gcr.io`.

Скрипт поддерживает Ubuntu 22.04/24.04/26.04 и Debian 12. Пользователь `deploy` состоит в группе `docker`, что фактически равно root-доступу; для тестового стенда это принято сознательно.

## Проверка конфигурации

```bash
docker run --rm -v "$PWD:/mnt" koalaman/shellcheck:stable /mnt/deploy/bootstrap.sh
docker compose -f deploy/compose.yml --env-file deploy/.env.example config --quiet
sed "s/__DOMAIN__/dev.plwedding.ru/" deploy/maxim.caddy > /tmp/maxim.caddy
docker run --rm -v /tmp/maxim.caddy:/etc/caddy/Caddyfile:ro caddy:2.11.7 caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
docker run --rm -i hadolint/hadolint < mockapi/Dockerfile
# SC2029 отключён: в ssh-командах workflow переменные намеренно раскрываются на раннере
docker run --rm -e SHELLCHECK_OPTS="-e SC2029" -v "$PWD:/repo" -w /repo rhysd/actionlint:1.7.7
```
