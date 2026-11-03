#!/usr/bin/env bash
# Run from a full checkout as root. Keys are public keys, never private credentials.
# bash deploy/app/bootstrap.sh --deploy-pubkey "ssh-ed25519 ..." --admin-pubkey "ssh-ed25519 ..."
set -Eeuo pipefail
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
LOG_COMPONENT=production-bootstrap
# shellcheck source=deploy/scripts/lib.sh
. "$SCRIPT_DIR/../scripts/lib.sh"
trap 'log error bootstrap_failed line="$LINENO"' ERR
[[ $(id -u) == 0 ]] || { log error root_required; exit 1; }
for arg in "$@"; do
  [[ $arg != --app-dir* ]] || { log error fixed_production_directory; exit 2; }
done
log info bootstrap_started directory=/opt/maxim-production
bash "$SCRIPT_DIR/../bootstrap.sh" "$@" --app-dir /opt/maxim-production
install -d -m 750 -o deploy -g deploy /opt/edge /opt/edge/sites
for file in compose.yml Caddyfile; do
  source_file="$SCRIPT_DIR/../edge/$file"
  if [[ -f /opt/edge/$file ]] && ! cmp -s "$source_file" "/opt/edge/$file"; then
    log error edge_configuration_exists file="$file" hint="review existing edge configuration before replacing it"
    exit 1
  fi
  install -m 640 -o deploy -g deploy "$source_file" "/opt/edge/$file"
done
if ! docker network inspect edge >/dev/null 2>&1; then
  docker network create edge >/dev/null
  log info network_created network=edge
fi
for file in compose.yml maxim-app.caddy release.sh compose.storage.yml storage-init.sh storage-release.sh; do
  install -m 640 -o deploy -g deploy "$SCRIPT_DIR/$file" "/opt/maxim-production/$file"
done
install -m 640 -o deploy -g deploy "$SCRIPT_DIR/../scripts/lib.sh" /opt/maxim-production/lib.sh
chmod 750 /opt/maxim-production/release.sh
for file in app.env stack.env; do
  if [[ ! -f /opt/maxim-production/$file ]]; then
    install -m 600 -o deploy -g deploy "$SCRIPT_DIR/env/production/$file.example" "/opt/maxim-production/$file"
    log warn configuration_requires_secrets file="$file"
  fi
done
cd /opt/edge
docker compose run --rm --no-deps caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
docker compose up -d
log info bootstrap_done hint="verify deploy key login; fill production configuration before release"
