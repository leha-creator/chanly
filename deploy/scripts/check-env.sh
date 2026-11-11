#!/usr/bin/env bash
# Проверка шаблонов окружений deploy/app/env/<env>/{app,stack}.env.example:
#   - каждая переменная app.env.example известна .env.example (опечатки и забытые переменные);
#   - в app.env.example нет переменных стека (пароли суперпользователя, ключи копий);
#   - каждая переменная stack.env.example — из списка переменных стека, обязательные заполнены;
#   - нет повторов ключей; во всех файлах есть блок «Модули в разработке».
#
# Использование (из любого каталога):
#   deploy/scripts/check-env.sh                 — проверка, код 1 при нарушении
#   deploy/scripts/check-env.sh --print-missing — ещё и список переменных .env.example,
#                                                 которых нет в шаблоне (для сверки после
#                                                 слияния модулей; не ошибка)
set -Eeuo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
LOG_COMPONENT=check-env
# shellcheck source=deploy/scripts/lib.sh
. "$ROOT/deploy/scripts/lib.sh"

PRINT_MISSING=0
for arg in "$@"; do
  case "$arg" in
    --print-missing) PRINT_MISSING=1 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) log error invalid_argument arg="$arg"; exit 2 ;;
  esac
done

MODULES_MARKER='=== Модули в разработке ==='

# Переменные стека: подстановка в compose.yml, контейнеры postgres и backup (stack.env).
STACK_KEYS=(
  COMPOSE_PROJECT_NAME MAXIM_IMAGE MAXIM_POSTGRES_IMAGE APP_ENV_FILE DOMAIN EDGE_ALIAS_PREFIX EDGE_ADMIN_ALLOW
  POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD POSTGRES_APP_ROLE POSTGRES_APP_PASSWORD
  POSTGRES_BACKUP_PASSWORD POSTGRES_ARCHIVE_TIMEOUT MIGRATE_DATABASE_URL
  BACKUP_S3_ENDPOINT BACKUP_S3_ACCESS_KEY BACKUP_S3_SECRET_KEY BACKUP_S3_BUCKET BACKUP_S3_USE_SSL
  BACKUP_PREFIX BACKUP_DUMP_RETENTION_DAYS BACKUP_BASE_RETENTION_DAYS BACKUP_MAX_AGE_HOURS
  BACKUP_WAL_INTERVAL BACKUP_BASE_CRON BACKUP_DUMP_CRON BACKUP_OBJECTS_CRON BACKUP_PRUNE_CRON
  BACKUP_RESTORE_CHECK_CRON
)
# Без них стек не поднимется.
STACK_REQUIRED=(
  COMPOSE_PROJECT_NAME MAXIM_IMAGE MAXIM_POSTGRES_IMAGE DOMAIN EDGE_ALIAS_PREFIX EDGE_ADMIN_ALLOW POSTGRES_PASSWORD
  POSTGRES_APP_PASSWORD POSTGRES_BACKUP_PASSWORD MIGRATE_DATABASE_URL BACKUP_S3_ENDPOINT
  BACKUP_S3_ACCESS_KEY BACKUP_S3_SECRET_KEY BACKUP_S3_BUCKET BACKUP_PREFIX
)

errors=0
fail() {
  errors=$((errors + 1))
  log error "$@"
}

# keys_of <file> — ключи строк KEY=VALUE (комментарии и пустые строки пропускаются).
keys_of() {
  sed -n 's/\r$//; s/^\([A-Z][A-Z0-9_]*\)=.*/\1/p' "$1"
}

# value_of <file> <key>
value_of() {
  sed -n "s/\r\$//; s/^$2=//p" "$1" | head -n 1
}

contains() {
  local needle=$1 item
  shift
  for item in "$@"; do
    [[ $item == "$needle" ]] && return 0
  done
  return 1
}

check_common() {
  local file=$1 need_marker=$2 dup
  dup=$(keys_of "$file" | sort | uniq -d)
  for k in $dup; do
    fail duplicate_key file="${file#"$ROOT"/}" key="$k"
  done
  if ((need_marker)) && ! grep -qF "$MODULES_MARKER" "$file"; then
    fail modules_block_missing file="${file#"$ROOT"/}"
  fi
}

