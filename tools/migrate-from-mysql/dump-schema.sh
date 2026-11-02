#!/usr/bin/env bash
# Источник для тестов переноса (server/cmd/migrate-from-mysql): НАСТОЯЩАЯ схема MySQL старого Laravel-проекта
# и демо-данные, записанные старым кодом.
#
#   tools/migrate-from-mysql/dump-schema.sh
#
# Что делает (сам старый проект не меняется — всё в одноразовой копии):
#   1. копия ../Zafiro + vendor из кэша (общий механизм tools/php-oracle/run.sh shell);
#   2. mysql:8.4 в Docker (utf8mb4_unicode_ci, как config/database.php старого проекта);
#   3. php:8.4 (+ pdo_mysql, собирается из исходников образа без сети): `php artisan migrate:fresh` — все миграции
#      старого проекта, затем tools/visual/old/seed.php — демо-фикстура tools/visual/fixtures/demo.json,
#      кассовые операции — сервисом старого кода (CashRegisterService), как для визуального сравнения;
#   4. mysqldump: схема (--no-data) → testdata/schema.sql, данные → testdata/demo-data.sql.
#
# Перезапускать при изменении миграций старого проекта или фикстуры; результат коммитится (тесты не запускают PHP).
#
# Переменные: ZAFIRO_SRC (../Zafiro), PHP_IMAGE (mirror.gcr.io/library/php:8.4.27-cli),
# MYSQL_IMAGE (mirror.gcr.io/library/mysql:8.4.13), ORACLE_VENDOR_CACHE (как у tools/php-oracle/run.sh).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
SRC="${ZAFIRO_SRC:-$REPO/../Zafiro}"
PHP_IMAGE="${PHP_IMAGE:-mirror.gcr.io/library/php:8.4.27-cli}"
MYSQL_IMAGE="${MYSQL_IMAGE:-mirror.gcr.io/library/mysql:8.4.13}"
OUT="$REPO/server/cmd/migrate-from-mysql/testdata"
VISUAL="$REPO/tools/visual"
NET="zafirogo-dump-schema-$$"
DB_CONTAINER="zafirogo-dump-mysql-$$"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/zafiro-dump-schema.XXXXXX")"

log() { echo "[dump-schema] $*" >&2; }
cleanup() {
  docker rm -f "$DB_CONTAINER" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

command -v docker >/dev/null || { log "нужен Docker"; exit 2; }
[ -f "$SRC/artisan" ] || { log "нет старого проекта в $SRC (задайте ZAFIRO_SRC)"; exit 2; }

log "копия старого проекта"
APP="$(ORACLE_WORK="$WORK" ORACLE_KEEP=1 ZAFIRO_SRC="$SRC" PHP_IMAGE="$PHP_IMAGE" "$REPO/tools/php-oracle/run.sh" shell)"

log "MySQL $MYSQL_IMAGE"
docker network create "$NET" >/dev/null
docker run -d --name "$DB_CONTAINER" --network "$NET" \
  -e MYSQL_ALLOW_EMPTY_PASSWORD=yes -e MYSQL_DATABASE=zafiro \
  "$MYSQL_IMAGE" --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci >/dev/null
# По TCP: временный сервер инициализации слушает только сокет, готов — окончательный.
for _ in $(seq 1 180); do
  if docker exec "$DB_CONTAINER" mysql -h127.0.0.1 -uroot -e 'SELECT 1' zafiro >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$DB_CONTAINER" mysql -h127.0.0.1 -uroot -e 'SELECT 1' zafiro >/dev/null

log "миграции старого проекта и демо-данные (php artisan migrate:fresh + tools/visual/old/seed.php)"
# Переменные окружения процесса важнее .env (Dotenv в Laravel неизменяемый) — .env копии не трогаем.
docker run --rm --network "$NET" -v "$APP":/app -v "$VISUAL":/visual:ro -w /app \
  -e DB_CONNECTION=mysql -e DB_HOST="$DB_CONTAINER" -e DB_PORT=3306 -e DB_DATABASE=zafiro \
  -e DB_USERNAME=root -e DB_PASSWORD= -e CACHE_STORE=array -e SESSION_DRIVER=array -e QUEUE_CONNECTION=sync \
  "$PHP_IMAGE" sh -c 'docker-php-ext-install pdo_mysql >/dev/null 2>&1 \
    && php artisan migrate:fresh --force >/dev/null \
    && php /visual/old/seed.php --app /app --fixture /visual/fixtures/demo.json'

mkdir -p "$OUT"
# Таблицы инфраструктуры Laravel (не переносятся, docs/schema-changes.md §1) — без данных.
# translations — платформенные (общие для всех клубов, ~7 тыс. строк), не поклубный перенос.
SKIP_DATA=(translations migrations sessions password_reset_tokens cache cache_locks jobs job_batches failed_jobs personal_access_tokens)
IGNORE=()
for t in "${SKIP_DATA[@]}"; do IGNORE+=("--ignore-table=zafiro.$t"); done

log "mysqldump → $OUT"
{
  echo "-- Схема MySQL старого проекта Zafiro после всех миграций (php artisan migrate:fresh)."
  echo "-- Сгенерировано tools/migrate-from-mysql/dump-schema.sh, не править руками."
  docker exec "$DB_CONTAINER" mysqldump -uroot --no-data --skip-comments --skip-add-drop-table --skip-set-charset \
    --no-tablespaces zafiro | sed -E 's/ AUTO_INCREMENT=[0-9]+//'
} > "$OUT/schema.sql"
{
  echo "-- Демо-данные tools/visual/fixtures/demo.json, записанные старым кодом (tools/visual/old/seed.php) в MySQL."
  echo "-- Сгенерировано tools/migrate-from-mysql/dump-schema.sh, не править руками."
  docker exec "$DB_CONTAINER" mysqldump -uroot --no-create-info --skip-comments --skip-extended-insert --complete-insert \
    --skip-set-charset --no-tablespaces --skip-triggers "${IGNORE[@]}" zafiro
} > "$OUT/demo-data.sql"
log "готово: $(grep -c '^CREATE TABLE' "$OUT/schema.sql") таблиц, $(grep -c '^INSERT' "$OUT/demo-data.sql") строк данных"
