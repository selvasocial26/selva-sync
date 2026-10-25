# Selva Sync: user guide

Selva Sync keeps a copy of your chosen folders in your Google Drive. It only sends files that are new or changed, at the times you choose, without filling your computer's disk or using up your internet.

## First-time setup (3 steps)

1. **Connect Google Drive.** On the Home screen, click **Connect Google Drive**. Your browser opens. Choose your Google account and click **Allow**. When you see *Connected!*, close the browser tab.
2. **Choose folders.** Go to **Folders → Add folder**. Open a drive (C:, D:, a USB drive...), open the folder you want, and click **Select this folder**. Add as many as you like.
3. **Choose when.** Go to **Schedule** and pick *Daily at 9 PM*, *Weekly*, *Every few hours* or *Manual*.

The first time you click **Sync now**, Selva Sync first shows how many files and how much data will be sent, and how much free space your Google Drive has. Click **Start sync** to go ahead.

Optional: to save the backup in a folder of your choice (also in a **shared drive**), see *Choosing the backup folder* below.

## Everyday use

You don't have to do anything. Selva Sync runs in the notification area (bottom-right corner, near the clock).

- **Click** the Selva Sync icon to open the window.
- **Right-click** it for *Sync now*, *Stop sync* or *Quit*.
- A small message appears when a sync finishes or needs your attention.

If your computer was switched off at the scheduled time, Selva Sync syncs as soon as it is on again.

## Choosing the backup folder

All backups from one PC are always saved inside **one** folder on Google Drive. By default this is *My Drive › Selva Sync*.

To use another folder, for example a folder in your office's **shared drive**:

1. Create the folder in Google Drive first, if it doesn't exist yet.
2. In Selva Sync, go to **Safety → Choose folder in Google Drive**.
3. Your browser opens Google's folder chooser. Sign in with the **same Google account** that is connected in Selva Sync, choose the folder and click **Select**.
4. The page says *Backup folder saved*. Close the tab.

Good to know:

- If you choose a different folder later, the next sync uploads everything again into the new folder. Nothing in the old folder is deleted.
- If the folder is deleted, moved to the bin or you lose access, Selva Sync **stops and tells you**. It never saves your files anywhere else.
- You can't choose a folder that is inside a Selva Sync backup (it could be archived by mistake). Choose the folder that contains "Selva Sync", or another folder.
- In a shared drive, files count against the shared drive's storage, and only managers can permanently delete. Old archived files are then moved to the shared drive's bin instead.
- *Use the default folder* goes back to *My Drive › Selva Sync*.

If you don't see the *Choose folder* button, this copy of Selva Sync was published without the folder chooser (see GOOGLE-SETUP.md).

## Reminders, laptops and reports

- **Backup reminder** (Schedule page): if there has been no backup for 3 days (you can change this), the Selva Sync icon turns red and a message appears once a day. 0 turns it off.
- **Only sync when the laptop is charging** (Schedule page): on battery, Selva Sync waits. If you unplug during a sync, it stops and continues when you plug in again.
- **Monthly report** (Safety page): at the start of each month a short file *Selva Sync report YYYY-MM.txt* is saved in your computer's backup folder on Drive, with the number of backups, files and data, and any problems.

## Setting up many office PCs

1. Set up Selva Sync on one PC the way you want it.
2. Go to **Settings → Save office setup file**. The file `selvasync-setup.json` is saved in *Downloads › Selva Sync office setup*.
3. Optional: open the file in Notepad and list the settings that staff may not change, for example `"managed": ["schedule", "archiveDays", "location"]`.
4. Put `selvasync-setup.json` in the **same folder as `SelvaSync.exe`** (for example in the shared ZIP file) and install on each PC.

New installations start with these settings. Settings listed under `"managed"` are applied at every start and are greyed out in the app. Personal settings (folders, language, computer name, Google credentials) are never taken from the file. Removing the file unlocks the settings.

