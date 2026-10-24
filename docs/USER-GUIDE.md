# Selva Sync: user guide

Selva Sync keeps a copy of your chosen folders in your Google Drive. It only sends files that are new or changed, at the times you choose, without filling your computer's disk or using up your internet.

## First-time setup (3 steps)

1. **Connect Google Drive.** On the Home screen, click **Connect Google Drive**. Your browser opens. Choose your Google account and click **Allow**. When you see *Connected!*, close the browser tab.
2. **Choose folders.** Go to **Folders → Add folder**. Open a drive (C:, D:, a USB drive...), open the folder you want, and click **Select this folder**. Add as many as you like.
3. **Choose when.** Go to **Schedule** and pick *Daily at 9 PM*, *Weekly*, *Every few hours* or *Manual*.

Tip: before the first sync, click **Preview changes** on the Home screen to see how many files and how much data will be sent.

## Everyday use

You don't have to do anything. Selva Sync runs in the notification area (bottom-right corner, near the clock).

- **Click** the Selva Sync icon to open the window.
- **Right-click** it for *Sync now*, *Stop sync* or *Quit*.
- A small message appears when a sync finishes or needs your attention.

If your computer was switched off at the scheduled time, Selva Sync syncs as soon as it is on again.

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

Click **Open in Google Drive** on the Home screen, or go to *My Drive → Selva Sync → (your computer name)*. Folders are arranged like your PC, for example `D-Drive / Accounts / 2026`.

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

## Uninstalling

**About → Uninstall**, or *Windows Settings → Apps → Selva Sync → Uninstall*. Your files on Google Drive stay where they are.
