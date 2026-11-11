# SSE кабинетов — реализация в dev

Исходные изменения рабочего дерева сохраняются. Фронтенд используется только для чтения.
Push, deploy и включение общего стенда не входят в задачу.

- [x] Транзакционный журнал изменений и матрица источников/audience.
- [x] Один reader на API-реплику, bounded hub, восстановление и остановка.
- [x] Streaming endpoints внутри Session/AdminGate, повторная проверка прав и таймауты.
- [x] OpenAPI, генерация, mockapi и документация.
- [x] Unit/integration/race, две реплики, rollback, proxy >2 минут, проверки репозитория.

Tasker: подключённого инструмента в текущей сессии нет; задачи, статусы и ключи не созданы.

Проверено: PostgreSQL rollback/две реплики/поздний commit/recovery/epoch/overflow,
HTTP-права и expiry, настоящий Worker → две API-сборки, mockapi, go vet, sqlc vet,
Docker build. Production Caddy 2.11.7: HTTP/2 135 секунд, 9 heartbeat, IP allow/deny.
Полный Windows-прогон: единственный сбой — устаревшее ожидание минимальной цены
100 ₽ из существующей до SSE миграции 00105; тест обновлён на запрет нулевой цены,
целевая перепроверка успешна.

Итог 2026-11-12: полный `go test -race -p 2 -count=1 ./...` в Linux успешен;
затем полный `go test -race -count=1 ./...` mockapi успешен. Финальные go vet
обоих модулей, sqlc vet, воспроизводимость OpenAPI/sqlc-генерации, Docker build
и git diff --check успешны. Миграция проверена up/down/up под штатной ролью.
Повторный production Caddy HTTP/2 прогон: 135.022 секунды, 9 heartbeat,
IP allow/deny и shutdown. Dev Caddy template проходит validate.
Настоящий Chromium EventSource с cookie трёх ролей в локальном mockapi:
ready → resync_required при сдвиге часов → heartbeat. Локальный сервер остановлен.

Остаётся за пределами backend-задачи: генерация типов в соседнем frontend,
его полная E2E-приёмка и включение флага на общем стенде отдельным запросом.
Нужна миграция 00107; новых серверных env-переменных нет. Revision/ETag поста
оценены отдельно в docs/realtime.md; атомарный If-Match не добавлен.
