# syntax=docker/dockerfile:1
# PostgreSQL бэкенда и инструменты резервного копирования в одном образе: одинаковые версии
# сервера, pg_dump и pg_basebackup. Контейнеры postgres и backup в deploy/app/compose.yml.
# Сборка из каталога deploy (контекст ограничивает Dockerfile.dockerignore рядом):
#   docker build -f deploy/postgres/Dockerfile -t maxim-postgres deploy

# Клиент S3: статический исполняемый файл.
FROM pgsty/mc:RELEASE.2026-09-16T00-00-00Z AS mc

FROM postgres:17.10-alpine3.24
LABEL org.opencontainers.image.source="https://github.com/leha-creator/chanly" \
      org.opencontainers.image.description="Chanly PostgreSQL with WAL archiving and backup tools"

# curl — письма о сбое копии по SMTP (backup-lib.sh, notify_failure); jq — манифесты копий.
# hadolint ignore=DL3018
RUN apk add --no-cache curl jq

COPY --from=mc /usr/bin/mc /usr/local/bin/mc
COPY scripts/lib.sh /usr/local/lib/maxim/lib.sh
COPY postgres/bin/ /usr/local/bin/
COPY postgres/initdb/ /docker-entrypoint-initdb.d/
COPY postgres/crontab /etc/maxim/backup.crontab

# Каталоги с владельцем postgres: именованный том при первом подключении берёт владельца
# из образа, иначе archive_command не сможет писать в архив WAL.
RUN chmod 0755 /usr/local/bin/*.sh /docker-entrypoint-initdb.d/*.sh \
 && install -d -o postgres -g postgres -m 0700 \
      /var/lib/postgresql/wal-archive /var/lib/backup-state /var/lib/backup-tmp \
 && mc --version >/dev/null
