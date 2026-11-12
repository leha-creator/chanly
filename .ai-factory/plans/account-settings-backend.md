# Настройки аккаунта — бэкенд
Tasker MCP недоступен в сессии. Фронтенд и чужие изменения не изменяются.
- [x] Контракт OpenAPI и генерация
- [x] Пользовательские настройки, каталог, конкурентная запись
- [x] Доставка, тихие часы, SSE создания
- [x] Подтверждённая смена почты и сеансы
- [x] Mockapi и документация
- [x] Тесты и итоговая проверка


Проверено 2026-11-12:
- Полный go test ./... и go vet ./... из корня; PostgreSQL в Docker, внешние системы поддельные по архитектуре.
- sqlc vet; миграционные guards; round-trip 00108 с suppressed-строкой.
- Все тесты и vet mockapi, включая TestEveryOperation.
- Linux go test -race -count=1: account/..., notification/..., platform/realtime.
- Linux -race: все TestAccountSettings и SSEEndpoint; финальные коды и их точность времени проверены.
- Caddy/HTTP2 long stream и IP gate: SSE_PROXY_TEST=1, native Windows, успешно.
- Docker image maxim:account-settings-check собран.
- Генерация серверов и mock policy под Linux Go 1.27.2 байт-в-байт совпала.

Среда: общий Linux race-прогон bootstrap с CI=true упёрся в HostAccessPorts/SSH port-forward Docker Desktop на Caddy-тесте; тот же Caddy-тест успешно выполнен отдельно с Windows-хоста. Целевые race-тесты приложения прошли.

Tasker MCP отсутствовал; регистрация в Tasker не выполнена. Коммит, push и развёртывание не выполнялись.
