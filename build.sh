#!/usr/bin/env bash
# Builds dist/SelvaSync.exe (Windows 64-bit). Optional: set GOOGLE_CLIENT_ID
# and GOOGLE_CLIENT_SECRET to build in your Google OAuth "Desktop app" client,
# and PICKER_URL (e.g. https://USERNAME.github.io/selva-sync/picker.html)
# to let users choose the backup folder.
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:-1.2.7}"
PKG=github.com/selvasync/selvasync/internal/app
LDFLAGS="-s -w -H=windowsgui -X ${PKG}.Version=${VERSION}"
if [ -n "${PICKER_URL:-}" ]; then
  LDFLAGS="${LDFLAGS} -X ${PKG}.DefaultPickerURL=${PICKER_URL}"
fi
if [ -n "${GOOGLE_CLIENT_ID:-}" ]; then
  LDFLAGS="${LDFLAGS} -X ${PKG}.DefaultClientID=${GOOGLE_CLIENT_ID} -X ${PKG}.DefaultClientSecret=${GOOGLE_CLIENT_SECRET:-}"
fi
go test ./...
# Stamp the real version into the Windows file details (needed for code
# signing). Uses the MinGW resource compiler when it is installed (the
# GitHub build installs it); otherwise the committed .syso is used as is.
if command -v x86_64-w64-mingw32-windres >/dev/null 2>&1; then
  NUM="${VERSION%%-*}"
  IFS=. read -r V1 V2 V3 _ <<<"${NUM}.0.0.0"
  sed -e "s/1,2,0,0/${V1:-0},${V2:-0},${V3:-0},0/g" -e "s/\"1\.2\.0\"/\"${VERSION}\"/g" \
    build/selvasync.rc > build/version.rc
  (cd build && x86_64-w64-mingw32-windres --preprocessor=cpp -O coff -i version.rc -o ../rsrc_windows_amd64.syso)
  rm -f build/version.rc
fi
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" -o dist/SelvaSync.exe .
ls -l dist/SelvaSync.exe
