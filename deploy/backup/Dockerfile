# Образ Postgres с WAL-G для непрерывного бэкапа (Этап 6, O2; выбор WAL-G — docs/open-questions/stage6-backup.md BK-1).
#
# Тот же postgres:18.6, что в dev/staging (одинаковые glibc и коллации — копия поднимается на том же образе),
# плюс статический бинарник WAL-G, собранный из исходников закреплённого тега (проверяется хеш коммита),
# и скрипты deploy/backup/bin. Сборка:
#
#   docker build -t zafiro-postgres-walg:18.6-3.0.9 deploy/backup
#
# Используется и сервером БД (archive_command = wal-push), и сайдкаром расписания (backup-push), и учениями
# восстановления (backup-fetch + restore_command = wal-fetch).
#
# Стадию walg можно подменить готовым бинарником того же тега (BuildKit: --build-context walg=<каталог с out/wal-g>),
# если у сборочной машины нет выхода на github.com; бинарник должен быть собран теми же командами.

ARG GO_IMAGE=golang:1.25.14-trixie@sha256:2c4c60ef415fbfa5e90300722293bef36c5e63fae17570ce18f580af933dbd73
ARG PG_IMAGE=postgres:18.6@sha256:979bccfde35445db618c65107d27d08c6c21339d8dc3ad4c830c6633947be0e4

FROM ${GO_IMAGE} AS walg
# WAL-G v3.0.9 (20.08.2026; PostgreSQL 18 в матрице тестов проекта). Тег сверяется с коммитом:
# подмена тега в репозитории даст ошибку сборки.
ARG WALG_VERSION=v3.0.9
ARG WALG_COMMIT=3e493188db28335fb93a45bd680c6ff8fc3028c1
# Без cgo: статический бинарник без libsodium/brotli/lzo. Сжатие zstd (klauspost, чистый Go),
# шифрование — OpenPGP (чистый Go): на сервере БД только открытый ключ.
ENV CGO_ENABLED=0 GOEXPERIMENT=jsonv2 GOFLAGS=-trimpath
# За прокси с подменой TLS — корневой сертификат секретом сборки (в слои не попадает), как в ../../Dockerfile:
#   docker build --secret id=build_ca,src=/путь/ca-bundle.crt deploy/backup
RUN --mount=type=secret,id=build_ca \
    if [ -s /run/secrets/build_ca ]; then export SSL_CERT_FILE=/run/secrets/build_ca GIT_SSL_CAINFO=/run/secrets/build_ca; fi \
 && git clone --depth 1 --branch "${WALG_VERSION}" https://github.com/wal-g/wal-g /src \
 && test "$(git -C /src rev-parse HEAD)" = "${WALG_COMMIT}" \
 && cd /src/main/pg \
 && go build -ldflags "-s -w -X github.com/wal-g/wal-g/cmd/pg.walgVersion=${WALG_VERSION} -X github.com/wal-g/wal-g/cmd/pg.gitRevision=${WALG_COMMIT}" \
      -o /out/wal-g . \
 && /out/wal-g --version

FROM ${PG_IMAGE}
COPY --from=walg --chmod=0755 /out/wal-g /usr/local/bin/wal-g
COPY --chmod=0755 bin/ /usr/local/bin/
# Каталог метрик (textfile для node_exporter) и рабочий каталог учений — владелец postgres.
RUN install -d -o postgres -g postgres -m 0750 /var/lib/walg-metrics /var/lib/walg-drill \
 && wal-g --version
