#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:-${VERSION:-dev}}"
OUT_DIR="${OUT_DIR:-dist}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v go >/dev/null 2>&1 || { echo "Go is required" >&2; exit 1; }
mkdir -p "$OUT_DIR"
rm -f "$OUT_DIR"/ktrx_*

targets=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
  "windows amd64"
  "windows arm64"
)

for target in "${targets[@]}"; do
  read -r GOOS GOARCH <<<"$target"
  name="ktrx_${VERSION}_${GOOS}_${GOARCH}"
  stage="$(mktemp -d)"
  trap 'rm -rf "$stage"' EXIT

  binary="ktrx"
  if [[ "$GOOS" == "windows" ]]; then binary="ktrx.exe"; fi

  echo "Building $GOOS/$GOARCH"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags="-s -w" -o "$stage/$binary" ./cmd/ktrx
  cp LICENSE "$stage/"
  cp README.md "$stage/"

  if [[ "$GOOS" == "windows" ]]; then
    (cd "$stage" && zip -q -r "$ROOT/$OUT_DIR/$name.zip" "$binary" LICENSE README.md)
  else
    tar -C "$stage" -czf "$OUT_DIR/$name.tar.gz" "$binary" LICENSE README.md
  fi
  rm -rf "$stage"
  trap - EXIT
done

(
  cd "$OUT_DIR"
  sha256sum ktrx_* > checksums.txt
)
echo "Release artifacts are in $OUT_DIR/"
