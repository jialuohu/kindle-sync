# Kindle Sync

Send papers from a Zotero collection to a Kindle Scribe, one PDF per email. Write on the paper and choose **Share → Google Drive → Upload** on the Scribe. The next sync stores the exported PDF in Koofr WebDAV and adds a **Kindle Notes** attachment to the original Zotero item. Identical exports of the same paper share one attachment. [Scribe export instructions](https://digprjsurvey.amazon.com/csad/help/node/TP8wuaQilIlwlITVTL)

## Install on Linux

The server needs systemd and Python 3.12 with venv and pip. From the repository root, use Go 1.26 or later and Make to build an x86-64 package. Use `GOARCH=arm64` for an ARM64 server.

```sh
make test
make dist GOARCH=amd64
```

Copy the archive from `dist/` to the server, extract it, and run `sudo ./deploy/install.sh` inside the release directory. The installer creates the service account, builds the Python environment and installs the systemd units.

## Connect the accounts

Edit `/etc/kindle-sync/config.json`. [Example configuration](deploy/config.example.json)

| Setting | Value |
| --- | --- |
| `library_id`, `collection_key` | Numeric personal Zotero library ID and eight-character key of the reading collection. The CLI also watches its subcollections. |
| `sender_email`, `kindle_email` | Primary Gmail or Workspace address for sending, and the Scribe's Send to Kindle address. |
| `webdav_url` | Full HTTPS URL of the Zotero storage directory on Koofr, including the final `/zotero/`. |
| `drive_folder_id` | Leave empty until the first export. Then open the device-created folder in Drive and copy its ID from the URL. |
| `state_dir` | Service-owned directory for SQLite and queued PDFs. The default is `/var/lib/kindle-sync`. |

Add the sender to Amazon's **Approved Personal Document E-mail List** in **Manage Your Content and Devices → Preferences → Personal Document Settings**. On the Scribe, find the device address under **Settings → Your Account → Send to Kindle Email** and connect the intended Drive account. The Gmail and Drive accounts can differ. [Amazon email setup](https://digprjsurvey.amazon.com/csad/help/node/G7NECT4B4ZWHQ8WV)

Place the following files under `/etc/kindle-sync/secrets`, owned by `kindle-sync`. Use mode `0700` for the directory and `0600` for the files.

| File | Contents |
| --- | --- |
| `zotero-api-key` | Zotero API key with read and write access to the personal library. |
| `koofr-username`, `koofr-password` | WebDAV username and application password, one value per file. |
| `google-client.json` | Desktop OAuth client from a Google Cloud project with the Gmail API and Drive API enabled. |

For a remote installation, connect with `ssh -L 43891:127.0.0.1:43891 user@server`. In that session, define this shortcut and open each consent link in your browser.

```sh
ks() {
  sudo -u kindle-sync env CODEX_SECRETS_DIR=/etc/kindle-sync/secrets \
    /opt/kindle-sync/bin/kindle-sync "$@"
}
ks auth gmail
ks auth drive
```

Gmail grants permission to send mail and verify the sender's address. Drive grants read-only access to the account. The CLI scans PDFs directly inside the configured folder. External Google apps in **Testing** receive seven-day refresh tokens with these permissions. [Google token expiration](https://developers.google.com/identity/protocols/oauth2#expiration)

## Verify one paper

1. Pick a paper with a stored PDF in the reading collection. Run `ks sync --dry-run` to preview, then `ks sync` to record existing papers as a baseline. Later runs send items when they first appear in the collection tree, even if a whole subcollection moves into it.
2. Send it with `ks sync --item ITEM_KEY --attachment ATTACHMENT_KEY`, using the eight-character keys of the paper and its stored PDF. A `submitted` status means Gmail accepted the message. Confirm receipt on the Scribe.
3. Mark a page on the Scribe and share the PDF to Google Drive. Set `drive_folder_id` to the folder created by the device. Keep the delivery identifier beginning with `KRS-` in the filename. It links the export to the paper.
4. Process the exported PDF with `ks receive DRIVE_FILE_ID`, using the file ID from its Drive link. Sync Zotero, open the Kindle Notes attachment and check the marks. Confirm that repeating the import leaves one attachment and that a changed PDF creates another version.

## Run and troubleshoot

Use `ks status --json` to inspect queued items, PDF candidates, delivery states and pending exports. Commands return JSON and report problems with a nonzero exit status.

| Situation | Action |
| --- | --- |
| Include existing papers | Preview `ks sync --backfill COLLECTION_KEY --dry-run`, then repeat without `--dry-run` to queue previously seen papers in the reading tree. |
| Original PDF still uploading | Let Zotero finish syncing the source to Koofr. A later run retries. |
| Uncertain send | Check the Kindle library. Use `ks resolve ITEM_KEY received` to confirm receipt or `ks resolve ITEM_KEY retry` to allow a retry. |
| Amazon rejected the email | Verify the message ID in status. Run `ks resolve ITEM_KEY rejected GMAIL_ID` to queue that delivery again. |
| Unknown parent for an export | Run `ks match DRIVE_FILE_ID ITEM_KEY`. If several versions are pending, use `ks match --version VERSION DRIVE_FILE_ID ITEM_KEY`. |
| PDF exceeds the email limit | Upload the PDF at `manual_file` with its generated filename. [Send to Kindle](https://www.amazon.com/sendtokindle) |

The limit applies to each paper separately. The default `email_limit_bytes` is 25,000,000 for personal Gmail. Workspace administrators set their own limits. The CLI also allows for the encoded email size. Amazon accepts up to 50 MB by email or 200 MB per web upload. [Gmail limits](https://support.google.com/mail/answer/6584) · [Kindle email limits](https://digprjsurvey.amazon.com/csad/help/node/G7NECT4B4ZWHQ8WV) · [Web upload limits](https://digprjsurvey.amazon.com/csad/help/node/G5WYD9SAF7PGXRNA)

## Enable scheduled runs

```sh
sudo systemctl enable --now kindle-sync.timer
systemctl list-timers kindle-sync.timer
journalctl -u kindle-sync.service
```

The timer runs every five minutes. For a backup, stop the timer, wait for active syncs to finish and copy the entire `state_dir`. Use separate state when switching the library, reading collection, Kindle address, or a previously bound Drive folder. Use `--config PATH` or `KINDLE_SYNC_CONFIG` to select a different config.

## Development

Run `make test` for the Go and Python tests. Run `go test -race ./...` to check for data races. For the local WebDAV integration tests, install `helper/requirements.lock` in a Python 3.12 virtual environment and set `KINDLE_SYNC_TEST_PYTHON` to its Python executable's absolute path.
