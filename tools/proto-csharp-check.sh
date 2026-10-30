#!/usr/bin/env bash
# Проверка, что контракт агента (proto/agent/v1/*.proto) генерируется и компилируется в C#.
# Сгенерированный C# в репозиторий НЕ кладётся: всё происходит во временном каталоге.
#
# Что делает:
#   1. Скачивает NuGet-пакет Grpc.Tools (protoc + grpc_csharp_plugin для linux_x64).
#   2. Генерирует C# (сообщения + клиентские/серверные заглушки gRPC).
#   3. Собирает мини-проект net10.0 с Google.Protobuf + Grpc.Net.Client: локальным `dotnet`,
#      а если его нет — в Docker-образе mcr.microsoft.com/dotnet/sdk:10.0.
#
# Использование: tools/proto-csharp-check.sh [рабочий-каталог]
# Переменные: GRPC_TOOLS_VERSION, GOOGLE_PROTOBUF_VERSION, GRPC_NET_CLIENT_VERSION, DOTNET_IMAGE.
# Требования: bash, curl, unzip; dotnet SDK 10 или docker. Только Linux x86_64.
set -euo pipefail

GRPC_TOOLS_VERSION="${GRPC_TOOLS_VERSION:-2.85.0}"
GOOGLE_PROTOBUF_VERSION="${GOOGLE_PROTOBUF_VERSION:-3.36.2}"
GRPC_NET_CLIENT_VERSION="${GRPC_NET_CLIENT_VERSION:-2.85.0}"
DOTNET_IMAGE="${DOTNET_IMAGE:-mcr.microsoft.com/dotnet/sdk:10.0}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_DIR="$ROOT/proto"

if [[ "$(uname -s)-$(uname -m)" != "Linux-x86_64" ]]; then
  echo "Скрипт рассчитан на Linux x86_64 (protoc из Grpc.Tools tools/linux_x64)." >&2
  exit 2
fi

WORK="${1:-$(mktemp -d -t zafiro-proto-cs-XXXXXX)}"
mkdir -p "$WORK"
WORK="$(cd "$WORK" && pwd)"
TOOLS="$WORK/grpc.tools"
OUT="$WORK/project/Generated"
echo "Рабочий каталог: $WORK"

# 1. Grpc.Tools
if [[ ! -x "$TOOLS/tools/linux_x64/protoc" ]]; then
  mkdir -p "$TOOLS"
  curl -fsSL -o "$WORK/grpc.tools.nupkg" \
    "https://api.nuget.org/v3-flatcontainer/grpc.tools/${GRPC_TOOLS_VERSION}/grpc.tools.${GRPC_TOOLS_VERSION}.nupkg"
  unzip -q -o "$WORK/grpc.tools.nupkg" -d "$TOOLS"
  chmod +x "$TOOLS/tools/linux_x64/protoc" "$TOOLS/tools/linux_x64/grpc_csharp_plugin"
fi
PROTOC="$TOOLS/tools/linux_x64/protoc"
PLUGIN="$TOOLS/tools/linux_x64/grpc_csharp_plugin"
"$PROTOC" --version

# 2. Генерация C#
rm -rf "$OUT" && mkdir -p "$OUT"
mapfile -t PROTOS < <(cd "$PROTO_DIR" && find agent -name '*.proto' | sort)
"$PROTOC" \
  -I "$PROTO_DIR" \
  -I "$TOOLS/build/native/include" \
  --csharp_out="$OUT" \
  --grpc_out="$OUT" \
  --plugin=protoc-gen-grpc="$PLUGIN" \
  "${PROTOS[@]}"
echo "Сгенерировано файлов C#: $(find "$OUT" -name '*.cs' | wc -l)"