mapfile -t APP_KEYS < <(keys_of "$ROOT/.env.example")
if [[ ${#APP_KEYS[@]} -eq 0 ]]; then
  fail no_keys file=.env.example
  exit 1
fi
if ! grep -qF "$MODULES_MARKER" "$ROOT/.env.example"; then
  fail modules_block_missing file=.env.example
fi
log info env_check file=.env.example keys="${#APP_KEYS[@]}"

shopt -s nullglob
envs=("$ROOT"/deploy/app/env/*/)
if [[ ${#envs[@]} -eq 0 ]]; then
  fail no_environments dir=deploy/app/env
fi
for dir in "${envs[@]}"; do
  dir=${dir%/}
  env_name=$(basename "$dir")
  app="$dir/app.env.example"
  stack="$dir/stack.env.example"
  for f in "$app" "$stack"; do
    [[ -f $f ]] || fail template_missing env="$env_name" file="${f#"$ROOT"/}"
  done
  [[ -f $app && -f $stack ]] || continue

  check_common "$app" 1
  [[ -z $(value_of "$app" TBANK_MODE) || $(value_of "$app" TBANK_MODE) == disabled ]] || \
    fail tbank_disabled_required env="$env_name" key=TBANK_MODE
  [[ $(value_of "$app" TBANK_ACCESS_MODE) == readonly ]] || \
    fail tbank_readonly_required env="$env_name" key=TBANK_ACCESS_MODE
  [[ $(value_of "$app" TBANK_DEV_REAL_ENABLED) == false ]] || \
    fail tbank_real_dev_disabled_required env="$env_name" key=TBANK_DEV_REAL_ENABLED
  [[ $(value_of "$app" TBANK_PRODUCTION_APPROVED) == false ]] || \
    fail tbank_production_not_admitted env="$env_name" key=TBANK_PRODUCTION_APPROVED
  if [[ $env_name == staging ]]; then
    [[ $(value_of "$app" TBANK_API_URL) == https://business.tbank.ru/openapi/sandbox ]] || \
      fail tbank_sandbox_required env="$env_name" key=TBANK_API_URL
  fi

  if [[ $env_name == staging ]]; then
    [[ $(value_of "$app" ORD_API_URL) == https://ord-prestable.yandex.net ]] || \
      fail ord_sandbox_required env="$env_name" key=ORD_API_URL
    [[ $(value_of "$app" ORD_REPORT_AUTO_SEND) == false ]] || \
      fail ord_auto_send_forbidden env="$env_name" key=ORD_REPORT_AUTO_SEND
  fi
  mapfile -t keys < <(keys_of "$app")
  for k in "${keys[@]}"; do
    if contains "$k" "${STACK_KEYS[@]}"; then
      fail stack_key_in_app_env env="$env_name" key="$k" hint="move it to stack.env.example"
    elif ! contains "$k" "${APP_KEYS[@]}"; then
      fail unknown_key env="$env_name" file=app.env.example key="$k" hint="add it to .env.example or fix the name"
    fi
  done
  log info env_check env="$env_name" file=app.env.example keys="${#keys[@]}"
  if ((PRINT_MISSING)); then
    missing=()
    for k in "${APP_KEYS[@]}"; do
      contains "$k" "${keys[@]}" || missing+=("$k")
    done
    log info env_defaults env="$env_name" count="${#missing[@]}" keys="${missing[*]}" \
      note="not set in the template: the code default from .env.example applies"
  fi

  check_common "$stack" 0
  mapfile -t skeys < <(keys_of "$stack")
  for k in "${skeys[@]}"; do
    contains "$k" "${STACK_KEYS[@]}" || fail unknown_key env="$env_name" file=stack.env.example key="$k" \
      hint="add it to STACK_KEYS in deploy/scripts/check-env.sh"
  done
  for k in "${STACK_REQUIRED[@]}"; do
    if ! contains "$k" "${skeys[@]}" || [[ -z $(value_of "$stack" "$k") ]]; then
      fail required_key_empty env="$env_name" file=stack.env.example key="$k"
    fi
  done
  log info env_check env="$env_name" file=stack.env.example keys="${#skeys[@]}"
done

if ((errors > 0)); then
  log error env_check_result ok=false errors="$errors"
  exit 1
fi
log info env_check_result ok=true
