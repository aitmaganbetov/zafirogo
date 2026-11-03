#!/bin/sh
# Адреса источника клубов для нагрузочного теста: по адресу на клуб на мосту сети staging (172.29.66.0/24), чтобы
# балансировщик и сервер видели клубы с разных IP (как настоящие клубы за NAT; лимиты по IP — на клуб, а не на весь
# тест). Генератор привязывает соединения клуба c к адресу FIRST + c − 1 (loadgen -src FIRST).
#
#   sh tools/loadgen/net.sh add 172.29.66.100 50     # добавить 50 адресов
#   sh tools/loadgen/net.sh del 172.29.66.100 50     # убрать (task staging:destroy удаляет мост вместе с ними)
#
# На хосте нет iproute2 — команда ip из busybox образа nginx staging (сеть хоста, NET_ADMIN).
set -eu
action=${1:?add|del}
first=${2:-172.29.66.100}
count=${3:-50}
subnet=172.29.66.1/24
img=$(grep '^STG_NGINX_IMAGE=' "$(dirname "$0")/../../deploy/staging/.env.staging" | cut -d= -f2-)
img=${img:-nginx:1.30.5-alpine3.24@sha256:0985e772fb9f729e6fa0980da05fca5d9c468e870eed43071545afa9d2e27d94}
bridge=$(docker run --rm --network host --entrypoint ip "$img" -4 addr | awk -v s="$subnet" '$1 == "inet" && $2 == s { print $NF }')
[ -n "$bridge" ] || { echo "мост сети staging ($subnet) не найден: task staging:up" >&2; exit 1; }
prefix=${first%.*}
start=${first##*.}
cmds=""
i=0
while [ $i -lt "$count" ]; do
	cmds="$cmds ip addr $action $prefix.$((start + i))/32 dev $bridge 2>/dev/null || true;"
	i=$((i + 1))
done
docker run --rm --network host --cap-add NET_ADMIN --entrypoint sh "$img" -c "$cmds"
echo "$action: $count адресов с $first на $bridge"
