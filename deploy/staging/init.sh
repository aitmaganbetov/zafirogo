#!/bin/sh
# Подготовка staging: пароли (.env.staging), конфигурация сервера с секретами (secrets/zafiro.yaml), PKI агентов
# (офлайн-корень → выпускающий CA «agent-issuing», серверный сертификат gRPC) и самоподписанный TLS веба для nginx.
#
# Идемпотентно: существующие файлы не перезаписываются (удалите secrets/ и .env.staging, чтобы начать заново).
# Всё создаётся в deploy/staging/ и не попадает в git (deploy/staging/.gitignore). Нужен openssl.
# В проде эти файлы не генерируются здесь: пароли и ключи — секрет-менеджер, PKI — офлайн-корень (docs/deploy.md).
set -eu

cd "$(dirname "$0")"
umask 077

DOMAIN=${STAGING_DOMAIN:-staging.zafiro.test}

rand() { openssl rand -hex "$1"; }

# ---------- пароли ----------
if [ ! -f .env.staging ]; then
	sed \
		-e "s/^STG_POSTGRES_PASSWORD=.*/STG_POSTGRES_PASSWORD=$(rand 24)/" \
		-e "s/^STG_DB_OWNER_PASSWORD=.*/STG_DB_OWNER_PASSWORD=$(rand 24)/" \
		-e "s/^STG_DB_APP_PASSWORD=.*/STG_DB_APP_PASSWORD=$(rand 24)/" \
		-e "s/^STG_DB_PLATFORM_PASSWORD=.*/STG_DB_PLATFORM_PASSWORD=$(rand 24)/" \
		-e "s/^STG_VALKEY_PASSWORD=.*/STG_VALKEY_PASSWORD=$(rand 24)/" \
		-e "s/^STG_NATS_PASSWORD=.*/STG_NATS_PASSWORD=$(rand 24)/" \
		.env.staging.example >.env.staging
	echo "staging: создан .env.staging"
fi
# shellcheck disable=SC1091
. ./.env.staging

mkdir -p secrets/pki secrets/web
chmod 700 secrets secrets/pki secrets/web

# ---------- конфигурация сервера с секретами ----------
# Секреты — только здесь (смонтирован как секрет /run/secrets/zafiro.yaml, ZAFIRO_CONFIG_FILE); не секретные
# настройки — переменные окружения в compose.yaml.
if [ ! -f secrets/zafiro.yaml ]; then
	cat >secrets/zafiro.yaml <<EOF
# staging: секреты сервера (сгенерировано deploy/staging/init.sh). Не коммитить.
database:
  url: postgres://${STG_DB_APP_USER}:${STG_DB_APP_PASSWORD}@postgres:5432/${STG_POSTGRES_DB}?sslmode=disable
  platform_url: postgres://${STG_DB_PLATFORM_USER}:${STG_DB_PLATFORM_PASSWORD}@postgres:5432/${STG_POSTGRES_DB}?sslmode=disable
valkey:
  password: ${STG_VALKEY_PASSWORD}
nats:
  url: nats://zafiro:${STG_NATS_PASSWORD}@nats:4222
auth:
  signing_keys:
    - id: stg1
      secret: $(rand 32)
security:
  data_keys:
    - id: stg1
      secret: $(rand 32)
agent:
  enroll:
    code_pepper: $(rand 32)
EOF
	echo "staging: создан secrets/zafiro.yaml"
fi

# ---------- PKI агентов ----------
p=secrets/pki
if [ ! -f $p/server.key ]; then
	cat >$p/openssl.cnf <<EOF
[req]
distinguished_name = dn
prompt = no
[dn]
CN = placeholder
[root]
basicConstraints = critical,CA:true
keyUsage = critical,keyCertSign,cRLSign
subjectKeyIdentifier = hash
[issuing]
basicConstraints = critical,CA:true,pathlen:0
keyUsage = critical,keyCertSign,cRLSign
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid
[server]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = DNS:agent.${DOMAIN},DNS:localhost,IP:127.0.0.1
authorityKeyIdentifier = keyid
EOF
	ec() { openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$1"; }
	# Корень (в проде — офлайн, ключ не на серверах).
	ec $p/root-ca.key
	openssl req -new -x509 -key $p/root-ca.key -sha256 -days 3650 -subj "/O=Zafiro (staging)/CN=Zafiro Staging Agent Root CA" \
		-config $p/openssl.cnf -extensions root -out $p/root-ca.pem
	# Выпускающий CA агентов: сертификат + цепочка до корня (ca_cert_file), ключ (ca_key_file, PKCS#8).
	ec $p/agent-ca.key
	openssl req -new -key $p/agent-ca.key -subj "/O=Zafiro (staging)/CN=Zafiro Staging agent-issuing" -config $p/openssl.cnf \
		| openssl x509 -req -CA $p/root-ca.pem -CAkey $p/root-ca.key -CAcreateserial -sha256 -days 1825 \
			-extfile $p/openssl.cnf -extensions issuing -out $p/agent-ca.crt
	cat $p/agent-ca.crt $p/root-ca.pem >$p/agent-ca.pem
	# Серверный сертификат gRPC (подписан корнем: агент доверяет только корню — pinning, docs/agent-protocol.md §12).
	ec $p/server.key
	openssl req -new -key $p/server.key -subj "/O=Zafiro (staging)/CN=agent.${DOMAIN}" -config $p/openssl.cnf \
		| openssl x509 -req -CA $p/root-ca.pem -CAkey $p/root-ca.key -CAcreateserial -sha256 -days 397 \
			-extfile $p/openssl.cnf -extensions server -out $p/server.pem
	rm -f $p/agent-ca.crt $p/*.srl
	echo "staging: создана PKI агентов (secrets/pki; корень агента — secrets/pki/root-ca.pem)"
fi

# ---------- TLS веба (nginx) ----------
w=secrets/web
if [ ! -f $w/tls.key ]; then
	cat >$w/openssl.cnf <<EOF
[req]
distinguished_name = dn
prompt = no
[dn]
CN = placeholder
[ca]
basicConstraints = critical,CA:true
keyUsage = critical,keyCertSign,cRLSign
[leaf]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = DNS:${DOMAIN},DNS:*.${DOMAIN}
EOF
	openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out $w/ca.key
	openssl req -new -x509 -key $w/ca.key -sha256 -days 3650 -subj "/O=Zafiro (staging)/CN=Zafiro Staging Web CA" \
		-config $w/openssl.cnf -extensions ca -out $w/ca.pem
	openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out $w/tls.key
	openssl req -new -key $w/tls.key -subj "/O=Zafiro (staging)/CN=${DOMAIN}" -config $w/openssl.cnf \
		| openssl x509 -req -CA $w/ca.pem -CAkey $w/ca.key -CAcreateserial -sha256 -days 397 \
			-extfile $w/openssl.cnf -extensions leaf -out $w/tls.pem
	rm -f $w/*.srl
	echo "staging: создан TLS веба (secrets/web; доверить в браузере — secrets/web/ca.pem)"
fi

# Файлы-секреты монтируются в контейнеры как есть (compose без swarm не меняет владельца): процесс zafiro
# (uid 65532) и nginx должны их читать. Каталог secrets/ закрыт (0700) от других пользователей хоста.
chmod 644 secrets/zafiro.yaml $p/agent-ca.pem $p/agent-ca.key $p/server.pem $p/server.key $p/root-ca.pem $w/tls.pem $w/tls.key $w/ca.pem
chmod 600 $p/root-ca.key $w/ca.key
