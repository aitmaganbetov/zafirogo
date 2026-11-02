# Образ сервера zafiro: один бинарник со вшитым фронтом + инструменты выкладки (docs/deploy.md).
#
#   docker build -t zafiro:dev .                          # или: task image
#   docker build --build-arg VERSION=v1.2.3 -t zafiro:v1.2.3 .
#
# Содержимое финального образа (distroless/static, без shell и пакетного менеджера, пользователь nonroot 65532):
#   /usr/local/bin/zafiro              — сервер (HTTP :8080, gRPC агентов :8443), ENTRYPOINT;
#   /usr/local/bin/goose               — миграции (job миграций под ролью-владельцем схемы, PD-9): `goose up`;
#   /usr/local/bin/migrate-from-mysql  — перенос клуба из MySQL (tools/migrate-from-mysql/README.md);
#   /migrations                        — db/migrations (GOOSE_MIGRATION_DIR);
#   /seed                              — db/seed (переводы новых строк: migrate-from-mysql --translations-seed /seed).
# CA-сертификаты и tzdata — из distroless; в бинарники tzdata ещё и вшит (-tags timetzdata): часовые пояса клубов
# работают в любом базовом образе.
# HEALTHCHECK не задан: в образе нет shell/curl; проверки — /healthz (liveness) и /readyz (readiness) оркестратора.
#
# Версии закреплены тегом и digest (PD-4: не моложе 14 дней; обновляет Dependabot, ecosystem docker).
#
# За корпоративным прокси с подменой TLS: корневой сертификат прокси передаётся только на время сборки секретом
# (в слои образа не попадает): docker build --secret id=build_ca,src=/путь/ca-bundle.crt .

ARG NODE_IMAGE=node:22.23.3-alpine3.24@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402
ARG GO_IMAGE=golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

# ---------- 1. фронт ----------
FROM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm --mount=type=secret,id=build_ca \
    if [ -s /run/secrets/build_ca ]; then export NODE_EXTRA_CA_CERTS=/run/secrets/build_ca; fi && \
    npm ci --no-audit --no-fund --ignore-scripts
COPY web/ ./
# tsc -b проверяет и тесты: vat.test.ts сверяется с общей таблицей НДС сервера.
COPY server/internal/cash/testdata/vat.json /src/server/internal/cash/testdata/vat.json
RUN npm run build

# ---------- 2. Go ----------
FROM ${GO_IMAGE} AS go
# GOTOOLCHAIN=local: собирать ровно той версией, что в образе (server/go.mod требует её же), без скачивания.
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-buildvcs=false
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=secret,id=build_ca \
    if [ -s /run/secrets/build_ca ]; then export SSL_CERT_FILE=/run/secrets/build_ca; fi && \
    go mod download
# goose — та же версия, что `task tools` (Taskfile GOOSE_VERSION); только драйвер Postgres.
ARG GOOSE_VERSION=v3.29.0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build --mount=type=secret,id=build_ca \
    if [ -s /run/secrets/build_ca ]; then export SSL_CERT_FILE=/run/secrets/build_ca; fi && \
    GOBIN=/out go install -trimpath -ldflags="-s -w" \
      -tags 'no_clickhouse no_libsql no_mssql no_mysql no_sqlite3 no_vertica no_ydb' \
      github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}
COPY server/ ./
COPY --from=web /src/web/dist/ ./internal/webui/dist/
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -tags timetzdata -ldflags="-s -w -X main.buildVersion=${VERSION}" -o /out/zafiro ./cmd/zafiro && \
    go build -trimpath -tags timetzdata -ldflags="-s -w" -o /out/migrate-from-mysql ./cmd/migrate-from-mysql

# ---------- 3. финальный образ ----------
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
LABEL org.opencontainers.image.title="zafiro" \
      org.opencontainers.image.description="Zafiro: биллинг компьютерных клубов (сервер, gRPC агентов, вшитый фронт)" \
      org.opencontainers.image.source="https://github.com/aitmaganbetov/zafirogo" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=go /out/zafiro /out/goose /out/migrate-from-mysql /usr/local/bin/
COPY db/migrations/ /migrations/
COPY db/seed/ /seed/
ENV GOOSE_DRIVER=postgres \
    GOOSE_MIGRATION_DIR=/migrations \
    ZAFIRO_ENV=prod \
    ZAFIRO_LOG_FORMAT=json
USER 65532:65532
EXPOSE 8080 8443
ENTRYPOINT ["/usr/local/bin/zafiro"]
