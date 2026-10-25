# One-time Google setup (for the publisher)

Selva Sync talks to Google Drive using an **OAuth Client ID**, a free "app identity" issued by Google Cloud. You create it **once**. After that, everyone who uses your copy of Selva Sync simply clicks *Connect Google Drive*.

Time needed: about 15 minutes. Cost: free. No credit card is needed for this.

> Google renames menu items from time to time. If a name below doesn't match exactly, look for the closest one. The steps stay the same.

---

## Step 1: Create a Google Cloud project

1. Open **https://console.cloud.google.com/** and sign in with the Google account that should "own" the app (for example your work account).
2. At the top, click the **project selector → New project**.
3. Name: `Selva Sync`. Click **Create**, then make sure the new project is selected.

## Step 2: Turn on the Google Drive API

1. Menu **☰ → APIs & Services → Library**.
2. Search for **Google Drive API**, open it and click **Enable**.

## Step 3: Set up the consent screen (what users see when they connect)

1. Menu **☰ → APIs & Services → OAuth consent screen**. This is also called **Google Auth Platform → Branding**.
2. Click **Get started** and fill in:
   - **App name:** `Selva Sync`
   - **User support email:** your email
   - **Audience:** **External** (so anyone with a Google account can use it)
   - **Contact email:** your email
3. Agree to the policy and click **Create**.
4. Under **Branding**, add the home page, privacy policy and terms links from the Selva Sync website, and add your site's domain under **Authorized domains**. The ready-made pages and how to put them online for free are in [PUBLISH-WEBSITE.md](PUBLISH-WEBSITE.md).
   *Logo (optional): upload `assets/selvasync-logo-120.png` (square, 120×120). Adding a logo makes Google review your branding before the logo is shown. Do it once the website is online and your domain is verified.*

## Step 4: Allow only the minimum permission

1. Go to **Data access** (or **Scopes**) → **Add or remove scopes**.
2. Find and tick **`.../auth/drive.file`** ("See, edit, create and delete only the specific Google Drive files you use with this app").
3. Click **Update → Save**.

This permission is classed by Google as *non-sensitive*. Selva Sync can only see files it created itself.

## Step 5: Publish the app ⚠️ important

1. Go to **Audience** (or **OAuth consent screen → Publishing status**).
2. Click **Publish app → Confirm** so the status becomes **In production**.

Why this matters: while an app is in *Testing* mode, Google signs users out **every 7 days** and only allows 100 listed test users. Apps that use only the `drive.file` permission normally don't need a Google review to publish.

## Step 6: Create the Client ID

1. Go to **Clients** (or **APIs & Services → Credentials**) → **Create client** (or **Create credentials → OAuth client ID**).
2. **Application type:** **Desktop app**
3. **Name:** `Selva Sync for Windows`
4. Click **Create**, then **Download JSON**.
5. Rename the downloaded file to **`google-client.json`**.

That's it. 🎉

---

## Using your credentials

Pick whichever is easiest:

**A. Share two files (no programming needed)**
Put `google-client.json` in the same folder as `SelvaSync.exe` and share both, for example in one ZIP file. When someone runs `SelvaSync.exe`, it installs itself and copies the credentials file along.

**B. Use them only on your own PC**
Open Selva Sync → **Settings → Advanced** and paste the **Client ID** and **Client secret** from the JSON file. Click **Save**.

**C. Build them into the program**
See [BUILDING.md](BUILDING.md). With GitHub, add `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET` as repository secrets and every release is built with them automatically.

### Is it safe to share the Client ID and secret?

Yes, for a *Desktop app* client. Google treats desktop client secrets as **not confidential**, because they are always inside the program anyway. They identify the app, not a person. They do **not** give anyone access to your Drive or anyone else's. Each user still signs in with their own account and approves access. Keep the Google Cloud **project** itself private.

---

## Optional: let users choose the backup folder (My Drive or shared drive)

Without this, every PC backs up into *My Drive › Selva Sync*. With it, users get a **Choose folder in Google Drive** button (Safety page) that opens Google's own folder chooser, including **shared drives**. It takes about 10 minutes more and stays free.

Why a web page is needed: Selva Sync only has Google's *per-file* permission (`drive.file`), so it cannot list your folders. Google's folder chooser ("Google Picker") runs on a web page and lets the user hand one folder to Selva Sync. The page is already in the website (`docs/picker.html`). You only fill in three values.

Do this **in the same Google Cloud project** as the steps above (that is how the folder chosen on the web page becomes usable by the desktop app).

1. **Put the website online first** ([PUBLISH-WEBSITE.md](PUBLISH-WEBSITE.md)). Your folder chooser address will be `https://USERNAME.github.io/selva-sync/picker.html`.
2. **Turn on the Google Picker API:** ☰ → APIs & Services → Library → search **Google Picker API** → **Enable**.
3. **Create an API key:** APIs & Services → Credentials → **Create credentials → API key**. Then click the key to edit it:
   - *Application restrictions:* **Websites**, add `https://USERNAME.github.io/*`
   - *API restrictions:* **Restrict key** → tick **Google Picker API** → **Save**.
4. **Create a web client:** Clients (or Credentials) → **Create client** → Application type **Web application**, name `Selva Sync folder chooser`. Under **Authorized JavaScript origins** add `https://USERNAME.github.io` (no slash at the end, no path). Leave redirect URIs empty. Click **Create** and copy the **Client ID**.
5. **Find the project number:** ☰ → **Cloud overview → Dashboard** (or the project selector → project settings). Copy the **Project number** (digits only).
6. **Fill in the page settings:** on GitHub, open `docs/picker-config.js` → pencil icon, paste the three values, and click **Commit changes**:
   ```js
   window.SELVA_PICKER = {
     clientId: "1234-abc.apps.googleusercontent.com",
     apiKey: "AIza...",
     appId: "123456789012"
   };
   ```
   None of these values are secret (they only work from your website, and every user still signs in with their own account).
7. **Tell Selva Sync where the page is.** Choose one:
   - **A. In `google-client.json`** (no rebuild): open the file in Notepad and add one line after the first `{`:
     ```json
     "selvasync_picker_url": "https://USERNAME.github.io/selva-sync/picker.html",
     ```
   - **B. In the build:** in GitHub, **Settings → Secrets and variables → Actions → Variables → New repository variable**, name `PICKER_URL`, value the address above. Or run `PICKER_URL=... ./build.sh`.
   - **C. On one PC:** Selva Sync → Settings → Advanced → *Folder chooser page*.

Test it: open Selva Sync → **Safety → Choose folder in Google Drive**, pick a folder, and check that the page says *Backup folder saved*.

If the page says *Folder choosing is not set up*, step 6 is missing. If Google shows *origin_mismatch*, check the JavaScript origin in step 4. If Selva Sync says it *cannot open this folder*, the user signed in with a different Google account in the browser, or the web client is in a different Cloud project.

---

## What users will see

When users click **Connect Google Drive**, their browser opens Google's sign-in page showing *"Selva Sync wants to access your Google Account"* with the single permission from Step 4. After they approve, the page says *Connected!* and they can close it.

If a user ever sees **"Google hasn't verified this app"**, the app is probably still in *Testing* mode (repeat Step 5). It is safe to continue with **Advanced → Go to Selva Sync**.

## Limits worth knowing

- Google allows a very large number of Drive API requests per day for free. Normal backup use never comes close.
- Each user's files count toward **their own** Google Drive storage (15 GB free, shared with Gmail and Photos). Nonprofits can apply for **Google for Nonprofits** for more storage.
