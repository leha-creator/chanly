# Проект Chanly

Бэкенд биржи рекламы в каналах MAX. Go, PostgreSQL, sqlc, net/http и OpenAPI;
единый бинарник с режимами api/worker/migrate. Приватное и публичное объектное
хранилище S3. Фронтенд React находится в соседнем `D:/work/maxim-frontend`.
Архитектура и границы модулей — в `ARCHITECTURE.md`.

Поддержка (2026-11-18): отдельный MAX-бот и модуль `internal/support`, очередь
обращений/ответов, картинки, видео, аудио и голосовые. Текст переписки не входит
в технические логи/outbound history/SSE/job payload. Отправки идут через worker.

Дополнительные runtime-зависимости поддержки: FFmpeg, ffprobe и Linux prlimit
(util-linux). Образ использует Debian bookworm-slim, UID 65532; код Go собирается
с CGO_ENABLED=0. Подключение и эксплуатация — `docs/support.md`.
