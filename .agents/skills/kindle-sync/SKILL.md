---
name: kindle-sync
description: Sync a Zotero reading collection with Kindle Scribe. Use for sending papers, receiving handwritten PDFs shared to Google Drive, previewing a sync, and checking a delivery.
---

# Kindle Sync

Run every command against the deployment that owns the queue. Use the executable named by `KINDLE_SYNC_RUNNER` if set, otherwise the checkout's `bin/kindle-sync-agent`. With a configured native installation, use its `kindle-sync` on PATH. If a selected runner fails, report the error. Starting a second database could send the same papers again. In the table, commands are arguments to the runner.

| User request | Command |
| --- | --- |
| Sync my Kindle | `sync --json` sends new papers and receives exported notes. |
| Preview what will sync | `sync --dry-run --json`. Keep any direction or item restriction the user supplied. |
| Send new papers | `sync --direction send --json` |
| Send one paper | Identify the Zotero parent, then `sync --direction send --item ITEM_KEY --json`. |
| Bring my notes back | `sync --direction receive --json` |
| Receive this Drive PDF | `receive DRIVE_FILE_ID --json`. Use the ID from the supplied file link. If it skips the file, report the result. |
| Check a delivery or earlier export | `status --json`. Filter jobs or receipts to the user's request. |

For a title, short title, system name, Zotero item link or key, pass the supplied text as a single argument to `find "QUERY" --json`. It searches the current reading collection and its descendants. Use the key in `matches` when exactly one paper matches. For multiple matches, show titles, first authors and years and ask the user to choose. If none match, report that and leave the library alone. If several source PDFs qualify, show their names and use the chosen `--attachment KEY`.

Read the JSON and the exit status. A run can submit some papers or save some notes before reporting a problem. Use `status` to verify the affected records. Report submissions to Gmail, notes saved under their Zotero parents, and outstanding work separately. Count repeat exports as already saved. When a user asks where their handwriting is, the device step is **Share → Google Drive → Upload**.

- `submitted` means Gmail accepted the email. Only report device receipt on user confirmation or a corresponding `confirmed_received` record.
- `uncertain` can mean the email was sent. Check the device with the user before authorizing a retry. A confirmed Amazon rejection requires the matching Gmail message ID.
- `manual_upload` gives the prepared PDF path. On Compose, it is inside the container. To give the user a local file for upload, use the deployment's `docker compose cp` to copy it to a private host directory with the same filename.
- Automatic receipt requires a unique match to a submitted or confirmed delivery and a live Zotero parent, even if the paper moved out of the reading collection. Leave unrelated and ambiguous PDFs in Drive. Use `match DRIVE_FILE_ID ITEM_KEY` only when the user requests that specific pairing. Add `--version VERSION` to select an older cached revision.

Use `--backfill` only for a request to send historical members of a collection. Full sync, backfill and live sends are separate from a request to inspect or preview. For installation or a missing deployment, see the [deployment skill](../kindle-sync-deploy/SKILL.md).
