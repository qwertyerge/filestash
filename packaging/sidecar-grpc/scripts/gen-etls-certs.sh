#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT/conf/certs}"
DAYS="${DAYS:-3650}"
SERVER_CN="${SERVER_CN:-localhost}"
SERVER_DNS="${SERVER_DNS:-localhost}"
SERVER_IP="${SERVER_IP:-127.0.0.1}"
CLIENT_CN="${CLIENT_CN:-filestash-sidecar-client}"
CLIENT_URI="${CLIENT_URI:-spiffe://filestash-sidecar/orchestrator}"

if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl is required" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

openssl_run() {
  if ! openssl "$@" >"$tmp_dir/openssl.out" 2>"$tmp_dir/openssl.err"; then
    cat "$tmp_dir/openssl.out" >&2
    cat "$tmp_dir/openssl.err" >&2
    exit 1
  fi
}

openssl_run req \
  -quiet \
  -x509 \
  -newkey rsa:4096 \
  -nodes \
  -days "$DAYS" \
  -sha256 \
  -subj "/CN=Filestash Sidecar CA" \
  -keyout "$OUT_DIR/client-ca.key" \
  -out "$OUT_DIR/client-ca.crt"

openssl_run req \
  -quiet \
  -newkey rsa:3072 \
  -nodes \
  -sha256 \
  -subj "/CN=$SERVER_CN" \
  -keyout "$OUT_DIR/server.key" \
  -out "$tmp_dir/server.csr"

cat > "$tmp_dir/server.ext" <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:${SERVER_DNS},IP:${SERVER_IP}
EOF

openssl_run x509 \
  -req \
  -in "$tmp_dir/server.csr" \
  -CA "$OUT_DIR/client-ca.crt" \
  -CAkey "$OUT_DIR/client-ca.key" \
  -CAcreateserial \
  -days "$DAYS" \
  -sha256 \
  -extfile "$tmp_dir/server.ext" \
  -out "$OUT_DIR/server.crt"

openssl_run req \
  -quiet \
  -newkey rsa:3072 \
  -nodes \
  -sha256 \
  -subj "/CN=$CLIENT_CN" \
  -keyout "$OUT_DIR/client.key" \
  -out "$tmp_dir/client.csr"

cat > "$tmp_dir/client.ext" <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=clientAuth
subjectAltName=URI:${CLIENT_URI}
EOF

openssl_run x509 \
  -req \
  -in "$tmp_dir/client.csr" \
  -CA "$OUT_DIR/client-ca.crt" \
  -CAkey "$OUT_DIR/client-ca.key" \
  -CAcreateserial \
  -days "$DAYS" \
  -sha256 \
  -extfile "$tmp_dir/client.ext" \
  -out "$OUT_DIR/client.crt"

rm -f "$OUT_DIR/client-ca.srl"
chmod 600 "$OUT_DIR"/*.key
chmod 644 "$OUT_DIR"/*.crt

cat <<EOF
generated transport credentials:
  ca:          $OUT_DIR/client-ca.crt
  server cert: $OUT_DIR/server.crt
  server key:  $OUT_DIR/server.key
  client cert: $OUT_DIR/client.crt
  client key:  $OUT_DIR/client.key

client identity:
  $CLIENT_URI
EOF
