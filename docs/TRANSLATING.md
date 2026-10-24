# Add or improve a language

Selva Sync's text lives in one simple file per language. You don't need programming skills.

## Try a new language on your own PC (no rebuild)

1. Open Selva Sync → **Settings → Open languages folder**. This opens `%APPDATA%\SelvaSync\languages`.
2. Copy [`internal/app/web/i18n/en.json`](../internal/app/web/i18n/en.json) into that folder and rename it with your language code, for example `de.json` (German), `te.json` (Telugu), `sw.json` (Swahili), `ar.json` (Arabic).
3. Open it in Notepad and translate the text **on the right side** of each line:
   ```json
   "btn_sync_now": "Jetzt synchronisieren",
   ```
4. Set the first two lines:
   ```json
   "_name": "Deutsch",
   "_dir": "ltr",
   ```
   Use `"rtl"` for right-to-left languages such as Arabic or Urdu.
5. Save the file as **UTF-8**, then restart Selva Sync. Your language appears in the Language list, marked with `*`.

## Rules

- Keep words in `{curly brackets}` exactly as they are, for example `{path}`, `{n}`, `{time}`. The program fills them in.
- Don't translate: *Selva Sync*, *Google Drive*, *Windows*, *Wi-Fi*, *KB/s*, *MB*, *GB*, and the folder name **"Archive (Selva Sync)"**.
- Keep button and menu text short.
- Any line you leave out or leave empty is shown in English.

## Share it with everyone

Send the file as a pull request to `internal/app/web/i18n/`, or attach it to a GitHub issue. Built-in languages are tested automatically to make sure no text or `{placeholder}` is missing.

## Current languages

English · हिन्दी (Hindi) · ગુજરાતી (Gujarati) · मराठी (Marathi) · தமிழ் (Tamil) · বাংলা (Bengali) · Español · Français · Português

Native-speaker reviews of the existing translations are very welcome.
