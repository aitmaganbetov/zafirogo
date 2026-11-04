#!/usr/bin/env bash
# Вектор подписи обновления для UpdateVerifierTests: подписывает тестовый «установщик» Go-утилитой server/cmd/sign-update
# (та же, что в CI выпуска агента) одноразовым ключом. В репозиторий кладутся установщик, манифест и ОТКРЫТЫЙ ключ;
# приватный ключ живёт только во время работы скрипта.
#
#   agent/tests/Zafiro.Agent.Tests/TestData/gen-update-vector.sh
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER="$(cd "$HERE/../../../../server" && pwd)"
KEY_ID="test-vector"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

keys="$(cd "$SERVER" && go run ./cmd/sign-update -genkey -key-id "$KEY_ID")"
seed="$(printf '%s\n' "$keys" | sed -n 's/^AGENT_UPDATE_SIGNING_KEY[^:]*: *//p')"
pub="$(printf '%s\n' "$keys" | sed -n 's/^agent\.update\.public_keys: *//p')"
[[ -n "$seed" && -n "$pub" ]] || { echo "unexpected sign-update -genkey output:" >&2; printf '%s\n' "$keys" | sed 's/^\(AGENT_UPDATE_SIGNING_KEY[^:]*:\).*/\1 ***/' >&2; exit 1; }

printf 'Zafiro agent update test vector: not an installer.\n' > "$HERE/update-vector.bin"
(cd "$SERVER" && AGENT_UPDATE_SIGNING_KEY="$seed" go run ./cmd/sign-update -file "$HERE/update-vector.bin" -version 2.0.1 \
  -channel stable -key-id "$KEY_ID" -url "https://github.com/aitmaganbetov/Zafiro/releases/download/agent-v2.0.1/ZafiroAgentSetup-2.0.1.exe" \
  -expect-public-key "$pub" -out "$HERE/update-vector.json" >/dev/null)
printf '%s\n' "$pub" > "$HERE/update-vector.pub"
echo "update vector: $HERE/update-vector.{bin,json,pub}"
