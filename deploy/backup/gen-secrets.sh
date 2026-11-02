#!/usr/bin/env bash
# Секреты бэкапа для staging/учений: ключи S3 (SeaweedFS), конфиг его учёток и пара ключей OpenPGP для WAL-G.
# Пишет в каталог $1 (по умолчанию deploy/backup/secrets — в .gitignore). Существующие файлы не перезаписывает.
#
#   deploy/backup/gen-secrets.sh [каталог]
#
# Прод: эти файлы не генерируются на сервере. Ключи S3 выдаёт провайдер хранилища (учётка «только запись»
# для сервера БД, отдельная — с удалением — для политики хранения), закрытый ключ OpenPGP создаётся офлайн и
# хранится вне сервера БД (сейф/менеджер секретов); на сервер БД кладётся только открытый ключ (runbook §2).
set -euo pipefail

DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/secrets}"
umask 077
mkdir -p "$DIR"
chmod 700 "$DIR"

need() { command -v "$1" >/dev/null || { echo "gen-secrets: нужен $1" >&2; exit 2; }; }
need openssl
need gpg

if [ ! -f "$DIR/s3_access_key" ]; then
  printf 'walg%s' "$(openssl rand -hex 8)" > "$DIR/s3_access_key"
  openssl rand -hex 32 > "$DIR/s3_secret_key"
fi
if [ ! -f "$DIR/s3.json" ]; then
  # Учётки SeaweedFS S3 (staging): walg — чтение/запись/удаление в бакете копий.
  printf '{"identities":[{"name":"walg","credentials":[{"accessKey":"%s","secretKey":"%s"}],"actions":["Read","Write","List","Tagging"]}]}\n' \
    "$(cat "$DIR/s3_access_key")" "$(tr -d '\n' < "$DIR/s3_secret_key")" > "$DIR/s3.json"
fi

if [ ! -f "$DIR/walg_pgp_public.asc" ]; then
  GNUPGHOME="$(mktemp -d)"
  export GNUPGHOME
  trap 'rm -rf "$GNUPGHOME"' EXIT
  openssl rand -hex 32 > "$DIR/walg_pgp_passphrase"
  uid="Zafiro backup $(date -u +%Y%m%d) <backup@zafiro.invalid>"
  gpg --batch --quiet --pinentry-mode loopback --passphrase-file "$DIR/walg_pgp_passphrase" \
    --quick-gen-key "$uid" rsa4096 cert never
  fpr="$(gpg --batch --with-colons --list-keys "$uid" | awk -F: '/^fpr/ {print $10; exit}')"
  gpg --batch --quiet --pinentry-mode loopback --passphrase-file "$DIR/walg_pgp_passphrase" \
    --quick-add-key "$fpr" rsa4096 encr never
  gpg --batch --armor --export "$fpr" > "$DIR/walg_pgp_public.asc"
  gpg --batch --pinentry-mode loopback --passphrase-file "$DIR/walg_pgp_passphrase" \
    --armor --export-secret-keys "$fpr" > "$DIR/walg_pgp_private.asc"
fi

# Compose (не swarm) монтирует секреты как файлы хоста без смены владельца: контейнер читает их от uid 999
# (postgres). Каталог закрыт (0700), файлы внутри — 0644.
chmod 644 "$DIR"/*
echo "секреты бэкапа: $DIR"
