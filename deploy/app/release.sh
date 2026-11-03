#!/usr/bin/env bash
# Выпуск бэкенда без простоя (07 §8) на сервере окружения. Руководство — deploy/app/README.md.
#
#   release.sh --tag sha-1a2b3c4 [опции]   выпуск тега в неактивную ячейку и переключение
#   release.sh --rollback [опции]          вернуть предыдущую ячейку (её контейнеры остановлены,
#                                          но не удалены; миграции не откатываются)
# Опции:
#   --dir DIR             каталог окружения (compose.yml, app.env, stack.env, slots.env);
#                         по умолчанию — каталог скрипта
#   --compose-file FILE   дополнительный файл compose (можно несколько; репетиция — deploy/local)
#   --health-timeout SEC  ожидание готовности новой ячейки, по умолчанию 180
#   --skip-pull           не скачивать образ (локальная сборка, откат)
#   --edge-dir DIR        каталог edge-Caddy (по умолчанию /opt/edge, если есть); --no-edge — не трогать
#   --dry-run             показать план и выйти
#   --reapply             повторно выпустить активный тег с новой конфигурацией
#   --expected-domain D   сверить домен workflow с DOMAIN до любых изменений
#
# Порядок: образ → проверка конфигурации новым образом → postgres и backup → миграции новым
# образом → запуск новой ячейки api+worker и ожидание готовности → сайт edge-прокси → плавная
# остановка старой ячейки → запись slots.env. Ошибка до переключения оставляет работать старую
# ячейку; миграции к этому моменту применены и по правилу совместимы со старым кодом.
set -Eeuo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
LOG_COMPONENT=release
if [[ -f $SCRIPT_DIR/lib.sh ]]; then
  # shellcheck source=deploy/scripts/lib.sh
  . "$SCRIPT_DIR/lib.sh"
else
  # shellcheck source=deploy/scripts/lib.sh
  . "$SCRIPT_DIR/../scripts/lib.sh"
fi

TAG=""
ROLLBACK=0
APP_DIR=$SCRIPT_DIR
EXTRA_FILES=()
HEALTH_TIMEOUT=180
SKIP_PULL=0
EDGE_DIR=""
NO_EDGE=0
DRY_RUN=0
REAPPLY=0
EXPECTED_DOMAIN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) TAG=${2:-}; shift 2 ;;
    --rollback) ROLLBACK=1; shift ;;
    --dir) APP_DIR=$(cd "${2:?--dir needs a value}" && pwd); shift 2 ;;
    --compose-file) EXTRA_FILES+=("${2:?--compose-file needs a value}"); shift 2 ;;
    --health-timeout) HEALTH_TIMEOUT=${2:-}; shift 2 ;;
    --skip-pull) SKIP_PULL=1; shift ;;
    --edge-dir) EDGE_DIR=${2:-}; shift 2 ;;
    --no-edge) NO_EDGE=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --reapply) REAPPLY=1; shift ;;
    --expected-domain) EXPECTED_DOMAIN=${2:?--expected-domain requires a value}; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) log error invalid_argument arg="$1"; exit 2 ;;
  esac
done
if [[ ( -z $TAG && $ROLLBACK -eq 0 ) || ( -n $TAG && $ROLLBACK -eq 1 ) ]]; then
  log error invalid_arguments hint="exactly one of --tag <tag> or --rollback"
  exit 2
fi
if [[ ! $HEALTH_TIMEOUT =~ ^[1-9][0-9]*$ ]]; then
  log error invalid_arguments hint="--health-timeout must be a positive number of seconds"
  exit 2
fi

STACK_ENV="$APP_DIR/stack.env"
SLOTS_ENV="$APP_DIR/slots.env"
COMPOSE_FILE_MAIN="$APP_DIR/compose.yml"
for f in "$STACK_ENV" "$COMPOSE_FILE_MAIN"; do
  [[ -f $f ]] || { log error file_missing file="$f"; exit 1; }
done

