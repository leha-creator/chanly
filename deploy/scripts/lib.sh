# shellcheck shell=bash
# Общие функции скриптов эксплуатации: журнал JSON по одной строке (07 §5).
# Подключается через `. "$(dirname "$0")/../scripts/lib.sh"`; компонент — LOG_COMPONENT.
#
#   log info release_step step=migrate tag=sha-1a2b3c4 duration_ms=1200
#   → {"ts":"…","level":"info","component":"release","event":"release_step","step":"migrate",…}
#
# Числа без ведущих нулей пишутся числами, остальное — строками. Секреты в журнал не передавать.

json_escape() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\r'/}
  s=${s//$'\t'/\\t}
  printf '%s' "$s"
}

# log <level> <event> [key=value ...]
log() {
  local level=$1 event=$2 out kv k v
  shift 2
  # Debug diagnostics are opt-in; operational checkpoints remain visible by default.
  case "${LOG_LEVEL:-info}:$level" in
    debug:*) ;;
    *:debug|warn:info|error:info|error:warn) return 0 ;;
  esac
  out=$(printf '{"ts":"%s","level":"%s","component":"%s","event":"%s"' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$level" "${LOG_COMPONENT:-deploy}" "$(json_escape "$event")")
  for kv in "$@"; do
    k=${kv%%=*}
    v=${kv#*=}
    if [[ $v =~ ^(0|-?[1-9][0-9]{0,15}|true|false)$ ]]; then
      out+=",\"$(json_escape "$k")\":$v"
    else
      out+=",\"$(json_escape "$k")\":\"$(json_escape "$v")\""
    fi
  done
  printf '%s}\n' "$out"
}

# now_ms — миллисекунды с начала эпохи (для duration_ms).
now_ms() {
  local s
  # bash 5: EPOCHREALTIME с микросекундами (busybox date в образе postgres не знает %N).
  if [[ ${EPOCHREALTIME:-} =~ ^([0-9]+)[.,]([0-9]{3}) ]]; then
    printf '%s%s' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
    return
  fi
  s=$(date +%s%N 2>/dev/null)
  if [[ $s =~ ^[0-9]+$ && ${#s} -gt 12 ]]; then
    printf '%s' $((s / 1000000))
  else
    printf '%s' $(($(date +%s) * 1000))
  fi
}
