# Changelog

## 1.2.0 (25 Oct 2026)

### New
- **Choose the backup folder**: all backups from a PC go into ONE folder you choose in Google Drive, in My Drive or in a **shared drive** (Safety → *Choose folder in Google Drive*). Selva Sync never falls back to another place: if the folder is deleted, moved to the bin or you lose access, nothing is uploaded and you are told to choose it again. Needs the one-time folder-chooser setup (see docs/GOOGLE-SETUP.md); without it, backups go to *My Drive › Selva Sync* as before.
- **Backup reminder**: if there has been no backup for a number of days (default 3), the tray icon turns red and a notification appears once a day.
- **Only sync when charging** (laptops): waits on battery, and stops if the laptop is unplugged during a sync.
- **Size estimate before the first backup**: the first *Sync now* shows how much will be uploaded and how much free space your Google Drive has.
- **Office setup file**: save your settings as `selvasync-setup.json`, put it next to `SelvaSync.exe` on other PCs, and they start with the same settings. Settings listed under `"managed"` cannot be changed on those PCs.
- **Monthly report**: a short text report of last month's backups is saved in the backup folder each month.

### Tested ("try to break it", round 4)
- A chosen folder inside another computer's Selva Sync backup is refused (it would have been archived by that PC's weekly check).
- If this PC's backup folder is moved out of the chosen folder, Selva Sync makes a new one inside the chosen folder instead of writing elsewhere.
- Switching folders and back keeps archiving deleted files correctly.
- Searches in shared drives are limited to that drive; incomplete answers from Google are never acted on (no duplicates).
- An organisation's fixed folder can be opened once on each PC to give access (Google's per-file permission), and only that folder is accepted.
- A settings save at the same moment as choosing a folder can no longer undo the choice.
- One wrong value in the office setup file no longer discards the whole file; mistakes are shown and logged. Removing the setup file unlocks the settings.
- The reminder counts from the first attempt (not from the last restart) and is not raised by runs that only had a few locked files.
- Monthly report month is correct on the 29th–31st; a wrongly set clock can no longer silence the reminder.

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