# value_of <file> <key> — значение KEY=VALUE (последнее вхождение).
value_of() {
  [[ -f $1 ]] || return 0
  sed -n "s/\r\$//; s/^$2=//p" "$1" | tail -n 1
}

APP_ENV_FILE=$(value_of "$STACK_ENV" APP_ENV_FILE)
APP_ENV_FILE=${APP_ENV_FILE:-./app.env}
[[ $APP_ENV_FILE == /* || $APP_ENV_FILE =~ ^[A-Za-z]: ]] || APP_ENV_FILE="$APP_DIR/${APP_ENV_FILE#./}"
[[ -f $APP_ENV_FILE ]] || { log error file_missing file="$APP_ENV_FILE"; exit 1; }
IMAGE=$(value_of "$STACK_ENV" MAXIM_IMAGE)
PROJECT=$(value_of "$STACK_ENV" COMPOSE_PROJECT_NAME)
PREFIX=$(value_of "$STACK_ENV" EDGE_ALIAS_PREFIX)
DOMAIN=$(value_of "$STACK_ENV" DOMAIN)
[[ $DOMAIN =~ ^([a-zA-Z0-9][a-zA-Z0-9.-]*|:[0-9]+)$ && $PREFIX =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ ]] || {
  log error invalid_config hint="DOMAIN and EDGE_ALIAS_PREFIX must be safe DNS names"; exit 1;
}
if [[ -n $EXPECTED_DOMAIN && $DOMAIN != "$EXPECTED_DOMAIN" ]]; then
  log error domain_mismatch expected="$EXPECTED_DOMAIN" actual="$DOMAIN"
  exit 1
fi
if [[ -n $EXPECTED_DOMAIN && $NO_EDGE == 0 && ! -d ${EDGE_DIR:-/opt/edge}/sites ]]; then
  log error edge_missing hint="run production bootstrap before release"
  exit 1
fi
# Адреса команды для API администратора (сайт edge): CIDR через пробел или private_ranges.
ADMIN_ALLOW=$(value_of "$STACK_ENV" EDGE_ADMIN_ALLOW)
admin_allow_re='^[0-9A-Za-z_.:/ ]+$'
[[ $ADMIN_ALLOW =~ $admin_allow_re ]] || { log error invalid_config file="$STACK_ENV" hint="EDGE_ADMIN_ALLOW is required: CIDR list or private_ranges"; exit 1; }
[[ -n $IMAGE && -n $PROJECT ]] || { log error invalid_config file="$STACK_ENV" hint="MAXIM_IMAGE and COMPOSE_PROJECT_NAME are required"; exit 1; }

[[ -f $SLOTS_ENV ]] || printf 'ACTIVE_SLOT=\nMAXIM_TAG_A=\nMAXIM_TAG_B=\n' > "$SLOTS_ENV"
ACTIVE=$(value_of "$SLOTS_ENV" ACTIVE_SLOT)
TAG_A=$(value_of "$SLOTS_ENV" MAXIM_TAG_A)
TAG_B=$(value_of "$SLOTS_ENV" MAXIM_TAG_B)

dc() {
  local args=(compose --project-directory "$APP_DIR" -f "$COMPOSE_FILE_MAIN")
  local f
  for f in "${EXTRA_FILES[@]}"; do
    args+=(-f "$f")
  done
  args+=(--env-file "$STACK_ENV" --env-file "$SLOTS_ENV")
  docker "${args[@]}" "$@"
}

tag_of() {
  case "$1" in
    a) printf '%s' "$TAG_A" ;;
    b) printf '%s' "$TAG_B" ;;
  esac
}
other() {
  if [[ $1 == a ]]; then printf b; else printf a; fi
}

# container_state <service> — health контейнера (healthy, unhealthy, starting) или его статус.
container_state() {
  local id
  id=$(dc ps -a -q "$1" 2>/dev/null | head -n 1)
  [[ -n $id ]] || { printf 'absent'; return; }
  docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}|{{.State.Status}}' "$id"
}

# wait_healthy <service...> — ждёт healthy всех; код 1 при unhealthy, выходе или тайм-ауте.
wait_healthy() {
  local deadline=$(($(date +%s) + HEALTH_TIMEOUT)) s state pending
  while :; do
    pending=0
    for s in "$@"; do
      state=$(container_state "$s")
      case "$state" in
        healthy\|running) ;;
        unhealthy\|*|*\|exited|*\|dead|absent)
          log error health_failed service="$s" state="$state"
          return 1 ;;
        *) pending=$((pending + 1)) ;;
      esac
    done
    ((pending == 0)) && return 0
    if (($(date +%s) > deadline)); then
      log error health_timeout services="$*" timeout_s="$HEALTH_TIMEOUT"
      return 1
    fi
    sleep 1
  done
}

write_slots() {
  local tmp="$SLOTS_ENV.tmp"
  printf '# Пишет release.sh: активная ячейка и теги образов ячеек.\nACTIVE_SLOT=%s\nMAXIM_TAG_A=%s\nMAXIM_TAG_B=%s\n' \
    "$1" "$2" "$3" > "$tmp"
  mv "$tmp" "$SLOTS_ENV"
}

# Один выпуск за раз: каталог-замок (flock есть не везде, где идёт репетиция).
LOCK="$APP_DIR/.release.lock"
if ! mkdir "$LOCK" 2>/dev/null; then
  log error release_locked lock="$LOCK" hint="another release is running; remove the directory if it is stale"
  exit 1
fi
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT
trap 'log error release_failed line="${LINENO}"' ERR

started=$(now_ms)
if ((ROLLBACK)); then
  if [[ -z $ACTIVE ]]; then
    log error rollback_impossible reason="no active slot"
    exit 1
  fi
  NEW=$(other "$ACTIVE")
  TAG=$(tag_of "$NEW")
  if [[ -z $TAG ]]; then
    log error rollback_impossible reason="previous slot has no image" slot="$NEW"
    exit 1
  fi
  MODE=rollback
else
  MODE=release
  if [[ $REAPPLY == 0 && -n $ACTIVE && $(tag_of "$ACTIVE") == "$TAG" && $(container_state "api-$ACTIVE") == "healthy|running" && $(container_state "worker-$ACTIVE") == "healthy|running" ]]; then
    log info release_noop slot="$ACTIVE" tag="$TAG" reason="tag is already active"
    exit 0
  fi
  NEW=$(other "${ACTIVE:-b}")
fi
OLD=$ACTIVE
NEW_VAR="MAXIM_TAG_${NEW^^}"
log info release_started mode="$MODE" project="$PROJECT" tag="$TAG" new_slot="$NEW" old_slot="${OLD:-none}" \
  old_tag="$( [[ -n $OLD ]] && tag_of "$OLD" || true )"
if ((DRY_RUN)); then
  log info release_plan steps="pull,config_check,base,migrate,start_new,edge,stop_old,slots" skip_pull="$SKIP_PULL" \
    migrate="$([[ $MODE == release ]] && echo yes || echo no)"
  exit 0
fi

step_start() { STEP=$1; STEP_T=$(now_ms); log info release_step step="$STEP" status=started slot="$NEW" tag="$TAG"; }
step_done() { log info release_step step="$STEP" status=done slot="$NEW" tag="$TAG" duration_ms="$(($(now_ms) - STEP_T))"; }

# 1. Образ.
if ((SKIP_PULL == 0)) && [[ $MODE == release ]]; then
  step_start pull
  docker pull -q "$IMAGE:$TAG" >/dev/null
  step_done
fi

# 2. Конфигурация новым образом: до миграций и до остановки чего-либо.
step_start config_check
export "$NEW_VAR=$TAG" MAXIM_TAG_NEW="$TAG"
# Через сервис migrate: тот же образ и app.env, но только сеть backend — временный контейнер
# не получает алиас ячейки в сети edge. DATABASE_URL у migrate — роль владельца схемы; для
# проверки конфигурации api и worker это безразлично (подключений config check не делает).
for mode in api worker migrate; do
  if ! out=$(dc run --rm --no-deps -T migrate config check --for "$mode" 2>&1); then
    log error config_invalid mode="$mode" tag="$TAG" output="$(printf '%s' "$out" | tail -c 2000)"
    exit 1
  fi
done
step_done

# 3. База и резервное копирование (без пересоздания, если ничего не поменялось).
step_start base
base_services=(postgres backup)
if [[ $(value_of "$APP_ENV_FILE" SMTP_HOST) == mailpit ]]; then
  base_services+=(mailpit)
fi
dc up -d --quiet-pull "${base_services[@]}"
wait_healthy postgres
step_done

# 4. Миграции новым образом (07 §8: до запуска новой версии, совместимы с предыдущей).
if [[ $MODE == release ]]; then
  step_start migrate
  if ! dc run --rm -T migrate; then
    log error migrate_failed tag="$TAG" note="old slot keeps running"
    exit 1
  fi
  step_done
fi

# 5. Новая ячейка.
step_start start_new
dc up -d --no-deps "api-$NEW" "worker-$NEW"
if ! wait_healthy "api-$NEW" "worker-$NEW"; then
  log error new_slot_unhealthy slot="$NEW" note="stopping the new slot, the old one keeps serving"
  dc logs --no-color --tail=50 "api-$NEW" "worker-$NEW" || true
  dc stop "api-$NEW" "worker-$NEW" || true
  exit 1
fi
step_done

# 6. Сайт edge-прокси: обе ячейки, домен окружения. Меняется только при изменении файла.
if ((NO_EDGE == 0)); then
  [[ -n $EDGE_DIR ]] || { [[ -d /opt/edge ]] && EDGE_DIR=/opt/edge; }
  if [[ -n $EDGE_DIR ]]; then
    step_start edge
    site="$EDGE_DIR/sites/$PREFIX.caddy"
    rendered=$(sed -e "s/__DOMAIN__/$DOMAIN/" -e "s/__PREFIX__/$PREFIX/g" -e "s|__ADMIN_ALLOW__|$ADMIN_ALLOW|g" "$APP_DIR/maxim-app.caddy")
    if [[ ! -f $site || $(cat "$site") != "$rendered" ]]; then
      [[ -f $site ]] && cp "$site" "$site.prev"
      printf '%s\n' "$rendered" > "$site"
      # Edge-прокси общий для проектов сервера: validate до reload; при ошибке прежний файл
      # возвращается, чтобы следующий reload чужого проекта не споткнулся о наш сайт.
      # MSYS2_ARG_CONV_EXCL — путь в контейнере не переписывается в Git Bash на Windows.
      if ! (cd "$EDGE_DIR" \
        && MSYS2_ARG_CONV_EXCL='*' docker compose exec -T caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null \
        && MSYS2_ARG_CONV_EXCL='*' docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile); then
        if [[ -f $site.prev ]]; then mv "$site.prev" "$site"; else rm -f "$site"; fi
        log error edge_reload_failed site="$site" note="site file restored, new slot stopped, old slot keeps serving"
        dc stop "api-$NEW" "worker-$NEW" || true
        exit 1
      fi
      rm -f "$site.prev"
      log info edge_site_updated site="$site"
    fi
    step_done
  fi
fi

# 7. Старая ячейка: edge-прокси видит новую живой (проверка раз в 2 с), затем плавная остановка
# (api: /readyz → 503, пауза HTTP_SHUTDOWN_DRAIN, Shutdown; worker: задания дорабатывают или
# возвращаются в очередь).
if [[ -n $OLD ]]; then
  step_start stop_old
  sleep 3
  dc stop "api-$OLD" "worker-$OLD"
  step_done
fi

# 8. Состояние.
if [[ $NEW == a ]]; then
  write_slots a "$TAG" "$TAG_B"
else
  write_slots b "$TAG_A" "$TAG"
fi
log info release_done mode="$MODE" slot="$NEW" tag="$TAG" previous_slot="${OLD:-none}" \
  duration_ms="$(($(now_ms) - started))"
