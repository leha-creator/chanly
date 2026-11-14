#!/usr/bin/env bash
# Репетиция выпуска без простоя на своей машине или в CI (07 §8, deploy/app/README.md).
#
#   deploy/local/rehearse-release.sh [--keep] [--skip-build] [--fee-transition]
#
# Собирает образ бэкенда двух версий (maxim:rehearsal-v1, -v2) и maxim-postgres:rehearsal,
# поднимает edge-прокси репетиции и стек deploy/app/compose.yml под проектом maxim-rehearsal,
# выпускает v1, затем под нагрузкой (запрос каждые 50 мс через прокси) выпускает v2 и
# откатывается на v1. Успех — ни одного ответа 5xx и ни одной ошибки соединения, теги ячеек
# совпадают с ожидаемыми, заданий в состоянии running без исполнителя нет.
# В конце `docker compose down -v` только своих проектов (--keep — оставить стек).
# Порт хоста — REHEARSAL_HTTP_PORT (по умолчанию 18480).
set -Eeuo pipefail

# pwd -W — путь вида D:/… в Git Bash на Windows (docker его понимает без преобразования).
SCRIPT_DIR=$(cd "$(dirname "$0")" && { pwd -W 2>/dev/null || pwd; })
ROOT=$(cd "$SCRIPT_DIR/../.." && { pwd -W 2>/dev/null || pwd; })
LOG_COMPONENT=rehearse-release
# shellcheck source=deploy/scripts/lib.sh
. "$ROOT/deploy/scripts/lib.sh"

KEEP=0
SKIP_BUILD=0
FEE_TRANSITION=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep) KEEP=1; shift ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    --fee-transition) FEE_TRANSITION=1; shift ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) log error invalid_argument arg="$1"; exit 2 ;;
  esac
done

export REHEARSAL_HTTP_PORT=${REHEARSAL_HTTP_PORT:-18480}
export REHEARSAL_EDGE_NETWORK=maxim-rehearsal-edge
export REHEARSAL_EDGE_PROJECT=maxim-rehearsal-edge
BASE_URL="http://127.0.0.1:$REHEARSAL_HTTP_PORT"
STATE="$SCRIPT_DIR/.state/release"
APP="$STATE/app"
EDGE="$STATE/edge"

edge() { docker compose --project-directory "$EDGE" -f "$EDGE/compose.yml" "$@"; }
app() {
  docker compose --project-directory "$APP" -f "$APP/compose.yml" -f "$APP/compose.rehearsal.yml" \
    --env-file "$APP/stack.env" --env-file "$APP/slots.env" "$@"
}
release() {
  "$APP/release.sh" --dir "$APP" --compose-file "$APP/compose.rehearsal.yml" --skip-pull \
    --edge-dir "$EDGE" --health-timeout 120 "$@"
}

LOAD_PID=""
cleanup() {
  local rc=$?
  [[ -n $LOAD_PID ]] && kill "$LOAD_PID" 2>/dev/null || true
  if ((rc != 0)); then
    log error rehearsal_failed exit_code="$rc"
    [[ -f $APP/compose.yml ]] && app ps -a 2>/dev/null || true
  fi
  if ((KEEP == 0)); then
    [[ -f $APP/compose.yml ]] && app --profile slot-a --profile slot-b --profile mailpit --profile tools down -v --remove-orphans >/dev/null 2>&1 || true
    [[ -f $EDGE/compose.yml ]] && edge down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$STATE"
  else
    log info rehearsal_kept app_dir="$APP" edge_dir="$EDGE" url="$BASE_URL"
  fi
  exit "$rc"
}
trap cleanup EXIT

started=$(now_ms)
# Остатки прошлой прерванной репетиции — только свои проекты.
if [[ -d $STATE ]]; then
  [[ -f $APP/compose.yml ]] && app --profile slot-a --profile slot-b --profile mailpit --profile tools down -v --remove-orphans >/dev/null 2>&1 || true
  [[ -f $EDGE/compose.yml ]] && edge down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$STATE"
fi

if ((SKIP_BUILD == 0)); then
  log info rehearsal_step step=build
  for v in v1 v2; do
    docker build -q --build-arg "VERSION=rehearsal-$v" -t "maxim:rehearsal-$v" "$ROOT" >/dev/null
  done
  docker build -q -f "$ROOT/deploy/postgres/Dockerfile" -t maxim-postgres:rehearsal "$ROOT/deploy" >/dev/null
fi

# Каталоги как на сервере: /opt/edge и /opt/maxim-<env>.
mkdir -p "$APP" "$EDGE/sites"
cp "$SCRIPT_DIR/edge/compose.yml" "$SCRIPT_DIR/edge/Caddyfile" "$EDGE/"
cp "$ROOT/deploy/app/compose.yml" "$ROOT/deploy/app/maxim-app.caddy" "$ROOT/deploy/app/release.sh" \
  "$SCRIPT_DIR/compose.rehearsal.yml" "$APP/"
cp "$ROOT/deploy/scripts/lib.sh" "$APP/lib.sh"
cp "$SCRIPT_DIR/rehearsal.app.env" "$APP/app.env"
if ((FEE_TRANSITION)); then
  printf "\nPLATFORM_FEE_RATE_BP=1500\n" >> "$APP/app.env"
fi
cp "$SCRIPT_DIR/rehearsal.stack.env" "$APP/stack.env"
# Пустой сайт до первого выпуска: Caddy стартует с import sites/*.caddy.
printf '' > "$EDGE/sites/.keep"
log info rehearsal_step step=edge_up port="$REHEARSAL_HTTP_PORT"
edge up -d --quiet-pull

