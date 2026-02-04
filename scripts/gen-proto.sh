#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_DIR="$ROOT_DIR/proto"
GO_OUT="$PROTO_DIR/gen/go"
TS_OUT="$ROOT_DIR/sdk/src/gen"

mkdir -p "$GO_OUT" "$TS_OUT"

protoc \
  -I "$PROTO_DIR" \
  --go_out "$GO_OUT" \
  --go_opt paths=source_relative \
  --connect-go_out "$GO_OUT" \
  --connect-go_opt paths=source_relative \
  $(find "$PROTO_DIR" -name "*.proto")

if command -v protoc-gen-es >/dev/null 2>&1 && command -v protoc-gen-connect-es >/dev/null 2>&1; then
  protoc \
    -I "$PROTO_DIR" \
    --es_out "$TS_OUT" \
    --es_opt target=ts,import_extension=none \
    --connect-es_out "$TS_OUT" \
    --connect-es_opt target=ts,import_extension=none \
    $(find "$PROTO_DIR" -name "*.proto")
else
  echo "protoc-gen-es/protoc-gen-connect-es not found; skip TS generation" >&2
fi