# 3. Мини-проект: ссылается на сгенерированные типы, чтобы сборка их проверила.
cat > "$WORK/project/ProtoCheck.csproj" <<EOF
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net10.0</TargetFramework>
    <Nullable>enable</Nullable>
    <TreatWarningsAsErrors>false</TreatWarningsAsErrors>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Google.Protobuf" Version="${GOOGLE_PROTOBUF_VERSION}" />
    <PackageReference Include="Grpc.Net.Client" Version="${GRPC_NET_CLIENT_VERSION}" />
  </ItemGroup>
</Project>
EOF
cat > "$WORK/project/Program.cs" <<'EOF'
using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Zafiro.Agent.V1;

// Сериализация и разбор сообщения стрима + проверка, что клиенты сервисов существуют.
var hello = new AgentMessage
{
    Hello = new Hello { ProtocolVersion = 1, AgentVersion = "2.0.0", LastCommandSeq = 42, BootId = "b" },
};
var bytes = hello.ToByteArray();
var parsed = AgentMessage.Parser.ParseFrom(bytes);
if (parsed.Hello.LastCommandSeq != 42) throw new System.Exception("roundtrip failed");

var cmd = new ServerMessage
{
    Command = new Command
    {
        CommandId = "0192f0c4-0000-7000-8000-000000000000",
        Seq = 43,
        IssuedAt = Timestamp.FromDateTime(System.DateTime.UtcNow),
        UpdateAvailable = new UpdateAvailable { Version = "2.0.1", Sha256 = new string('0', 64), SignatureEd25519 = ByteString.CopyFrom(new byte[64]) },
    },
};
var purchase = new PurchaseTariffRequest { IdempotencyKey = System.Guid.NewGuid().ToString(), TariffId = 7, BalanceSource = BalanceSource.Main };
System.Type[] clients =
{
    typeof(AgentService.AgentServiceClient), typeof(EnrollmentService.EnrollmentServiceClient),
    typeof(GuestAuthService.GuestAuthServiceClient), typeof(GuestService.GuestServiceClient),
    typeof(ShopService.ShopServiceClient), typeof(SocialService.SocialServiceClient),
};
System.Console.WriteLine($"OK: {bytes.Length} bytes, command {cmd.Command.KindCase}, purchase {purchase.CalculateSize()} bytes, {clients.Length} clients, ErrorCode values {System.Enum.GetValues<ErrorCode>().Length}");
EOF

if command -v dotnet >/dev/null 2>&1 && dotnet --list-sdks | grep -q '^10\.'; then
  (cd "$WORK/project" && dotnet build -nologo -v q && dotnet run --no-build)
elif command -v docker >/dev/null 2>&1; then
  # Сеть хоста и прокси/CA окружения — чтобы NuGet работал за корпоративным прокси.
  extra=()
  [[ -n "${SSL_CERT_FILE:-}" && -f "${SSL_CERT_FILE}" ]] && extra+=(-v "$SSL_CERT_FILE:/etc/ssl/certs/extra-ca.crt:ro" -e SSL_CERT_FILE=/etc/ssl/certs/extra-ca.crt)
  [[ -z "${SSL_CERT_FILE:-}" && -f /root/.ccr/ca-bundle.crt ]] && extra+=(-v /root/.ccr/ca-bundle.crt:/etc/ssl/certs/extra-ca.crt:ro -e SSL_CERT_FILE=/etc/ssl/certs/extra-ca.crt)
  docker run --rm --network host \
    -e HTTPS_PROXY -e HTTP_PROXY -e NO_PROXY -e https_proxy -e http_proxy -e no_proxy \
    -e DOTNET_CLI_TELEMETRY_OPTOUT=1 -e DOTNET_NOLOGO=1 \
    "${extra[@]}" \
    -v "$WORK/project:/src" -w /src \
    "$DOTNET_IMAGE" sh -c 'dotnet build -nologo -v q && dotnet run --no-build'
else
  echo "Нет ни dotnet SDK 10, ни docker: C# сгенерирован в $OUT, но не скомпилирован." >&2
  exit 3
fi
echo "C#: генерация и компиляция контракта — успешно."