# slot_tag <slot> — тег образа запущенной ячейки.
slot_tag() {
  local id
  id=$(app ps -q "api-$1")
  [[ -n $id ]] || { printf 'stopped'; return; }
  docker inspect -f '{{.Config.Image}}' "$id" | sed 's/.*://'
}
state_of() { sed -n "s/^$1=//p" "$APP/slots.env"; }
expect_slots() {
  local active tag
  active=$(state_of ACTIVE_SLOT)
  tag=$(slot_tag "$active")
  if [[ $active != "$1" || $tag != "$2" ]]; then
    log error rehearsal_slots_mismatch expected_slot="$1" expected_tag="$2" slot="$active" tag="$tag"
    exit 1
  fi
  local other=a
  [[ $1 == a ]] && other=b
  if [[ $(slot_tag "$other") != stopped ]]; then
    log error rehearsal_old_slot_running slot="$other"
    exit 1
  fi
}

log info rehearsal_step step=release_v1
t=$(now_ms)
release --tag rehearsal-v1
d_v1=$(($(now_ms) - t))
expect_slots a rehearsal-v1
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/livez")
[[ $code == 200 ]] || { log error rehearsal_smoke_failed url="$BASE_URL/livez" code="$code"; exit 1; }

# Нагрузка: /livez — 200 от приложения; /api/v1/me без сессии — 401 от приложения (а не
# 502/503 прокси). Код 000 — ошибка соединения.
RESULTS="$STATE/requests.log"
: > "$RESULTS"
(
  trap 'exit 0' TERM
  while :; do
    for path in /livez /api/v1/me; do
      c=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$BASE_URL$path" || true)
      printf '%s %s %s\n' "$(date -u +%H:%M:%S)" "$path" "${c:-000}" >> "$RESULTS"
    done
    sleep 0.05
  done
) &
LOAD_PID=$!
sleep 2

log info rehearsal_step step=release_v2
t=$(now_ms)
release --tag rehearsal-v2
d_v2=$(($(now_ms) - t))
expect_slots b rehearsal-v2
sleep 2

log info rehearsal_step step=rollback
t=$(now_ms)
release --rollback
d_rb=$(($(now_ms) - t))
expect_slots a rehearsal-v1
sleep 2

log info rehearsal_step step=reapply_configuration
sed -i 's/^EDGE_ADMIN_ALLOW=.*/EDGE_ADMIN_ALLOW=127.0.0.1\/32 private_ranges/' "$APP/stack.env"
release --tag rehearsal-v1 --reapply
expect_slots b rehearsal-v1
grep -q '127.0.0.1/32 private_ranges' "$EDGE/sites/maxim-rehearsal.caddy"
sleep 2

kill "$LOAD_PID" 2>/dev/null || true
wait "$LOAD_PID" 2>/dev/null || true
LOAD_PID=""

# The load is already stopped: a tariff change deliberately uses a maintenance window.
if ((FEE_TRANSITION)); then
  expect_fee() {
    local slot=$1 expected=$2 id actual kind
    for kind in api worker; do
      id=$(app ps -q "$kind-$slot")
      [[ -n $id ]] || { log error fee_process_missing service="$kind-$slot"; exit 1; }
      # Render only the matching nonsensitive variable, never the full environment.
      actual=$(docker inspect -f '{{range .Config.Env}}{{if eq . "PLATFORM_FEE_RATE_BP='"$expected"'"}}matched{{end}}{{end}}' "$id")
      [[ $actual == matched ]] || { log error fee_configuration_mismatch service="$kind-$slot" expected="$expected"; exit 1; }
    done
    log info fee_configuration_verified slot="$slot" platform_fee_rate_bp="$expected"
  }
  expect_fee b 1500
  log info rehearsal_step step=fee_cutover_2500
  app stop api-a api-b worker-a worker-b
  sed -i 's/^PLATFORM_FEE_RATE_BP=.*/PLATFORM_FEE_RATE_BP=2500/' "$APP/app.env"
  release --tag rehearsal-v1 --reapply
  expect_slots a rehearsal-v1
  expect_fee a 2500
  log info rehearsal_step step=fee_rollback_1500
  app stop api-a api-b worker-a worker-b
  sed -i 's/^PLATFORM_FEE_RATE_BP=.*/PLATFORM_FEE_RATE_BP=1500/' "$APP/app.env"
  release --rollback
  expect_slots b rehearsal-v1
  expect_fee b 1500
fi

total=$(wc -l < "$RESULTS" | tr -d ' ')
bad=$(awk '($2 == "/livez" && $3 != "200") || ($2 == "/api/v1/me" && $3 != "401")' "$RESULTS" | wc -l | tr -d ' ')
conn=$(awk '$3 == "000"' "$RESULTS" | wc -l | tr -d ' ')
s5xx=$(awk '$3 ~ /^5/' "$RESULTS" | wc -l | tr -d ' ')
orphans=$(app exec -T postgres psql -U maxim -d maxim -At \
  -c "SELECT count(*) FROM job WHERE status = 'running' AND locked_by IS NULL" | tr -d '\r')
ok=true
if ((total < 50 || bad > 0 || orphans > 0)); then
  ok=false
  awk '($2 == "/livez" && $3 != "200") || ($2 == "/api/v1/me" && $3 != "401")' "$RESULTS" | head -n 20 >&2
fi
log "$([[ $ok == true ]] && echo info || echo error)" rehearsal_result ok="$ok" requests="$total" unexpected="$bad" \
  errors_5xx="$s5xx" connection_errors="$conn" orphan_running_jobs="$orphans" \
  release_v1_ms="$d_v1" release_v2_ms="$d_v2" rollback_ms="$d_rb" duration_ms="$(($(now_ms) - started))"
[[ $ok == true ]]
