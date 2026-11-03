#!/usr/bin/env bash
# Regression checks without Docker or network: target validation and same-tag behavior.
set -Eeuo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/app" "$tmp/bin"
cp "$ROOT/deploy/app/compose.yml" "$tmp/app/"
cat > "$tmp/app/stack.env" <<'EOF'
APP_ENV_FILE=./app.env
MAXIM_IMAGE=maxim
COMPOSE_PROJECT_NAME=maxim-test
EDGE_ALIAS_PREFIX=maxim-test
DOMAIN=chanly.ru
EDGE_ADMIN_ALLOW=127.0.0.1/32
EOF
touch "$tmp/app/app.env"
run_release() { bash "$ROOT/deploy/app/release.sh" --dir "$tmp/app" "$@"; }
if run_release --tag test --expected-domain wrong.example > "$tmp/result" 2>&1; then
  echo 'ERROR domain mismatch accepted'; exit 1
fi
grep -q domain_mismatch "$tmp/result"
[[ ! -f $tmp/app/slots.env ]] # Target validation must not mutate state.
if run_release --tag test --expected-domain chanly.ru --edge-dir "$tmp/missing" > "$tmp/result" 2>&1; then
  echo 'ERROR missing edge accepted'; exit 1
fi
grep -q edge_missing "$tmp/result"
printf 'ACTIVE_SLOT=a\nMAXIM_TAG_A=test\nMAXIM_TAG_B=\n' > "$tmp/app/slots.env"
cat > "$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
if [[ $1 == inspect ]]; then printf 'healthy|running\n'; else printf 'container-id\n'; fi
EOF
chmod +x "$tmp/bin/docker"
export PATH="$tmp/bin:$PATH"
run_release --tag test --no-edge > "$tmp/result"
grep -q release_noop "$tmp/result"
run_release --tag test --reapply --no-edge --dry-run > "$tmp/result"
grep -q release_plan "$tmp/result"
if grep -q release_noop "$tmp/result"; then
  echo 'ERROR reapply skipped configuration'; exit 1
fi
if run_release --rollback --no-edge > "$tmp/result" 2>&1; then
  echo 'ERROR first release rollback accepted'; exit 1
fi
grep -q rollback_impossible "$tmp/result"
echo 'INFO release guards passed'
