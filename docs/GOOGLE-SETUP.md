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
   *Don't upload a logo for now. Adding a logo makes Google require a brand review.*

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

## What users will see

When users click **Connect Google Drive**, their browser opens Google's sign-in page showing *"Selva Sync wants to access your Google Account"* with the single permission from Step 4. After they approve, the page says *Connected!* and they can close it.

If a user ever sees **"Google hasn't verified this app"**, the app is probably still in *Testing* mode (repeat Step 5). It is safe to continue with **Advanced → Go to Selva Sync**.

## Limits worth knowing

- Google allows a very large number of Drive API requests per day for free. Normal backup use never comes close.
- Each user's files count toward **their own** Google Drive storage (15 GB free, shared with Gmail and Photos). Nonprofits can apply for **Google for Nonprofits** for more storage.
