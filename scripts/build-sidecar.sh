#!/usr/bin/env bash
# 构建 Go sidecar 并放到 Tauri externalBin 位置（本地联调/手动打包用）
set -euo pipefail
cd "$(dirname "$0")/../core"

GOOS_OUT="${GOOS:-$(go env GOOS)}"
GOARCH_OUT="${GOARCH:-$(go env GOARCH)}"

case "$GOOS_OUT" in
  windows) TRIPLE="x86_64-pc-windows-msvc"; EXT=".exe" ;;
  linux)   TRIPLE="x86_64-unknown-linux-gnu"; EXT="" ;;
  darwin)  TRIPLE="x86_64-apple-darwin"; EXT="" ;;
  *) echo "不支持的 GOOS: $GOOS_OUT"; exit 1 ;;
esac

echo "==> go vet + test"
go vet ./...
go test ./...

echo "==> build clipvault-core ($GOOS_OUT/$GOARCH_OUT)"
CGO_ENABLED=0 GOOS="$GOOS_OUT" GOARCH="$GOARCH_OUT" \
  go build -trimpath -ldflags="-s -w" -o "clipvault-core$EXT" .

mkdir -p ../app/src-tauri/binaries
cp "clipvault-core$EXT" "../app/src-tauri/binaries/clipvault-core-$TRIPLE$EXT"
ls -la "../app/src-tauri/binaries/clipvault-core-$TRIPLE$EXT"
echo "完成"
