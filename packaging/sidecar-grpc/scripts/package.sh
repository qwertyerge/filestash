#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$TEMPLATE_DIR/../.." && pwd)"
PACKAGE_DIR="${PACKAGE_DIR:-$REPO_ROOT/dist/filestash-sidecar-grpc}"
GO_TAGS="${GO_TAGS:-fts5}"

rm -rf "$PACKAGE_DIR"
mkdir -p "$PACKAGE_DIR/bin" "$PACKAGE_DIR/conf/certs" "$PACKAGE_DIR/scripts" "$PACKAGE_DIR/data"

CGO_ENABLED="${CGO_ENABLED:-0}" go build --tags "$GO_TAGS" -o "$PACKAGE_DIR/bin/filestash-sidecar" "$REPO_ROOT/cmd/sidecar"

cp "$TEMPLATE_DIR/bin/start.sh" "$PACKAGE_DIR/bin/start.sh"
cp "$TEMPLATE_DIR/conf/config.json" "$PACKAGE_DIR/conf/config.json"
cp "$TEMPLATE_DIR/scripts/gen-etls-certs.sh" "$PACKAGE_DIR/scripts/gen-etls-certs.sh"
cp "$TEMPLATE_DIR/README.md" "$PACKAGE_DIR/README.md"

chmod +x "$PACKAGE_DIR/bin/filestash-sidecar" "$PACKAGE_DIR/bin/start.sh" "$PACKAGE_DIR/scripts/gen-etls-certs.sh"

cat <<EOF
package ready:
  $PACKAGE_DIR

next:
  cd "$PACKAGE_DIR"
  ./scripts/gen-etls-certs.sh
  ./bin/start.sh
EOF
