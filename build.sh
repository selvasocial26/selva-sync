#!/usr/bin/env bash
# Builds dist/SelvaSync.exe (Windows 64-bit). Optional: set GOOGLE_CLIENT_ID
# and GOOGLE_CLIENT_SECRET to build in your Google OAuth "Desktop app" client.
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:-1.1.0}"
PKG=github.com/selvasync/selvasync/internal/app
LDFLAGS="-s -w -H=windowsgui -X ${PKG}.Version=${VERSION}"
if [ -n "${GOOGLE_CLIENT_ID:-}" ]; then
  LDFLAGS="${LDFLAGS} -X ${PKG}.DefaultClientID=${GOOGLE_CLIENT_ID} -X ${PKG}.DefaultClientSecret=${GOOGLE_CLIENT_SECRET:-}"
fi
go test ./...
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" -o dist/SelvaSync.exe .
ls -l dist/SelvaSync.exe
