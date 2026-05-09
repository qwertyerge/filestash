#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${FILESTASH_SIDECAR_BIN:-$ROOT/bin/filestash-sidecar}"
CONFIG_SRC="${FILESTASH_SIDECAR_CONFIG:-$ROOT/conf/config.json}"
DATA_DIR="${FILESTASH_PATH:-$ROOT/data}"
CONFIG_DST="$DATA_DIR/state/config/config.json"

required_files=(
  "$BIN"
  "$CONFIG_SRC"
  "$ROOT/conf/certs/server.crt"
  "$ROOT/conf/certs/server.key"
  "$ROOT/conf/certs/client-ca.crt"
)

for file in "${required_files[@]}"; do
  if [[ ! -f "$file" ]]; then
    echo "missing required file: $file" >&2
    echo "run: $ROOT/scripts/gen-etls-certs.sh" >&2
    exit 1
  fi
done

mkdir -p "$DATA_DIR/state/config" "$DATA_DIR/state/certs" "$DATA_DIR/state/db" "$DATA_DIR/state/plugins" "$DATA_DIR/state/search" "$DATA_DIR/state/log"

if [[ ! -f "$CONFIG_DST" || "${FILESTASH_SIDECAR_OVERWRITE_CONFIG:-0}" == "1" ]]; then
  cp "$CONFIG_SRC" "$CONFIG_DST"
fi

export FILESTASH_PATH="$DATA_DIR"
cd "$ROOT"
exec "$BIN" "$@"
