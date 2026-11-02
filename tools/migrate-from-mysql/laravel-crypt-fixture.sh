#!/usr/bin/env bash
# Эталон шифрования старого Laravel для теста расшифровки секретов при переносе (решение Q-050):
# server/internal/migrate/testdata/laravel_crypt.json — значения, зашифрованные НАСТОЯЩИМ
# Illuminate\Encryption\Encrypter (vendor старого проекта, laravel/framework из его composer.lock) в php:8.4.27-cli.
#
#   tools/migrate-from-mysql/laravel-crypt-fixture.sh
#
# Ключи — тестовые (не APP_KEY прода). Перезапускать при обновлении laravel/framework в старом проекте.
# Переменные: ZAFIRO_SRC (../Zafiro), PHP_IMAGE, ORACLE_VENDOR_CACHE (кэш vendor, как у tools/php-oracle/run.sh).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
SRC="${ZAFIRO_SRC:-$REPO/../Zafiro}"
PHP_IMAGE="${PHP_IMAGE:-mirror.gcr.io/library/php:8.4.27-cli}"
CACHE_ROOT="${ORACLE_VENDOR_CACHE:-$HOME/.cache/zafirogo-php-oracle}"
OUT="$REPO/server/internal/migrate/testdata/laravel_crypt.json"

LOCK_HASH="$(sha256sum "$SRC/composer.lock" | cut -c1-16)"
VENDOR="$CACHE_ROOT/vendor-$LOCK_HASH"
[ -d "$VENDOR" ] || { echo "нет vendor в $VENDOR: выполните tools/php-oracle/run.sh shell" >&2; exit 2; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/zafiro-crypt.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/fixture.php" <<'PHP'
<?php
require '/vendor/autoload.php';

use Illuminate\Encryption\Encrypter;

// Тестовые ключи (32 байта, как key:generate для AES-256-CBC).
$current = 'base64:'.base64_encode(str_repeat("\x01", 16).str_repeat("\x02", 16));
$previous = 'base64:'.base64_encode(str_repeat("\x07", 32));
$key = fn (string $k): string => base64_decode(substr($k, 7));

$enc = new Encrypter($key($current), 'aes-256-cbc');
$old = new Encrypter($key($previous), 'aes-256-cbc');
$cases = [
    // Каст 'encrypted' (WebhookEndpoint::secret): encrypt($value, false).
    ['name' => 'webhook_secret', 'plain' => 'whsec_'.str_repeat('Ab1', 13).'x', 'unserialize' => false,
        'payload' => $enc->encryptString('whsec_'.str_repeat('Ab1', 13).'x')],
    // Crypt::encryptString (TelegramGateway::save()).
    ['name' => 'telegram_token', 'plain' => '123456789:AAH-telegram_Token-ЖЁ', 'unserialize' => false,
        'payload' => $enc->encryptString('123456789:AAH-telegram_Token-ЖЁ')],
    // Ровно блок (16 байт): PKCS#7 добавляет целый блок.
    ['name' => 'block', 'plain' => '0123456789abcdef', 'unserialize' => false, 'payload' => $enc->encryptString('0123456789abcdef')],
    ['name' => 'empty', 'plain' => '', 'unserialize' => false, 'payload' => $enc->encryptString('')],
    // Crypt::encrypt() — с serialize().
    ['name' => 'serialized', 'plain' => 'whsec_serialized "кавычки";', 'unserialize' => true,
        'payload' => $enc->encrypt('whsec_serialized "кавычки";')],
    // Значение, зашифрованное прежним ключом (APP_PREVIOUS_KEYS).
    ['name' => 'previous_key', 'plain' => 'whsec_previous', 'unserialize' => false, 'payload' => $old->encryptString('whsec_previous')],
];
foreach ($cases as $c) {
    $back = (new Encrypter($key($current), 'aes-256-cbc'))->previousKeys([$key($previous)])->decrypt($c['payload'], $c['unserialize']);
    if ($back !== $c['plain']) {
        fwrite(STDERR, "roundtrip failed: {$c['name']}\n");
        exit(1);
    }
}
echo json_encode(['php' => PHP_VERSION, 'laravel' => \Illuminate\Foundation\Application::VERSION,
    'app_key' => $current, 'previous_keys' => $previous, 'cases' => $cases],
    JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES)."\n";
PHP
docker run --rm -v "$VENDOR:/vendor:ro" -v "$WORK:/work:ro" "$PHP_IMAGE" php /work/fixture.php > "$OUT"
echo "записано: $OUT" >&2
