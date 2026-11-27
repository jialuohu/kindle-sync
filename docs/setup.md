# Set up Kindle Sync on Linux

Use systemd for a native Linux install. Docker Compose is the recommended deployment. Both use the account settings and paper test below. [Docker Compose guide](docker.md)

## Install on Linux

The server needs systemd and Python 3.12 with `venv` and pip. Build the release from the checkout with Go 1.26 or later, Make, and Python 3.

```sh
make dist GOARCH=amd64
```

Use `GOARCH=arm64` for ARM64. Copy the archive from `dist/` to the server. The example installs the x86-64 build.

```sh
tar -xzf kindle-sync-linux-amd64.tar.gz
cd kindle-sync-linux-amd64
sudo ./deploy/install.sh
```

The installer creates the service account, Python environment, and timer. Enable the timer after the one-paper test.

## Connect your accounts

### Set the library, sender, and destination

For systemd, edit `/etc/kindle-sync/config.json`. For Compose, edit the private config you mount. Keep the supplied helper and state paths. [Example configuration](../deploy/config.example.json)

| Setting | Value |
| --- | --- |
| `library_id` | Your numeric user ID on the [Zotero API keys page](https://www.zotero.org/settings/keys). |
| `collection_key` | The eight-character key in the reading collection's Zotero web URL. The service also watches its subcollections. |
| `webdav_url` | The full HTTPS URL of the Koofr storage directory, including the final `/zotero/`. |
| `sender_email` | The primary Gmail or Google Workspace address that sends the PDFs. |
| `kindle_email` | The Scribe's Send to Kindle email address. Look under **Settings → Your Account**. |
| `drive_folder_id` | The ID of the Scribe's export folder. For a new setup, leave it empty until the first export. |

In Amazon's **Manage Your Content and Devices → Preferences → Personal Document Settings**, add `sender_email` to the **Approved Personal Document Email List**. On the Scribe, connect the Google Drive account you want to use for exports. It can differ from the sender. Drive export requires a Scribe released in 2025 or later. [Amazon sender setup](https://digprjsurvey.amazon.com/csad/help/node/GX9XLEVV8G4DB28H) · [Scribe export instructions](https://digprjsurvey.amazon.com/csad/help/node/TP8wuaQilIlwlITVTL)

### Save the credentials

Put these files in the service's `CODEX_SECRETS_DIR`. For systemd, use `/etc/kindle-sync/secrets`, owned by `kindle-sync`. Set directory permissions to `0700` and file permissions to `0600`.

| File | Contents |
| --- | --- |
| `zotero-api-key` | A Zotero API key with read and write access to your personal library. |
| `koofr-username`, `koofr-password` | The WebDAV username and Koofr application password, one value per file. |
| `google-client.json` | A desktop OAuth client from a Google Cloud project with the Gmail and Google Drive APIs enabled. |

### Authorize Gmail and Google Drive

For a remote server, connect with `ssh -L 43891:127.0.0.1:43891 user@server` so your browser can complete sign-in. For Compose, follow the [container authorization steps](docker.md#authorize-gmail-and-google-drive). For systemd, run these commands in the server shell.

```sh
ks() {
  sudo -u kindle-sync env CODEX_SECRETS_DIR=/etc/kindle-sync/secrets \
    /opt/kindle-sync/bin/kindle-sync "$@"
}
ks auth gmail
ks auth drive
```

Open each consent link. Authorize Gmail to send mail and verify the sender's address. Grant read-only access to the Scribe's Drive account. The CLI saves `gmail-token.json` and `drive-token.json` in the secrets directory.

External OAuth apps in **Testing** issue seven-day refresh tokens for these permissions. [Google token expiration](https://developers.google.com/identity/protocols/oauth2#expiration)

## Verify one paper

1. **Find the paper.** Choose a paper with a stored PDF in the reading collection. Run `ks find "PAPER TITLE"` and copy its `key` from `matches`.
2. **Send it.** Replace `ITEM_KEY` below. Preview the filename, then send the paper. If multiple PDFs qualify, add `--attachment ATTACHMENT_KEY` to select the original.

   ```sh
   ks sync --item ITEM_KEY --dry-run
   ks sync --item ITEM_KEY
   ```

   On a new installation, this records existing papers and sends only the selected one.
3. **Export a mark.** Open the paper on the Scribe, mark a page, and tap **Share → Google Drive → Upload**. Keep the exported filename.
4. **Check the return.** Open the export folder in Drive and copy its ID from the URL into `drive_folder_id`. Copy the PDF's file ID from its link and run `ks receive DRIVE_FILE_ID`. Sync Zotero, then open **Kindle Notes** under the original paper and check your mark.

## Run on a schedule

Enable the five-minute timer after the test.

```sh
sudo systemctl enable --now kindle-sync.timer
systemctl list-timers kindle-sync.timer
journalctl -u kindle-sync.service
```

Scheduled scans send newly added papers. To include older papers in a watched collection and its subcollections, preview `ks sync --backfill COLLECTION_KEY --dry-run`, then remove `--dry-run`.

## Kindle filenames

A 2026 paper by Reed introducing *Quill* becomes `reed26-quill.pdf`. The parts are the first author's surname, two-digit year, and artifact name. To choose the name, pass `--artifact quill` with `--item` or set `artifact_names` for scheduled sends.

Kindle Sync uses the first year it can identify.

1. Zotero's publication date, an explicit date in **Extra**, or the first submission year from a recognized arXiv identifier.
2. The publication year from Crossref, using only the paper's DOI.
3. A publication year printed on the PDF's first page.
4. The year you added the item to Zotero. If that date is missing too, the filename uses `nd`.

Previews use metadata. The PDF can refine the year at send time. The fallback applies only to the filename. Zotero keeps its original **Date** field.

To resend under a corrected filename, run `ks sync --item ITEM_KEY --resend-as reed26-quill.pdf`. The name must match the CLI's calculation. Repeats skip an existing delivery. Either name matches future exports, and the old Kindle copy stays until you remove it.

## Which exports return to Zotero?

Kindle Sync checks PDFs directly inside the configured Drive folder. The filename must match one paper submitted to Gmail or confirmed on the device. Either `reed26-quill.pdf` or `reed26-quill - Kindle.pdf` matches a delivery named `reed26-quill.pdf` if the Drive file was created after the CLI named that delivery. Older deliveries can match by token. Give unrelated PDFs distinct names.

| Situation | What happens |
| --- | --- |
| The name is unknown or ambiguous | The PDF stays in Drive. Kindle Sync skips the download and creates no pending match. |
| You moved the paper out of the reading collection | Notes return to the same Zotero item. |
| You trashed or deleted the paper | The export stays in Drive. |
| You exported the current notes again | Identical PDF contents reuse the current attachment. |
| You exported new notes | Kindle Sync verifies the new attachment, then moves the previous Kindle Notes attachment to Zotero Trash. The original PDF and Drive exports stay in place. |

For one Drive file, the highest revision wins. Across uploads, Kindle Sync compares modification times, using creation times when needed. An older export that arrives late leaves newer notes in place.

## Recovery

Run `ks status --json` for delivery states, attachment candidates, Gmail IDs, and export records. Sync commands return JSON and exit with a nonzero status when a paper needs attention.

| Status or symptom | Action |
| --- | --- |
| `waiting_pdf` | Check the reported error. If the PDF is still syncing to Koofr, the service retries on the next scan. |
| `submitted` | Gmail accepted the message. Check the Kindle library for the paper. |
| `uncertain` | Check your device first. If the paper arrived, run `ks resolve ITEM_KEY received`. If it did not, use `ks resolve ITEM_KEY retry` to allow another send. |
| Amazon reports a rejection | Find the matching Gmail ID in status. Run `ks resolve ITEM_KEY rejected GMAIL_ID`, then `ks sync --item ITEM_KEY`. |
| `manual_upload` | Upload the PDF at `manual_file`, then use `ks match DRIVE_FILE_ID ITEM_KEY` when you export its notes. [Send to Kindle](https://www.amazon.com/sendtokindle) |
| You want to assign an export manually | Run `ks match DRIVE_FILE_ID ITEM_KEY`. For a specific previously cached revision, use `ks match --version VERSION DRIVE_FILE_ID ITEM_KEY`. |
| The CLI cannot tell which notes are newer | Share a fresh copy from the Kindle. Its upload time lets Kindle Sync compare it with older attachments that lack a date. |

### File size limits

Each paper gets its own email. The default `email_limit_bytes` is 25,000,000 bytes, the personal Gmail attachment limit. Workspace administrators can set a different limit. The CLI also accounts for the encoded message size. Amazon accepts up to 50 MB per email and 200 MB per web upload. [Gmail limits](https://support.google.com/mail/answer/6584) · [Kindle email limits](https://digprjsurvey.amazon.com/csad/help/node/G7NECT4B4ZWHQ8WV) · [Web upload limits](https://digprjsurvey.amazon.com/csad/help/node/G5WYD9SAF7PGXRNA)

### Back up the queue

Stop the timer, wait for active syncs to finish, and back up all of `state_dir` (default `/var/lib/kindle-sync`). It contains SQLite and cached PDFs. Use a separate state directory if you change the personal library, reading collection, Kindle destination, or previously bound Drive folder. Select another configuration with `--config PATH` or `KINDLE_SYNC_CONFIG`.
