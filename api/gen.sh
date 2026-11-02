#!/bin/sh
# Генерация Go-кода REST по разделам (запуск из корня репозитория: task gen:api).
#   api/common.yaml    → server/internal/http/api/common/common.gen.go  (package apicommon: общие модели и ответы)
#   api/<раздел>.yaml  → server/internal/http/api/<раздел>/<раздел>.gen.go (package <раздел>api: модели,
#                         chi-роутер, strict-сервер). Ссылки "common.yaml#/..." → пакет apicommon.
# Раздел — имя файла [a-z][a-z0-9]* (это имя Go-пакета). Конвенции — api/README.md.
set -eu
cd "$(dirname "$0")/.."
OAPI=${OAPI_CODEGEN:-oapi-codegen}
BASE=github.com/aitmaganbetov/zafirogo/server/internal/http/api
OUT=server/internal/http/api
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Удаляем прежний сгенерированный код: раздел, чей файл удалён, исчезает вместе с пакетом.
find "$OUT" -name '*.gen.go' -delete
find "$OUT" -mindepth 1 -type d -empty -delete

mkdir -p "$OUT/common"
cat > "$tmp/common.yaml" <<CFG
package: apicommon
output: $OUT/common/common.gen.go
generate: {models: true, strict-server: true}
output-options: {skip-prune: true}
CFG
"$OAPI" -config "$tmp/common.yaml" api/common.yaml

for spec in api/*.yaml; do
  name=$(basename "$spec" .yaml)
  [ "$name" = common ] && continue
  case "$name" in
    [!a-z]* | *[!a-z0-9]*) echo "api/$name.yaml: имя раздела — [a-z][a-z0-9]*" >&2; exit 1 ;;
  esac
  mkdir -p "$OUT/$name"
  cat > "$tmp/$name.yaml" <<CFG
package: ${name}api
output: $OUT/$name/$name.gen.go
generate: {models: true, chi-server: true, strict-server: true}
import-mapping:
  common.yaml: $BASE/common
CFG
  "$OAPI" -config "$tmp/$name.yaml" "$spec"
done

# Справка /docs/api показывает спецификацию раздела v1 (go:embed не видит каталог api/ — копия в пакете public).
cp api/v1.yaml server/internal/http/sections/public/openapi-v1.yaml
