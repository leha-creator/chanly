#!/usr/bin/env bash
# Isolated Git fixtures: reviewed widening must not weaken immutable/destructive guards.
set -Eeuo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fixture_number=0

new_fixture() {
  fixture_number=$((fixture_number + 1))
  repo="$tmp/repo-$fixture_number"
  mkdir -p "$repo/deploy/scripts" "$repo/migrations"
  cp "$ROOT/deploy/scripts/"{check-migrations.sh,lib.sh,reviewed-migration-expansions.txt} "$repo/deploy/scripts/"
  git -C "$repo" init -q
  git -C "$repo" config user.name 'Migration guard test'
  git -C "$repo" config user.email 'migration-guard@example.test'
  commit_fixture
  base=$(git -C "$repo" rev-parse HEAD)
}
commit_fixture() {
  git -C "$repo" add .
  git -C "$repo" commit -qm fixture --allow-empty
}
expect_pass() {
  if ! (cd "$repo" && bash deploy/scripts/check-migrations.sh "$base") > "$tmp/result" 2>&1; then
    cat "$tmp/result"; echo "ERROR expected compatible migration: $1"; exit 1
  fi
  grep -q "$1" "$tmp/result"
}
expect_fail() {
  if (cd "$repo" && bash deploy/scripts/check-migrations.sh "$base") > "$tmp/result" 2>&1; then
    cat "$tmp/result"; echo "ERROR unsafe migration accepted: $1"; exit 1
  fi
  grep -q "$1" "$tmp/result"
}
add_reviewed() {
  cp "$ROOT/migrations/00105_channel_price_dev_minimum.sql" "$repo/migrations/"
}

new_fixture
add_reviewed
commit_fixture
expect_pass migration_reviewed_expansion

new_fixture
add_reviewed
printf '\nDROP TABLE organization;\n' >> "$repo/migrations/00105_channel_price_dev_minimum.sql"
commit_fixture
expect_fail reviewed_expansion_digest_mismatch

new_fixture
cp "$ROOT/migrations/00105_channel_price_dev_minimum.sql" "$repo/migrations/00200_copy.sql"
commit_fixture
expect_fail migration_incompatible

new_fixture
printf '%s\n' '-- +goose Up' 'DROP TABLE organization;' > "$repo/migrations/00200_unsafe.sql"
commit_fixture
expect_fail migration_incompatible

# Existing migration edits still fail, even with a matching manifest digest.
new_fixture
add_reviewed
commit_fixture
base=$(git -C "$repo" rev-parse HEAD)
printf '\n-- changed\n' >> "$repo/migrations/00105_channel_price_dev_minimum.sql"
digest=$(sha256sum "$repo/migrations/00105_channel_price_dev_minimum.sql" | cut -d ' ' -f 1)
printf '%s  migrations/00105_channel_price_dev_minimum.sql\n' "$digest" > "$repo/deploy/scripts/reviewed-migration-expansions.txt"
commit_fixture
expect_fail migration_changed

new_fixture
add_reviewed
commit_fixture
base=$(git -C "$repo" rev-parse HEAD)
rm "$repo/migrations/00105_channel_price_dev_minimum.sql"
commit_fixture
expect_fail migration_changed

new_fixture
add_reviewed
commit_fixture
base=$(git -C "$repo" rev-parse HEAD)
git -C "$repo" mv migrations/00105_channel_price_dev_minimum.sql migrations/00200_renamed.sql
commit_fixture
expect_fail migration_changed

new_fixture
printf '%s\n' '-- +goose Up' 'CREATE TABLE additive(id uuid);' '-- +goose Down' 'DROP TABLE additive;' > "$repo/migrations/00200_additive.sql"
commit_fixture
expect_pass check_migrations_ok

new_fixture
printf '%s\n' '-- release: contract' '-- +goose Up' 'DROP TABLE obsolete;' > "$repo/migrations/00200_contract.sql"
commit_fixture
expect_pass migration_contract

new_fixture
printf '%s\n' 'invalid-digest migrations/00105_channel_price_dev_minimum.sql' > "$repo/deploy/scripts/reviewed-migration-expansions.txt"
commit_fixture
expect_fail reviewed_expansion_manifest_invalid

new_fixture
cat "$repo/deploy/scripts/reviewed-migration-expansions.txt" >> "$tmp/duplicate"
cat "$tmp/duplicate" >> "$repo/deploy/scripts/reviewed-migration-expansions.txt"
commit_fixture
expect_fail reviewed_expansion_manifest_invalid

echo 'INFO migration guards passed: exact expansion, digest/path checks, immutable files and destructive SQL'
