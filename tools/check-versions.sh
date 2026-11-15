#!/bin/sh
# Версии инструментов записаны в одном месте — tools/versions.env (аудит BC-09). Скрипт ловит расхождения там,
# где файл прочитать нельзя или версия продублирована текстом:
#   1. в Taskfile, Dockerfile, workflows и скриптах нет второго определения *_VERSION из tools/versions.env;
#   2. литералы `…/cmd/<инструмент>@vX.Y.Z` (комментарии buf.yaml, buf.gen.yaml и т.п.) совпадают с файлом;
#   3. образ node в Dockerfile (NODE_IMAGE, обновляет Dependabot) — той же версии, что NODE_VERSION для CI;
#   4. protoc-gen-go — той же версии, что рантайм google.golang.org/protobuf в server/go.mod.
# Запуск из любого каталога: sh tools/check-versions.sh (CI: .github/workflows/go.yml, task versions:check).
set -eu
cd "$(dirname "$0")/.."
. tools/versions.env
fail=0
err() { echo "check-versions: $*" >&2; fail=1; }

files=$(git ls-files -co --exclude-standard -- Taskfile.yml Dockerfile 'deploy/**' '.github/**' 'tools/**' 'agent/**/*.sh' 'api/*.sh' buf.yaml buf.gen.yaml sqlc.yaml \
  | grep -v -e '^tools/versions.env$' -e '^tools/check-versions.sh$' -e '/package-lock.json$' -e '^tools/visual/fixtures/' \
  | while IFS= read -r f; do [ -f "$f" ] && echo "$f"; done)

# 1. Второе определение переменной.
names=$(sed -n 's/^\([A-Z_]*_VERSION\)=.*/\1/p' tools/versions.env)
for name in $names; do
  # shellcheck disable=SC2086
  hits=$(grep -nE "(^|[[:space:]])(ARG |ENV )?${name}[:=][[:space:]]*\"?v?[0-9]" $files /dev/null || true)
  [ -z "$hits" ] || err "$name определена вне tools/versions.env:
$hits"
done

# 2. Литералы «инструмент@версия».
check_literal() { # $1 — хвост пути модуля, $2 — ожидаемая версия
  # shellcheck disable=SC2086
  grep -noE "$1@v[0-9]+\.[0-9]+\.[0-9]+" $files /dev/null | while IFS= read -r line; do
    case "$line" in
      *"@$2") ;;
      *) echo "$line (ожидается $2)" ;;
    esac
  done
}
bad=$(
  check_literal 'cmd/task' "$TASK_VERSION"
  check_literal 'cmd/sqlc' "$SQLC_VERSION"
  check_literal 'cmd/goose' "$GOOSE_VERSION"
  check_literal 'cmd/oapi-codegen' "$OAPI_CODEGEN_VERSION"
  check_literal 'cmd/buf' "$BUF_VERSION"
  check_literal 'cmd/protoc-gen-go' "$PROTOC_GEN_GO_VERSION"
  check_literal 'cmd/protoc-gen-go-grpc' "$PROTOC_GEN_GO_GRPC_VERSION"
  check_literal 'cmd/golangci-lint' "$GOLANGCI_LINT_VERSION"
  check_literal 'cmd/govulncheck' "$GOVULNCHECK_VERSION"
  check_literal 'cmd/gosec' "$GOSEC_VERSION"
)
[ -z "$bad" ] || err "версия в тексте расходится с tools/versions.env:
$bad"

# 3. Node: образ Dockerfile ↔ NODE_VERSION.
docker_node=$(sed -n 's/^ARG NODE_IMAGE=node:\([0-9.]*\).*/\1/p' Dockerfile)
[ "$docker_node" = "$NODE_VERSION" ] || err "Dockerfile NODE_IMAGE — node $docker_node, tools/versions.env NODE_VERSION — $NODE_VERSION"

# 4. protoc-gen-go ↔ google.golang.org/protobuf.
mod_pb=$(sed -n 's/^[[:space:]]*google\.golang\.org\/protobuf \(v[0-9.]*\).*/\1/p' server/go.mod | head -1)
[ "$mod_pb" = "$PROTOC_GEN_GO_VERSION" ] || err "server/go.mod protobuf $mod_pb, PROTOC_GEN_GO_VERSION $PROTOC_GEN_GO_VERSION"

[ "$fail" = 0 ] && echo "check-versions: ok"
exit "$fail"
