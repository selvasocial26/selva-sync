# Privacy and security

Selva Sync is designed so that **nobody except you and Google ever handles your data**, and Google only receives what you choose to back up to your own Drive.

## What Selva Sync does NOT do

- No registration, user accounts, licence keys or e-mail collection.
- No analytics, crash reporting, telemetry, update checks or advertising.
- No servers of its own. The only internet addresses it contacts are Google's:
  - `accounts.google.com` and `oauth2.googleapis.com` for sign-in
  - `www.googleapis.com` for Google Drive

## Permission requested from Google

Only **`https://www.googleapis.com/auth/drive.file`**. With this permission Google lets Selva Sync see and change **only the files and folders Selva Sync itself created**. It cannot list, read or delete anything else in your Drive.

## Data stored on your computer

Everything is in `%APPDATA%\SelvaSync` (for example `C:\Users\<you>\AppData\Roaming\SelvaSync`):

| File | Contents |
|---|---|
| `settings.json` | Your choices (folders, schedule, limits, language) |
| `state.json` | The list of files already uploaded (path, size, date, Drive file ID) so nothing is sent twice |
| `token.bin` | Your Google sign-in token, **encrypted with Windows DPAPI**. Only your Windows user on this PC can decrypt it. |
| `install.id` | A random ID for this installation, used to find this PC's folder on Drive |
| `machine.id` | Only created if Windows has no machine GUID: a random ID to tell this PC's Drive folder apart |
| `settings.json.bak` | The previous good copy of your settings (used if the main file gets damaged) |
| `sync.lock` | Makes sure only one copy of Selva Sync syncs at a time |
| `logs\*.log` | What was uploaded and when (kept on your PC only) |
| `languages\` | Optional extra translations you add |

The program itself is in `%LOCALAPPDATA%\Programs\Selva Sync`.

**Sign out** (Settings) revokes the token at Google and deletes it from your PC. **Uninstall** with *"Also delete settings"* removes the whole folder.

## The app window

The window you see is a small local web page served by Selva Sync **only on `127.0.0.1`** (your own computer, not reachable from the network). It is protected by:
- a random session key that changes every time Selva Sync starts
- a required security header on every action, so other websites can't trigger actions (CSRF protection)
- host checks against DNS-rebinding attacks
- a strict Content-Security-Policy with no external scripts, fonts or images

## Machine identity

To keep several PCs on one Google account apart, Selva Sync tags each computer's Drive folder with a random installation ID and a **one-way hash** of the Windows machine GUID and Windows user name. The hash cannot be turned back into the GUID and is not linked to you.

## Open source

The full source code is public under the MIT licence and uses no third-party libraries, only the Go standard library. Anyone can check exactly what Selva Sync does.

## Reporting a security issue

Please contact the author privately via [LinkedIn](https://www.linkedin.com/in/selvakumar-vellingiri-phd-97744572/), or use a private security advisory on the GitHub repository, before disclosing publicly.
