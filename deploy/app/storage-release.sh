#!/usr/bin/env bash
# Run from the production application directory, independently of backend slots.
set -Eeuo pipefail
cd "$(dirname "$0")"
[[ -f storage.env ]] || { echo 'storage.env is required' >&2; exit 1; }
dc() { docker compose -f compose.storage.yml --env-file storage.env "$@"; }
dc up -d --wait --wait-timeout 180 minio
dc run --rm --no-deps minio-init
site=/opt/edge/sites/maxim-production-storage.caddy
previous=$(mktemp)
had_previous=0
if [[ -f $site ]]; then cp "$site" "$previous"; had_previous=1; fi
trap 'rm -f "$previous"' EXIT
cat >"$site" <<'CADDY'
s3.chanly.ru {
    reverse_proxy maxim-production-storage:9000
}
CADDY
if ! (cd /opt/edge && docker compose exec -T caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile && docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile); then
  if [[ $had_previous == 1 ]]; then cp "$previous" "$site"; else rm -f "$site"; fi
  exit 1
fi
echo 'Production MinIO running; public HTTPS requires s3.chanly.ru DNS'
