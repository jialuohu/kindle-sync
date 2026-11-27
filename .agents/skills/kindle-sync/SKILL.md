---
name: kindle-sync
description: Sync a Zotero reading collection with Kindle Scribe. Use this skill to send papers, receive annotated PDFs from Google Drive, preview a sync, or check a delivery.
---

# Kindle Sync

Use the runner for the deployment that owns the queue. First check `KINDLE_SYNC_RUNNER`. If it is unset, use the checkout's `bin/kindle-sync-agent` for Docker Compose or the configured `kindle-sync` on PATH for a native installation. If the selected runner fails, report the error. Opening a second database can send papers twice. For installation, follow the [deployment skill](../kindle-sync-deploy/SKILL.md).

## Run the requested sync

Pass the arguments below to the runner. For a preview, keep the requested direction and item or collection selector and add `--dry-run`.

| Request | Arguments |
| --- | --- |
| Sync my Kindle | `sync --json` |
| Preview my Kindle sync | `sync --dry-run --json` |
| Send new papers | `sync --direction send --json` |
| Send one paper | `sync --item ITEM_KEY --json` |
| Send unsent papers already in a collection at setup | `sync --backfill COLLECTION_KEY --json` |
| Resend one paper with a corrected filename | `sync --item ITEM_KEY --resend-as EXPECTED.pdf --json` |
| Bring back exported notes | `sync --direction receive --json` |
| Receive one PDF from a Drive link | `receive DRIVE_FILE_ID --json` |
| Check a delivery or export | `status --json` |

A full sync sends new papers and checks Drive for annotations. Use `--backfill` only on request. For a resend, use the exact requested filename. To export handwriting on the Scribe, the reader taps **Share → Google Drive → Upload**.

## Find the paper

Run `find "QUERY" --json` with the user's title, short title, system name, Zotero item link, or key as one argument. The search includes subcollections. Use the item key for a single match. If several papers match, show their titles, first authors, and years and ask which one to use. Report an empty result. If several source PDFs qualify, show their filenames and pass the user's choice with `--attachment KEY`.

## Check the result

Read the exit status and JSON report. Even a failed run can submit papers or archive notes. Check the affected jobs or receipts with `status --json` and report Gmail submissions, saved notes, and remaining problems.

| State | What to report or do |
| --- | --- |
| `submitted` | Gmail accepted the email. Confirm arrival on the Kindle only when the reader verifies it or the job says `confirmed_received`. |
| `uncertain` | The email may have been sent. Ask the reader to check the Kindle before retrying. A confirmed Amazon rejection requires the matching Gmail message ID. |
| `manual_upload` | Give the reader the prepared PDF. With Compose, use the deployment's `docker compose cp` command to copy it to a private host directory under the same filename. |
| `retired` | This is an older export. Point the reader to the current **Kindle Notes** attachment. Identical exports reuse it. After verifying a newer, changed PDF, the CLI moves the older attachment to Zotero Trash. |

The CLI leaves unrelated or ambiguous PDFs in Drive. To return automatically, an export must match one submitted or confirmed delivery with a live Zotero parent. Moving the parent out of the reading collection preserves the match. If `receive` skips a file, report that outcome. Use `match DRIVE_FILE_ID ITEM_KEY` only when the user requests that specific pairing. To select an older cached revision, place `--version VERSION` before the IDs.
