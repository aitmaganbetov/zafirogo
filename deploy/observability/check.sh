#!/bin/sh
# Проверка конфигурации наблюдаемости в Docker (Этап 6, O3): promtool check config/rules, модульные тесты алертов,
# amtool check-config. Образы — те же, что в compose.observability.yml.
set -eu
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
prom=${OBS_PROMETHEUS_IMAGE:-prom/prometheus:v3.15.0@sha256:efd719c99d83b060d9daefdcf00360461adf279f45ef5391f8d111892118753e}
am=${OBS_ALERTMANAGER_IMAGE:-prom/alertmanager:v0.34.1@sha256:e9733bafb1bdef9b00e25a21f8f99dc26a22224bf16641ad754d1649f4c3357a}

docker run --rm --entrypoint promtool \
	-v "$dir/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml:ro" \
	-v "$dir/prometheus/rules:/etc/prometheus/rules:ro" \
	"$prom" check config /etc/prometheus/prometheus.yml
docker run --rm --entrypoint promtool -v "$dir/prometheus:/w:ro" "$prom" test rules /w/tests/alerts.test.yml
docker run --rm --entrypoint amtool -v "$dir/alertmanager:/w:ro" "$am" check-config /w/alertmanager.yml
# Дашборды — корректный JSON с uid.
for f in "$dir"/grafana/dashboards/*.json; do
	python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["uid"] and d["panels"], sys.argv[1]' "$f"
done
echo "observability: OK"
