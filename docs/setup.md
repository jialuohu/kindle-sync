# Linux server setup with systemd

## Install on Linux

The server needs systemd and Python 3.12 with `venv` and pip. Build the release with Go 1.26 or later, Make, and Python 3. Run `make dist GOARCH=amd64` for x86-64 or `make dist GOARCH=arm64` for ARM64.

Copy the archive from `dist/` to the server. Install the x86-64 build with these commands.

```sh
tar -xzf kindle-sync-linux-amd64.tar.gz
cd kindle-sync-linux-amd64
sudo ./deploy/install.sh
```

The installer creates a service account, a Python environment, and a systemd timer. Enable the timer after the first round trip.

## Connect your accounts

Edit `/etc/kindle-sync/config.json` on the server. The installer supplies the helper paths and state directory. [Example configuration](../deploy/config.example.json)

| Setting | What to enter |
| --- | --- |
| `library_id` | Numeric user ID shown on your [Zotero API Keys page](https://www.zotero.org/settings/keys). |
| `collection_key` | Eight-character key from the reading collection’s URL in the web library. Subcollections are included. |
| `sender_email` | Primary address of the Gmail or Google Workspace account that will send papers. |
| `kindle_email` | Your Scribe’s **Send to Kindle Email**, under **Settings → Your Account**. |
| `webdav_url` | Full HTTPS URL of the Zotero storage directory on Koofr, including the final `/zotero/`. |
| `drive_folder_id` | Leave empty until you export the first paper. |

In Amazon’s **Manage Your Content and Devices → Preferences → Personal Document Settings**, add the sender to the **Approved Personal Document E-mail List**. On the Scribe, connect the Google Drive account that will receive your exports. It can differ from the Gmail sender. [Amazon sender setup](https://digprjsurvey.amazon.com/csad/help/node/GX9XLEVV8G4DB28H)

Store these credentials in `/etc/kindle-sync/secrets` (the service’s `CODEX_SECRETS_DIR`). Set the owner to `kindle-sync`, the directory mode to `0700`, and file modes to `0600`.

| File | Contents |
| --- | --- |
| `zotero-api-key` | Zotero API key with read and write access to the personal library. |
| `koofr-username`, `koofr-password` | WebDAV username and Koofr application password, one value per file. |
| `google-client.json` | Desktop app OAuth client downloaded from a Google Cloud project with the Gmail API and Google Drive API enabled. |

For a remote server, connect with `ssh -L 43891:127.0.0.1:43891 user@server` so your browser can complete authorization. Define this shortcut in the server shell and open each consent link when prompted.

```sh
ks() {
  sudo -u kindle-sync env CODEX_SECRETS_DIR=/etc/kindle-sync/secrets \
    /opt/kindle-sync/bin/kindle-sync "$@"
}
ks auth gmail
ks auth drive
```

Gmail grants permission to send and to verify the sender’s email address. Drive grants read-only access to the account. The CLI scans PDFs directly inside the configured export folder. It saves `gmail-token.json` and `drive-token.json` in the secrets directory. For external OAuth apps in **Testing**, Google expires these refresh tokens after seven days. [Google token expiration](https://developers.google.com/identity/protocols/oauth2#expiration)

## Verify one paper

1. Pick a paper with a stored PDF in the reading collection. Run `ks sync --dry-run`, then `ks sync` to record existing papers as a baseline. Later runs send papers when they first appear in the collection tree.
2. Look up the paper with `ks find "TITLE"` or use its Zotero key. Preview the filename with `ks sync --direction send --item ITEM_KEY --dry-run`, then remove `--dry-run` to send it. If several PDFs qualify, add `--attachment ATTACHMENT_KEY`. To choose the artifact name (e.g., `quill` in `reed26-quill.pdf`), pass `--artifact quill` or set the item’s entry in `artifact_names` for scheduled runs.
3. Open the paper in your Kindle library, mark a page, and tap **Share → Google Drive → Upload**. In Drive, open the folder the Scribe created and copy its ID from the URL into `drive_folder_id`. Keep the exported filename so Kindle Sync can identify the paper. Other PDFs can share this folder. Drive export requires a Scribe released in 2025 or later. [Scribe export instructions](https://digprjsurvey.amazon.com/csad/help/node/TP8wuaQilIlwlITVTL)
4. Copy the exported PDF’s file ID from its Drive link and run `ks receive DRIVE_FILE_ID`. Sync Zotero and open the new **Kindle Notes** attachment under the original paper. Check your mark. Receiving the same PDF again should leave a single notes attachment.

## Kindle filenames

The filename combines the first author's surname, the last two digits of the year, and the artifact name, as in `reed26-quill.pdf`. Kindle Sync first reads Zotero's bibliographic date, including an explicit issued date or a recognized arXiv identifier. If the year is still missing, it can look up the item's DOI in Crossref and check the PDF's first page for a publication year. Only the DOI is sent to Crossref.

When no publication year can be established, Kindle Sync uses the year the item was added to Zotero for the filename. If that is also missing, it uses `nd` (no date). A preview reads metadata only, so the PDF can refine a provisional filename at send time. Zotero's Date field is untouched. Already sent papers keep their recorded filenames so their notes still match.

## Which exports return to Zotero?

Kindle Sync checks Drive metadata before downloading. The filename must identify one paper submitted to Gmail or confirmed as received. For example, a delivery named `reed26-quill.pdf` also matches `reed26-quill - Kindle.pdf`. Older deliveries can match by their delivery token. An unrelated PDF with the same qualifying name can match too. Give personal documents different names when sharing the folder.

| What happened | What the next sync does |
| --- | --- |
| The export has an unknown or ambiguous name | Leaves it in Drive without downloading it or adding it to a matching queue. |
| You moved the sent paper out of the reading collection | Attaches notes to the paper using its original Zotero item key. |
| You trashed or permanently deleted the paper | Leaves the export in Drive. |
| You exported the same paper again | Identical PDF bytes reuse the current attachment. A newer export with changed bytes replaces it after the new copy is verified. An older export arriving later cannot replace newer notes. |

For revisions of one Drive file, the higher revision number wins. For separate uploads, Kindle Sync compares modification times, falling back to creation times. It moves superseded attachments it previously archived to Zotero Trash, where you can recover them. Drive exports and the original paper stay in place.

## Run on a schedule

```sh
sudo systemctl enable --now kindle-sync.timer
systemctl list-timers kindle-sync.timer
journalctl -u kindle-sync.service
```

The timer runs every five minutes. To include older papers intentionally, use `ks sync --backfill COLLECTION_KEY --dry-run` to preview the collection and its descendants, then repeat without `--dry-run`. Only papers in the configured reading tree are eligible.

## Recovery

Run `ks status --json` for delivery states, attachment candidates, Gmail message IDs, and pending exports. Sync commands write results to stdout and exit with a nonzero status when a paper needs attention.

| Situation | Action |
| --- | --- |
| Source PDF pending | Let the original finish syncing to Koofr. The next run retries automatically. |
| `submitted` | Gmail accepted the message. Check the Kindle library for delivery. |
| Uncertain send | Check the device first. Use `ks resolve ITEM_KEY received` to confirm receipt, or `ks resolve ITEM_KEY retry` to allow another send. |
| Amazon rejected the message | Verify the corresponding Gmail ID in status, run `ks resolve ITEM_KEY rejected GMAIL_ID`, then `ks sync --item ITEM_KEY` to resend. |
| `manual_upload` | Upload the PDF at `manual_file`. To return its notes, use `ks match DRIVE_FILE_ID ITEM_KEY`. [Send to Kindle](https://www.amazon.com/sendtokindle) |
| You want to attach a specific export manually | Run `ks match DRIVE_FILE_ID ITEM_KEY`. To select a revision cached by an older installation, add `--version VERSION`. |
| A sync cannot determine which older notes are newest | Earlier installations may lack export dates. Share a fresh copy from the Kindle. Its upload time lets the next sync safely replace the older attachments. |

Each paper gets its own email. The default `email_limit_bytes` is 25,000,000, the personal Gmail attachment limit. Workspace administrators can set a different limit. The CLI also accounts for the encoded message size. Amazon accepts up to 50 MB by email and 200 MB per web upload. [Gmail limits](https://support.google.com/mail/answer/6584?hl=en) · [Kindle email limits](https://digprjsurvey.amazon.com/csad/help/node/G7NECT4B4ZWHQ8WV) · [Web upload limits](https://digprjsurvey.amazon.com/csad/help/node/G5WYD9SAF7PGXRNA)

Back up the entire `state_dir` (default `/var/lib/kindle-sync`), including SQLite and queued PDFs, while the timer is stopped and all sync commands are idle. Use a separate state directory when changing the personal library, reading collection, Kindle destination, or an already bound Drive folder. Select another configuration with `--config PATH` or `KINDLE_SYNC_CONFIG`.
