#!/usr/bin/env bash
# Сборка и тесты агента (.NET 10) в Docker-образе mcr.microsoft.com/dotnet/sdk:10.0 — локальный dotnet не нужен.
#
#   agent/test.sh build         # dotnet build (Release)
#   agent/test.sh unit          # build + unit-тесты (без сервера)
#   agent/test.sh integration   # поднять Go-сервер (agent/tests/integration/server.sh) + интеграционные тесты
#   agent/test.sh all           # unit + integration
#   agent/test.sh aot           # проверка Native AOT: publish консольного хоста службы (linux-x64) → agent/.work/aot
#   agent/test.sh dotnet <args> # произвольная команда dotnet в контейнере (например: restore --force-evaluate)
#
# Переменные: DOTNET_IMAGE (mcr.microsoft.com/dotnet/sdk:10.0), AGENT_NUGET_VOLUME (zafirogo-agent-nuget),
# KEEP_SERVER=1 — не останавливать сервер после integration.
# Сеть контейнера — сеть хоста (прокси/CA окружения прокидываются, как в tools/proto-csharp-check.sh), поэтому
# интеграционные тесты видят Go-сервер на 127.0.0.1.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
DOTNET_IMAGE="${DOTNET_IMAGE:-mcr.microsoft.com/dotnet/sdk:10.0}"
NUGET_VOLUME="${AGENT_NUGET_VOLUME:-zafirogo-agent-nuget}"
MODE="${1:-unit}"
shift || true

log() { echo "[agent] $*" >&2; }

in_docker() {
  local extra=()
  if [[ -n "${SSL_CERT_FILE:-}" && -f "${SSL_CERT_FILE}" ]]; then
    extra+=(-v "$SSL_CERT_FILE:/etc/ssl/certs/extra-ca.crt:ro" -e SSL_CERT_FILE=/etc/ssl/certs/extra-ca.crt)
  elif [[ -f /root/.ccr/ca-bundle.crt ]]; then
    extra+=(-v /root/.ccr/ca-bundle.crt:/etc/ssl/certs/extra-ca.crt:ro -e SSL_CERT_FILE=/etc/ssl/certs/extra-ca.crt)
  fi
  # Переменные интеграционных тестов (ZAFIRO_IT_*) — из окружения скрипта.
  while IFS='=' read -r name _; do extra+=(-e "$name"); done < <(env | grep '^ZAFIRO_IT_' || true)
  [[ -n "${ZAFIRO_IT_WORK:-}" ]] && extra+=(-v "$ZAFIRO_IT_WORK:$ZAFIRO_IT_WORK:ro")
  docker run --rm --network host \
    -e HTTPS_PROXY -e HTTP_PROXY -e NO_PROXY -e https_proxy -e http_proxy -e no_proxy \
    -e DOTNET_CLI_TELEMETRY_OPTOUT=1 -e DOTNET_NOLOGO=1 -e DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 \
    -e NUGET_PACKAGES=/nuget \
    "${extra[@]}" \
    -v "$NUGET_VOLUME:/nuget" \
    -v "$REPO:/repo" -w /repo/agent \
    "$DOTNET_IMAGE" bash -c "$1"
}

# Файлы сборки создаются в контейнере под root — вернуть владельца каталогов bin/obj.
fix_owner() {
  local uid gid
  uid="$(id -u)" gid="$(id -g)"
  [[ "$uid" == 0 ]] && return 0
  in_docker "chown -R $uid:$gid /repo/agent/src /repo/agent/tests 2>/dev/null || true" >/dev/null
}

build() { in_docker "dotnet restore ZafiroAgent.slnx --locked-mode && dotnet build ZafiroAgent.slnx -c Release --no-restore -nologo"; }

unit() {
  in_docker "dotnet restore ZafiroAgent.slnx --locked-mode && dotnet build ZafiroAgent.slnx -c Release --no-restore -nologo \
    && dotnet test tests/Zafiro.Agent.Tests/Zafiro.Agent.Tests.csproj -c Release --no-build -nologo --filter 'Category!=Integration' --logger 'console;verbosity=normal'"
}

integration() {
  local env_file
  env_file="$("$HERE/tests/integration/server.sh" start)"
  # shellcheck disable=SC1090
  set -a; . "$env_file"; set +a
  local rc=0
  in_docker "dotnet restore ZafiroAgent.slnx --locked-mode && dotnet build ZafiroAgent.slnx -c Release --no-restore -nologo \
    && dotnet test tests/Zafiro.Agent.Tests/Zafiro.Agent.Tests.csproj -c Release --no-build -nologo --filter 'Category=Integration' --logger 'console;verbosity=normal'" || rc=$?
  if [[ "${KEEP_SERVER:-0}" != 1 ]]; then "$HERE/tests/integration/server.sh" stop; fi
  return $rc
}

aot() {
  # Native AOT требует clang и zlib в образе SDK — ставятся во временный контейнер. Публикация — в копии исходников
  # (/tmp/w): restore с RID дописал бы runtime-разделы в закоммиченные packages.lock.json.
  in_docker "set -eo pipefail; (command -v clang >/dev/null || (apt-get update -qq && apt-get install -y -qq clang zlib1g-dev >/dev/null)) \
    && mkdir -p /tmp/w && cp -r /repo/proto /tmp/w/proto && cp -r /repo/agent /tmp/w/agent \
    && find /tmp/w/agent -name packages.lock.json -delete && find /tmp/w/agent \( -name bin -o -name obj \) -type d -prune -exec rm -rf {} + \
    && cd /tmp/w/agent \
    && dotnet publish src/Zafiro.Agent.Service/Zafiro.Agent.Service.csproj -c Release -r linux-x64 -p:PublishAot=true \
       -p:RestorePackagesWithLockFile=false -o /tmp/aot-out -nologo 2>&1 | tee /tmp/aot.log \
    && ! grep -E 'warning (IL|SYSLIB|CS)' /tmp/aot.log \
    && ls -la /tmp/aot-out && /tmp/aot-out/Zafiro.Agent.Service --version \
    && mkdir -p /repo/agent/.work/aot && cp /tmp/aot-out/Zafiro.Agent.Service /tmp/aot-out/appsettings.json /repo/agent/.work/aot/"
}

trap fix_owner EXIT
case "$MODE" in
  build) build ;;
  unit) unit ;;
  integration) integration ;;
  all) unit; integration ;;
  aot) aot ;;
  dotnet) in_docker "dotnet $*" ;;
  *) echo "usage: $0 build|unit|integration|all|aot|dotnet <args>" >&2; exit 2 ;;
esac
