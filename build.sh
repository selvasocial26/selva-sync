#!/usr/bin/env bash
# Builds dist/SelvaSync.exe (Windows 64-bit). Optional: set GOOGLE_CLIENT_ID
# and GOOGLE_CLIENT_SECRET to build in your Google OAuth "Desktop app" client,
# and PICKER_URL (e.g. https://USERNAME.github.io/selva-sync/picker.html)
# to let users choose the backup folder.
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:-1.2.5}"
PKG=github.com/selvasync/selvasync/internal/app
LDFLAGS="-s -w -H=windowsgui -X ${PKG}.Version=${VERSION}"
if [ -n "${PICKER_URL:-}" ]; then
  LDFLAGS="${LDFLAGS} -X ${PKG}.DefaultPickerURL=${PICKER_URL}"
fi
if [ -n "${GOOGLE_CLIENT_ID:-}" ]; then
  LDFLAGS="${LDFLAGS} -X ${PKG}.DefaultClientID=${GOOGLE_CLIENT_ID} -X ${PKG}.DefaultClientSecret=${GOOGLE_CLIENT_SECRET:-}"
fi
go test ./...
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" -o dist/SelvaSync.exe .
ls -l dist/SelvaSync.exe
