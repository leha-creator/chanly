#!/bin/sh
set -eu
for value in "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" "$S3_ACCESS_KEY" "$S3_SECRET_KEY"; do
  [ -n "$value" ] && [ "$value" != CHANGE_ME ] || { echo 'Replace storage credential placeholders' >&2; exit 1; }
done
[ "$MINIO_ROOT_USER" != "$S3_ACCESS_KEY" ] || { echo 'Use separate application credentials' >&2; exit 1; }
mc alias set local http://maxim-production-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
mc mb --ignore-existing local/maxim-production-private local/maxim-production-public
mc anonymous set none local/maxim-production-private
# Anonymous users may download known public objects, but cannot list or write.
cat >/tmp/public.json <<'JSON'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::maxim-production-public/*"]}]}
JSON
mc anonymous set-json /tmp/public.json local/maxim-production-public
cat >/tmp/application.json <<'JSON'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetBucketLocation","s3:ListBucket","s3:ListBucketMultipartUploads"],"Resource":["arn:aws:s3:::maxim-production-private","arn:aws:s3:::maxim-production-public"]},{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::maxim-production-private/*","arn:aws:s3:::maxim-production-public/*"]}]}
JSON
mc admin user add local "$S3_ACCESS_KEY" "$S3_SECRET_KEY" >/dev/null
mc admin policy create local maxim-application /tmp/application.json >/dev/null
mc admin policy attach local maxim-application --user "$S3_ACCESS_KEY" >/dev/null
# Optional owner-approved local backups, with credentials isolated from media.
# These copies do not protect against loss of this host or its disk.
if [ "${LOCAL_BACKUPS_ENABLED:-false}" = true ]; then
  for value in "${BACKUP_S3_ACCESS_KEY:-}" "${BACKUP_S3_SECRET_KEY:-}"; do
    [ -n "$value" ] && [ "$value" != CHANGE_ME ] || { echo 'Local backup credentials required' >&2; exit 1; }
  done
  [ "$BACKUP_S3_ACCESS_KEY" != "$S3_ACCESS_KEY" ] && [ "$BACKUP_S3_ACCESS_KEY" != "$MINIO_ROOT_USER" ] || { echo 'Independent backup identity required' >&2; exit 1; }
  mc mb --ignore-existing local/maxim-production-backups
  mc anonymous set none local/maxim-production-backups
  cat >/tmp/backup.json <<'JSON'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetBucketLocation","s3:ListBucket","s3:ListBucketMultipartUploads"],"Resource":["arn:aws:s3:::maxim-production-backups"]},{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::maxim-production-backups/*"]}]}
JSON
  mc admin user add local "$BACKUP_S3_ACCESS_KEY" "$BACKUP_S3_SECRET_KEY" >/dev/null
  mc admin policy create local maxim-backups /tmp/backup.json >/dev/null
  mc admin policy attach local maxim-backups --user "$BACKUP_S3_ACCESS_KEY" >/dev/null
fi
echo 'MinIO buckets and application policy ready'
