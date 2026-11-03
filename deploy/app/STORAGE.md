# MinIO в production

MinIO запускается отдельным постоянным Compose-проектом `maxim-production-storage`.
Данные находятся в Docker volume `maxim-production-storage_minio-data`.
Не выполняйте `down -v`: это удалит объекты.

1. Добавьте A-запись `s3.chanly.ru → 194.58.120.196`.
2. Скопируйте `compose.storage.yml`, `storage-init.sh`, `storage-release.sh` в
   `/opt/maxim-production`. Создайте там `storage.env` по шаблону
   `env/production/storage.env.example`, задайте независимые случайные root-ключи
   и ключи приложения; права файла `600`, владелец `deploy`.
3. Выполните `bash /opt/maxim-production/storage-release.sh`.
4. В `app.env` задайте `S3_ENDPOINT=s3.chanly.ru`, `S3_REGION=us-east-1`,
   `S3_USE_SSL=true`, `S3_PUBLIC_BASE_URL=https://s3.chanly.ru/maxim-production-public`,
   корзины `maxim-production-private` и `maxim-production-public`.
   `S3_ACCESS_KEY` и `S3_SECRET_KEY` должны совпадать с ключами приложения в
   `storage.env`. `S3_PRESIGN_ENDPOINT` оставьте пустым.

Наличие `storage.env` включает обновление MinIO при выпуске бэкенда.
CI доставляет файлы хранилища для production; секреты остаются на сервере.
Приватная корзина закрыта, публичная разрешает только скачивание по известному
ключу. Приложению разрешены операции только с этими двумя корзинами.
Повторный запуск сохраняет объекты и восстанавливает политики доступа.

Проверка: `curl -fsS https://s3.chanly.ru/minio/health/live`.
Консоль доступна через SSH-туннель
`ssh -L 9001:127.0.0.1:9001 deploy@194.58.120.196` и `http://localhost:9001`.
Порт S3 наружу не публикуется; HTTPS обслуживает общий Caddy.

`BACKUP_S3_*` должны указывать на отдельное внешнее хранилище: MinIO на том же
сервере не защищает от потери сервера или его диска. Настройка MinIO сама по себе
не завершает запуск бэкенда: остаются обязательные параметры интеграций и бизнеса.
