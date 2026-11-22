// Служебные бинарники образа сервера (аудит OPS-7/OPS-14/OPS-15/OPS-20): обёртка job миграций и проверка здоровья
// для distroless-образа без shell. Отдельный модуль: сервер (server/) от них не зависит; собирает Dockerfile,
// проверяет .github/workflows/deploy.yml. Версии зависимостей — те же, что в server/go.mod.
module github.com/aitmaganbetov/zafirogo/deploy/ops

go 1.27.4

require github.com/jackc/pgx/v5 v5.11.0

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	golang.org/x/text v0.42.0 // indirect
)
