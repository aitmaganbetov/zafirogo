#!/bin/sh
# Секреты наблюдаемости staging (Этап 6, O3): пароль администратора Grafana. Повторный запуск ничего не меняет.
set -eu
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mkdir -p "$dir/secrets"
chmod 700 "$dir/secrets"
f="$dir/secrets/grafana_admin_password"
if [ ! -s "$f" ]; then
	umask 077
	# 24 байта случайных данных → 32 символа base64 без / и +.
	head -c 24 /dev/urandom | base64 | tr '/+' '_-' > "$f"
	echo "Создан $f (вход в Grafana: admin / содержимое файла)."
fi