If `"location"` is managed (your organisation's backup folder), each person clicks **Safety → Give access to this folder** once and chooses that same folder in Google Drive. This is needed because Selva Sync can only open folders a person has chosen with it.

Setting names you can use: `schedule`, `location`, `dayLimitKBps`, `nightLimitKBps`, `nightStart`, `nightEnd`, `monthlyCapMB`, `pauseOnMetered`, `blockedWifi`, `allowedWifi`, `excludes`, `maxFileSizeMB`, `deleteMode`, `keepOldVersions`, `archiveDays`, `onlyOnAC`, `alertDays`, `reports`, `driveFolderName`, `parallel`, `syncOnStartup`, `startWithWindows`, `notifications`, `pickerUrl`.

The managed list helps keep PCs consistent. It is not a security lock: a person with access to the PC's files can still change it.

## Saving internet data

Go to **Data & Internet**:

| Setting | What it does | Example |
|---|---|---|
| Daytime limit | Maximum upload speed during the day | 256 KB/s ≈ 0.9 GB per hour |
| Night-time limit | Speed during night hours (often cheaper or unused) | 0 = full speed |
| Monthly data limit | Stops uploading after this much data each month | 5 GB |
| Pause on metered connections | No syncing on mobile data or phone hotspots | On |
| Never sync on these Wi-Fi networks | Block specific networks | your phone's hotspot name |
| Only sync on these Wi-Fi networks | Allow only listed networks (wired is always allowed) | office Wi-Fi |

Large files continue where they stopped if the connection breaks, so no data is wasted.

## What happens when I delete or change a file?

- **Changed file:** the Drive copy is replaced with the new version. Google Drive keeps older versions for about 30 days (right-click the file in Drive → *Manage versions*). In **Safety** you can also turn on *Keep a copy of the old version*.
- **Renamed or moved file or folder:** Selva Sync moves or renames it on Drive as well, without uploading it again. It first checks that the contents are really the same file.
- **Deleted file** (default): it moves to **Archive (Selva Sync) / date** on Drive. After 90 days (you can change this in **Safety**) it is deleted permanently. You can also choose *Keep it on Drive* or *Delete it on Drive too*.
- **USB drive not plugged in:** nothing is deleted or archived. Selva Sync waits until the drive is back.
- **A different disk with the same drive letter** (for example another USB stick): Selva Sync skips it so the first disk's backup is never mixed up. If you replaced the disk on purpose, open **Folders** and click **Use this disk**.
- **Files deleted directly in Google Drive:** they are uploaded again at the next sync.

## Finding your files on Google Drive

Click **Open in Google Drive** on the Home screen, or go to *My Drive → Selva Sync → (your computer name)* (or to the folder you chose). Folders are arranged like your PC, for example `D-Drive / Accounts / 2026`.

To restore a file, download it from Drive like any other file.

## Several computers, one Google account

That's fine. Each computer gets its own folder. You can rename it in **Safety → This computer's name**.

## Changing the language

Use the **Language** box at the bottom-left of the window. The change is immediate.

## Common messages

| Message | What to do |
|---|---|
| *Paused: metered connection* | You are on mobile data or a hotspot. Wait for Wi-Fi, or click **Sync anyway**. |
| *Monthly data limit reached* | Remaining files upload next month, or raise the limit in Data & Internet. |
| *Finished with problems* | Some files were open or locked (for example an open Outlook file). See **Activity**. They are retried next time. |
| *Please sign in again* | Go to **Settings → Connect Google Drive**. |
| *Google Drive is full* | Free up space in Drive or get more storage. |
| *Folder not found* | The drive is not connected (USB, network). Nothing is deleted. |
| *Backup folder not available* | The chosen Drive folder was deleted, moved to the bin, or you lost access. Choose it again in **Safety**. Nothing was uploaded anywhere else. |
| *Backup folder not allowed* | The chosen folder is inside a Selva Sync backup. Choose a different folder. |
| *Waiting: the laptop is on battery* | Plug in the charger, or turn off *Only sync when charging*. |
| *No backup for N days* | Check the internet connection and click **Sync now**. See **Activity** for details. |

## Uninstalling

**About → Uninstall**, or *Windows Settings → Apps → Selva Sync → Uninstall*. Your files on Google Drive stay where they are.
