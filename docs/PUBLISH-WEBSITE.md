# Put the Selva Sync website online (free, with GitHub Pages)

The `docs` folder contains a ready-made website:

| Page | File | Use it for |
|---|---|---|
| Home page | `docs/index.html` | Google consent screen "Application home page" |
| Privacy policy | `docs/privacy.html` | Google consent screen "Application privacy policy link" |
| Terms of use | `docs/terms.html` | Google consent screen "Application terms of service link" (optional) |

The download and source-code buttons fill in their links automatically once the site is online, so nothing needs editing.

> Why not use the LinkedIn profile as the home page? Google requires the home page and privacy policy to be on a domain **you can prove you own** (an "authorized domain"). You can't prove ownership of linkedin.com, but you can for your own GitHub Pages address. The LinkedIn profile is linked from every page instead.

Time needed: about 20 minutes. Cost: free.

## 1. Create a GitHub account and repository

1. Go to **https://github.com/signup** and create a free account. Choose your username carefully, because it becomes part of the web address (for example `selvasync` gives `https://selvasync.github.io/...`).
2. Click **+ → New repository**.
   - Name: `selva-sync`
   - Visibility: **Public**
   - Click **Create repository**.
3. On the new repository page, click **uploading an existing file**. Drag in **all the files and folders** from the unzipped `selva-sync` source folder, then click **Commit changes**.
   (If the browser does not upload folders, use GitHub Desktop: https://desktop.github.com)

## 2. Turn on GitHub Pages

1. In the repository, open **Settings → Pages**.
2. Under **Build and deployment**, choose:
   - Source: **Deploy from a branch**
   - Branch: **main**, folder: **/docs**
3. Click **Save**. After 1–2 minutes the page shows: *"Your site is live at https://USERNAME.github.io/selva-sync/"*.

Your three addresses are:

- Home page: `https://USERNAME.github.io/selva-sync/`
- Privacy policy: `https://USERNAME.github.io/selva-sync/privacy.html`
- Terms: `https://USERNAME.github.io/selva-sync/terms.html`

Open them to check they work.

## 3. Prove to Google that you own the site

1. Go to **https://search.google.com/search-console** and sign in with the **same Google account** that owns the Google Cloud project.
2. Click **Add property**, choose **URL prefix**, and enter `https://USERNAME.github.io/` (with the slash at the end, no repository name).
   - If Google does not accept the shorter address, enter the full site address `https://USERNAME.github.io/selva-sync/` instead.
3. Choose the **HTML file** method and download the small file it offers (named like `google1234abcd.html`).
4. Put that file where the address you entered points to:
   - For `https://USERNAME.github.io/`, create a second public repository named exactly **`USERNAME.github.io`**, upload the file there and turn on Pages for it (Settings → Pages → branch **main**, folder **/ (root)**).
   - For the full site address, upload the file into the **`docs`** folder of the `selva-sync` repository.
5. Wait a minute, then click **Verify** in Search Console.

## 4. Add the pages to the Google consent screen

In **Google Cloud Console → Google Auth Platform → Branding** (or *APIs & Services → OAuth consent screen*):

1. **Application home page:** `https://USERNAME.github.io/selva-sync/`
2. **Application privacy policy link:** `https://USERNAME.github.io/selva-sync/privacy.html`
3. **Application terms of service link:** `https://USERNAME.github.io/selva-sync/terms.html`
4. **Authorized domains:** add `USERNAME.github.io`
5. Click **Save**.

Then continue with Step 5 ("Publish the app") in [GOOGLE-SETUP.md](GOOGLE-SETUP.md).

## Updating the pages later

Edit `docs/privacy.html` (or the other pages) in GitHub (open the file → pencil icon → **Commit changes**). The site updates within a minute or two. If you change what the app does with data, update the privacy policy and its effective date.
