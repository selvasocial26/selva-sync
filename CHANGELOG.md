# Changelog

## 1.1.0 (24 Oct 2026)

Three rounds of adversarial testing ("try to break it"); every issue below has an automated regression test.

### Data safety
- Renamed and moved files and folders are now moved on Drive instead of uploaded again; contents are verified by checksum first, so different files are never confused.
- Files of another PC (cloned PCs, two Windows users) are never archived: only files this installation has seen are archived; computer folders are identified by an installation ID.
- A different USB stick or disk under the same drive letter is skipped (with a "Use this disk" option) instead of overwriting the first disk's backup.
- Interrupted large uploads can no longer resume into an archived folder.
- Signing out / switching account during a sync stops the sync first; no duplicates or files in the wrong account.
- System folder rules (Windows, Program Files, AppData…) now apply only at the top of a drive, so your own folders with those names are backed up.
- Files or folders deleted in Google Drive are uploaded again; changes are never written into a trashed file.
- Two selected folders can no longer map to the same Drive folder.
- Duplicate folders created on Drive (e.g. restored from trash) are merged at the weekly check; items moved or renamed inside the mirror on Drive are put back.
- "Keep old versions" no longer copies a locked file on every failed run.
- Monthly checksum check catches edits that kept the same size and time; FAT/exFAT daylight-saving time shifts no longer cause re-uploads.
- Crash / power-cut recovery: no duplicates, deleted files still archived.
- Archive retention now permanently deletes old day folders, measured with Google's clock (a wrong PC clock cannot expire files early).
- Only one copy of Selva Sync syncs at a time (data-folder lock), even when the same user is signed in twice.

### Security
- Folder browsing and sources accept only local drive paths (no network-share paths that could leak Windows credentials, no device or reserved names).
- The Google client secret is never sent to the app window; the session key in instance.json is encrypted with Windows DPAPI.
- Windows tools are run from System32 by full path with time limits.
- Running a different SelvaSync.exe asks before replacing the installed copy.

### Reliability and usability
- Damaged settings are restored from a backup copy with a warning (never silently reset).
- The tray and window stay responsive during slow Google sign-in refreshes.
- Schedules are daylight-saving safe (including midnight changes); adding the first folder or changing the schedule no longer triggers an immediate sync; failed syncs retry after 3, 15, 30… minutes and notify only once.
- Wi-Fi rules handle Windows 11's location permission and non-English Wi-Fi names.
- Uninstall removes the program folder completely.
- UI fixes: turning folders off/removing them always saves, settings typed during a save are kept, offline notice, preview shows moves, translated installer messages.

## 1.0.0
First release.
