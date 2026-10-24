# Building Selva Sync from source

Selva Sync is written in Go and uses **only the Go standard library**, so there are no dependencies to download. The Windows icon, version info and manifest are pre-compiled in `rsrc_windows_amd64.syso`, so you can build on Windows, Linux or macOS.

## Requirements

- Go 1.22 or newer: https://go.dev/dl/

## Build the Windows program

**On Windows (PowerShell):**
```powershell
$env:GOOS="windows"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -trimpath -ldflags "-s -w -H=windowsgui" -o SelvaSync.exe .
```

**On Linux / macOS:**
```bash
./build.sh            # creates dist/SelvaSync.exe
```

### Building in your Google credentials

```bash
GOOGLE_CLIENT_ID="1234-abc.apps.googleusercontent.com" \
GOOGLE_CLIENT_SECRET="GOCSPX-..." \
./build.sh
```

This sets `app.DefaultClientID` and `app.DefaultClientSecret` with `-ldflags -X`. Users can still override them in *Settings → Advanced*.

## Run the tests

```bash
go test ./...
```

The tests run the real sync engine against an in-memory imitation of Google Drive (`internal/fakedrive`). They check first sync, unchanged runs, in-place updates, archiving, reinstall recovery, crash recovery, lost network replies, resumable uploads, data caps, network rules, junction folders, swapped USB drives, two PCs with the same name, and that every translation is complete.

## Project layout

| Path | What it is |
|---|---|
| `main.go` | Start-up, self-install, single instance |
| `internal/engine` | Scans folders, decides what to upload or archive, bandwidth limiter |
| `internal/drive` | Minimal Google Drive v3 client (resumable uploads, retries) |
| `internal/auth` | Google sign-in (OAuth 2.0 + PKCE, loopback redirect) |
| `internal/app` | Settings, scheduler, tray menu, local app window server |
| `internal/app/web` | The app window (HTML/CSS/JS) and translations (`i18n/*.json`) |
| `internal/platform` | Windows-specific code: tray icon, DPAPI, registry, install |
| `build/` | Resource script and manifest (to regenerate the `.syso`) |

## Regenerating the Windows resources (only if you change the icon)

```bash
sudo apt install binutils-mingw-w64-x86-64
cd build && x86_64-w64-mingw32-windres --preprocessor=cpp -O coff -i selvasync.rc -o ../rsrc_windows_amd64.syso
```

## Automatic releases with GitHub

`.github/workflows/release.yml` builds `SelvaSync.exe` whenever you push a tag such as `v1.0.1`, and attaches it to a GitHub Release.

1. In your repository: **Settings → Secrets and variables → Actions → New repository secret**. Add `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET`.
2. Create a release tag: `git tag v1.0.1 && git push origin v1.0.1`.

## Code signing (optional, free for open source)

Unsigned programs show a *"Windows protected your PC"* warning the first time. Open-source projects can apply for free code signing at **SignPath Foundation** (https://signpath.org). Once approved, add their signing step to the release workflow.
